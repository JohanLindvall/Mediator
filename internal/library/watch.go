package library

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher keeps the library in sync with filesystem changes using fsnotify.
// Watches are installed recursively on every directory under the roots.
type Watcher struct {
	lib *Library
	fsw *fsnotify.Watcher

	mu      sync.Mutex
	settles int // re-walks outstanding, so a mass move cannot spawn timers without end
}

// settleWalks is when a newly created directory is walked again.
//
// A watch on a new directory can only be installed once the directory has
// been reported, and whatever is created inside it before that lands in a
// window nothing is listening to. That window is small and real: a torrent
// client preallocating a release makes the directory and then all thirteen
// of its files **four milliseconds later**, which is how a 1.1 GB download
// came to sit unindexed until the ten-minute rescan found it.
//
// walkNew's immediate walk is what covers a directory moved in whole, and it
// cannot cover this one: at that instant the files either do not exist yet or
// are empty. So the directory is walked again as its contents settle. The
// last of these is minutes out on purpose — a set of archive volumes is not
// indexable until the last byte of the last volume has arrived, and a torrent
// takes as long as it takes.
var settleWalks = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

// maxSettles bounds the timers outstanding. A directory tree moved in
// wholesale is one Create event per directory, and every one of them would
// otherwise schedule its own; past this they are left to the rescan, which is
// what it is for.
const maxSettles = 512

// NewWatcher creates the underlying fsnotify watcher.
func NewWatcher(lib *Library) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{lib: lib, fsw: fsw}, nil
}

// AddDir installs a watch on a single directory. Errors (e.g. inotify limits)
// are logged, not fatal — the periodic rescan still picks changes up.
func (w *Watcher) AddDir(dir string) {
	if err := w.fsw.Add(dir); err != nil {
		w.lib.log.Warn("watch failed", "dir", dir, "err", err)
	}
}

// Reset drops every watch this watcher holds.
//
// The scan that follows a change of directories reinstalls watches for the
// new set as it walks it. Without this the watches left over from a removed
// directory would keep reporting it, and the watcher would put back exactly
// what the scan had just taken out.
func (w *Watcher) Reset() {
	for _, dir := range w.fsw.WatchList() {
		if err := w.fsw.Remove(dir); err != nil {
			w.lib.log.Debug("unwatch failed", "dir", dir, "err", err)
		}
	}
}

// Run processes filesystem events until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	defer w.fsw.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.lib.log.Warn("watcher error", "err", err)
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	path := filepath.Clean(ev.Name)
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return
	}
	switch {
	case ev.Op.Has(fsnotify.Create):
		if isDir(path) {
			// New directory: watch it and index anything already inside
			// (e.g. a directory moved in wholesale).
			w.walkNew(path)
		} else {
			w.lib.AddFile(path)
		}
	case ev.Op.Has(fsnotify.Remove) || ev.Op.Has(fsnotify.Rename):
		w.lib.Remove(path)
	case ev.Op.Has(fsnotify.Write):
		// Content changed: refresh size/mtime. Writers often emit many Write
		// events; upsert is cheap and notify is coalesced downstream.
		w.lib.AddFile(path)
	case ev.Op.Has(fsnotify.Chmod):
		// Attributes changed, which for our purposes means the mtime: every
		// tool that preserves timestamps stamps them after the copy, and the
		// mtime is part of what an item is here — it keys the metadata cache,
		// the cell and the thumbnail URL. Ignoring these left a file that had
		// been stamped looking exactly as it did before it was written.
		// Directories need no guard: AddFile stats and refuses one, and the
		// re-read it triggers is debounced (enrichAfterQuiet), so a tool
		// stamping a whole release cannot spawn a reader per file.
		w.lib.AddFile(path)
	}
}

// walkNew indexes what is already in a newly created directory and installs
// watches inside it, then arranges to look again as its contents settle.
func (w *Watcher) walkNew(dir string) {
	w.rewalk(dir)
	for _, d := range settleWalks {
		if !w.claimSettle() {
			return
		}
		time.AfterFunc(d, func() {
			defer w.releaseSettle()
			w.rewalk(dir)
		})
	}
}

// claimSettle takes one of the outstanding-timer slots.
func (w *Watcher) claimSettle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.settles >= maxSettles {
		return false
	}
	w.settles++
	return true
}

func (w *Watcher) releaseSettle() {
	w.mu.Lock()
	w.settles--
	w.mu.Unlock()
}

func (w *Watcher) rewalk(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			if w.lib.excluded(p) {
				return filepath.SkipDir // no watches inside an excluded tree
			}
			w.AddDir(p)
			return nil
		}
		w.lib.AddFile(p)
		return nil
	})
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		// The Create event can arrive before the entry is fully visible.
		time.Sleep(10 * time.Millisecond)
		info, err = os.Stat(path)
	}
	return err == nil && info.IsDir()
}
