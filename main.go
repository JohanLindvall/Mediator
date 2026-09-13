// Command media serves a web-based browser for video, image and music
// collections. Usage:
//
//	media [flags] DIR [DIR...]
//
// Each DIR is scanned recursively and watched for changes.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/browse"
	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/server"
	"github.com/JohanLindvall/Mediator/internal/state"
)

//go:generate go run ./cmd/gen-ts

//go:embed all:web/dist
var distFS embed.FS

// config is everything run needs, assembled from the command line.
type config struct {
	roots    []string
	excludes []string
	listen   string
	dataDir  string
	dbPath   string
	rescan   time.Duration
	analyze  bool
	open     bool
	lock     bool
	debug    bool
	tmpDir   string
	tmpMax   int64
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// uiQuiet is how long after a request the analysis waits before reading
// another track. Short, because it is only meant to keep a second of ffmpeg
// from starting under somebody's fingers: a page that is being used asks for
// something every few seconds, and one that is merely open asks for nothing.
const uiQuiet = 5 * time.Second

// analysisDrain is how long shutdown waits for the analysis to notice that
// it has been cancelled. It is a bound rather than an outright wait because
// the pass sits behind the first walk, which takes no context and cannot be
// asked to stop: without one, a signal arriving during a cold start would
// hold the process open for the length of the walk. Long enough for a
// decode to reach its next check of the context, short enough that nobody
// wonders whether the process has hung.
const analysisDrain = 2 * time.Second

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address (port 0 picks a free one)")
	dataDir := flag.String("data", "data", "directory for playback state and the default blob database")
	dbPath := flag.String("db", "", `blob database path (thumbnails + probed metadata; default <data>/media.db, "off" disables persistent caching)`)
	rescan := flag.Duration("rescan", 10*time.Minute, "full rescan interval as a safety net (0 disables)")
	analyze := flag.Bool("analyze", true, "read how the music sounds in the background, for similar tracks, radio and audiobooks")
	version := flag.Bool("version", false, "print the build and exit")
	open := flag.Bool("open", false, "open the UI in the default browser once listening (without -listen: a free port on 127.0.0.1)")
	tmpDir := flag.String("tmp", "", "directory for converted files being served (default: the system temp directory)")
	tmpMax := flag.String("tmp-max", "8G", `how much converted material may be held at once ("off" for no limit)`)
	debug := flag.Bool("debug", false, "log every API request (method, range, status, bytes) and raise the log level")
	lock := flag.Bool("lock", false, "refuse changes to the scanned directories: the preferences show what is indexed and nothing can alter it")
	var excludes stringList
	flag.Var(&excludes, "exclude", "glob of paths to keep out of the index, repeatable (no slash in the pattern: matched against the file or directory name; otherwise against the whole path)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] DIR [DIR...]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if *version {
		// An operator checking a rollout has the binary and not yet a server:
		// the stamp /api/info carries, printed without starting anything.
		b := server.BuildInfo()
		fmt.Printf("mediator %s", b.Version)
		if b.Commit != "" && b.Commit != b.Version {
			fmt.Printf(" (%s)", b.Commit)
		}
		if b.Modified {
			fmt.Print(" with uncommitted changes")
		}
		if b.Time != "" {
			fmt.Printf(", built %s", b.Time)
		}
		fmt.Printf(", %s %s/%s\n", b.Go, b.OS, b.Arch)
		return
	}

	// A browser session is for this machine only: bind loopback on a free
	// port so it neither collides with a running instance nor exposes the
	// library to the network. An explicit -listen always wins.
	if *open && !flagSet("listen") {
		*listen = "127.0.0.1:0"
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	maxScratch, err := server.ParseSize(*tmpMax)
	if err != nil {
		log.Error("bad -tmp-max", "err", err)
		os.Exit(2)
	}
	if err := library.ValidateExcludes(excludes); err != nil {
		log.Error("bad exclude pattern", "err", err)
		os.Exit(2)
	}
	var roots []string
	for _, arg := range flag.Args() {
		abs, err := filepath.Abs(arg)
		if err != nil {
			log.Error("bad directory", "dir", arg, "err", err)
			os.Exit(1)
		}
		info, err := os.Stat(abs)
		if err != nil {
			log.Error("cannot use directory", "dir", abs, "err", err)
			os.Exit(1)
		}
		if !info.IsDir() {
			log.Error("not a directory", "dir", abs)
			os.Exit(1)
		}
		roots = append(roots, filepath.Clean(abs))
	}

	cfg := config{
		roots: roots, excludes: excludes, listen: *listen, dataDir: *dataDir,
		dbPath: *dbPath, rescan: *rescan, analyze: *analyze, open: *open, lock: *lock, debug: *debug,
		tmpDir: *tmpDir, tmpMax: maxScratch,
	}
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// flagSet reports whether the named flag was given on the command line.
func flagSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.dataDir, 0o755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	lib := library.New(cfg.roots, log)
	lib.SetExcludes(cfg.excludes) // before anything indexes

	// The two stores flush on a context of their own rather than on the
	// signal's. They are the last things that may stop: a handler still
	// answering when the signal lands goes on recording positions, plays and
	// index changes, and on the signal's context both loops had already run
	// their final flush and returned by the time the HTTP server was so much
	// as asked to drain — so those writes landed in a dirty set with no
	// flusher left alive, acknowledged to the caller and then dropped. This
	// context is cancelled after the drain, below.
	storeCtx, stopStores := context.WithCancel(context.Background())
	defer stopStores()

	// Closed when PersistLoop has written its final flush, so shutdown can
	// wait for it before the deferred db.Close — otherwise the two race and
	// the last two seconds of index changes lose to "database not open".
	persistDone := make(chan struct{})
	// And the same for the analysis, which writes vectors to that database
	// on its own beat: it is on the request context rather than the stores'
	// one, so cancelling it is the same gesture that ends the watcher, but
	// waiting for it is not — and a PutFeatures still in flight when the
	// deferred close runs is a write to a closed database like any other.
	// Closed unconditionally below, since the pass starts only after the
	// first walk and may never start at all.
	//
	// **This one is waited for with a bound, where the two stores are waited
	// for outright**, and the difference is what sits in front of it: the
	// first walk, which takes no context and cannot be asked to stop. A
	// signal during a cold start would otherwise hold the process open for
	// the whole of it — a minute and a half on a large library — with
	// nothing on screen to say why. Past the bound its own check of the
	// context and bolt's refusal to write to a closed database are what
	// cover the rest, which is a logged line rather than a lost vector: the
	// track is simply read again next run.
	analysisDone := make(chan struct{})
	var db *blob.DB
	if cfg.dbPath == "off" {
		close(persistDone) // no loop to wait for
		log.Info("blob database disabled; thumbnails and metadata regenerate per run")
	} else {
		dbPath := cfg.dbPath
		if dbPath == "" {
			dbPath = filepath.Join(cfg.dataDir, "media.db")
		}
		var err error
		db, err = blob.Open(dbPath)
		if err != nil {
			return fmt.Errorf("blob db: %w", err)
		}
		defer db.Close()
		lib.SetMetaDB(db)
		log.Info("blob database", "path", dbPath)

		// Directories chosen in the preferences outrank the ones on the
		// command line, which are the seed for a first run rather than the
		// setting itself — otherwise a directory removed in the dialog would
		// come back at every restart.
		if stored := db.Roots(); len(stored) > 0 {
			lib.SetRoots(stored)
			log.Info("scanning the stored directories", "dirs", lib.Roots())
		}

		// Serve the previous run's index straight away; the scan below
		// reconciles it against the disk and drops whatever is gone. The
		// vectors are restored beside it in either order: they are keyed by
		// id and checked against the item's stamp only when the analysis
		// asks.
		lib.LoadFeatures(db)
		if n := lib.LoadFromDB(db); n > 0 {
			log.Info("restored index", "files", n)
		}
		go func() {
			lib.PersistLoop(storeCtx, db)
			close(persistDone)
		}()
	}

	// After the database, because that is where positions live now.
	st := state.Load(db, log)
	// Closed when the store's loop has written its final flush — waited for
	// at shutdown like persistDone, and for the same reason: a flush still
	// in flight when the deferred db.Close runs is a write to a closed
	// database, and the positions of the last few seconds are what it holds.
	stateDone := make(chan struct{})
	go func() {
		st.Run(storeCtx)
		close(stateDone)
	}()
	// Whichever way run leaves — the signal, a serve error, a bind that
	// failed, a watcher that would not start — both loops are stopped and
	// waited for before the deferred db.Close, which was registered above and
	// so runs after this. The error returns used to skip that altogether: the
	// loops were never asked to flush, and whatever the walk had mirrored
	// meanwhile went with them. It is registered here, as early as both loops
	// exist, so that every return below it is covered.
	//
	// How thorough that last write is differs between the two, and only one
	// half is in these files. The state store looks again for what landed
	// during its final commit (flushFinal); the index mirror's PersistLoop
	// flushes exactly once on cancellation, so an index change recorded
	// during *that* commit is still dropped — the same fault, one file along
	// in internal/library.
	defer func() {
		stopStores()
		<-stateDone
		<-persistDone
		select {
		case <-analysisDone:
		case <-time.After(analysisDrain):
			log.Info("the analysis was still reading at shutdown; its last vector may not have been stored")
		}
	}()
	// What has been watched is part of what the listing filters on, so the
	// library is given the positions the store just restored.
	all := st.All()
	watched := make(map[string]library.Watch, len(all))
	for id, p := range all {
		watched[id] = library.Watch{Pos: p.Time, Len: p.Duration}
	}
	lib.SetWatchAll(watched)
	// And how often each has been played, which the popularity views sort on
	// and the collections sum.
	lib.SetPlaysAll(st.Plays())
	// And the verdicts, which outrank the counts in the popular orders.
	lib.SetLikesAll(st.Likes())
	thumbs := server.NewThumbnailer(db, lib.Streaming, log)

	watcher, err := library.NewWatcher(lib)
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}

	go lib.BroadcastLoop(ctx)
	go watcher.Run(ctx)
	// A walk and the pruning that follows it are one operation, and this gate
	// is what makes them one. The library serializes the walks themselves,
	// but the prune is outside that: it takes its live set after the walk has
	// let go, so a second walk could start, index a newly added root and have
	// its records written by the persist loop before the first walk's
	// db.Prune ran — which then deleted exactly those records for being
	// absent from a set taken before they existed. What is lost is one batch
	// of index, metadata and thumbnail records, and with them the moment each
	// of those files was first seen, which puts them at the top of the Added
	// order as though they had arrived today. The three walks main starts are
	// the only ones there are, so one gate across the walk and the prune
	// closes that window.
	//
	// It does not close every window, and the remaining one is not this
	// file's: PruneDB takes its live set under a read lock, lets go, and only
	// then asks the database to delete everything outside it — so a file the
	// *watcher* indexes in between, whose record the persist loop writes
	// before the delete runs, is deleted for being absent from a set taken
	// before it existed. That is microseconds wide and the cure is to take
	// the snapshot under the library's own scan lock, which is not reachable
	// from here.
	var scanGate sync.Mutex
	// What every completed scan is followed by: the caches and the owner's
	// records of files that are gone are dropped, never on an empty index —
	// an unreadable root looks empty and would wipe the house.
	pruneAll := func() {
		if db != nil {
			lib.PruneDB(db)
		}
		if live := lib.LiveIDs(); live != nil {
			if n := st.Prune(live); n > 0 {
				log.Info("pruned positions and verdicts of files that are gone", "records", n)
			}
		}
	}

	// Initial scan in the background: the server is reachable immediately and
	// the UI fills in progressively via SSE while large trees are walked.
	go func() {
		// This goroutine ends with the analysis, which is the last thing on
		// it, so it is the one that says the database has no more writers.
		defer close(analysisDone)
		start := time.Now()
		scanGate.Lock()
		lib.Scan(watcher.AddDir)
		log.Info("initial scan complete", "files", lib.Size(), "took", time.Since(start).Round(time.Millisecond))
		// Album/artist totals are fed by their builds; an unchanged warm
		// start emits no change event, so seed them here or the header
		// would show zero until the first library change.
		lib.RefreshCounts()
		// Only now is the index known to match the disk, so only now is it
		// safe to throw away what the database holds for everything else.
		pruneAll()
		scanGate.Unlock()
		// Playback outranks thumbnailing, which outranks metadata reading.
		// What the background tiers stand down for. The analysis adds the
		// interface itself to that list (uiQuiet): it is the lowest tier and
		// the only one that runs for minutes at a time, and a viewer waiting
		// for a listing should not be waiting behind it.
		busy := func() bool { return lib.Streaming() || thumbs.Generating() }
		analysisBusy := func() bool { return busy() || lib.UsedWithin(uiQuiet) }
		lib.EnrichMeta(ctx, busy)
		// And reading how the music sounds comes after all of them.
		if cfg.analyze {
			lib.AnalyzeLoop(ctx, db, analysisBusy)
		}
	}()

	if cfg.rescan > 0 {
		go func() {
			t := time.NewTicker(cfg.rescan)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					scanGate.Lock()
					lib.Scan(watcher.AddDir)
					// A completed scan, like the first: what it found gone
					// is gone from the caches and the records too.
					pruneAll()
					scanGate.Unlock()
				}
			}
		}()
	}

	dist, err := fs.Sub(distFS, "web/dist")
	if err != nil {
		return err
	}
	// Both converters write into one working space with one budget, because
	// the disk is one disk. -tmp says where, -tmp-max says how much.
	scratch := server.NewScratch(cfg.tmpDir, cfg.tmpMax)
	log.Info("scratch space", "dir", scratchDirName(cfg.tmpDir), "limit", cfg.tmpMax)

	// Rewrapping shares ffmpeg with the thumbnailer; without it on PATH the
	// rewrapper declines everything and the converter stays the only route.
	remux := server.NewRemuxer(thumbs.FFmpegPath(), scratch, log)
	// Take over what an earlier run converted, and clear out what it left
	// half-written: a film already converted should not be converted again
	// just because the server was restarted.
	remux.Adopt()
	defer func() {
		if err := remux.Close(); err != nil {
			log.Warn("could not clear the rewrap scratch directory", "err", err)
		}
	}()
	// The segmented converter shares ffmpeg with the rest. It is what Safari
	// plays: a conversion piped down one response cannot answer the range
	// request that browser opens with, so on a phone a file that needed
	// converting did not start at all.
	hls := server.NewHLS(thumbs.FFmpegPath(), lib, scratch, log)
	hls.Adopt()
	defer hls.Close()
	srv := server.New(lib, st, thumbs, remux, hls, db, dist, log)
	if cfg.debug {
		srv.LogRequests()
	}
	// Find out what this machine can convert on before anybody asks it to.
	// In the background: it is a five-frame encode, but a machine with a
	// broken driver can take the whole probe budget to say so, and nothing
	// should wait behind that.
	go srv.FindHardware()
	// Changing what is indexed is one operation, not three: the list, the
	// watches and the index have to move together or they disagree until the
	// next restart. With no database there is nowhere to write the list, so
	// the change lasts the run and the dialog says as much.
	// -lock leaves the callback unset, which is what makes the endpoint
	// read-only: the preferences still report what is being indexed, and
	// every attempt to change it is refused. A server reachable from
	// somewhere its owner does not control wants this, since nothing here
	// asks who is calling.
	if cfg.lock {
		log.Info("directories are locked; the preferences are read-only")
	} else {
		// Two requests changing the directories at once are two
		// read-modify-writes over two stores with nothing between them: one
		// can write its list to the database while the other writes a
		// different list to the index, leaving the running library indexing
		// one set and the next restart reading the other — which is the very
		// disagreement this callback exists to prevent, and the dialog is
		// then answered with the list that lost. Nothing else serializes
		// them, there being no authentication here and so no reason two
		// clients cannot ask together.
		var prefsMu sync.Mutex
		srv.AllowRootChanges(func(roots []string) ([]string, error) {
			prefsMu.Lock()
			defer prefsMu.Unlock()
			if db != nil {
				if err := db.SetRoots(roots); err != nil {
					return nil, err
				}
			}
			lib.SetRoots(roots)
			go func() {
				start := time.Now()
				scanGate.Lock()
				// Old watches would report a directory that is no longer
				// indexed, and the watcher would put back exactly what the
				// scan is about to take out. The scan reinstalls them for the
				// new set as it walks. It happens here rather than before the
				// goroutine because a walk already running would otherwise go
				// on installing watches under the removed root behind the
				// reset, and nothing ever takes those away again.
				watcher.Reset()
				lib.Scan(watcher.AddDir)
				lib.RefreshCounts()
				log.Info("rescanned after a change of directories",
					"files", lib.Size(), "took", time.Since(start).Round(time.Millisecond))
				// Only after a completed scan, and never on an empty index: the
				// same rule the initial scan follows.
				pruneAll()
				scanGate.Unlock()
			}()
			return lib.Roots(), nil
		}, db != nil)
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Bind before serving so the actual port is known (-listen :0 asks the
	// kernel for a free one) and so a bind failure is reported as such.
	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	port := 0
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		port = a.Port
	}
	url := browse.URL(cfg.listen, port)

	// Tell the library where we can reach ourselves. /api/stream serves item
	// content with Range support, which is the only seekable view of a
	// member of an archive set there is — the thumbnailer and the metadata
	// probe read archived content through it instead of piping a prefix they
	// cannot seek in. A listener bound to one specific non-loopback
	// interface yields "", and both fall back to the pipe.
	// And tell the server, which hands a television on the LAN an address
	// it can fetch from — the same fact, pointed outward instead of at
	// ourselves (internal/server/cast.go).
	srv.SetLocalPort(port)

	if base := library.LoopbackAddr(ln.Addr()); base != "" {
		library.SetLoopback(base)
		defer library.SetLoopback("")
	} else {
		log.Info("no loopback address for the listener; archived media reads fall back to piping a prefix",
			"listen", cfg.listen)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "url", url, "roots", cfg.roots)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// The socket is bound, so connections queue even before Serve runs.
	if cfg.open {
		if err := browse.Open(url); err != nil {
			log.Warn("could not open a browser", "url", url, "err", err)
		}
	}

	var runErr error
	select {
	case runErr = <-errCh:
		// Fall through to the same drain rather than returning here: a bind
		// or serve error still has to flush the stores before db.Close, or
		// the deferred close races the final writes.
	case <-ctx.Done():
	}

	log.Info("shutting down")
	// The rewrapper stops before the drain rather than only in the defer
	// after it. For archived content its ffmpeg reads its input from this
	// server's own /api/stream, which is an ordinary in-flight request and
	// exactly what Shutdown waits for — so the drain was waiting on work
	// that only the code after the drain would release, and spent its whole
	// budget whenever an archived film was being rewrapped with nobody
	// watching it. A copy still being written is unplayable in any case, and
	// Close takes its .part with it, so nothing a viewer could have used is
	// lost by ending it here. The defer above stays as the backstop for the
	// paths that never reach here; Close is safe to call twice.
	//
	// The segmented converter is deliberately *not* closed here, though its
	// ffmpeg holds the same kind of read. HLS.Close deletes the directory of
	// every session still converting, and during the drain a viewer's player
	// is still asking for segments out of it — so closing it first would
	// answer them 404 for the five seconds they would otherwise have played.
	// Ending the ffmpeg while keeping the files is what this wants, and that
	// is hlsSession.stopConverting, which has no exported way in.
	if err := remux.Close(); err != nil {
		log.Warn("could not clear the rewrap scratch directory", "err", err)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		// Not a warning: Shutdown waits for active requests and does not
		// cancel their contexts, and a page with the event stream open holds
		// one open until it is killed — so a browser left on the library is
		// enough to spend the whole budget, every time. Said at all because
		// it is the one line that explains why an exit took five seconds.
		log.Info("shut down with connections still open", "err", err)
	}
	// The handlers have finished, or have had their five seconds: only now
	// are the stores told to write their last, in the deferred drain above,
	// and only after that does the deferred db.Close run. The server-error
	// path arrives here with ctx still live, so end that too — the watcher,
	// the broadcast loop and the background passes are on it.
	stop()
	return runErr
}

// scratchDirName is what to call the working space in a log line when the
// operator did not name one.
func scratchDirName(dir string) string {
	if dir == "" {
		return os.TempDir()
	}
	return dir
}
