// SPDX-License-Identifier: MIT

package library

// Members stored compressed in their container: a deflated zip member, and
// a compressed member of a rar set.
//
// Their bytes are not the content, so they are not a storedEntry's to stitch:
// they are read by unpacking them, and unpacking is a stream — it starts at
// the beginning and goes forward. Two readers follow from that, and the line
// between them is what the reader is doing.
//
// Everything this process reads for itself — a picture's header for its
// size, a song's tags, a thumbnail, the opening boxes of a film — reads from
// the start, or near it, once. For those the content is unpacked as it is
// read and never written anywhere (packedReader): a forward seek unpacks and
// discards up to where it is going, and going back starts again from the
// beginning. That is free for what those readers do, and it keeps a pass
// over a photo archive from writing every picture in it to the disk to read
// its first few hundred bytes.
//
// Playback is the other thing. A browser and a converter seek all over a
// film, every range request a new reader starting again from the top, and
// for a member of any size that is minutes of unpacking a scrub. So a large
// member being streamed is unpacked once into the scratch space the
// rewrapper uses, under the same budget, and served from there as a file
// (unpack.go) — the owner's choice, made over unpacking it in pieces.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/klauspost/compress/flate"
	"github.com/nwaples/rardecode/v2"
)

// packFormat says what a packed member is packed in.
type packFormat uint8

const (
	packZip packFormat = iota + 1 // deflated, in one run of a zip file
	packRar                       // compressed by rar, in a set's volumes
)

// packing describes a member stored compressed.
type packing struct {
	format packFormat
	// crc is the content's checksum where the container records one (zip),
	// checked when a member is unpacked into the scratch space.
	crc uint32
	// ended and short are a rar member's completeness: the last part seen
	// says nothing follows it, and no volume holds less than its header
	// says. A set still arriving is otherwise indistinguishable.
	ended, short bool
	// firstPacked is the size of a rar member's first part, which finds it
	// again in the set's listing where its name does not (see rarMember).
	firstPacked int64
}

// packed says whether an item's content is stored compressed.
func (it Item) packed() bool { return it.stored != nil && it.stored.pack != nil }

// openPacked opens a packed member: the copy in the scratch space where one
// has been made already, which is a file like any other, and otherwise a
// reader that unpacks as it reads.
func openPacked(it Item) (File, error) {
	if p, ok := unpacks.ready(it); ok {
		if f, err := os.Open(p); err == nil {
			return f, nil
		}
	}
	return &packedReader{it: it}, nil
}

// packedReader serves a packed member by unpacking it as it is read. Reading
// forward costs what unpacking costs; reading back starts again from the top.
// Safe for concurrent use, as io.ReaderAt requires: the stream is one, so
// the calls take turns.
type packedReader struct {
	it Item

	mu     sync.Mutex
	pos    int64         // where Read reads next
	src    io.ReadCloser // the unpacking stream, `at` bytes in
	at     int64
	closed bool
}

func (r *packedReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readAt(p, off)
}

// readAt implements ReadAt; the caller holds r.mu.
func (r *packedReader) readAt(p []byte, off int64) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	size := r.it.stored.size
	if off >= size {
		return 0, io.EOF
	}
	if r.src == nil || off < r.at {
		if r.src != nil {
			r.src.Close()
			r.src = nil
		}
		src, err := openUnpacking(r.it)
		if err != nil {
			return 0, err
		}
		r.src, r.at = src, 0
	}
	if gap := off - r.at; gap > 0 {
		n, err := io.CopyN(io.Discard, r.src, gap)
		r.at += n
		if err != nil {
			return 0, shortMember(err)
		}
	}
	want := p
	if rest := size - off; int64(len(want)) > rest {
		want = want[:rest]
	}
	n, err := io.ReadFull(r.src, want)
	r.at += int64(n)
	if err != nil {
		return n, shortMember(err)
	}
	if len(want) < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// shortMember says what an end of stream inside a member's stated size is:
// the archive promised more than it unpacks to.
func shortMember(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (r *packedReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.pos >= r.it.stored.size {
		return 0, io.EOF
	}
	n, err := r.readAt(p, r.pos)
	r.pos += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

// Seek moves where Read reads next and nothing else: the stream is moved
// only when something is read, so ServeContent asking how long the member
// is — a seek to the end and back — costs nothing.
func (r *packedReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	pos, err := seekPosition(r.pos, r.it.stored.size, offset, whence)
	if err != nil {
		return 0, err
	}
	r.pos = pos
	return r.pos, nil
}

func (r *packedReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.src != nil {
		err := r.src.Close()
		r.src = nil
		return err
	}
	return nil
}

// openUnpacking opens a packed member's content as a stream from its first
// byte.
func openUnpacking(it Item) (io.ReadCloser, error) {
	e := it.stored
	switch e.pack.format {
	case packZip:
		// The packed bytes, in one file or across the parts of a split set,
		// through the reader that stitches a rar set's volumes.
		var n int64
		for _, s := range e.segs {
			n += s.n
		}
		src := newStoredReader(&storedEntry{name: e.name, size: n, segs: e.segs})
		z := flate.NewReader(src)
		return &closers{Reader: z, close: []io.Closer{z, src}}, nil
	case packRar:
		container, _, _ := strings.Cut(it.Path, "\x00")
		return openRarMember(container, it.ModTime, e)
	}
	return nil, fmt.Errorf("unknown packing %d", e.pack.format)
}

// closers is a reader that closes several things when it is closed.
type closers struct {
	io.Reader
	close []io.Closer
}

func (c *closers) Close() error {
	var first error
	for _, x := range c.close {
		if err := x.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// openRarMember opens a compressed member of a rar set as a stream. A
// variable so that a test can stand in for the decoder: nothing that builds
// test fixtures can compress the way rar does.
var openRarMember = func(first string, mtime int64, e *storedEntry) (io.ReadCloser, error) {
	f, err := rarMember(first, mtime, e)
	if err != nil {
		return nil, err
	}
	return f.Open()
}

// rarListings keeps the last few sets' member lists, as rardecode reads
// them. Opening one member of a set needs its listing — where its header is,
// in which volume — and a set of a thousand pictures read for its thumbnails
// would otherwise read all thousand headers once per picture. Keyed by the
// first volume and the set's time, so a set that changes is listed afresh.
var rarListings = struct {
	sync.Mutex
	m     map[string]*rarListing
	order []string // least recently wanted first
}{m: map[string]*rarListing{}}

// rarListingsKept is how many sets' listings are kept: a few, since what is
// being looked at is a handful of sets at once.
const rarListingsKept = 4

type rarListing struct {
	once   sync.Once
	byName map[string]*rardecode.File
	files  []*rardecode.File
	err    error
}

// rarMember finds a member in its set's listing: by name, and where the name
// was read differently — rar keeps a name twice, once in the old encoding
// and once in Unicode, and this parser reads the first — by its sizes.
func rarMember(first string, mtime int64, e *storedEntry) (*rardecode.File, error) {
	key := fmt.Sprintf("%s\x00%d", first, mtime)
	rarListings.Lock()
	ls := rarListings.m[key]
	if ls == nil {
		ls = &rarListing{}
		rarListings.m[key] = ls
	}
	rarListings.order = append(slicesWithout(rarListings.order, key), key)
	for len(rarListings.order) > rarListingsKept {
		delete(rarListings.m, rarListings.order[0])
		rarListings.order = rarListings.order[1:]
	}
	rarListings.Unlock()

	ls.once.Do(func() {
		ls.files, ls.err = rardecode.List(first)
		ls.byName = make(map[string]*rardecode.File, len(ls.files))
		for _, f := range ls.files {
			ls.byName[f.Name] = f
		}
	})
	if ls.err != nil {
		forgetRarListing(key, ls)
		return nil, ls.err
	}
	if f, ok := ls.byName[e.name]; ok {
		return f, nil
	}
	var found *rardecode.File
	for _, f := range ls.files {
		if f.UnPackedSize == e.size && f.PackedSize == e.pack.firstPacked && !f.IsDir {
			if found != nil {
				return nil, fmt.Errorf("%s: two members fit %q", first, e.name)
			}
			found = f
		}
	}
	if found == nil {
		return nil, fmt.Errorf("%s: no member %q", first, e.name)
	}
	return found, nil
}

// forgetRarListing drops a listing that failed, so the next open lists again
// rather than inheriting a failure that may have been a moment's.
func forgetRarListing(key string, ls *rarListing) {
	rarListings.Lock()
	defer rarListings.Unlock()
	if rarListings.m[key] == ls {
		delete(rarListings.m, key)
		rarListings.order = slicesWithout(rarListings.order, key)
	}
}

func slicesWithout(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// ctxReader stops a long read when its context is done: an unpacking into the
// scratch space is gigabytes, and shutdown must not wait for the last of them.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
