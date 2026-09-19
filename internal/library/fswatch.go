package library

// What the operating system tells the watcher, in the four words it needs.
//
// On Linux this is inotify, spoken directly (fswatch_linux.go); elsewhere it
// is fsnotify (fswatch_other.go). The Linux one exists for a single reason:
// **a write is not an event here, a close is.** A watch that subscribes to
// writes gets one event per write() call, at whatever granularity the writer
// happens to use — which is decided by other people's programs. Measured on
// this machine: a downloader of the owner's own writing three files at
// 21 MB/s in calls of about 46 bytes made 464,000 events a second, the
// kernel's queue of 16,384 overflowed eight times a second for three hours,
// and this process spent a whole core discarding events for files that were
// not even media, while losing the creations underneath them. fsnotify's
// public API cannot choose the mask (its WithOps is unexported in every
// released version), so the mask is chosen here: a file counts when a writer
// closes it, when its attributes change, and when it appears or goes.
//
// What that costs is the live size of a file still being written — a torrent
// landing used to grow in the listing with every burst — which now settles
// when the writer closes it, or at the next rescan, whichever comes first.

import "errors"

// fsEvent is one thing the operating system reported about a path.
type fsEvent struct {
	Name string
	Op   fsOp
}

// fsOp is what happened. A move is reported as a removal of the old name and
// an appearance of the new one, which is what the index does with it anyway.
type fsOp uint8

const (
	fsCreate fsOp = 1 << iota // it appeared: created, or moved in
	fsRemove                  // it went: deleted, or moved out
	fsWrite                   // a writer closed it — or, where closes cannot be watched, wrote to it
	fsChmod                   // its attributes changed, the timestamps included
)

// Has reports whether the operation includes f.
func (o fsOp) Has(f fsOp) bool { return o&f != 0 }

// errEventOverflow is the kernel saying it dropped events: its queue was
// full and nothing was reading it fast enough. What was dropped is unknown,
// which is why the answer to it is a walk.
var errEventOverflow = errors.New("the kernel's event queue overflowed and events were lost")

// errWatcherClosed is an Add after Close.
var errWatcherClosed = errors.New("the watcher is closed")

// fsBackend is the operating system's side of the watcher.
type fsBackend interface {
	// Add watches a directory: what appears, goes, is closed after writing
	// or has its attributes changed inside it. Watching the same directory
	// twice is one watch.
	Add(dir string) error
	// Remove takes the watch off a directory.
	Remove(dir string) error
	// WatchList is every directory watched.
	WatchList() []string
	// Close stops everything and closes both channels.
	Close() error
	Events() <-chan fsEvent
	Errors() <-chan error
}
