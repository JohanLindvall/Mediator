//go:build !linux

package library

import (
	"errors"

	"github.com/fsnotify/fsnotify"
)

// fsnotifyBackend is the watcher everywhere but Linux, where fsnotify's own
// choice of events stands: it reports writes, since it cannot be told not
// to, and the watcher treats each as a close. See fswatch.go for why Linux
// does not go through here.
type fsnotifyBackend struct {
	w      *fsnotify.Watcher
	events chan fsEvent
	errors chan error
}

func newFSBackend() (fsBackend, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	b := &fsnotifyBackend{w: w, events: make(chan fsEvent), errors: make(chan error)}
	go b.forward()
	return b, nil
}

func (b *fsnotifyBackend) forward() {
	defer close(b.events)
	defer close(b.errors)
	for b.w.Events != nil || b.w.Errors != nil {
		select {
		case ev, ok := <-b.w.Events:
			if !ok {
				return
			}
			var op fsOp
			if ev.Op.Has(fsnotify.Create) {
				op |= fsCreate
			}
			if ev.Op.Has(fsnotify.Remove) || ev.Op.Has(fsnotify.Rename) {
				op |= fsRemove
			}
			if ev.Op.Has(fsnotify.Write) {
				op |= fsWrite
			}
			if ev.Op.Has(fsnotify.Chmod) {
				op |= fsChmod
			}
			if op != 0 {
				b.events <- fsEvent{Name: ev.Name, Op: op}
			}
		case err, ok := <-b.w.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				err = errEventOverflow
			}
			b.errors <- err
		}
	}
}

func (b *fsnotifyBackend) Add(dir string) error    { return b.w.Add(dir) }
func (b *fsnotifyBackend) Remove(dir string) error { return b.w.Remove(dir) }
func (b *fsnotifyBackend) WatchList() []string     { return b.w.WatchList() }
func (b *fsnotifyBackend) Close() error            { return b.w.Close() }
func (b *fsnotifyBackend) Events() <-chan fsEvent  { return b.events }
func (b *fsnotifyBackend) Errors() <-chan error    { return b.errors }
