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
	// The timers behind those re-walks, so that a change of directories can
	// stop them rather than leave them to fire minutes later over a tree
	// that is no longer the library's (see armSettle). Numbered rather than
	// held by pointer: a timer cannot name itself to the callback it was
	// created with without being read before it has been assigned.
	timers     map[int64]*time.Timer
	nextSettle int64
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
	return &Watcher{lib: lib, fsw: fsw, timers: map[int64]*time.Timer{}}, nil
}

// AddDir installs a watch on a single directory. Errors (e.g. inotify limits)
// are logged, not fatal — the periodic rescan still picks changes up.
//
// A directory outside the roots is refused, because the two things that
// install watches both outlive a change of directories: a settle timer armed
// minutes ago, and a walk that took its root list before the change. Reset
// has run by then, so whatever they install afterwards is never removed
// again — inotify watches, and a wakeup apiece for every event under a tree
// nothing indexes any more. AddFile has refused such a path all along; this
// is the same refusal one door along.
func (w *Watcher) AddDir(dir string) {
	if !w.lib.UnderRoots(dir) {
		return
	}
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
	w.stopSettles()
	w.lib.stopMemberReads()
	for _, dir := range w.fsw.WatchList() {
		if err := w.fsw.Remove(dir); err != nil {
			w.lib.log.Debug("unwatch failed", "dir", dir, "err", err)
		}
	}
}

// eventQueue is how many events wait here while the work behind them is done.
//
// fsnotify's inotify backend hands events over on an unbuffered channel, so
// whoever consumes them is what keeps the kernel's own queue drained — and
// the work behind one event is not small: a recursive walk of a directory
// moved in wholesale, or a reparse of every volume of an eighty-nine part
// set, which is seconds of disk while the writer that caused it goes on
// emitting events. For as long as that takes, nothing is reading the inotify
// descriptor, and what the kernel cannot queue it drops — a Create that
// nobody will ever report again. So Run does nothing but take events, and
// one worker does the work behind them in the order they arrived. The queue
// is generous rather than unbounded: a full one still blocks, but it is
// thousands of events of slack rather than none.
const eventQueue = 4096

// Run processes filesystem events until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	defer w.fsw.Close()
	defer w.stopSettles()
	// And the reads pending on the containers those events were about: a
	// timer armed two seconds ago is the same kind of leftover as a settle
	// walk armed two minutes ago. This runs after the worker has stopped
	// (the defer below), so nothing can arm another behind it.
	defer w.lib.stopMemberReads()
	events := make(chan fsnotify.Event, eventQueue)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.work(ctx, events)
	}()
	// Wait for the worker to put down whatever it is holding, which is what
	// this goroutine did for itself when it did the work as well.
	defer func() {
		close(events)
		<-done
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.lib.log.Warn("watcher error", "err", err)
		}
	}
}

// work does what each event asks for, one at a time and in the order they
// arrived: a Create and the Write that follows it must not be reordered.
func (w *Watcher) work(ctx context.Context, events <-chan fsnotify.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			w.handle(ev)
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
		w.armSettle(d, dir)
	}
}

// armSettle schedules one re-walk and keeps hold of its timer.
//
// The last of these is two minutes out, and the preferences can change in
// two minutes: a timer armed before a root was removed used to fire after
// Reset had dropped every watch and walk the removed tree, installing
// watches on it that nothing would ever remove again. Nothing under it is
// indexed — AddFile and AddDir both refuse a path outside the roots — but
// the watches and the work their events cause stay for the life of the
// process.
func (w *Watcher) armSettle(after time.Duration, dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextSettle++
	id := w.nextSettle
	// The callback waits on this lock until the timer has been recorded,
	// so it can never find its own slot missing.
	w.timers[id] = time.AfterFunc(after, func() {
		// The slot is held until the walk is done, not merely until the
		// timer fires: maxSettles bounds the re-walks outstanding, and a
		// tree moved in wholesale is one Create per directory, so counting
		// only the armed timers would let a mass arrival put as many
		// recursive walks on the disk at once as it had directories.
		if !w.forget(id) {
			return // stopped: the directories changed, or the watcher did
		}
		defer w.releaseSettle()
		w.rewalk(dir)
	})
}

// forget takes a re-walk's timer off the list and says whether it was still
// there — which is to say whether this caller now owns its slot. Two things
// can reach one timer, its own callback and stopSettles, and only one of
// them may hand the slot back.
func (w *Watcher) forget(id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, held := w.timers[id]
	delete(w.timers, id)
	return held
}

// stopSettles drops every re-walk still outstanding: the directories have
// changed, or the watcher is stopping. A walk already running is left to
// finish, since nothing it can do is indexed or watched any more.
func (w *Watcher) stopSettles() {
	w.mu.Lock()
	armed := make(map[int64]*time.Timer, len(w.timers))
	for id, t := range w.timers {
		armed[id] = t
	}
	w.mu.Unlock()
	for id, t := range armed {
		t.Stop()
		if w.forget(id) {
			w.releaseSettle()
		}
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
			if !w.lib.UnderRoots(p) {
				// A settle timer armed before the preferences changed, and
				// the tree it was armed over is not the library's any more.
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
