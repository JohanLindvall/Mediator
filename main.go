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

	// Closed when PersistLoop has written its final flush, so shutdown can
	// wait for it before the deferred db.Close — otherwise the two race and
	// the last two seconds of index changes lose to "database not open".
	persistDone := make(chan struct{})
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
			lib.PersistLoop(ctx, db)
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
	go func() {
		st.Run(ctx)
		close(stateDone)
	}()
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
		start := time.Now()
		lib.Scan(watcher.AddDir)
		log.Info("initial scan complete", "files", lib.Size(), "took", time.Since(start).Round(time.Millisecond))
		// Album/artist totals are fed by their builds; an unchanged warm
		// start emits no change event, so seed them here or the header
		// would show zero until the first library change.
		lib.RefreshCounts()
		// Only now is the index known to match the disk, so only now is it
		// safe to throw away what the database holds for everything else.
		pruneAll()
		// Playback outranks thumbnailing, which outranks metadata reading.
		busy := func() bool { return lib.Streaming() || thumbs.Generating() }
		lib.EnrichMeta(ctx, busy)
		// And reading how the music sounds comes after all of them.
		if cfg.analyze {
			go lib.AnalyzeLoop(ctx, db, busy)
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
					lib.Scan(watcher.AddDir)
					// A completed scan, like the first: what it found gone
					// is gone from the caches and the records too.
					pruneAll()
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
		srv.AllowRootChanges(func(roots []string) ([]string, error) {
			if db != nil {
				if err := db.SetRoots(roots); err != nil {
					return nil, err
				}
			}
			lib.SetRoots(roots)
			// Old watches would report a directory that is no longer indexed,
			// and the watcher would put back exactly what the scan is about to
			// take out. The scan reinstalls them for the new set as it walks.
			watcher.Reset()
			go func() {
				start := time.Now()
				lib.Scan(watcher.AddDir)
				lib.RefreshCounts()
				log.Info("rescanned after a change of directories",
					"files", lib.Size(), "took", time.Since(start).Round(time.Millisecond))
				// Only after a completed scan, and never on an empty index: the
				// same rule the initial scan follows.
				pruneAll()
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

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	// The server-error path arrives here with ctx still live; end it so both
	// loops run their final flush, and wait for each before the deferred
	// db.Close runs under them.
	stop()
	<-stateDone
	<-persistDone
	return nil
}

// scratchDirName is what to call the working space in a log line when the
// operator did not name one.
func scratchDirName(dir string) string {
	if dir == "" {
		return os.TempDir()
	}
	return dir
}
