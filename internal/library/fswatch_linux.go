//go:build linux

package library

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// inotifyMask is what a watched directory reports. IN_MODIFY is deliberately
// not in it (see fswatch.go); IN_CLOSE_WRITE is what says a file's contents
// have settled. IN_ONLYDIR refuses a watch on anything but a directory, so a
// path that is a file by the time the watch is asked for is an error rather
// than a watch on the wrong thing.
const inotifyMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_DELETE_SELF |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_MOVE_SELF |
	unix.IN_ATTRIB | unix.IN_CLOSE_WRITE | unix.IN_ONLYDIR

// inotifyBuf is one read's worth of events: a name is at most 255 bytes and
// an event header 16, and the kernel refuses a read that cannot hold the
// next whole event.
const inotifyBuf = 64 << 10

type inotifyBackend struct {
	// f is the inotify descriptor as a pollable File: opened non-blocking,
	// so Read waits in the runtime's poller and Close wakes it. fd is the
	// same descriptor for the watch calls — never f.Fd(), which would put
	// the descriptor back into blocking mode and take the poller away.
	f  *os.File
	fd int

	mu     sync.Mutex
	byWd   map[int32]string
	byPath map[string]int32
	closed bool

	events chan fsEvent
	errors chan error
	quit   chan struct{} // closed first by Close, so a blocked send lets go
	done   chan struct{} // closed by the reader as it leaves
	once   sync.Once
}

func newFSBackend() (fsBackend, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	b := &inotifyBackend{
		f: os.NewFile(uintptr(fd), "inotify"), fd: fd,
		byWd: map[int32]string{}, byPath: map[string]int32{},
		events: make(chan fsEvent), errors: make(chan error),
		quit: make(chan struct{}), done: make(chan struct{}),
	}
	go b.read()
	return b, nil
}

func (b *inotifyBackend) Events() <-chan fsEvent { return b.events }
func (b *inotifyBackend) Errors() <-chan error   { return b.errors }

func (b *inotifyBackend) Add(dir string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errWatcherClosed
	}
	wd, err := unix.InotifyAddWatch(b.fd, dir, inotifyMask)
	if err != nil {
		return err
	}
	// The kernel answers the same descriptor for a directory already
	// watched — and for another path to the same directory, which then
	// reports under the newer name, as it would under fsnotify.
	if old, ok := b.byWd[int32(wd)]; ok && old != dir {
		delete(b.byPath, old)
	}
	b.byWd[int32(wd)] = dir
	b.byPath[dir] = int32(wd)
	return nil
}

func (b *inotifyBackend) Remove(dir string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	wd, ok := b.byPath[dir]
	if !ok {
		return errors.New("not watched: " + dir)
	}
	delete(b.byPath, dir)
	delete(b.byWd, wd)
	if b.closed {
		return nil
	}
	// EINVAL is a watch the kernel has already dropped — the directory
	// went — which is the outcome asked for.
	if _, err := unix.InotifyRmWatch(b.fd, uint32(wd)); err != nil && !errors.Is(err, unix.EINVAL) {
		return err
	}
	return nil
}

func (b *inotifyBackend) WatchList() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.byPath))
	for dir := range b.byPath {
		out = append(out, dir)
	}
	return out
}

// Close stops the reader and closes the channels. quit goes first so that a
// reader blocked handing an event to nobody lets go; closing the file is
// what wakes one blocked in the kernel.
func (b *inotifyBackend) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		close(b.quit)
		_ = b.f.Close()
		<-b.done
	})
	return nil
}

// read is the reader: one goroutine for the life of the descriptor, parsing
// each read into events and handing them over one at a time.
func (b *inotifyBackend) read() {
	defer close(b.done)
	defer close(b.errors)
	defer close(b.events)
	buf := make([]byte, inotifyBuf)
	for {
		n, err := b.f.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return
			}
			if !b.send(fsEvent{}, err) {
				return
			}
			continue
		}
		off := 0
		for off+unix.SizeofInotifyEvent <= n {
			wd := int32(binary.NativeEndian.Uint32(buf[off:]))
			mask := binary.NativeEndian.Uint32(buf[off+4:])
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			name := ""
			if nameLen > 0 && off+unix.SizeofInotifyEvent+nameLen <= n {
				name = string(bytes.TrimRight(buf[off+unix.SizeofInotifyEvent:off+unix.SizeofInotifyEvent+nameLen], "\x00"))
			}
			off += unix.SizeofInotifyEvent + nameLen
			if !b.deliver(wd, mask, name) {
				return
			}
		}
	}
}

// deliver turns one kernel event into ours, or into nothing.
func (b *inotifyBackend) deliver(wd int32, mask uint32, name string) bool {
	if mask&unix.IN_Q_OVERFLOW != 0 {
		return b.send(fsEvent{}, errEventOverflow)
	}
	b.mu.Lock()
	dir, known := b.byWd[wd]
	if mask&(unix.IN_IGNORED|unix.IN_UNMOUNT) != 0 && known {
		// The kernel has dropped the watch — the directory went, or was
		// unmounted — and the descriptor may be given out again.
		delete(b.byWd, wd)
		if b.byPath[dir] == wd {
			delete(b.byPath, dir)
		}
	}
	b.mu.Unlock()
	if !known {
		return true // a watch already removed, or never ours
	}
	var op fsOp
	if mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
		op |= fsCreate
	}
	if mask&(unix.IN_DELETE|unix.IN_DELETE_SELF|unix.IN_MOVED_FROM|unix.IN_MOVE_SELF) != 0 {
		op |= fsRemove
	}
	if mask&unix.IN_CLOSE_WRITE != 0 {
		op |= fsWrite
	}
	if mask&unix.IN_ATTRIB != 0 {
		op |= fsChmod
	}
	if op == 0 {
		return true
	}
	path := dir
	if name != "" {
		path = filepath.Join(dir, name)
	}
	return b.send(fsEvent{Name: path, Op: op}, nil)
}

// send hands over an event or an error, unless the backend is closing —
// in which case it says so, and the reader leaves.
func (b *inotifyBackend) send(ev fsEvent, err error) bool {
	if err != nil {
		select {
		case b.errors <- err:
			return true
		case <-b.quit:
			return false
		}
	}
	select {
	case b.events <- ev:
		return true
	case <-b.quit:
		return false
	}
}
