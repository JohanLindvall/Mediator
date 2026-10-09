# AGENTS.md

Guidance for coding agents working in this repository.

## What this is

A self-contained web media browser: a Go backend (module `github.com/JohanLindvall/Mediator`)
that scans/watches directories and streams video, images and music to a
TypeScript frontend embedded into the binary via `go:embed`.

## Commands

Every make target builds inside Docker (BuildKit); the host needs only Docker:

```sh
make docker           # runtime image (mediator:latest): gen-ts → npm → go build, all in Docker
make build            # same build; extracts the static linux binary to ./mediator
make generate         # regenerate web/src/types.gen.ts via the Docker gen stage
make test             # go vet (Linux, macOS, Windows) + go test -race ./... inside the image build
make vet              # go vet only, for the same three platforms

./mediator -listen :8080 -data data DIR [DIR...]   # run against media directories
./mediator -open DIR                            # free loopback port + open a browser
```

CI (`.github/workflows/docker.yml`) builds the `test` stage (`make test`) on
every push and PR; a push to main publishes `ghcr.io/johanlindvall/mediator`
(`:latest`, `:sha-<commit>`; a `vX.Y.Z` tag adds `:vX.Y.Z`, `:X.Y.Z` and `:X.Y`
but never moves `:latest`), stamped with
`git describe --tags --always` like local builds, with provenance, SBOM and an
attestation. amd64 only, deliberately: the runtime's `intel-media-driver` is
x86-only.

Optional flows that DO need a local toolchain (never required to build):

```sh
cd web && npm run dev                           # Vite dev server on :5173, proxies /api to :8080
go test ./internal/library -run TestAlbums      # single test (needs web/dist to exist — see below)
```

Dockerfile stage layout and gotchas:

- Stages: `gosrc` → `gen` (types.gen.ts) → `web` (npm ci, tsc, vite) → `build`
  (dist embedded) → `vet`/`test`; `make build`/`make generate` extract the
  `scratch` stages `bin`/`types` via `docker build --target ... --output`.
- `web/dist`, `web/node_modules` and `web/src/types.gen.ts` are dockerignored,
  so stale local artifacts are never embedded.
- `gosrc` copies `main*.go`, `cmd/` and `internal/` explicitly so frontend edits
  keep Go layer caches: **a new top-level Go package needs a COPY line**.
- Local `go build`/`go test ./...` must run from the repo root with a current
  `web/dist` (`main.go` embeds `all:web/dist`; build it with
  `cd web && npm run build`).
- `vet` also vets for macOS and Windows, tests included, since nothing runs
  there to notice a break: what only Linux has (`EUCLEAN`, `/proc`, inotify)
  lives in a `_linux.go` file with a `!linux` twin (`fsfault`, `procio`,
  `fswatch`), what only Unix has in a `_unix.go` one (`inode`), and a test
  needing a FIFO is `//go:build unix`.

## Generated TypeScript API model — single source of truth

Go structs alone define the wire format: `cmd/gen-ts` reflects their `json`
tags into `web/src/types.gen.ts` (checked in; `go generate ./...` via
`main.go`). When touching the API:

1. Never return anonymous structs from handlers; add/extend a named type in
   `internal/server/apitypes.go` (or the library/state types).
2. Register new root types in `rootTypes` in `cmd/gen-ts/main.go` (the
   generator panics on unregistered struct references).
3. Run `make generate` and commit both sides. Image builds always regenerate
   `types.gen.ts`; the checked-in copy is dockerignored and serves
   `npm run dev`.

`json:"-"` is omitted, `omitempty` becomes optional, and the `Kind` union
derives from `library.AllKinds`.

## Architecture

### The index and its persistence

- The index is mirrored into the blob db (`persist.go`, `items` bucket) so
  restarts serve it before the walk ends (36k files: 0.7 s vs 101 s):
  `SetMetaDB` tracks changes, `LoadFromDB` restores, `PersistLoop` batches
  every 2 s (failed writes stay dirty). `PruneDB` (deleting vanished files'
  records) and then the state-store prune on the same `LiveIDs` (nil when
  empty) run after **every** *completed* scan via `pruneAll` in `main` — never
  on an empty index, or an unmounted root would wipe the cache.
- A prune restores what it deleted from under the watcher, prune and repair
  being one function (`pruneTo`, called by `PruneDB`) so the repair always
  runs: items indexed after the live-set snapshot are re-marked dirty
  (`remarkAfterPrune`; cheaper than freezing the index), and their metadata is
  rebuilt from the item, since the restored "examined" mark keeps
  `needsEnrich` from re-reading — only where examined at the current shape
  recipe, so no withheld verdict is written. Thumbnails and feature vectors
  are deliberately not restored (made on demand; re-analysed next pass).
- A walk and its prune are one operation (`scanGate` in `main`, all three walk
  sites), or a concurrent walk's new records are pruned against an older live
  set. The watcher's microseconds-wide snapshot-to-delete window remains;
  closing it needs the library's scan lock, unreachable from `main`.
- An id is never both dirty and removed (one flush transaction would write then
  delete it; a recreated path keeps its id): `markDirty` and `upsert`'s
  creation branch clear removals, as `markRemoved` clears dirty; `flush` never
  deletes an indexed id; `SaveItems`/`PutPositions` apply removals before puts.
- Stores stop after the HTTP drain (on the `signal.NotifyContext` context they
  would exit before handlers in `ServeHTTP` record writes): flush loops use
  `storeCtx`, stopped by one deferred wait registered after the deferred
  `db.Close` (so LIFO runs it first, error paths included). The analysis
  goroutine (`PutFeatures`) is awaited there (`analysisDone`) for at most
  `analysisDrain` (2 s): it runs after the first walk and `Scan` takes no
  context, so an outright wait could last a whole cold walk; its context check
  and bolt's closed-db refusal cover the rest.
- Final flushes serialize snapshot-and-commit and re-check at most three rounds
  (`finalFlushRounds`, `finalPersistFlushRounds`), catching mid-commit writes
  without hanging on a failing database; a failed metadata write is restored
  only if nothing newer is pending. Order: cancel producers, bounded analysis
  drain, final commits.
- Rar members and DVD titles are never persisted: stale byte offsets would
  serve garbage, so the scan re-derives them.

### Scale invariants (measured at 150k files; keep them true)

- `List` serves pages from a cached per-(query, version) sorted result; never
  add per-request work that walks the index.
  - `queryMu` guards only that immutable result (`cachedQuery`), not page
    copies or counting (a narrowed count can trigger a disk-reading album
    build, stalling every listing).
  - A cache newer than the asker satisfies it: compare with `<`, never `!=`
    (`cachedQuery`, `perVersion.get` in `cache.go`, the watch version) —
    versions are read before the cache lock, so `!=` makes straddling callers
    rebuild and restamp older. Builds read the live index after stamping (data
    never older than its stamp). Never stamp a version re-read after the
    build: `buildAlbums` works outside the read lock, so stale data would pass
    as current. A listing reports the stamp of the answer it got, not the
    version it read, or clients refetch what they hold.
  - Ask for the affinity above the index lock: `affinities()` and the scaled
    vectors are lazy ~0.5 s whole-library rebuilds that would stall writers and
    readers. `Get` and `buildQuery`'s popular sort do, as `stamper` does; `Get`
    uses one `stamper()`, not a warm-up plus a stamp (each ask compares release
    verdicts by content).
- Watch progress is the library's (`watched.go`), since listings filter under
  the index lock: positions are pushed in, reduced to started/finished/neither
  (under five seconds: no start; past `WatchedFraction`: finished, no resume).
  The client uses `START_FLOOR_S`/`WATCHED_FRACTION` (`playback.ts`) via one
  tested `watchState`, so tiles, chips and `resumeStart` agree.
  - Positions bump `watchVersion`, so only watch-filtered queries rebuild.
    Totals are cached per (version, watchVer) from the watch map ∩ index;
    `commitWatchTotals` refuses older stamps (they would force re-walks),
    monotonic counters sparing it `watchMu` across the index lock.
- `Counts` is O(1): `upsert` and `dropItem`, the only doors in and out, keep
  per-kind totals (`countKind` nowhere else); collection totals ride on their
  caches.
  - `CountsFor` (`counts.go`) is one index pass per narrowed view, cached per
    (search, flags, group version), eight answers deep (faced or confined
    callers ask two per page). It reads albums, performers and genres before
    taking its lock: grouped caches lock unconditionally, so a rebuild would
    block every narrowed count.
  - Hidden totals carry the version their walk saw (`hiddenCounts`,
    `annotate.go`), never the one asked, or one stamp would cover two indexes;
    older callers walk for themselves. `hiddenMu` spans the walk and versions
    only rise, so no backward check is needed. Totals and hidden counts take
    separate locks, so a response may briefly disagree.
  - The album chip clamps at 0: audiobook and album totals publish at two
    moments of one build (fix: publish together). Never read both under the
    album lock: `Counts` rides every listing.
  - Items count as items, albums and artists as collections. `CountQuery`
    carries artist and search, so a performer's chips count their tracks,
    releases and the one artist (by artist tag; videos read 0).
  - Only the visible view writes chip numbers (off-screen sources hold older
    questions). `countsToShow` (`query.ts`, tested): totals when unnarrowed,
    matches once answered, none while pending; last hits stay only while
    `countKey` (the narrowing sent to the server) is unchanged. `narrowed`
    (tested with `countKey`) covers performer, genre, show and sounds-like,
    not search (hits stay while typing). A view that fetches nothing shares its
    source's key (seasons: shows), or its chips stay blank. Every drill-down
    and exit goes through this.
  - `List` and the album and artist endpoints return `Matching` beside
    unfiltered `Counts` ("no matches" is not an empty library). Only the
    broadcast loop rebuilds them (`RefreshCounts`, once per coalesced event);
    `main` seeds them after the first scan (a warm start emits no event).
- Enrichment buffers results (`queueMeta`) for bulk writes; a commit+fsync per
  track caps cold passes at a few hundred items/s (135k: 10m42s vs ~20s).
- Natively timed videos get no eager ffprobe; `EnsureCodecs` probes on open,
  once per item per process. `Probe.Probed` means ffprobe answered
  (`ffprobeResult.answered`: ran, printed a parseable document), never "found
  a duration" (many containers never do) or "tried" (no ffprobe, unreadable
  member, killed run). It takes the caller's context and a `probeSem` slot,
  deliberately not waiting for playback (the player needs the answer). Nothing
  is recorded on context expiry, ffprobe's own ceiling (`Probe.Interrupted`),
  or a changed identity.
- Album/artist endpoints send `ETag: W/"v<group version>.<watch version>"` +
  `Cache-Control: no-cache`; `writeJSON` defaults to no-store only without a
  handler policy. The frontend throttles those reloads to one per 2 s in
  bursts; `LibrarySource` evicts pages beyond 64 so a long scroll cannot pin
  the library.

### Scanning, watching and change propagation

- `extKind` (`scan.go`) decides what is media, every entry measured. `.ts` is
  in (captures, opening with the TS sync byte; `remuxable` copies their
  H.264/AAC); deliberately out of the table: `.dat` (anything at all by name;
  sniffing admits the one whose bytes are a VCD's MPEG) and `.tif` (a decoder
  dependency for two files).
- **A file whose name says nothing has its first bytes read** (`sniff.go`,
  `SniffContent`): no extension, or one this library does not know
  (`nameSaysNothing`: a dotted release name, a suffix a tool cut short, a type
  it has no use for), from `sniffMinSize` (1 MiB) up. Left alone: a known
  name, which has answered; an unfinished download (`unfinished`: `.part`,
  `.crdownload` and kin), renamed to its real name when whole and indexed
  then, where read now it would show twice; a part of a rar or zip set, which
  its set's first part answers for; and anything smaller, likelier a
  repository object or a lock file (measured: 462 of 138,579 files over the
  floor, 301 of them unfinished). `kindOfMagic` and `archiveOfMagic` are pure,
  tested tables of fixed-offset signatures — no scanning, guessing or
  statistics, as a wrong answer hands a program to the player: ISO base media
  (an unknown brand reads as video; `EnsureCodecs` settles it), EBML, RIFF AVI,
  WAVE, WebP and CDXA (a VCD), ASF, Ogg, FLAC, ID3, an MPEG program stream's
  pack header, a transport stream's sync byte three packets running, the
  pictures, and **ZIP and RAR archives, which are read as any other archive
  is** (`indexZip`, `indexRarSet` — a single file whatever its name), by the
  walk and the watcher alike. `Name` never changes; `opensDirectly` lets
  unknown containers try, `mimeFor` falls back on the kind, the mirrored record
  keeps it.
- `internal/library` holds the index (`map[id]*Item`,
  ID = `sha1(abs path)[:16]`), fed via `upsert`/`removePath` by `Scan` (which
  reconciles deletions), the watcher (`watch.go`, every directory) and
  `-rescan` (default 10m).
  - New directories are re-walked (`settleWalks`: 5 s, 30 s, 2 min) after
    `walkNew`: files created before the watch exists are never reported, and
    archive sets need their last volume. `maxSettles` bounds outstanding
    re-walks, not timers (a moved-in tree is one Create per directory);
    overflow is left to the rescan. Settle timers live on the `Watcher`,
    numbered (`armSettle`: a timer cannot name itself to its callback),
    dropped by `Reset` and `Run`'s return; `forget` decides whether the
    callback or `stopSettles` returns the slot, and a callback finding none
    does not walk.
  - Watches are refused outside the roots (`AddDir`, `rewalk`, `AddFile`):
    timers and walks begun before a change outlive `Reset`, leaving watches
    nothing removes.
  - A changed file forgets what was read (`forgetContent`: duration, codecs,
    shape, track/caption lists, looked-for marks) in both upserts; a DVD title
    then restores its declared length (`declaresDuration`). `Chmod` re-reads
    the file: copy tools stamp times last, and the mtime keys the metadata
    cache, grid cell and thumbnail URL.
  - Closes are events, writes are not (`fswatch.go`, `fswatch_linux.go`):
    `IN_MODIFY` fires per `write()` (tiny writes to `.part` files flooded the
    16,384-event queue at 464k events/s) and the kernel merges only identical
    tail events. fsnotify cannot set the mask (`WithOps` unexported through
    v1.10.1), so Linux speaks inotify (`IN_CLOSE_WRITE`, `IN_ATTRIB`,
    `IN_CREATE`/`IN_MOVED_TO`, `IN_DELETE`/`IN_MOVED_FROM`/`*_SELF`,
    `IN_ONLYDIR`); elsewhere fsnotify sits behind `fsBackend`, a write
    counting as a close. The non-blocking fd is a pollable `os.File` (`Close`
    wakes the reader); keep the raw fd for watch calls, as `File.Fd()` would
    make it blocking. A growing file's size updates on close or rescan.
  - A `Q_OVERFLOW` burst is one loss, answered by one walk after it (mid-burst
    it would be stale): `overflowQuiet` (10 s) after the last, `Run` logs the
    count and signals `Watcher.Lost()`; `main` walks via `rescanNow` even with
    `-rescan 0` (that disables timed walks, not correctness).
  - Reconciliation stats unseen paths lock-free (a slow disk must not stall
    readers), deleting under the write lock after a recheck; containers
    reconcile against their own `members`. Member paths hold a NUL (`os.Stat`:
    EINVAL), so the container is asked, once; a failed parse is no verdict.
  - An item whose `FirstSeen` follows the walk's `startedAt` (set after
    `scanMu.Lock()`) survives an empty stat for one cycle (its path may have
    been recreated mid-stat); where the disk answered, or it became a
    duplicate, the rules still decide.
  - Sidecars reconcile like items, dropped only when gone or not `UnderRoots`
    (a removed root's sidecars go with its films); a change sets both counters
    so the closing notify fires.
  - Scans serialize on `scanMu` (rescans and prefs changes overlap cold
    walks): concurrent walks would clear `byInode` and reconcile different
    `seen` sets, misreading hard links and dropping each other's files.
  - Watcher enrichment is debounced (`enrichAfterQuiet`, `enrichQuiet` 2 s, a
    var only for tests): one read after a file's last close or stamp, then a
    notify (the watcher paths' publish contract). Every event arms a fresh
    timer, never resetting the mapped one: `Reset` cannot tell a pending timer
    from one fired and blocked on the mutex, and a stray one deletes a live
    entry, letting reads land mid-write. The reads take a process-wide slot
    (`enrichSlots`, `enrichWorkers` = 4; one disk) and wait out playback
    (`standDownForPlayback`, `Library.Streaming`) at most `enrichBusyWait`
    (30 s), since the waiter holds a scarce slot.
  - `Run` only receives events, keeping the kernel queue (which drops overflow)
    drained; one worker works them in arrival order (Create before close) via
    `eventQueue` (4096, blocks when full). Shutdown: close the queue, wait for
    the worker, stop pending container reads, settles, then the watcher, so
    nothing arms a read after the worker; queued events are abandoned
    (draining them would overrun shutdown).
  - A container's parse and its `indexStored` reconcile are one
    read-modify-write, held whole per container (`containers`,
    `enterContainer` in `scan.go`): watcher events and timers skip `scanMu`,
    and an older parse finishing last would drop new members or install stale
    ranges. Keyed by path alone (two libraries over one file must exclude each
    other), refcounted; order container → `l.mu`, never `enterContainer` under
    the index lock. Walk and watcher name a container identically (rar: first
    volume; zip: `.zip` or first piece; disc image: itself; DVD folder: its
    VOB directory), or the hold does nothing; tested, divergence being silent.
  - Members are read once the writer is quiet (`readMembersSoon`/
    `readMembers`, `containerQuiet` 2 s), as a growing set moves every
    member's mtime: one read pending and one running per container at most.
    Clear the running flag in a defer, or the container is stranded forever;
    `enrichOne` defers its marks too (`tag.ReadFrom` panics on corrupt files).
    `stopMemberReads` drops pending reads on `Watcher.Reset` and `Run`'s
    return, per library (it is in the key), so none reads outside the roots
    or a closing database; running reads finish, and `Run` drops them only
    after draining the worker.

- Every mutation calls `notify()` → version bump → `BroadcastLoop` coalesces
  bursts (400 ms) → `Event` → `/api/events` SSE → the frontend drops its page
  cache and refetches the visible window; versions let clients skip no-op
  refreshes.
  - **Two versions**: `version` counts every change, `groupVersion` only
    changes to what the library holds (files, tags); mere growth bumps only
    `version` (`notifyBytes`, decided in `upsert`). Derived views (albums,
    artists, genres, shows, narrowed counts, and via the release verdicts the
    affinity) and the across-kind `Counts` totals (`hiddenCounts`,
    `watchTotals`, `playedTotal`) key on `groupVersion`, or a download
    (~45 bumps/s) rebuilds them per request (searches 270 ms → 10–50 s). The
    listing keys on `version` (it shows and sorts by size; its rebuild is just
    a filter and sort). Cost: release size and mtime sums lag until tags are
    read. A rescan that saw only growth bumps only `version` (`held` in
    `Scan`).
  - Grouped views rebuild lazily per version via `perVersion` (`cache.go`,
    also holding the chips' total) and share one sort rule (`orderBy`: having
    the key beats lacking it in either direction, then name, then id) and the
    common keys (modified, added, size, tracks, popularity, length) via
    `collection`/`compareCommon`.
  - **Added is not modified**: added is `Item.FirstSeen` (first sight, kept in
    the mirrored record); a collection's is its newest member's. Releases and
    performers offer it in menus; genres and shows sort by it without one. A
    playlist album uses its tracks' arrivals; an archived member or DVD title
    its container's time (`upsertStored`) — never persisted, it would
    otherwise be dated today on every restart.
  - An mtime later than now plus `mtimeSlack` (a day) is shown but sorts as
    unknown, after all real times either way (`knownTime`, `mtime.go`,
    tested; `knownKey`); a collection's modified time uses real member times
    only (none: sorts last). Old times are never judged.
- `List` filters, sorts and pages under `RLock` and returns copies:
  enrichment (`enrich.go`) mutates items under the write lock, so never hand
  out `*Item` across the lock. `buildAlbums` groups copies too (`cp := *it`
  under the read lock, playlist branch included), as it reads them after
  unlocking. The album sheet's tracks come through one stamper and lock
  (`tracksOfAlbum`), not a `Get` per track. Enrichment results persist in
  `meta` (id + mtime/size). `duration.go` reads mp4/m4a/mov, mp3, flac,
  ogg/opus and wav headers in pure Go; one optional ffprobe adds video codecs
  and missing durations. Archived video is probed over the loopback URL
  (`probeItem`), which can range to an index at the file's end that no piped
  prefix holds. `ProbeMedia` takes the caller's context, never a background
  one (`EnrichNow`/`EnrichSoon` call it from waiting requests).

### What is indexed, and search

- A `Sample`/`Samples` subdirectory is skipped when its release is present
  (`isRedundantSampleDir` → `releaseNear`), else kept; the release is sought
  to `sampleReleaseDepth` (2) below the parent (multi-disc releases hold only
  disc folders, and a disc may be a folder of parts), and another sample
  folder never counts. A sample file beside the film (`isRedundantSampleFile`)
  is skipped only if it is a video ("sample" means something else among
  music) and the directory holds a release ≥ `sampleRatio` (5×) its size or
  an archive set — never by name alone. `namedLikeASample` matches the word
  wherever punctuation sets it off, never splitting on spaces ("Sample Text"
  is a title). Reconciliation asks `stillIndexable` before keeping a path
  still on disk (which also drops new duplicates).
- Duplicates collapse by `(device, inode)` (`dedup.go`; inode alone collides
  across volumes): the first claiming path is indexed, but a real file beats
  a symlink to it in either order. `Scan` clears `byInode` before each walk:
  claims reconcile only afterwards, so a deleted path's claim would drop its
  surviving hard link. `upsert` returns `(changed, dup)`; a duplicate stays
  out of `seen` so a demoted path is removed. `statEntry` follows symlinks
  (`DirEntry.Info` is an lstat).
- Search (`search.go`): items index lowercase word runs of name + absolute
  path + tags; every query word must be a substring. Rebuild `lower` via
  `indexText` whenever its inputs change, at every door (`upsert`,
  `upsertStored`, a repaired path, `setMeta`, `LoadFromDB`). Absolute, not `Rel`, which starts at
  the root's base name and hides where the root is. Albums index
  name/artist/genre/year plus their directory or playlist file (`fillAlbum`),
  so a file-listing hit answers one view across, and sort by a separate
  `sortName`; artists index no place.
  - Since the search persists across views, a release also matches through
    any one visible track holding every word, and a performer through any
    matched release (`albumsAnswering`, `performersAnswering`; shows via
    `answering`); tracks are checked only for releases their own text missed.
    Album and artist listings, like-this, chips and queue-all share this;
    genres do not.
  - **Results rank by how directly they answer** (`rankByHit`, after every
    view's sort — items, releases, performers, genres, shows — and so for
    queue-all too): first what the search names, word for word as `tokenize`
    reads words (`hitName`); then what shows every word on its card in this
    view (`hitCard`: a track's title, performer, release, genre and year; a
    release's title, performer, genres and year; a performer's name, genre and
    years; a genre's or a show's name); then what was found further away
    (`hitBeyond`: a folder, a file name behind a title, a release's tracks, a
    performer's releases, a genre's bands, a show's episodes). Each tier keeps
    the view's sort, either direction. The text is built in those three
    segments with where the first two end (`segmentedText`, `nameEnd`,
    `cardEnd` on each item and collection), so a tier is a slice compare and
    a substring check, never a tokenize per search.
- **A release spread over discs is one release**: disc-named directories
  (`CD2`, `disc 3`, `disk-4`, `CD 1-…`) fold into their parent. `discPattern`
  wants a standalone number and whole words, and reads numbers spelled out up
  to ten (past that everyone writes digits; each word could begin a title);
  `discSuffix` takes spelled forms too, or folder and tag disagree on the
  name. `foldDiscs` folds only where a release has more than one disc (a lone
  one would merge unrelated neighbours), one level deep. Tracks sort by disc,
  then number; the album-name vote strips disc markers (`albumTitle`).
- `dropDuplicateAlbums` drops a directory album when a playlist lists exactly
  its tracks (the playlist has the running order); partial or cross-directory
  playlists stay separate. It runs before artists are grouped, so they count
  each album once.

### Plays, verdicts and how music sounds

- **Plays are counted by the client** (`POST /api/plays/{id}`, `plays.go`),
  since a byte response cannot tell a play from a seek or a range request:
  after `PLAY_AFTER` seconds of playing time on the element's clock, counted
  from where the file was loaded — deliberately the "started" floor (5 s), as
  two floors for one idea drift. Handing to a television counts at once.
  - The count shares the position's state record; `Set` reads before writing,
    or position saves would reset it.
  - A play bumps the library `version` (a position, saved every few seconds,
    bumps `watchVersion`), refreshing the per-version collections (which sum
    plays), their ETags and the chips. Counts are pushed into the library
    (listings must not reach another store under the index lock) and stamped
    on handed-out copies, never on indexed items, which walks rebuild.
  - Popular is a filter (played or judged), not only a sort, lest the
    untouched majority bury the rest.
  - Verdicts (`likes.go`, `POST /api/like/{id}`) travel like plays: same
    record (`Position.Like`), pushed into the library, stamped on copies,
    bumping the version;
    in the library both are `ownerCounts` (own lock, a generation the
    affinity cache checks). Popular orders sort on `popularity(like, plays)`,
    the verdict 40 bits above the count; a collection's verdict is net. The
    key is `popular` (`plays` an alias for old addresses). The lit thumb
    withdraws; empty records are dropped; tiles show the verdict beside the
    count.
  - The state store and the library copy are written in one turn per id
    (`Server.owning`, `server.go`): the values are absolute (`Play` returns
    the new total), so unordered writes land out of order. 64 stripes
    (`ownerStripes`, FNV-1a over the hex id; cheaper splits cluster, a per-id
    mutex map needs pruning), taken by `handleStatePut`, `handlePlay`,
    `handleStateDelete`, `handleLike`.
  - Turns, busy gates and locks are always released by `defer`, in a closure
    so the turn returns before the answer is written: net/http recovers
    panics, so a plain release leaks for the process's life (a stuck stream
    count throttles all background work; a stuck stripe wedges every write
    hashing there).
  - Owner flags have their own lock (`flagStore`, `annotate.go`), not the
    index's (bolt commits can take seconds). Concurrent first callers share
    one load (else each request of the first burst re-reads the bucket and
    bumps the version); `LoadFlags` takes it too. `SetFlags` holds it from the
    decision (under the index lock) through `SaveFlags`, so bolt sees writes
    in decision order. A load finding the work done discards its read whole,
    never merging per key: a withdrawal is a deletion, which a merge would
    undo.
- **How music sounds is read once in the background** (`analyze.go`,
  `features.go`), the lowest tier: one track at a time, only while `busy()`
  reports no stream or thumbnail and no tag pass runs (`enriching`), resting a
  minute between passes. ffmpeg decodes three 20 s windows at ¼, ½, ¾
  (`analysisOffsets`, skipping front matter) by path or loopback;
  `extractFeatures` yields 56 numbers (pure-Go FFT; MFCCs, spectral shape,
  chroma, loudness, dynamics, tempo with a prior toward 120, three speech
  cues).
  - Vectors live in `features`, stamped with mtime, size and
    `featuresVersion` (like `shapeVersion`); restored at startup
    (`LoadFeatures`), pruned with the item, never re-read for an unchanged
    file (~1 s per track). Pauses count within a window, never across seams;
    older vectors are kept on purpose (`featuresVersion` unchanged: the rule
    only lowers pause shares, old vectors lean toward the tuned verdict, and
    re-reading costs a core-day). `-analyze=false` disables it; without
    ffmpeg it does nothing. A failed decode is remembered for the run; a
    silent track's empty vector restores as "nothing to say"; a failed db
    write keeps the vector in memory (not a decode failure); a pass publishes
    only if it read something.
  - Windows are described separately and merged (`extractFeaturesFrom`), as
    concatenation fakes an onset at each seam. Flatness takes one log per
    eight bins. An unmeasured track is sampled at fixed marks, else from the
    start. Synthetic-signal tests pin the arithmetic.
  - A timeout is not a failure: `analyzeOne` bounds a track by
    `analysisTimeout` (90 s, a var for tests). The pass checks the parent's
    `Err` first (a shutdown writes nothing), then maps
    `DeadlineExceeded`/`Canceled` (`errors.Is`) to `markInterrupted`, which
    records only a retry time (`analysisRetryAfter`, 1 h, rather than every
    pass) that `needsAnalysis` honours. `markFailed` would drop
    the track for the run from vectors, resemblances, radio, its release's
    sound and the audiobook vote.
  - `readyForAnalysis` (a measured length, or the tag pass done) gates both
    `analysisTodo` and the pass itself (the file may have changed;
    `forgetContent` clears its length): windows come from the duration, a
    wrong-seconds vector is stamped final, and an unready queued track makes
    the loop spin empty passes. It stays out of `needsAnalysis` ("still to be
    read", for other callers).
  - The write is skipped once the caller's context is gone — a narrowing
    (bolt refuses writes after `Close`); shutdown waits via `analysisDone` in
    `main`.
  - Memos never go backwards: `putScaled`, `putAffinity`, `putSounds` install
    only over something not newer (lock-free builders overlap; an older
    install makes every reader rebuild). An affinity build whose release
    verdicts (replaced wholesale per album build, compared by content) no
    longer match the library's is not installed.
  - One reading of release verdicts per answer: `Similar` filters and stamps
    from the stamper's set; `affinities` hands its verdicts to `spokenWith`
    instead of `spokenSet` re-reading. Two readings can straddle an album
    build.
  - **Shown tempo** (`tempo.go`, `Item.BPM`, `sort=tempo`): column 50 is never
    shown (5–10 BPM whole-lag steps) nor changed (vectors compare only under
    one recipe). `bpmOf` reads the decode's onset envelopes
    (`describeWindows`): candidates a quarter beat apart, scored at their
    period and three multiples under the prior toward 120, the correlation
    read between frames (`correlationAt`). Tags are ignored (2% carry one,
    mostly 0 or a bitrate). Not done: period from multiple peaks (no gain),
    envelope smoothing (hides real tempos), a drawn beat (phase unknown).
    - Floors, each with a test showing the tempo it prevents
      (`TestAnEncodersFlickerIsNoTempo`): onsets floor each band `tempoTopDB`
      (80 dB) under the loudest (MP3 encoders' empty bands flicker like
      drums; the vector's onsets stay unfloored), and the strongest must reach
      `bpmOnsetFloor` (residue peaks 0.4–3.4, the weakest real track 15).
      Clarity under `bpmClarity` (0.3) shows nothing; spoken tracks have none
      (`stamp`). A strong vibrato can still read as a tempo.
    - Stored apart (`blob.PutTempo`, `tempos`, `tempoVersion`, pruned,
      `LoadTempos`). A current non-empty vector lacking a tempo is re-read for
      it alone (`needsAnalysis`) without rewriting the vector (`analyzeOne`
      reports freshness); tempo batches publish via `publishRead` without
      moving the features generation, which would rebuild resemblances for
      nothing. Stamper and order read one map (`publishTempos`); none sorts
      last.
    - Offered where a listing is only tracks (`listsTracks`, `content.ts`):
      the music chip, plus All and Popular on a music-only face; `sortOptions`
      takes the face at both key checks, and boot reads the address only after
      `/api/info`. Shown whole (`tempoLabel`) on tile, sheet (not under
      480px), bar and hover.
  - Similarity is cosine over per-column z-scored unit vectors (`similar.go`),
    brute force deliberately (56 × 100k is milliseconds; an index is one more
    thing to keep right). `Similar` keeps the n best in a sorted slice,
    building a candidate's recording key only once it beats the last (keys
    dominate a radio top-up); releases and performers rank via `nearest`.
  - One recording once (`RecordingKey`, tagged performer + title): only the
    nearest copy is kept, copies of the seed dropped. Untagged files and
    titles naming nothing (`namesNothing`, as `PLACEHOLDER` in `queue.ts`) get
    no key (distinct songs). `FoldRecordings` folds every `of=` answer in
    `handleTracks`, keeping the first (arrival order is what was asked); a
    performance titled as one ("… [Live]") survives — the key is the title,
    not the sound.
  - `Similar` backs "more like this" and radio (`of=similar` on
    `/api/tracks`). A release's or performer's sound is its tracks' mean vector
    (`sounds`, per version and features generation); `near=` on album and
    artist listings orders by it, stamps `Similarity`, counts via
    `countsOfAlbums`/`countsOfArtists`, offers only "Similarity" (direction
    the viewer's) and opens most alike first (`showNear`).
  - Affinity: greatest resemblance to a liked track minus to a disliked one,
    graded −2..2 (`affinityBucket`, 0.35/0.6), stamped with `Akin`; it ranks,
    stays on the wire, and is never drawn (the owner wants no mark).
    `trackPopularity` is verdict, affinity, plays; collections use
    `popularity`. Rebuilt only on `likesGen`/`featuresGen` or changed release
    verdicts, compared by content (`sameMap`; each album build makes a fresh
    map).
  - Pages, similar tracks, the queue and `Get` stamp from one `stamper`
    snapshot. The owner flags' lazy load belongs to `stamper()`, which every
    producer (album sheet included) builds before taking the index lock, or a
    cold request reports nothing hidden, favourite or turned.
  - Speech (`spoken.go`) is scored on the stored raw vector (scaled columns
    lose their mean), tunable without re-analysis; weights live only in that
    file's comment, the threshold mid-gap. A song taken for a book leaves the
    music while a book taken for music stays filed, so: unread is music;
    pauses count only between first and last sound (column 52); a track needs
    a full window of sound (column 55, `spokenMinSound` 19 s, as a 20 s
    window measures 19.99; `spokenVerdict`) or is music; a release votes by
    playing time and needs `spokenMargin` (2:1, not a bare majority; both
    tracks of a two-track release); no analysed verdict before half its
    playing time is judged (`markSpoken`; quiet intros read like pauses); a
    genre naming an audiobook (`spokenGenre`) shelves outright; a track's
    mark follows its release (`byRelease`, from the album build, read by
    `spokenOf` without forcing a build, which would wait on itself under the
    index lock).
  - Audiobooks (releases so judged or tagged) get their own chip
    (`audiobooks=1`, `Counts.Audiobooks`, kept out of `Counts.Albums` so the
    two add up), leave the Albums, Artists and Genres views and queue-all
    groupings (a narrator is not a performer), match "audiobook", and
    `Similar` answers music with music and readings with readings only.
  - Analysis publishes every `analysisReport` tracks and at a pass's end
    (`publishAnalysis`), never per track: the version (collections) and the
    features generation (scaled vectors, resemblances, affinity — rebuilt on
    the request path, so per-track moves make every listing re-z-score the
    library). The pass writes with `putFeatures`, which publishes nothing;
    `SetFeatures` (loader, tests) publishes.
  - **Radio** (`audio.ts`): the remembered toggle tops up with similar tracks
    when fewer than `RADIO_AHEAD` follow, on each track change and at the
    queue's end (moving the parked player on); `append` is enqueue without
    toasts.
    - It draws `RADIO_BATCH` from a `RADIO_POOL` (50) with position-linear
      weights (`pickRadio`, tested), never the head (which would play one
      neighbourhood in one order for ever).
    - A performer's weight falls by `ARTIST_DAMP` (a third) per track drawn
      or among the last `RADIO_MEMORY` queued — multiplicative, not a quota,
      so a one-band neighbourhood still fills a batch.
    - Nothing queued returns (`freshForRadio`), file or copy. Memory is by
      song (`songKey`): `songTitle` strips version tails (words from the
      library's titles; "(Part II)", "(Reprise)", "(Intro)" kept; prefix match
      only for four unambiguous compounds); accents, punctuation and "&" are
      spelling; apostrophes are dropped, not split at (`folded`); a guest in
      the artist tag is still the performer's. An untitled file keys by its
      name's title (`trackTitle`; a wrong fold costs less than a repeat); a
      letterless name has no key.
  - **Artist radio** (`station.go`, `of=station`; `topUpStation`,
    `startStation`) keeps the queue to one performer: one setting with radio
    (`RadioMode`, `media.radio`, "1" = plain radio); bar buttons fold into "⋯"
    on a phone; a performer page's chip starts a new queue.
    - A station is the whole catalogue, nearest the seed first (a 50-pool of
      one band runs dry), unanalysed tracks after by popularity so it never
      falls silent; no seed means popularity alone. The bar draws
      `RADIO_POOL` from the unqueued head, undamped (`pickRadio` `damp` 1;
      damping favours tracks naming a guest).
    - Whose a track is (`performerOf`): its tag decides whenever it names
      anyone, after `withoutGuests` ("feat.", "ft.", "featuring", or a bracket
      opening on one; a bare "feat" outside brackets stays). The release
      credit applies only where the tag names nobody or leads with the
      release's performer (`leadsWith`: "A, B", "A/B", "A;B", "AB", wanting a
      separator or capital after). Never fall back to it because the tag's
      name has no release (that admits split partners and tribute bands).
      Stations are asked for by seed, so every track a station answers seeds
      it again (tested).
    - It ends rather than repeats (`stationSpent`) until the queue, setting or
      manual queueing changes.
  - `radioAsk` generation-guards radio requests: a new setting or queue
    discards answers in flight and frees the busy flag.
  - The queue is effectively unbounded (`QUEUE_CAP`, `maxQueue`: a million):
    extended by loop (spreading 100k arguments overflows the stack), painted
    as a window over one tall spacer (`paintQueue`, `Q_ROW` pinned in the
    stylesheet); "play from here" uses queue-all's single answer.

### Genres, television, intros and performers

- **Genre tags** are cleaned through `cleanGenreTag` at **both** metadata
  doors, `setMeta` and `LoadFromDB`, or a restart would not tidy what a
  re-read does. `cleanGenre` folds whitespace (else a lookalike second card)
  and collapses an exact doubling ("X X", "X/X") left by ID3 numeric
  references (a reader expands "(138)Black Metal" and keeps the text too), for
  genres only: a title may repeat itself. `splitGenres` splits on pipe,
  semicolon or comma, **never on a slash**, which mostly joins one compound
  genre ("Black/Death Metal"; 48 slash tags vs 19 lists). Albums carry
  `Genres` beside `Genre` (the first: sort key, caption); grouping walks all,
  the drill-down matches any.
- **Television is parsed from names** (`series.go`); half the episodes are
  named only by a "Season N" folder.
  - Series name: the **shallowest directory marking a season with text before
    the marker** (a bare "Season 5" defers to its parent); the file comes
    last, often naming the release group. **A file is an episode only if its
    name says which episode**; the bare pack "S02" (two digits) and "Series 2"
    are directory-only forms. **Markers are whole tokens** (else "-S74tb48v"
    is season 74); "1x02" needs real numbers and no screen shape ("03x00",
    "9x16"). **A series needs more than one episode** (lone clips are the
    false positives; the file stays listed).
  - Parsed at all four index doors, warm start included (the mirrored record
    has no series field). **Video's own**: no video, no shows; a restricted
    face counts shows itself, by the same rule (more than one visible
    episode).
  - **Search finds a show by name or by any one episode's own text**
    (`answers`, `answering`, shared by `SearchSeries` and `CountsFor`;
    `Item.lower` kept as `eps`), as the file listing does, after grouping over
    what the caller may see (hidden episodes reveal nothing). Matched only
    through some seasons, a show is a copy naming them (`Series.Matched`) for
    the seasons view (`seasonsOffered` in `query.ts`, tested), whose listing
    searches episodes too; the card stays whole. The seasons view refetches
    shows when the search changes (`CollectionSource.searched`).
    Hidden/favourite flags do not affect show counts.
  - **A restriction regroups** (`AllowedSeries`), never filters the cached
    list, so shows, counts, running times and seasons are the reachable ones
    (paid only with the header set). **A show's face is a total order**
    (`before`: season, number, id; season covers' mtime ties likewise; albums:
    `ID` after `lower`), or tiles cycle as the list rebuilds.
  - **Seasons ride inside the show**; episodes are an ordinary listing sorted
    by episode, not a sheet, so the player steps through them. Running times
    only when every episode is measured.
- **Intros and credits are detected from audio** (`fingerprint.go`,
  `skipdetect.go`, `skips.ts`, `GET /api/skip/{id}`), never asked for (no
  dialogs): a season's episodes share opening and closing audio.
  - The first `skipHeadWindow` (10 min, ≤40% of the episode; cold opens run 5+
    min) and last `skipTailWindow` (4 min, ≤30%) are fingerprinted
    Haitsma–Kalker style (32 bands 300 Hz–2 kHz, 16 frames/s, 31
    band-difference sign bits: level-invariant, nearly EQ-invariant;
    `fpMaxBits` 9), with **one bit for silence**, or silent openings match.
    The intro is the longest opening stretch another episode shares; credits
    likewise.
  - A match is a run at one alignment (`commonRun`, walking every alignment in
    tens of ms), riding up to 1 s of misses with ≥3/5 matching; `trimRun` cuts
    its ends to dense matches (chance matches, 1 in 60, would pad them and win
    the vote). **Neighbours must agree**: two answers within 4 s from the
    nearest episodes (until three answer or six are asked; one may share a
    recap); a lone answer stands only if all others were asked. Over
    `skipMaxIntro` (3 min) is repeated footage; an intro running into the
    window's end takes the season's typical length from its start
    (`typicalIntro`, median of whole ones). Marks to 0.25 s; credits as
    seconds **before the end**.
  - **Lowest tier**: `SkipDetectLoop` on the music analysis's gate, one
    episode at a time, a minute between passes. A season is rejudged only when
    its `signature` (ids, identities, lengths) changes; fingerprints are
    stored by file identity (`prints`, pruned with the item; `printsVersion`
    for the recipe), so decodes (days for a large library) are paid once.
    `GET /api/skip/{id}` calls `WantSkips`, which puts the season first and
    wakes the loop; it is read past the busy gate, **outwards from that
    episode** (`nearestFirst`), and judged once its neighbours are in and
    again at the end. Results (`SkipFor`, `LoadSkips`, `skips` bucket) go
    through the face.
  - **Buttons only**: *Skip intro* above the controls, outside their fade;
    *Next episode* in the credits opens the next **past its intro**
    (`startAfterIntro`, marks prefetched) and records this one **finished**
    (`persistDone`), so it does not resume at its credits. A cold open plays
    first; via *Next episode* the intro skips itself once
    (`skipIntroOnArrival`), so seeking back offers the button. Nothing else
    skips unpressed (a wrong mark costs a button, not a scene); none in the
    intro's last second (`SKIP_MARGIN_S`).
- **Genres** (`genres.go`) group from albums, like artists, so the views
  agree; each counts **its performers**, deduplicated on `buildArtists`'s
  lowercase key so the artists chip agrees. Untagged releases join none
  (deliberately no "Unknown"); search text is name plus performers, not
  release titles. **Drill-downs clear each other**: `state.artist` and
  `state.genre` are never both set (else an intersection under a chip naming
  one); leaving either uses `setModeForce`, as returning to the same chip is
  no mode change and would be ignored. In a narrowing, `CountsFor` gathers the
  matching releases' performers and genres as it counts.
- Artists (`artists.go`) group from **albums**, not tracks, so the views agree
  and `SearchAlbums`'s artist filter shows exactly those counted. Playlists
  are excluded (double counting). Cached per version, searched and sorted like
  albums.

### Names and text encodings

- **A file name is bytes** (`name.go`): `encoding/json` turns invalid UTF-8
  into U+FFFD, so `displayText` decodes **only display name, display path and
  search text** (invalid bytes as Windows-1252, byte by byte). `Path`, its
  hashed id and the database record stay raw, or they name no file and change
  every id: `Subtitles` matches `filepath.Base(it.Path)`, never `it.Name`;
  `blob.Item` keeps such paths in `PathBytes` (base64, only when needed; JSON
  strings lose them); `upsert` repairs an item whose stored path differs from
  the walked one (a lossy record), or reconciliation drops it.
- **Thai arrives as TIS-620**, which bytes cannot tell from Windows-1252, so
  `looksTIS620` judges shape: **four or more Thai-range bytes with no ASCII
  between** (European accents come one or two at a time). Any byte in
  0x80–0xA0 means Windows-1252 (unassigned in TIS-620).
- **CP1251 mistagging** (Cyrillic where Latin-1 has accented vowels: 0xF6 ö/ц,
  0xE4 ä/д, 0xE5 å/е, 0xF8 ø/ш) is valid text (even in UTF-16 tags),
  undetectable from bytes. `reinterpretCyrillic` judges by company: a Cyrillic
  letter beside a Latin one is a misread byte, a Cyrillic word alone a word
  (real Russian far outnumbers the damage); letters only, never punctuation.
  In `setMeta`, so cached values are fixed without a rescan. Double-encoded,
  accent-folded names ("Ã¶" → "A¶") are ambiguous: never repaired by rule;
  rename them on disk.
- **Misread-and-rewritten text is put back** (`reinterpret`,
  `reinterpretParts`, `westernBytes` in `name.go`, tested): UTF-8 made by
  reading bytes as Windows-1252 becomes those bytes again, read as UTF-8 if
  valid (first: its Thai lies in TIS-620's range), else as TIS-620 if
  Thai-looking, up to `misreadRounds` passes. Safe: accidental UTF-8 is rare
  in Western text, and `westernBytes` refuses other alphabets at their first
  letter. A two-byte-script letter (Greek to Arabic) beside a Latin one is
  refused (`strandedLetter`), or a chance Ukrainian letter in English gets
  "repaired"; three-byte scripts go unchecked (CJK sits beside Latin in
  titles). Paths go a component at a time (`reinterpretParts`); `mayBeMisread`
  returns early, unallocated, without a character in U+00A1..U+00FF. In
  `displayText`, it reaches all shown and stored text without a rescan.
- `cleanTag` decodes ID3v1 frames (never UTF-8) the same way. `setMeta` trims
  tag whitespace (a leading space sorts first and splits an artist) and
  repairs misreads via `displayText`, rebuilding bytes from the reader's
  Latin-1 string (`westernBytes`); one door cleans cached values too.

### Releases and their cards

- **Names on cards are links** (performer, genre, **the release a song is
  on**) wherever shown; the album sheet closes onto the result (a drill-down
  behind it is invisible). Release ids hash a directory unknown to the client,
  so the album tag labels the link and the **track's id** opens the sheet:
  `AlbumByID` takes track ids in a second pass (release ids never pay for it;
  their letter prefix prevents collisions). No album tag, no link. Links look
  like caption text until pointed at. `viaArtistLink`/`viaGenreLink` match the
  link class too: `data-artist` also marks the music bar's non-link artist
  line.
- **A music-only face opens on the artists** (`defaultMode`), its file listing
  being tracks in disk order; a link naming a view still opens that view.
- **A collection's sleeve must exist** (`betterCover`, `Album.hasArt`):
  performers, genres and shelves show **the most recent release with
  artwork**, else the most recent. Artwork is judged from the **index** (a
  picture in the release's first track's directory), free in the album build
  and deliberately weaker than the thumbnailer's search (certainty suffices
  for a preference).
- A release card's artwork badge is **its track count, playlist or not** (the
  caption has a count only without a performer); a playlist's marker takes the
  other corner (`CollectionCard.tag`).
- **A release card's hover is path, then technical line**, as on tiles
  (`Album.Path`, `Album.Formats`, `releaseShape`, `hoverLines` in `format.ts`,
  tested): the folder (above folded discs) or playlist file via `displayPath`;
  formats commonest first (`formatOf`: probed codec, else extension; track
  hovers spell extensions via `codecName` too); a rate only with a running
  time. Set on the cell root and cleared every render (a key change redraws in
  place; `scrubCell` runs only on recycling); the name element keeps its own
  tooltip; the sheet reuses the text. The lines are never joined and re-split
  on a separator (folder names contain them). **A confined caller is never
  told where an out-of-view playlist lives** (`shownTo`, in `AllowedAlbums`
  and via `ShownUnder` in the sheet's `albumFor`): it gets a path-less copy of
  the shared cached release, tested on the absolute location (`where`).
- **Cards say what is known** (release and track: performer, year, genre),
  mostly true over empty, the full line in `title`; a performer's adds tracks,
  running time, the span of dated releases and the majority genre
  (`Artist.Genre`, `FromYear`, `ToYear` from `buildArtists`, ties broken by
  name so builds agree). For music, **tags are part of the cell key**,
  enrichment adding them later.
- **A year in a release's name moves to its year** (`liftYear`): "2018 - Some
  Release (Single)" shows without it and is dated by it unless the tags give a
  year (tags outrank a typed folder); a bare-year name stays. `splitYear`
  wants a separator after or brackets around ("2018Something" is a word),
  `splitTrailingYear` brackets. Search still matches the directory as spelt;
  chronology is the year sort's job.
  - **Else the location may date it** (`yearOfPlace`, `yearOfName`, tested):
    the release's own directory (above folded discs), never its parent; a
    playlist's file name, then directory. In order: leading and spaced
    ("2007 - Title (Reissue 2020)"; unspaced, a leading year is a scene
    performer's name), alone in brackets ("Title (2019) [V0]"), last between
    separators ("Performer-Title-WEB-2026-GROUP"; last, as a title may be a
    year). Not years: digits run into letters, spans ("1993-1997"), bare
    years. **The .nfo is deliberately not read**: its year is already in the
    directory name.
- **An untagged release takes its parent directory as performer only where a
  tagged release has established that name** (`artistFromParent`, gathered on
  the grouping walk), parents being as often containers ("complete", "EP,
  Single, Demo") as performers; it never invents one. Only where **no track is
  tagged**, for **directory albums only** (a playlist's tracks live anywhere),
  returning the **tagged** spelling, matched case-insensitively (a second
  spelling is a second performer). Separately, **one voice suffices without
  contradiction**: one tagged track among blanks names the release; two names
  with no majority read "Various Artists". Half the tracks is a majority only
  for a name that leads alone (`sharedLead`): a split of one track each is a
  disagreement, and the name that sorts first is no answer to one.
- **A track naming nobody is credited to its release's performer**
  (`Item.Performer`), so its row says whose it is and the client can strip
  that name from the file name. **Never written into `Artist`**:
  `RecordingKey` derives from the tag's `Artist`, and the client builds the
  same key over the queue to keep radio off what is queued (`freshForRadio`);
  a derived `Artist` gives a file two identities by endpoint. Answered **from
  the album build**, beside the spoken verdict (`performers`, under `featMu`,
  read by `stamper`), so every route a track takes agrees, without forcing a
  build (`spokenOf`'s reason). On the copy only, never the indexed item (the
  build votes on indexed tags and would feed on itself); a track that names a
  performer is not corrected; a compilation credits nobody (its marker means
  "more than one"; compared case-insensitively).

- `fillAlbum` also derives `Genre`, `Year` (majority of tagged tracks) and
  `Duration` (sum; 0 unless *every* track is measured, never half-counted).

### Reading files: shape and enrichment

- **Technical shape is read from the file's own header** (`shape.go`,
  `mp4box.go`), never a per-file ffprobe (hover shows it on every tile):
  `image.DecodeConfig` for stills; the box tree for a film's size, codecs
  (`codecOfSampleFormat`: fourcc → the probe's name, unknown → nothing) and
  FPS (samples over summed `stts` time, read only to `sttsMaxEntries`;
  `hwWorthIt` needs it). A file probed for its duration takes shape from that
  probe; Matroska, AVI and transport streams get one ffprobe where the box tree
  found nothing; a probe the context killed is never recorded (a stable file's
  key never changes).
  - Persisted (`blob.Meta`: `Width`, `Height`, `FPS`) with a `Shape` marker
    (empty fields cannot mean "unread": many files have no picture) in the
    metadata record and the mirrored index (`blob.Item.Shape`), consulted by
    `needsEnrich`. It is a version: raising `shapeVersion` brings a new header
    fact to files already read. `Item.MoovLate` (`shapeVersion` 3: `moov`
    after `mdat`, which makes a file the browser opens worth a copy) is set
    only by the box reading and cleared only by a changed file, never by the
    open-time ffprobe. **Every writer of the record keeps shape and marker**,
    `EnsureCodecs` included (`TestEnsureCodecsKeepsTheShape`), or each restart
    re-reads every opened film.
- **Every completed scan ends with `EnrichMeta`**, not only the first: the
  watcher's debounced read is the only other tag reader, and it misses
  arrivals. It returns at once when nothing needs reading and runs **outside**
  `scanGate` (it yields to playback and thumbnails; the next walk must not wait
  on it). The walk itself never reads tags.
- Enrichment is priority-driven: `EnrichNow` (blocking, caller's context) and
  `EnrichSoon` (one background pass at a time) read what the browser shows
  first (listing page, opened album, `/api/item/{id}`), ignoring the busy gate.
  `needsEnrich` keys off a persisted `enriched` flag set whatever the outcome.
  - **A reading belongs to the bytes it was read from**: every commit goes
    through `applyReading`, which writes only while the item is still the file
    read and undoes the write if it moved after (`unchangedSince`,
    `keptReading`) — `upsert` may meanwhile have run `forgetContent`, and a
    stale reading under the new identity is permanent (for `EnsureCodecs` it
    re-arms the sticky `probed` with the old file's tracks). `EnsureCodecs`'
    queued record is written only under the identity probed. The examined mark
    (carried by `blob.Item`) has no identity and ends all reading, so
    `markEnrichedIf` compares and writes in **one** lock turn.
  - `keptReading`'s `forgetContent` re-marks the item dirty (or a flush in
    between persists the polluted record) and must not forget a container's
    declared duration (`declaresDuration`: `setMeta`/`setProbe` never restore
    it; `upsertStored` restores a DVD title's after its own `forgetContent`).
  - **Not media is a verdict** (`Probe.Unreadable`, `Item.Unreadable`,
    `unreadableInput`), and an answer like a parsed document (`answered` is
    set with it): only "Invalid data found
    when processing input" or "moov atom not found", judged **after** the
    parse (found streams are kept); no other failure (path, permission,
    loopback not up, HTTP status, killed run) is ever recorded. Persisted,
    cleared by `forgetContent`; the player gives up at once ("This file is
    damaged or incomplete") and Try again re-asks the server.
  - Two more readings are the same verdict. **A file of no bytes**
    (`emptyFile`, asked of the disk rather than the index's size) needs no
    ffprobe, which calls it only "Invalid argument". **A document with no
    real stream and no length** (sound with no channels and no sample rate,
    a picture with no size): a download's placeholder of zeros under a media
    name, which ffprobe takes for the format the extension claims, finds no
    frame in and exits on without complaint.
  - **A track found not to be media is not listed** (`Item.listed`): no
    listing, count, release, performer, genre, queue, station or analysis —
    it has nothing to play. It stays indexed, so a change to the file brings
    it back to be judged, by-id requests answer, and a delete of its folder
    finds it (`leftover` takes it as the folder's junk, or it would hold the
    folder back). The running totals count listed items only (`countItem`),
    so every change to a kind or a verdict sits between a count out and a
    count in (`forget` wraps `forgetContent`; `setProbe`, both upserts,
    `dropItem`, `LoadFromDB`), and the across-kind totals skip such tracks
    too. A track with no length is asked once a run whether it is media
    (`judged`, in memory, reset with the content), since a record written
    before the question existed comes back examined and would never be
    asked; only a verdict or a length is written down. Films are left
    listed: the player says what is wrong with one.
  - A probe killed by its own ceiling (`ffprobeTimeout` 30 s,
    `ffprobePipeTimeout` 60 s) is no answer (`ffprobeResult.cutShort`,
    `Probe.Interrupted`): neither recorded nor marked examined (either would
    persist a wrong verdict; `archiveProbeMissed` suits only never-persisted
    archived items) but remembered per run by identity (`enrichCutShort`), or
    `EnrichSoon` re-probes it from every page; fed only by
    `p.Interrupted && ctx.Err() == nil`.
  - A cache hit commits title, performer and length before the shape-fill
    probe, and again after. A sweep that abandons an item taken off the
    channel must not log finishing ("media metadata loaded").
- Enrichment must publish: `EnrichMeta` notifies in batches and watcher paths
  notify again **after** the read (`AddFile`'s debounced timer, `readMembers`),
  or tags never reach clients.

### Archives, discs and the loopback input

- **A member a set holds but cannot serve is reported**: `parseRarSet` and
  `parseZip` return `[]rarSkip`, and `reportSkips` logs why — solid (with
  method), encrypted, a non-deflate zip method, a name held twice, a missing
  split-zip part, incomplete (bytes of total; normal mid-download). **One line
  per set, once per process, naming no members** (`skipReasons` groups by
  kind); `Library.once` keys it by set and reason kinds, so rescans repeat it
  only when what is wrong changes; a set's parse failure likewise.
  - **Compressed members are unpacked** (`pack.go`, `unpack.go`). The
    process's own near-start reads (headers, tags, thumbnails) use
    `packedReader`: unpacked as read, never written; a forward seek discards,
    a backward one restarts. Playback seeks everywhere, so a streamed member of
    `unpackFrom` (32 MiB) or more is unpacked once into the shared scratch
    budget and served as a file (`OpenForPlayback`; the owner's choice over
    unpacking in pieces); one bigger than the whole budget streams as packed.
    The copy follows the rewrap's rules: one per (path, time, size), made by
    the first asker, renamed into place, checksum-verified, room made first
    counting copies in progress, adopted across restarts, owned by no request,
    `unpackAtOnce` (2) at a time; least recently wanted is evicted but
    **never within `unpackKeepFor`** (5 min, as `remuxKeepFor`: a player
    reopens it per range); `CloseScratch` stops it before the drain, a late
    copy kept for the next run and not offered. `serveStream` marks playback
    **before** the open, which is the unpacking.
  - **Native Go only, by the owner's rule** (no unrar or 7z): klauspost/compress
    flate, rardecode. rardecode v2.4.1 truncates RAR 2.9 members whose LZ match
    passes the window's end (`copyBytes` clamps); `go.mod` pins it plus the two
    commits of nwaples/rardecode#69 — drop the `replace` once a release has
    them.
  - **Solid archives' members are reported and left out** (each read would
    unpack every earlier member). A compressed member is complete only if its
    last part says nothing follows and no volume is short (`packing.ended`,
    `short`); it is never served in part. It is matched in rardecode's listing
    by name, else by sizes (rar keeps two name encodings; this parser reads
    the first); `rarListings` caches the last four sets' listings.
  - **A member that does not unpack is damaged** (`ErrDamagedMember`: broken
    deflate, checksum mismatch, short output, or rardecode's equivalents): a
    verdict, unlike a failed read — refused as "the archive it is in is
    damaged" (`openFault`) and remembered per run by identity.
- **Zip** (`archive.go`, `zipset.go`) uses a rar set's doors (members after a
  NUL, `indexStored`, not persisted, `OpenItem`, `enterContainer`). **The
  directory is parsed here, not by the zip package**, which drops the part a
  member starts in; one reader serves every shape, the single file included.
  Spanned (`name.z01 … name.zip`) and byte-split (`name.zip.001, .002 …`) sets
  map members to `storedEntry` segments (`zipSet.span`). The container (the
  `.zip`, or the first piece) is named from any part by `zipContainerOf`
  exactly as the walk names it (test-pinned); a part arriving or leaving
  re-reads the set; `name.z01` is a zip part only where `name.zip` exists.
  Pieces a file host renamed apart group by name, else by name less a trailing
  `-identifier` (`hostID`); a number two files hold joins nothing.
  - **A member's own header must name it before it is served** (`dataStart`),
    catching any wrong offset. A missing part costs only its members
    (`incomplete: part 1 of 4 is not here`); an archive read cleanly but not
    whole (no end record, unparsable directory, byte-split set short of its
    last piece) is a verdict (`zipShape`) and its members go, where a failed
    read keeps them. A self-extractor's prefix shifts offsets, as in the zip
    package.
- **An archive with more media than `-archive-max` is left out whole**
  (`archiveMax`, default 1,000, 0 for none; `mediaMembers`), never cut to its
  first thousand: a verdict, logged once at Info (`archiveOverCap`). A zip is
  judged from its end record (`zipEntriesPerMember`: over ten entries of any
  kind per allowed member) before its directory is read. Its cost is in
  `DefaultArchiveMax`'s comment.
- Rar (`rar.go`): RAR4/RAR5 members, stored or compressed, are virtual items
  (`Path` = "<rar>\x00<member>", `Archived()` true) read via `OpenItem`. A
  `storedEntry` (name, size, byte ranges in files we do not own) also carries
  DVD titles and zip members, so `Archived()` means "bytes inside something
  else"; its segment index lives on the reader (`newStoredReader`, searched by
  `readAt`), so no producer can forget it.
  - Open item content via `library.OpenItem`, never `os.Open`. ffmpeg reads
    archived bytes over the loopback stream URL (thumbnail, probe, all three
    converters); the pipe is only the no-loopback fallback and cannot seek.
  - Archived members get scrub sheets too: **priority gates a `hover=1` sheet,
    not the container** — it stands down while anything streams
    (`Library.Streaming`); a stored sheet is always served, and the client
    does not remember a refusal.
  - A second member under a held name is refused and reported. Legacy
    numbering runs past `.r99` into `.s00…` up to `rarMaxVolumes`. The
    stitched reader drops zero-length segments, keeps at most
    `rarMaxOpenFiles` (4) volume fds (LRU; never 100 per viewer) and is
    mutex-guarded for parallel `ReadAt`, as `io.ReaderAt` requires.
- **Interlaced video is deinterlaced wherever a picture is made**
  (`deinterlace.go`; browsers do not separate fields and a copy cannot): both
  converters, thumbnails, the piped fallback and sprite frames use
  `videoFilter` — `bwdif` with **`deint=interlaced`**, so it touches only
  flagged frames and is safe on every file; **before any scale** (scaling
  merges the fields for good; test-pinned); **`send_frame` stated
  explicitly**, the default `send_field` doubling every re-encode's frame
  rate. `crop.go` has none: bars are black in both fields.
- **DVD-Video** (`disc.go`) is a parser, not a reader: VOBs (MPEG-2 program
  streams, stored plainly, split at 1 GB) are a `storedEntry` in an image or a
  `VIDEO_TS` folder alike, so reading, loopback, unpersisted offsets,
  thumbnails and sprites come free.
  - A **title** is indexed: `VTS_01_1.VOB` … `VTS_01_4.VOB` are stitched in
    the disc's numbering order, never directory or offset order (a film
    written back to front would start half way). Menus (`VTS_nn_0.VOB`,
    `VIDEO_TS.VOB`) and titles under `discTitleFraction` (1/20) of the largest
    are left out. The ISO9660 parse decides, not the extension (`.iso`/`.img`
    prefilter only); UDF-only images (Blu-ray) answer nothing, deliberately; a
    partial image's ranges are clipped to what is there.
  - A title of an image inside a rar set is two mappings deep (`placeInStored`
    the second, `discsInside` the replacing) and is named after the set's
    **directory**, or the member's stem where the set holds several images;
    an unpacked disc's after the first directory at or above its VOBs that is
    not `VIDEO_TS`, `AUDIO_TS` or `dvd`; a title number only where there are
    several. A DVD folder is folded into its titles
    (`isDVDStructure`, once per folder per walk) and `stillIndexable` agrees,
    or reconciliation restores its VOBs; any other `.vob` is a plain video.
  - **Only the disc knows a title's length** (ffprobe's estimate can be half):
    `ifoDuration` sums the distinct **cells** of `VTS_nn_0.IFO`, not programme
    chains. It rides the `storedEntry` (`upsertStored`, `nativeDuration`);
    `setMeta` and `setProbe` must honour it (`declaresDuration`). Only a
    probe's duration is outranked, not the rest of it.
  - **A DVD is seeked by byte** (`library.SeekByte`), ffmpeg refusing to seek
    past its wrong estimate: cell sectors map time → offset, passed as
    `-seekable 0 -offset <n>` instead of `-ss`, loopback only. **Both flags
    are required**: with a seekable input the demuxer rewinds and silently
    undoes the offset. The map is kept only if the cells cover the whole
    title; within a cell it interpolates.
  - `convertInput` (`remux.go`) decides input and seek together for all three
    converters (`-ss` only where position did not seek); `planConversion`
    (`convert.go`) is all the pipe and the segmented converter share (the seek
    with `-copyts`, hardware, picture, soundtrack) — keep it one tested
    function, as copies drift. `timeSeekInput` refuses a title read by
    position, so `handleKeyframe` answers the time asked for.
  - No episode boundaries are recorded, so a multi-episode title is one item,
    never a guessed split.
  - **A downloaded title's clock is straightened** (`ps.go`): each authored
    piece restarts near zero, so timestamps jump back at cell joins (not the
    stitching: a plain `cat` of VOBs does it too). The rewriter fixes
    SCR/PTS/DTS in place from the cell table: 2048-byte packs and fixed-width
    fields keep the length (so `Content-Length` and Range stay honest), and
    joins are known, not guessed.
    **Download only**: playback seeks by byte and must not depend on it.
    Navigation packs (stream 0xBF) are untouched. ffmpeg `-c copy` into MKV is
    rejected (minutes and gigabytes of scratch per download, no resuming, no
    longer a `.vob`).
  - Nothing decodes MPEG-2: `opensDirectly` and `decodesVideo` map `vob`,
    `mpeg2video` and `mpeg1video` to `video/mpeg`, so conversion starts at
    once and the browser is never handed the disc.

- **The loopback input** (`loopback.go`): ffmpeg/ffprobe read archived content
  over `/api/stream/{id}` (Range over `OpenItem`, a member's only seekable
  view; a pipe reaches only the first minute) at the address `run` gives
  `library.SetLoopback` (`LoopbackAddr`: unspecified/loopback IP → base URL;
  one LAN interface → `""`, pipe fallback). `X-Media-Internal: <per-process
  random token>` (`library.InternalHeader`/`InternalToken`) authorises
  nothing: `handleStream` only skips `StartStream` for it (else thumbnailing
  throttles against itself and pauses enrichment) and applies
  `internalStreamCap` (64 MiB per response); a runaway reader is bounded by
  the tile's budget and ffmpeg's `-rw_timeout`.

### Frontend: the grid and its sources (`web/src`, no framework, no runtime deps)

- `grid.ts`: windowed virtual grid over a `GridAdapter` (`main.ts` swaps the
  items view's `LibrarySource` — 200-item pages keyed by a query generation
  counter — and whole album/artist lists). It asks only `need(a, b)` for the
  visible range; keep it so. Cells reconcile by `itemKey` (identity), never
  index, or an insertion repaints everything; identity keeps DOM and decoded
  thumbnails (tested).
- **Hover adds only what the row lacks**: tile → path and technical line;
  queue row → card (`trackcard.ts`: sleeve, what it belongs to, technical
  line; a `title` holds no picture); a release's track row → technical line.
  `belongsTo` (`format.ts`, tested): release, year, genre, falsy ones dropped
  (year 0 is unknown). The card waits `DWELL_MS` (crossing the queue is no
  request), takes no pointer events (the row takes the click), is
  viewport-fixed (the panel clips), and hides on leave, scroll, panel close
  and every window repaint.
- **Technical hover** (`mediaShape`, `format.ts`, tested) on the cell root
  (`scrubCell` clears it, or it outlives recycling): codecs, picture size,
  frame rate; a track's format and bitrate as size over playing time (a film's
  counts the whole file — all that is known without reading the stream).
  Absent parts drop with their separators; codec names as written ("H.264"),
  unknown ones verbatim.
- Grid cells are keyed by id **plus mtime and size**, thumbnail URLs carry
  `v=<mtime>`, so a growing file's cell retries a failed thumbnail and escapes
  the immutable cache. Never key a cell by id alone.
- `LibrarySource` keeps its previous pages and total until refetched ones
  land, on `invalidate` and `setQuery` alike (`holdOver`), so typing never
  blanks the grid and common items keep their cells; total 0 is not held
  (`fetchPage`'s past-the-end guard would refuse the refetch). Below zero (an
  answer on its way) the grid draws skeletons and asks nothing; a never-asked
  source answers 0, not "unknown" (`count`), as the grid lays out before
  `/api/info` lets the address be read and a placeholder promises an answer in
  flight.
  - Rows are held over **only while they are the rows on screen** (one door:
    `applyQuery`, `arriving`): arriving from another source empties the view's
    own (`reset` on either source kind, moving the generation so an older
    view's in-flight answer cannot refill it) and shows skeletons; from its
    own source it keeps them. A **changed address** (link, shortlink, Back)
    keeps nothing, via `fromAddress`, only where the view fetches (seasons,
    derived from the shows list, would never refill).
  - `viewSource` (`query.ts`, tested) is the one table of a view's source for
    this rule, the chips and change-event refetches (`itemsOnScreen`); a
    derived view belongs to its parent's. `arrivalPlan` (tested) decides
    source, drop and fetch; `drawCount` (tested) is 0 before any ask, -1 while
    answering, then the count; sources must announce loading (or arrival
    skeletons never draw) and clear it on failure. Off-screen sources are not
    refetched on change events.
  - `writeHash` omits sort key or direction only when equal to
    `openingSort`/`openingDesc`, `readHash`'s fallback (else Back loses it),
    and `m` only for the face's default (an absent `m` on a music face reads
    as artists); seasons reached by address load the shows first.
- **Nothing is refetched behind an open viewer** (it competes with playback):
  change events wait for close (`MutationObserver` on `body.viewing`);
  on-demand fetches (a drag's neighbours) still run.
- `sameSubject` (`sources.ts`) holds rows over only for the same subject —
  narrowing, reordering, to/from "everything" — never across performers or
  particular kinds (the wrong listing under the new chip).
- `setAdapter` hard-resets only for a different adapter, else `rewind`s with
  cells mounted. Data-driven mounts (`refresh`) fade in (`.cell-in`, opacity
  only — the transform places the cell); scrolled-in ones do not.
- Grid thumbnails go through `thumbs.ts` (`loadThumb`/`cancelThumb`): ≤3
  *recent* fetches (past `SLOW_MS` one stops counting, so a slow one cannot
  wedge the slots), oldest visible cell first (newest-first starves tiles),
  aborted on recycle (the grid's `release` hook, cancelling server-side
  generation). **Never set thumb URLs straight on `img.src`**: ~6 connections
  per origin, one SSE, and unbounded loads starve `/api/stream`. Failures
  retry while on screen (`retryThumbs`, also on library changes), since cells
  are not re-rendered on changes and would stay grey while the failure is
  remembered (`negTTL`, ten minutes); renderers keep the `img` on error (the
  retry needs it; it shows only with `.ok`).
- **A track is named in one place** (`trackTitle`, `format.ts`, tested), never
  `it.title || it.name`. A title tag is verbatim, never cleaned (as
  `RecordingKey` never reads titles from file names). A file name loses, in
  order: the extension; a leading performer prefix, only where it matches the
  track's known performer plus a separator (a compilation keeps its prefixes),
  tried before and after the number; the track number (`withoutTrackNumber`,
  only where a separator marks the digits a number: "44 Winters" stays); a
  trailing marker of a real bitrate only ("_320", "[320k]" go; "1979" stays).
  A step that would leave nothing is skipped.
- The album panel renders from tags first (file name, size as fallback) and
  re-fetches on every SSE change (`reloadAlbumPanel`), or a panel opened
  mid-scan stays pre-enrichment; it diffs its HTML and restores scroll.

### Frontend: overlays and the players

- Overlays (`video.ts`, `lightbox.ts`, `albumpanel.ts`) are self-contained
  classes mounting into `#overlays`, owning their document-level key handlers,
  cleaning up on close. `audio.ts` is a singleton bar with a queue/shuffle
  model (`order[]` of queue indices).
  - Toggles set class and `aria-pressed` together (`press`); a doubled verdict
    press keeps the latest answer (`rateGen`). Pure queue rules live in
    `queue.ts`, tested (`nextPosition`, `placeFirst`, `windowRows`,
    `resumable`). Below 720 px spectrum, radio and link fold into "⋯", as do
    the sheet's Download and Link.
  - The queue panel keeps the listener's scroll unless the current row leaves
    view and repaints on a frame; its length column never yields, is absent
    until measured, and has one right-aligned width over the whole queue
    ("59:59" or "9:59:59", `runsHours`, tested, updated on append).
  - When a set stops, `endCast` re-points the decks (whose sources `startCast`
    cleared) to the current track at the set's position, autoplaying only if
    the listener chose to play here; resuming a parked cast queue restarts the
    transport tick. A track the set advanced to by itself counts as played;
    `advanceWithSet` uses `nextPosition`. `queuedAhead` is tri-state: a `null`
    URI also means "refused", not re-asked each top-up.
  - Radio keeps queued ids and songs (`songKey`) in two Sets extended with the
    queue (`freshFrom`), never walking it per top-up; recent performers are
    read backwards from the position through the order (`recentArtists`,
    tested), not off the tail; `ahead` (tested) counts what follows.
  - Queue and spectrum stack in one anchoring container (`.ab-panels`) so they
    cannot overlap: spectrum lowest, against the bar (the queue never moves
    it); it grows upward within the window, the queue yielding, and is
    pointer-transparent (or the gap swallows presses).
  - Add to queue (`enqueue`): `appendToOrder` (`queue.ts`, tested) shuffles
    new entries among themselves and appends them, never into the existing
    order. Nothing loaded → play the release; played out (`exhausted`) → jump
    to the appended segment's start (a bar shuffle may have rebuilt the
    order); playing → only the order changes, and a set with nothing queued
    ahead gets the first new track (`queueAhead`). The owner is kept; cap
    `QUEUE_CAP`, as `playItems`.
  - The sleeve is never another release's (`sameRelease`, `cover.ts`, tested;
    an `<img>` keeps its picture until the next loads): `prefetchArt` fetches
    the next sleeve with the next track (deck preload, TV handover); one not
    yet loaded hides behind the placeholder unless the release continues (else
    a blink per boundary). A release is one directory, or one album tag under
    one performer.
  - Queue-all (`queueSource`, `content.ts`, tested; `handleTracks`,
    `library/tracks.go`): `/api/tracks?of=` returns a view's tracks at once —
    listed releases in order; each listed performer's releases from their
    first (`ReleasesOf`, matched as the artists view groups) or genre's, once
    each, performer by performer (`ReleasesIn`, uncredited last); or the music
    listing forced to audio. `TracksOf` flattens under one read lock, giving a
    confined caller only tracks it may see. `maxQueue` must equal `QUEUE_CAP`;
    the answer says when cut. Offered on the grouped views, the music chip,
    and mixed listings only on a music-only face. `listFilters` (`query.ts`)
    is the one query wording for grid, m3u export and this; the bar words
    every enqueue outcome.
  - A paged collection needs one version (`collectPages`, `server.go`,
    tested): offsets index one sorted result and the library changes
    constantly, so mixed versions duplicate, miss and stop short of `Total`.
    Spanning versions, it is retaken from the top **once**, then accepted —
    more passes move too, each hundreds of listings under the one query lock.
    The cure, a listing held open across pages, belongs to the library (`List`
    clamps limit to 500).
- **A new file starts where *it* was left**: until the element holds its
  source, `video.currentTime` is the previous file's, and a container the
  browser will not open never reaches the element, so rewraps and conversions
  must not ask it. `load` computes `startAt` from the file's resume record,
  `sourced` says whether the element shows this file, `switchAt` picks for
  every source change. A source with no start position is told 0
  (`sourceStart`, tested), as a `currentTime` set before metadata survives a
  source change until metadata consumes it.
- `resumeStart` (`playback.ts`, tested): no resume in the first few seconds or
  at/past the end, judged by the record's stored length or else the library's.
- Player and picture viewer step through the listing they came from
  (`ItemSource` + `findKind`, `sources.ts`) — swipes, and a video's end —
  within their own kind; the player switches file in place (closing drops
  fullscreen). Subtitles and soundtracks are listed on every route,
  conversions included (a pick unlike the running conversion costs a reopen).
  `load()` is the one place per-file state is cleared, `startSource` the one
  place the element gets a source (file, rewrap, sound-fix file, conversion),
  carrying seek, track and the decode-check settle so no route forgets it;
  volume, speed and subtitle language are the viewer's. Rotation is the
  file's: `load` restores it from `library.Flags` (blob db, so devices agree);
  the picture viewer's is a viewing aid, not persisted (EXIF already uprights
  photos).
- **Black borders in the file** (`crop.go`, `/api/crop/{id}`): `cropdetect` at
  four points across the film, never the opening (fades, title cards), samples
  unioned, not averaged (dark scenes detect small; trim less).
  - Stored in the blob db stamped with mtime and size, empty boxes too, but
    only measured ones: `detectCrop` returns `(box, measured)`, as an empty
    box means either and the key never changes. No ffmpeg, no duration,
    archived without loopback, every sample failing, or an interrupted run is
    unmeasured; a sample counts only where `cropdetect` printed a box.
  - The handler runs `probed` (the player asks before the item request reads
    the length) only where `it.Duration <= 0`: `EnsureCodecs` does not dedupe
    in flight and `probeSem` holds two, so probing unconditionally doubles
    ffprobe per open.
  - Per-file dedup hands waiters the answer (box and `ok`, set under the mutex
    before the channel closes), not a bare close, or waiters re-run the seeks
    when storing fails (`-db off`, failed `PutCrop`); a not-ok leader is not
    inherited. Seeks take the thumbnailer's slot; the run marks
    `Library.StartStream` (a viewer waits on it); frame size comes from the
    header reading.
  - Applied as a scale about the element's centre, composed with the rotation,
    element clipped; `cropScale` refuses off-centre boxes and near-total trims
    and stops at the first edge reached, so only the smaller border goes.
    Geometry uses `video.videoWidth/videoHeight`, detection as fractions
    (coded pixels over-scale an anamorphic DVD by a fifth), redone on
    `loadedmetadata`. It answers for this screen: the button shows only where
    the window gains; `Flags.NoCrop` keeps borders (a 2.39:1 film has them on
    purpose).
- `swipe.ts` defines gestures for both viewers: one `SlideDeck` drag
  (neighbours resolved while down, damped travel, layer home before the file
  changes), differing only in `arriveEarly` (the picture viewer renders under
  the layer; the player shows its poster, then opens). `watchDrag` follows
  drags (player vertical, viewer sideways); `watchSwipes` catches flicks with
  no touchmove, standing down on `ev.defaultPrevented` (no double step).
  `ignore` disowns a gesture by where it starts (seek bar, zoomed picture). A
  recognised gesture `preventDefault`s the touchend, the only way to cancel
  the synthesised click.
- A file switch is covered by the **poster** (`.vo-poster`: incoming thumbnail
  from `load()` to `loadeddata`, turned with the item by `turnPoster`, a
  thumbnail being the file as stored — the neighbour's rotation mid-swipe; the
  picture during a cast) and the **slides** (`.vo-slides`, `.lb-slides`:
  current frame on a canvas, neighbour thumbnails either side). On release:
  layer home, poster up with the same picture, then layer down — that order
  hides the outgoing file. Release goes to the neighbour resolved during the
  drag, never a new search; before that answers, the plain step.
- Viewers sit on the **visible** viewport (`viewport.ts` →
  `--app-vw/-vh/-top/-left`), as iOS lays fixed elements on the layout
  viewport (taller under Safari's bars, wider after a pinch); the app is
  hidden while one is open (`body.viewing`), or the grid shows around it. The
  player is `touch-action: none`; the picture viewer keeps `manipulation` for
  browser panning.

### Frontend: views, sorting, addresses and chrome

- The performer's name under a release is its own click target
  (`.link-artist`, `data-artist`), read by `viaArtistLink` as the play badge is
  by `viaPlayBadge`: one delegated click per cell, meaning set by where it
  lands. **Every** adapter drawing one must check it, track cells included, or
  the click falls through and queues the whole listing. It is styled as caption
  until pointed at.
- **Resolution and bitrate are separate orders** (`pixels`, `bitrate`; videos
  only). Resolution sorts by **area** (one dimension misranks portrait clips; a
  turned picture ties its twin); bitrate is file size over playing time (no
  stream read), deliberately not size. An unread shape sorts to the unsorted
  end.
- Every view change refills the sort select from `sortOptions(mode)`, drilling
  into an artist included (releases take album keys). A key that does not
  survive falls back to the view's opening key (`openingSort`, in `sorts.ts`
  beside the option table, both pure and tested) rather than reach a server
  that ignores it: the table's first row, except the popularity listing
  (popularity key) and a performer's releases (by year: a discography reads in
  order); a genre's releases stay by name. The drill-down sets the key
  outright, or the artists view's name key carries over. `openingDesc`
  (tested): newest first, except a **season**, which opens from its first
  episode whatever list led there. An address naming a sort or direction keeps
  it; after that the direction is the viewer's. Every way into a view (chip,
  drill-down, brand, address) goes through `enterView`. One `bindCollection`
  wires the four grouped sources and one `collectionOnScreen` says which is
  showing, for both `reloadGroupedView` and the guard keeping a listing's stale
  counts out of a grouped view. The five collection cards are one
  `renderCollectionCell`.
- UI state (mode/search/sort/artist) is the URL hash (`readHash`/`writeHash`,
  `main.ts`): a view change is **pushed**, a keystroke or sort **replaces**
  (Back leaves a drill-down, not the app; a search leaves no trail); arriving
  by address or shortlink replaces. Grid cells take focus and open on Enter or
  Space. A tile's play count, verdict and spoken-word marks are one `.marks`
  row beside the watched tick, styled like the duration pill. The audiobook
  shelf and like-this listings say *why* they are empty while unanalysed.
- **A shortlink is the hash under a short name** (`links.go`, `blob.PutLink`,
  `web/src/links.ts`).
  - The server stores the fragment and **never parses it**, so no view, present
    or future, needs backend work; validation is of shape only (printable
    ASCII, no `#`, bounded length), never meaning.
  - `/s/{code}` redirects (a fragment never reaches a server): 302 to
    `/#<target>`, an unknown code to the library rather than an error.
  - Both directions are stored (`c`/`t` prefixes, one bucket, one transaction)
    so a view keeps one code. A mint is one operation (`links.mintMu`), or
    simultaneous presses both miss and orphan a code; it is taken outside the
    index's lock, which the memory path takes inside.
  - The item is arrival state (`i=`, `al=` for a release): `readHash` reads it,
    `writeHash` never writes it (an overlay is not a view), `openLinked` clears
    it on reading (or it comes back). A linked item opens on a listing of one
    (`justThis`) on top of the view; `viewParams` drops any existing `i=`
    before adding one (or the address names two).
  - A link belongs to its hostname (one server, several faces): the host is the
    first component of both keys (`linkKey`, NUL-separated, NUL being in
    neither part), so under another name a code reads as unminted. It is a key
    component, not part of the code (a second mechanism; short markers could
    collide). `linkHost` reads `X-Forwarded-Host`, then `Host`, as do the m3u
    export's absolute URLs (`requestBase`); without nginx's
    `proxy_set_header Host $host` every face is one host and links cross
    silently, so the mint logs its host. Lower-cased; port kept (two servers,
    two libraries).
  - No authentication, and no face filter on the redirect: enforcement is on
    the request for the thing itself.
  - The alphabet omits `i`, `l`, `o`, `0`, `1` (codes are read aloud).
- The listing's scroll position belongs to the app (`scrollhold.ts`): overlays
  save it on open and restore it on close, `#scroller` being
  `overflow: hidden` meanwhile. On leaving iOS's native fullscreen (the only
  one a `<video>` gets on an iPhone), WebKit scrolls the grid to the top
  *after* the close event, so the restore re-asserts next frame and shortly
  after, only if the listing sits at the very top (sparing a reader who
  scrolled).
- Touch-keyboard text fields are 16px (`@media (pointer: coarse)`): iOS Safari
  zooms in on smaller ones and never zooms out. `#scroller` refuses horizontal
  overflow.
- The preferences list truncates long directories from the head with a
  right-to-left box, so each path is in a `<bdi>` (a leading `/` is
  bidi-neutral), its `title` on that element (tooltips inherit direction).
- **The grid takes the whole window from the top left**: nothing (grid, top
  bar, transport) is capped or centred; a capped grid looks displaced and
  wastes columns.
- **The chips are reconciled, not rebuilt**: counts change constantly, and a
  click fires only when press and release hit one element, so rebuilding
  swallows clicks. Structure is redrawn only when it changes (chip set,
  drill-down); numbers and lit state are written into existing elements.
- Filter chips wrap onto as many rows as needed at **every** width, never
  scrolling sideways (later filters would hide behind an edge); the top bar has
  no fixed height.
  - At ≤560px the chips container dissolves (`display: contents`) so the sort
    pill ends the last chip line when it fits, else wraps right-aligned;
    `space-between` goes (it would spread loose lines), chips packing left and
    the toolbar's margin pushing it right; the controls share one height.
  - On **coarse pointers only** (a desktop window has a wheel; a bar ducking
    every tick is a twitch): counts compact (`chipCount`, tested; five digits
    and up round to thousands, "210k"), and the bar hides while browsing,
    returning on an upward flick or at the top (`barhide.ts`, a pure tested
    reducer; `main.ts` only toggles a body class). The collapse is
    `grid-template-rows: 1fr → 0fr` with the padding on an inner `.bar-inner`
    (outer padding would leave a strip). Reducer rules: downward travel
    accumulates, so slow scrolls hide; a jump larger than a finger makes in a
    frame is an overlay restoring scroll and decides nothing; the top outranks
    that guard (a view switch resets scroll at once, and a bar hidden at the
    top could never return).
  - The player's control row wraps too, so `keepMenuOnScreen` measures each
    menu (anchored to its button's right edge) as it opens and slides it on
    screen. The overlay has `touch-action: manipulation`: a double tap is
    fullscreen, never zoom.

### Receivers: AirPlay, DLNA and signed URLs

- **Playing to a receiver** (`airplay.ts`) is our own button (the browser's
  picker was in the replaced controls) over two feature-detected routes,
  WebKit's prefixed AirPlay API and the Remote Playback API, shown only once a
  receiver is reported (`webkitplaybacktargetavailabilitychanged`,
  `remote.watchAvailability`; an empty picker is worse than none).
  `watchRemoteState` merges the two connection events.
  - `watchAirPlay` attaches WebKit's listener (system route detection, a
    documented power cost) and Chrome's watch only while the document is
    visible (WebKit answers at once on re-attach); a chosen route is
    unaffected. A phone or tablet (`maxTouchPoints > 1`) is never asked: its
    picker always has routes, so the button is offered outright and the system
    scans only while the picker is open.
  - WebKit answers whether *any* target exists, Chrome whether one can play
    **what this element holds**, so its watch is re-armed (the old cancelled)
    on every `loadedmetadata`; the bar keeps the set of decks that can reach
    something, not one flag (or the idle deck hides the button), and appends
    its decks to the bar rather than leaving them detached.
  - A rejected `watchAvailability` shows the button and lets Chrome's dialog
    decide; only an explicit `false` hides it.
  - Both routes hand over a **URL** (hence signing). A set merely discovered
    over DIAL never appears in `remote.prompt()` (DIAL launches a named app);
    it is reached by AirPlay from an Apple device, or by DLNA.
  - When playback moves to a receiver, a codec a television likely lacks is
    converted at once (else sound over black): `playsOnReceiver` is the safe
    list (H.264, HEVC); an unprobed file is left alone (converting on a hunch
    costs more). Coming back does not switch back (a stall each way).
  - Limits: an element whose output was **moved** into Web Audio has no route
    left (where a deck's sound cannot be copied, opening the spectrum ends
    AirPlay until reload); the receiver must reach the page's address (an old
    set rejecting a modern certificate just spins).
- **A television is driven over DLNA from the server** (`internal/dlna`,
  `internal/server/cast.go`, `web/src/cast.ts`): SOAP (`SetAVTransportURI`,
  `Play`), the set fetching the file itself.
  - Discovery searches out of every interface (container bridges hide sets
    from the default route), twice each (UDP is lossy), waiting 2.5 s (`MX`
    2); IPv4 only, deliberately (search, descriptions, `LocalIPFor`).
    Descriptions use one client with a dial timeout and short idle life, at
    most `describeAtOnce` in flight, through a fixed pool fed from a channel
    (`describeAll`), not a goroutine per location, as other hosts decide the
    datagram count. Locations are deliberately uncapped (never drop a real
    renderer).
  - `Discover` runs on handler contexts with no deadline, so folding in
    `ctx.Deadline()` is not enough: `endReadsWhenDone` winds the socket
    deadline forward on cancellation (waking a blocked read) and returns its
    teardown, registered after `defer conn.Close()` so LIFO runs it first (else
    an abandoned search holds a socket and goroutine per interface and the
    search mutex). Nothing is cached where `ctx.Err()` is set (a dead context
    empties every describe, which would hide every set for the TTL).
  - `casting.known` keeps every set ever reported (refreshed per search) and
    resolves ids clients hold (`renderer()`), so a lost reply never strands a
    playing set; one really off times out (45 s `SetAVTransportURI`, 8 s poll)
    as "did not answer". `known` is unbounded on purpose (a handful;
    UDN-derived ids survive lease changes). The picker lists sets seen within
    `rendererMemory` (5 minutes), this round's first, since a search misses an
    answering set ~1 time in 72 (cause unknown: not the protocol, `search`,
    `describeAll`, load or SOAP contention); `DiscoverReport` returns sets that
    answered but could not be described and `Server.search` logs them (a lost
    datagram and a failed description want different cures).
  - Handed-over URLs use our address on the set's network (`LocalIPFor`;
    loopback and the page's hostname are unreachable from it), signed.
  - The calls a set answers only once the film is open get `openTimeout`
    (45 s against 8 s; `budgetFor`): `SetAVTransportURI`, and `Play`, which an
    LG holds until the picture is up (1.4–7 s for an ordinary film, past 8 s
    for a 4K one). Timing either out at the ordinary budget reports a failure
    for a film that is about to play, and the page then stops it.
  - `castAlias` first seeks another honest name for the *same bytes*: WebM as
    `video/x-matroska` for a set refusing WebM (**one way only**: an MKV named
    WebM promises VP8/VP9), AVI's spellings (`video/avi`, `video/msvideo`,
    `video/x-msvideo`), `video/mpeg` for a transport stream. It is keyed by
    **type**, not extension (a copy may change container).
  - Codecs cannot be asked (`GetProtocolInfo` lists containers and legacy
    `DLNA.ORG_PN` profiles, Matroska as a bare `*`): an undecodable picture is
    left to fail visibly, but a soundtrack no set decodes (`noReceiverAudio`,
    `castSoundKind`: DTS, DCA, TrueHD, MLP) fails silently, so it is converted
    first by `remuxSound`, gated on `soundFixable`, **not** `remuxable` (which
    refuses these files); a set listing DTS among its sinks is exempt.
  - MIME types come from our table (`mimeFor`, registered in `server.go`'s
    `init`): `.webm` is `video/webm`, as Go's built-in `audio/webm` overrules
    even `/usr/share/mime/globs2` and `/etc/mime.types`; a set judges by that
    name alone.
  - Offered is the file, or a rewrap with ranges where the set does not list
    our container; never the live conversion (no ranges or length). A set that
    will not say what it accepts is assumed to accept everything (its refusal
    on screen beats ours).
  - Music carries full DIDL: tagged title, artist under both spellings, album,
    genre, year, track, size, bitrate, and a `castArtWidth` thumbnail as cover
    (hence `thumb` is a signed path; it reveals nothing else). Artwork only for
    music. UPnP `bitrate` is **bytes** per second.
  - DLNA cannot pick a soundtrack (no `-map`), so a choice is a copy with that
    track alone: `Remuxer.File` takes it, in the **key and file name**
    (`-a<n>`) so one language is never served as another; `Adopt` deletes
    names that do not parse (their soundtrack is unrecorded). A track is named
    only where it differs from the file's default (`castAudioChoice`, tested),
    a copy being the cost — not on a non-zero index, which drops track 0 chosen
    over another default and copies for nothing. `castSource` settles the file
    before naming it: a `switch` whose rename branch `break`s never reaches the
    soundtrack. A dubbed Matroska download (VP9, an Opus track per language;
    not `remuxable`) gets `remuxTrack`: streams copied, **container kept**, one
    soundtrack; `trackContainer` allows only Matroska (`.webm` → `webm`,
    `.mkv` → `matroska`; an MP4 is already `remuxable`, others a set is
    unlikely to list), the name carries it (`remuxExt`), and
    `remuxKeyFromName` cuts any extension, not only `.mp4`.
  - The copy is made **before** the URL is handed over (a set must not wait on
    ffmpeg), its progress (`/api/convert/{id}`) shown in the player's label by
    `countCastPreparation` until the set has the URL, a late tick dropped; it
    is bytes over the source's size, so it under-reports. Where no copy fits
    (usually the scratch budget), `CastStatus.Note` warns the viewer, on queued
    tracks too, since the set will play the leading soundtrack; an
    unrewrappable file goes as it is. No address on the set's network is 503,
    not 422.
  - Subtitles: `?sub=` carries the choice (`off` for none, else the first),
    indexed like the listing (sidecars, then embedded), so embedded captions
    reach a set like sidecars; both cast handlers run `EnsureCodecs` first or
    the index counts a shorter list. Sent in three places, as sets differ
    (`sec:CaptionInfoEx`, `sec:CaptionInfo`, a `text/srt` `res`; Samsung's
    namespace, read by LG too), as SubRip (`ToSRT` over `ToVTT`, encodings
    handled once): sets read no WebVTT and have no subtitle menu.
  - **A subtitle inside the file is read out before the set is handed
    anything** (`castCaption`, `readCaption`): a set fetches the subtitle as
    it opens the film and holds `Play` until it has it, and the read is the
    whole film (a minute or more for a large one). The page counts the wait
    through `/api/convert/{id}?for=cast` (kind `subtitles` while
    `casting.captioning` marks the item), not by whatever the film is being
    converted for in the browser. One that cannot be read is left out and the
    viewer told (`CastStatus.Note`). `handleCastNext` (the music queue) names
    a subtitle without waiting: no `Play` there for it to hold up.
  - A set's fetch carries `tv=1` (minted by `remuxQuery`, honoured by
    `handleRemux`), lifting the rewrap's 404 for a picture reordering beyond
    its declaration — right for a browser, wrong for a set decoding like VLC
    (else `716 Resource not found`). It authorises nothing else.
  - Refusals reach the viewer as sentences naming the set (`castFault`), the
    SOAP fault going to the log.
  - **A cast claims the set before preparing the film** (`castClaim`; the set
    itself cannot be owned): the newest request takes the claim and cancels
    the previous, every SOAP call runs on that context, and the loser issues
    nothing more, so no viewer's play or resume point lands on another's film.
    Nothing queues (a slow set's 45 s is worse); a generation guards the
    release so an old `done()` cannot clear a newer claim. Claimed at the first
    SOAP call, an abandoned film's long copy could finish later and take the
    set; the copy itself outlives the request. Every step re-checks
    (`castTaken`): in `showing`, in the seek, after preparation, before
    answering (any step can succeed and still be overtaken). A superseded
    request gets **409 naming the set**, never a 5xx (not the set's fault).
  - Silence is not failure: a set may not answer `SetAVTransportURI` while
    already showing the film, so `showing` asks what it holds and the seek goes
    ahead; a `Play` left unanswered is asked the stricter question, whether the
    set is playing the URI it was handed (`started`; holding it while still
    opening is not enough). A refusal (`dlna.Fault`) is an answer, reported at
    once.
  - UPnP 701 (a state the set cannot leave that way, e.g. `LG_TRANSITIONING`,
    whose `GetCurrentTransportActions` offers only Stop) gets Stop and retry
    (`dlna.SetURIFromAnyState`; `endCast` also sends Stop on clean-up): at
    once, then `stopRetries` more times `stopSettle` apart, as a stopping set
    can refuse once more. Nothing else stops a set: 714 (a type it will not
    play) leaves it playing; still refusing after the stop is reported
    **busy**. Reactive, never a Stop before every cast (the cost is one quick
    refusal).
  - `Item.Unreadable` files are never handed to a set (422 at `/play` and
    `/next`; a set can stick on one). Automatic roll-on skips them (`damaged`
    in `query.ts`, tested, applied by `findKind` wherever the player advances
    *by itself*); a swipe still lands on one.
  - Music queue: the bar hands the set the next track in advance
    (`SetNextAVTransportURI`, optional) and sees it move on when the reported
    URI changes; failing that it sends the next on an ending, polling more
    often than the player (`CAST_POLL` 3 against 4) and every second near the
    end (`CAST_END_WINDOW`), as gaps are heard. A set about to advance says
    STOPPED briefly, so with something queued a stop waits `CAST_HANDOVER`
    polls before counting. A stop short of a track's end is the remote, so the
    bar stops driving, unless the set gives no duration (then the next is
    sent). STOPPED before playing is no ending (`seen`; a start can take 7 s).
    `load` splits into `loadDecks` and `castLoad`. The bar's spectrum is
    dimmed, not removed, while a set plays (routing into Web Audio cannot be
    undone and spends the AirPlay route).
  - **One transport** (`CastTransport`, `casting.ts`) under two adapters
    (`audio.ts`, `video.ts`): tick, carried clock corrected by answers,
    cadence, `castStep`'s seen/ended/stopped reading, handover wait, and a
    generation guard dropping answers after a stop, kept once (two copies drift
    in what a stop means). No DOM, injected timers, tested by `casting.test.ts`
    against a fake set. Only playing and paused write the carried state, not
    opening. The player's hooks persist position per poll and roll a season
    on; the bar's clear its error streak, follow the set into the queued track
    and send the next.
  - While a set holds the film nothing reaches `startSource` (the one door for
    sound fix, track change, escalation and rewrap; a source here is a second,
    audible showing); source-bearing polls stop when the cast begins; media
    keys drive the set; the element's `error` is ignored; a chosen subtitle is
    re-sent like a soundtrack; the set's own stop hides the poster and resets
    the clock.
  - Every await in the player is guarded by the film's id (`refreshItem`,
    `loadSubs`), or a swipe mid-request repoints it; `load` shares one
    in-flight item request per file; `menuShift` (`playback.ts`, tested) places
    menus against the visible viewport.
  - Casting forks the player's transport: `curT`/`totT`/`seekTo`/`togglePlay`
    answer for the set, so the seek bar, clock, resume point and media keys
    work unchanged, the poster covering the paused, empty element. The clock is
    corrected every fourth tick (`CAST_POLL`), counting ticks, never the
    fractional position (a modulo would misfire).
  - A season rolls on while a set plays it: `goTo` passes the next file to
    `castItem` after the shared per-file reset (`beginFile`), so remembered
    languages apply; `endedOnSet` (`playback.ts`, tested) tells an ending from
    a remote stop by the clock (ended rolls on, stopped ends casting); the poll
    stands down during a handover, or the old film's STOPPED reads as a second
    ending. `playSeason` plays a season card as an ordered listing. Closing the
    player stops the set.
  - Deliberately no authentication: whoever reaches the port can play to a
    television in the house.

- **Media URLs carry their own permission** (`sign.go`): receivers (AirPlay)
  fetch URLs without credentials, which a password proxy refuses. The page
  prefixes a token from `/api/info` to every media path
  (`/api/signed/<token>/stream/<id>`; also `hls`, `remux`, `transcode`,
  `subs`) via one route (`{rest...}`), so relative HLS segments stay signed.
  `signedPaths` is a media-only allowlist; a token is no login. It encodes
  only its expiry, so the page builds URLs itself (per-file tokens would
  defeat thumbnail caching); a leaked URL streams until `signTTL` (12 h). The
  key, `blob.SignKey`, survives restarts; deleting the db revokes every link
  (`-db off`: per run). The proxy needs a `/api/signed/` location with auth
  off carrying the face's `X-Media-Content`, or a music face serves films
  (README).

### Frontend: the music bar, the spectrum and media keys

- **Pausing fades** (`FADE_MS`, `audio.ts`; an abrupt stop clicks): 150 ms
  down, then the pause; resuming climbs to the live volume setting. It ramps
  the element's volume, not a Web Audio gain (graph routing is
  irreversible). Frames smooth it and a timeout ensures it ends (hidden tabs
  run no frames); the first applies the endpoint once, under a generation
  counter. A track boundary or close cancels it and restores both decks to
  full (boundaries are gapless swaps; a part-faded deck would play the next
  track quietly). Where inaudible (`fadeHeard`: iOS ignores element volume,
  `volumeSettable`; hidden pages throttle timers) a pause is immediate, or a
  late pause overrides a play pressed meanwhile. A play during a fade-out
  reverses it (`fadingOut`).
- **iOS queues keep the element the listener started** (`AudioDecks` in
  `audiodecks.ts`, chosen by `singleAudioElement` in `playback.ts`):
  permission and the audio session belong to an element. iPhone, iPod and
  iPad WebKit (incl. an iPad's desktop UA, CriOS, FxiOS) swap the source on
  that element and call `play()` in the same callback — no `pause`, `load` or
  async wait; the idle element never plays or preloads (cost: a possible
  buffer wait at boundaries). Never "unlock" a spare by an empty-source play
  counting any error but `NotAllowedError` as success: it proves nothing, and
  a second element can disturb the route. The active element loops
  (`wrappedAround`, `checkWrap`), never ending naturally before the swap
  (WebKit may drop the session there); each seek updates the seek-vs-loop
  clock (`seekDeck`, also the lock-screen scrubber), reset by a source change
  before queued events; the queue's end pauses. Desktop alternates
  prebuffered decks. `AudioDecks` owns source changes, play requests and the
  cancellation generation; stale rejections are dropped, a current one
  reaches `audio-refused`. Verify iOS's audio session and car Bluetooth on a
  device after deployment; the per-track `audio-stalls`/`audio-refused`
  reports name no cause, and request logs are blind behind a buffering proxy.
- **The player has the spectrum too** (the same `Visualizer`, whose `attach`
  takes media elements, and `SpectrumPanel`), prepended to `.vo-controls` to
  stay above the seek bar however the row wraps, and hidden in fullscreen
  (`syncViz` stops painting). Where the sound must be moved it costs the
  AirPlay route: the button warns first, `showReceiverButton` drops the
  receiver button, and `routed` follows the actual tap (`movedOutput`).
  Dimmed while a DLNA set plays. `dispose()` closes the context with the
  player, as a browser allows few.
- **The spectrum copies the sound and moves it only where it must**
  (`tapChoice` in `visualizer.ts`, tested). `createMediaElementSource` moves
  output irreversibly, taking the AirPlay route with it, and any later
  failure (e.g. a `video.src` swap) mutes the film until reload and falsely
  trips `videoAudioIsReadable`. Copy via `HTMLMediaElement.captureStream`
  (`mozCaptureStream`); **wait**, never move, while the deck has no
  soundtrack (the capture would carry no track); move only with no capture
  (Safari). The
  deck's `loadeddata` re-takes the tap; `release()` returns copies on close
  (a move stays). The analyser is a leaf on a silent gain (to the
  destination a captured deck plays twice; an unpulled graph reads zeros); a
  moved deck plays through its own gain, wired first, so `giveUp` has
  nothing to repair. Build the graph on first open only, fade moved decks in
  over `ROUTE_FADE`, and resume the context before tapping.
- **A quarter-octave analyser**: 36 ratio-spaced bands of 0.2516 octaves
  from 30 Hz to `min(16 kHz, sampleRate * 0.45)` (lossy files are silent
  above), 24 below a 288 px canvas; a power curve would put DC under the
  first bars. `fftSize` 4096 (17 bins under 200 Hz), not 8192 (a 170 ms
  window smears transients). Tilted +3 dB/octave about the geometric centre
  so music reads roughly flat, deliberately not raw magnitude; hence
  `minDecibels` -100, or the lifted top band shows in silence. A band is its
  loudest bin, not its mean (which flattens tonal peaks); edges interpolate,
  but the bottom two octaves are not truly resolved. `smoothingTimeConstant`
  applies per read (frame-rate dependent), so it is only 0.25; the real
  smoothing is `1 - exp(-dt/tau)` on the frame clock, 20 ms up, 150 ms down.
  Caps hold `PEAK_HOLD`, then fall under `PEAK_GRAVITY`.
- **The panel is the picture**: no heading or close button, 6 px padding;
  its button closes it too and is lit while open (`markViz`); the label is
  the canvas `aria-label`; the only text, the fault report `.viz-note`,
  hides by `:empty`.
- **Silence is a designed state**: nothing moves without sound; never add an
  idle animation. The floor dims when the context is not running, and every
  showpiece term is zero at silence. Once bars and caps are under
  `STILL_EPS`, energy and kick have decayed and one settled frame is
  painted, painting stops, but the analyser is still read each frame (~0.4%
  of budget) so sound shows on the next frame (a timer throttle would lag
  ~¼ s).
- **A dead audio path says so** (`deafStep` in `playback.ts`, tested;
  `watchForSilence`): phone WebKit accepts routing a `<video>` yet passes no
  sound (an `<audio>` works), so no feature test predicts it, and nothing
  avoids it (no `captureStream()` in Safari; a second element would
  double the bytes). After `DEAF_AFTER` s of real playing (unmuted, volume
  up) with no band ever moving, the header reads "no audio reaches the page"
  — never once sound was heard, counting playing time only, withdrawn if
  sound arrives. `onDeaf` then has `videoAudioIsReadable` drop the button
  for the session (the measuring film keeps its panel); not persisted, as
  WebKit may fix it.
- **The canvas carries no padding** (under `border-box` the bitmap would not
  match its box, resampling every composite); padding is on `.viz-body`.
  Size via `getBoundingClientRect`, not truncating `clientWidth`, checked in
  device pixels each frame rather than by a resize listener (a pixel-ratio
  change fires none; gradients are device-space); pixel ratio capped at 2.
- **Hue runs along the frequency axis**, not up the bars (a vertical ramp is
  only sampled to the tallest bar). The 36 bodies are one path and one fill;
  refilling it (`fill()` keeps the path) washes toward the background up to
  two fifths high; a constant crown at 22% of `--text` edges each bar.
  Colours come from the app's tokens at build time, never literals.
- **The showpiece layer is driven by sound alone**, zero at silence:
  - **Bloom**: the panel redrawn `BLOOM_DOWN` times smaller, multiplied by
    itself (a bright-pass, or dull pixels fog) and added back with `lighter`;
    no `filter()` or shader; radius five, not eight (tinier is blotchy).
  - **Glass floor** (`REFLECT`: the strip above the baseline mirrored below
    it, faded destination-out), **stage light** (alpha = smoothed energy) and
    **sparks** (a crown flashes when its band rose over `SPARK_DELTA` in a
    frame).
  - **Hue flow**: translating between path build and fill slides a periodic
    gradient (accent → accent-2 → accent, twice over double width, so it
    wraps seamlessly) through the geometry, advancing with energy.
  - **Kick** (the bottom `KICK_BANDS` bands; slams light, bloom and flow):
    an onset is a rise of `KICK_DELTA` over the recent **floor**, not the
    average, which dense blast beats lift past the hits; the floor drops at
    once and climbs slowly; a kick above half strength does not re-fire.
  - **`pace`**: spectral flux smoothed over `FLUX_TAU`, mapped to
    `[1, PACE_MAX]`, divides every release (bar fall, cap hold and gravity,
    kick decay, floor climb), never an attack. Reduced motion pins it at 1
    and stills flow, sparks and kick.
  - Deliberately not done: album-art palettes (per-track cost, off the
    accents), per-bar fill styles (break the batched path), frequency
    hairlines (unreadable on a phone).
- The tab title (`nowplaying.ts`) is the player's file, else the bar's track,
  else the app's name.
- Media keys are **claimed, not registered** (`mediakeys.ts`): the video
  player takes the page's one set while open and returns it on close;
  unclaimed actions are cleared rather than left to a hidden bar, and the
  player leaves previous/next alone. Actions do what they name, never toggle
  (the two sides' states can disagree); `stop` pauses in place;
  `playbackState` is published on every play and pause; mute is the system
  mixer's. The player's keys are one table, `PLAYER_KEYS` (`playback.ts`,
  tested), which `?` draws, so the card cannot drift from the handler.
- The queue panel renders only while visible: unhide it before filling it.

### Background work

Background work is priority-ordered: **the interface > playback >
thumbnails > tag enrichment > reading how the music sounds**. Playback,
thumbnails and tag reading report through `busy()`; requests somebody waits
on mark the library (`Library.Used`, one `Handler` wrapper, skipping
loopback reads) and the analysis then rests for `uiQuiet` (5 s), retrying
rather than giving up. `handleStream` marks media responses
(`Library.StartStream`/`Streaming`) except internal reads
(`library.InternalHeader`); while one is live, thumbnailing drops to one job
(`bgSem` in `thumbs.go`) and `EnrichMeta` pauses between files (its `busy`
also checks `Thumbnailer.Generating`). Overlays call
`holdThumbs`/`releaseThumbs` to abort and re-queue grid thumbnails. New
background I/O keeps this order: on cold start the disk is the bottleneck
and playback must win.

**The gate is held across the wait, not only the delivery**: a rewrap marks
it in `handleRemux` and cast preparation (a viewer waits on that read), but
a production nobody waits on is unmarked (it outlives its request). Border
detection marks it (it borrows the thumbnailer's ffmpeg slot); the watcher's
debounced reads stand down for `Library.Streaming`, bounded by
`enrichBusyWait` (they hold a process-wide slot). `serveStream` marks before
the open (opening a compressed member unpacks it); caption extraction marks
it on its own context, not the first asker's. Every release is a `defer`: a
leaked count reads as playback for ever.

### Faces and path restrictions

**One library, several faces** (`internal/server/content.go`):
`X-Media-Content: music` (or `videos`, `images`, comma-separated) shows that
and nothing else, in listings, counts and by id. One scan feeds every face.

The proxy sets the header, **never the page** (`<img>`/`<video>` requests
carry none), so filtering is in the backend. Nothing depending on
`/api/info` is drawn before it arrives (`contentKnown`), or the first live
event draws every chip; hiding empty views (`content.ts`, tested) is a
courtesy. Enforcement is
`Server.item` for every by-id handler — the album sheet and zip (`albumFor`:
permitted tracks only, else 404) and positions (one proves a film exists)
included — plus `content.kinds()` on listings and `content.mask()` on
counts. Albums and artists are music's; music includes playlists.

**A restricted face counts; it does not mask**: masking cannot handle the
cross-kind totals (started, finished, played) and lets stream and listing
disagree, so restricted callers go through `CountsFor` with the face's
`Kinds`, unrestricted ones keep the O(1) running totals (`q.Kinds == 0`),
and `mask` stays as a belt. The debug access log records each request's
**face**, as a proxy block missing the header fails invisibly.

**A caller can also be restricted to part of the library**
(`X-Allowed-Paths`, `internal/library/paths.go`: directories, CSV-quoted or
one per repeated header field), everywhere; absent or empty is everything.
**The two compose**: `Server.item` asks both, listing queries carry both, and
the result is the intersection. Three things about the path restriction are
load-bearing:

**It matches the absolute path**, not the display path (`Rel` starts at the
root's base name, so two roots can collide); an archived member's path is
the archive's plus a NUL and the member, so needs no special case (tested).

**The prefix test is component-aware**: `/srv/mediax` is not under
`/srv/media`.

**It is a value type in every cache key** — listing, counts and ETag — or one
caller's restricted answer reaches the next; `versionTag` hashes the
restrictions in and sends `Vary: X-Media-Content, X-Allowed-Paths`.

Collections (albums, artists, genres, shows) are cached over everything; a
restricted caller gets the albums with any permitted **track**, and artists
and genres regrouped from those, never filtered afterwards (which counts
unseen ones), paid only when the header is set. **Every grouped endpoint
passes the restriction on** via `groupedView`/`listOrNear` (`near` or
ordinary by one rule; a list, never `null`), pinned by
`TestPathsRestrictEveryCollection`: one that skips it serves the whole
library under restricted chips, invisibly.

`library.KindSet` carries the face into a query as `Query.Kinds` (not
`Query.Kind`, the viewer's chosen view), a value type to key the cache.

### Build info, directories and the listener

**What is running is on the screen** (`about.go`, via `/api/info`): the build
and the hardware. The linker stamps the version (images build from copied
sources); a checkout's `debug.ReadBuildInfo` wins, never being stale, and
`modified` is shown. Capabilities list only what is established; the box
names only what is missing. `FindHardware` runs at startup in a goroutine (a
broken driver can spend the whole probe budget); conversions before it
finishes use software. Read via `chosen()` under a lock: `/api/info` can ask
mid-search (`sync.Once` covers only later readers).

The directories to index can change at runtime (`internal/server/prefs.go`,
`/api/prefs`); one callback in `main` moves three things together: the list,
the watches (`Watcher.Reset`, or a watch on a removed directory restores
what the scan removes) and the index, where `stillIndexable` asks
`underRoots` (a removed directory's files are still on disk). Stored roots
outrank the command line, which seeds a first run; with `-db off` a change
lasts the run and `/api/prefs` reports `persisted: false`. Directories
nested in a listed one are dropped (it walks them already).

**One change of the directories at a time**: with no authentication two PUTs
can interleave, leaving the database naming one set of roots while the index
walks another. `main`'s `prefsMu` spans both stores and the scan launch;
`Server.rootsMu` spans the callback and the read of the answer, so no request
reports the set another applied; both are brief and released before the answer
is written. `watcher.Reset()` runs inside the queued scan under `scanGate`, or
a walk already running installs watches under the removed root that nothing
removes; `AddDir` refuses paths outside the roots (for settle timers).
**A confined caller is refused the preferences outright**, reading too (the
list names roots it must not learn), checked before `-lock` so it cannot learn
what `-lock` would say; `/api/info`'s `confined` hides the button (like
`content`), the handler is the guarantee.

**There is no authentication in front of this**: whoever reaches the port can
point the library at any directory the process can read — deliberate for a
personal server on a trusted network.

`run` binds the listener itself (not `ListenAndServe`) so `-listen` port 0
resolves to the real port for `browse.URL` and `library.SetLoopback`, and bind
errors surface before anything claims to listen. `-open` without `-listen` uses
`127.0.0.1:0`: an ad-hoc session must not collide with a running instance or
expose the library to the LAN.

### Serving details worth knowing before "fixing" them

- **Some conversions run on the graphics hardware** (`hwaccel.go`), only above
  `hwPixelRate` (a little over 1080p60): software cannot keep up with 4K60
  HEVC and no setting fixes it (`ultrafast` 0.61× real time, decoding alone
  0.78×), but fixed-function encoders spend far more bits and look worse (DVD:
  4.3 vs 0.8 Mbit/s).
  - `hwaccel_output_format` is load-bearing: without it frames are copied back
    to system memory at more cost than the hardware saves. The codec list is
    short on purpose: hardware that cannot decode silently yields no frames,
    and old codecs are the likeliest to need converting. No deinterlacer:
    `bwdif` needs system-memory frames, the hardware's own fails on this
    driver, and nothing above the line is interlaced.
  - **Wide colour is tone-mapped** (`hdr.go`, `Item.HDR`): a re-encode would
    otherwise copy the BT.2020 description into H.264, which in HLS may not
    claim HDR. `tonemap_vaapi` reports success and right tags but outputs
    no picture (test bytes per frame, not tags), so the processor tone-maps
    even in a hardware run: the engine decodes and **scales first** (0.86× at
    1080p vs 0.15× at 4K), then `zscale → tonemap(hable) → zscale`, then back
    up to encode. Without `zscale` the picture is labelled BT.709 (wrong colour
    beats unplayable). An ordinary picture is never tone-mapped (it would drain
    its colour).
  - `hwRefused`: a failed hardware run (silent, unpredictable from codecs)
    writes the engine off for that file for the run, a `perFileVerdict` like
    `aspects`; the processor takes over, at once if nothing was sent yet.
  - **A start-over is a loop, never a re-entry**: a re-entered piped handler
    holds one of the two conversion slots while asking for the other (two
    concurrent failures deadlock) and counts twice against the background-work
    gate. `HLS.run` must `clearSession` before retrying (no `-y`), so it
    refuses to retry once a segment exists that waiters may be reading (see the
    HLS sessions).
    `watchFirst` starts once per session; the plan is closed before a retry so
    an abandoned repair pipe dies; the aspect retry is gated on `repairable`
    everywhere (else archived and MPEG-4 files retry in vain).
  - `hwProveToneMap` proves the tone-map splice at startup (the base proof
    strips `-vf`); on failure the engine still serves, wide-colour films keep
    their colour, and a warning is logged.
  - `haveFilter` memoises only success (one transient failure must not disable
    tone-mapping for good) and reads `ffmpeg -filters` with **nothing held**,
    or a slow binary or hung mount stalls every HDR plan, again and again since
    failures are not remembered. One reading per binary, shared by waiters
    (empty means no; the next request asks again). It publishes from a
    **defer**: followers have no context and leave only when the channel
    closes, so a panic would strand them for the process's life. `readFilters`
    has `hwProbeBudget` (20 s) and `WaitDelay` (a grandchild can hold
    `Output()`'s pipe past the kill); an expiry is not recorded.
  - Backends VAAPI, QSV, NVENC, VideoToolbox; only VAAPI measured — safe
    because each is **proved by a real encode** before use (device nodes,
    loaded drivers and encoder lists can all be wrong).
  - Meant for the binary; the image has `intel-media-driver` but needs
    `--device /dev/dri`. `Item` keeps `Width`, `Height`, `FPS` from
    `EnsureCodecs` (run at open, when the conversion is decided): the rule is
    pixels per second.
- `/api/transcode/{id}?t=SECONDS[&mode=audio]`: fragmented MP4 on stdout, no
  ranges, seek = reopen with `t`; at most 2 concurrent; counts as streaming for
  the background-work gate. `mode=audio` copies the picture and re-encodes the
  sound (mandatory for 4K HEVC, far below realtime re-encoded). `checkDecodes`:
  no decoded frames → full; frames but no decoded audio while `item.acodec`
  names a soundtrack → audio (E-AC3 in MKV plays silent with no error). Input:
  `it.Path`, else the loopback URL for content inside another file, else the
  stdin pipe (a seek reads from the start).
  - Known codecs settle the soundtrack **before** playback (`useKnownCodecs`,
    `decodesAudio` in `playback.ts`): `ac-3`/`ec-3` answer definitively, and
    Chrome would skip an undecodable AC3 for a decodable commentary, fooling
    the decode check — so convert from the leading track. Untyped codecs
    (`wmav2`) go to the decode check; DTS and TrueHD are refused.
  - **The frame counter is evidence only where the browser keeps one**
    (`framesReported`): an undecodable picture draws black with no error (read
    off `getVideoPlaybackQuality`), but on the **segmented path iOS decodes
    outside the page** and reports zero for a healthy stream, which would
    escalate a copy into a stalling re-encode. It is `null` there
    (`pictureRoute` judges by width); an undecodable stream there errors.
  - Audio mode escalates to full on an undecoded picture **or an element
    error** (with the picture copied, the error is the picture's) — never
    writing HLS off for the pipe those browsers cannot play.
- **Browser faults go to the server log** (`clientlog.go`, `report.ts`,
  `POST /api/log`): element errors by code (`mediaErrorText`), give-ups with the
  sentence shown, feed stops and resumes, failed API requests, uncaught errors,
  the music bar's stalls and refused plays — each with film, position and route
  (`routeName`). **Bounded**, as the one route where clients write the log: a
  large body is refused unread; fields are trimmed and stripped of control
  characters (a newline forges a record); a process-wide rate limit reports its
  drops; the page won't repeat a fault within a quiet window (`shouldReport`).
  Sent with `keepalive` and forgotten; a failed report is never reported. A
  film the caller may not see is logged **without the film** (no existence
  oracle). Always 204.
- **A re-encode keeps the source's timing** (`-fps_mode vfr` in
  `planConversion`): with no declared rate, ffmpeg's constant-rate default takes
  the container's time base — 1000 fps for a millisecond ASF — duplicating
  every frame. Constant sources and copies are unaffected.
- **A viewer can ask for fewer bits** (`q=` on `/api/transcode` and `/api/hls`;
  `quality`, `qualityTiers` in `convert.go`; `qualities` on `/api/info`): the
  link cannot be measured, and a native file plays at its own rate. The
  "Original" pill offers 6, 3 and 1.5 Mbit/s, only rungs at least a fifth under
  the file's rate (`qualityChoices` in `playback.ts`; closer is a re-encode for
  nothing).
  - A rung is **always a re-encode** (`effectiveCopy`) and a **ceiling, not a
    target** (`rateCap`: `-b:v`/`-maxrate` at the rung, `-bufsize` twice it):
    a burst above the link is the stall being cured. The picture fits the
    rung's **box** (`boxScale`, `hwScale`: 1080/720/480 high, 16:9, either way
    up), bounding a portrait clip by height (verified on `scale` and
    `scale_vaapi`; unmeasured engines keep width-only scaling); an HDR
    tone-map still runs on the processor.
  - Off-ladder rates are refused (`parseQuality`). The rung is the last part of
    `hlsKey` (older keys still parse); the master `BANDWIDTH` is rung plus
    soundtrack. The choice lasts the session (`chosenKbps`, `video.ts`), never
    persisted (the link is the day's), and is disabled while a set plays (it
    fetches the file itself).
- **A conversion the page can name is fed by the page** (`mse.ts`): the pipe
  has no ranges, so the browser's reconnects restart it from zero — waste that
  grows with the position and on a slow link is the link. For H.264+AAC (every
  full conversion) the page reads one `fetch` into an MSE buffer, leaving no
  URL to reconnect to.
  - Back-pressure: reading stops at `FEED_AHEAD_S` (ffmpeg stalls, a pause
    costs nothing); what is more than `FEED_BEHIND_S` behind the playhead is
    dropped (the quota is unannounced), with one evict-and-retry.
    `initSegmentEnd` finds the whole init segment; `codecStringOf` reads `avcC`
    (e.g. `avc1.64002A`, `mp4a.40.2`). A seek reopens at the keyframe as a new
    feed; `tcOffset` is unchanged.
  - **A broken feed resumes where its buffer ends** (`resumeAt`), never via a
    new source, which discards the buffer: same conversion from that time,
    `timestampOffset` aligned, appended to **the same SourceBuffer**; at most
    `FEED_RESUMES` (8); never with nothing buffered or after a server status
    (asking again repeats it).
  - **A message never contradicts the picture**: feed errors must not reach
    "cannot be played"; without an HTTP status they restart from the picture's
    time (`FEED_RETRIES`, 2, so an instant failure cannot loop); `onTime`
    clears faults while the clock advances; Try again on a playing film only
    clears.
  - The receiver button goes while fed (object URLs are unfetchable; DLNA
    stays). Not on Safari (native HLS has ranges) or for soundtrack-only
    conversions (the picture is copied in any codec, and the file arrives
    behind it). Hand-written: the frontend has no runtime dependencies.
- `/api/remux/{id}` rewraps (`-c copy` into a faststart MP4, no frame touched)
  where `remuxable` (`remux.go`) finds: a container the browser won't open
  around decodable streams (a short allowlist — HEVC moved into MP4 only meets
  a browser that cannot decode it); HEVC tagged `hev1`, which ffmpeg writes
  unless told while Apple takes only `hvc1` (copy with `-tag:v hvc1`); or
  `moov` after `mdat`, as phones record — not progressive, so Safari hunts (907
  requests, 11× the file); `+faststart` cures it (`wantsFaststart` in
  `playback.ts`, from the listing or `useKnownCodecs`; the wait bounded as for
  any rewrap).
  - The case is read from the sample entry's four-character code
    (`library.VideoSampleFormat`, `mp4box.go`) in pure Go via `OpenItem` (rar
    members too), not ffprobe.
  - A decode failure asks for the rewrap only where `canPlayType` has HEVC
    (`decodesHEVC`), else a copy is waited for in vain. The re-armed decode
    check waits for some *playback* (`checkAt`) — the frame counter restarts at
    zero and an early check would re-encode a file about to play; a resume
    counts from the file's real start.
  - 404 means "rewrapping would not help" → converter; nothing is asked first.
- **A stream can understate its reordering** (`reorder.go`): more B-frames than
  `max_num_reorder_frames` makes a browser emit early and drop frames (ffmpeg
  and VLC cope); a faithful copy keeps the lie, so re-encode.
  - One ffprobe over `reorderProbeFrames` of the opening; `reorderVerdict`
    decides. Two signals, **not equals**: **backwards output timestamps** are
    direct evidence (two needed; an edit list causes one); a **long B-run** is
    a heuristic — non-reference Bs need depth **one** at any length and
    `pict_type` cannot reveal a pyramid — so a run counts only when it exceeds
    the declaration by **more than one**. Never loosen this: `declared+1`
    (`IBBPBBP…` under 1) is ordinary, and an unguarded comparison or a
    `declared < 2` guard condemns such files to a re-encode. A declaration of
    *none* with any B-frame needs no margin.
  - **The verdict rides the listing** (`stampReencode`) so the player checks it
    **before** handing the element the file (`load`), not after a second of
    playback; only existing verdicts are stamped, never a look per tile.
  - Per run, by file identity. **No answer is not an accusation** (no ffprobe,
    a pipe-only file, a timeout, unparsable output) — a false one re-encodes a
    film whole; an unfinished look is **not cached**, or the item fetch's short
    budget would excuse the film for the process's life.
  - **One look per film** (`reorderCache.claim`/`settle`, leader/follower), as
    an opening asks from several places against the disk playback needs:
    followers take the verdict or, where the leader's budget ran out, look
    themselves (serializing only where ffprobe hangs); each leaves on its own
    `ctx.Done()` (the client's, on `/api/remux` and `/api/hls`). Settled from a
    **defer** (`Server.look`); its ffprobe sets `WaitDelay`.
  - Applied wherever a picture is copied (the rewrap answers 404; the piped and
    segmented soundtrack conversions), since the client sees neither a header
    nor the status behind a `<video>` source, and to native playback:
    `/api/item` looks at open (once per film per process) and stamps
    `Reencode`, so `plannedRoute` (`playback.ts`) converts and `tryRemux` stands
    down. **Not** for a television (it decodes generously). Common in AVI,
    which cannot express reordering.

- **A soundtrack conversion becomes a file while it is being watched**
  (`?mode=audio` on `/api/remux/{id}`, `remuxSound`, `upgradeToSoundFix` in
  `video.ts`) — the commonest case, a decodable picture with an undecodable
  soundtrack. The rangeless pipe re-sends from byte zero on each reconnect,
  waste that grows with position and no pacing or buffering removes (one 4K
  viewing: 963 MB for 167 MB); the file is made behind playback (4K, 22 min:
  2 s copy + 37 s encode).
  - `-aac_coder fast` here only, being the whole wait (37 s vs 61 s); the
    live conversion keeps the default.
  - The kind is in key and file name (`-aac`), or a plain copy can be served
    as the sound-fixed one and the film is silent; plain copies carry no
    marker, so older names still parse and adopt.
  - `-tag:v hvc1` for any HEVC picture, not only `hev1`-tagged sources:
    copied out of Matroska, ffmpeg writes `hev1`, which Apple refuses.
  - The player polls (production outlives the asker; a long request pins a
    connection), and stops on 404, when the picture needs converting too,
    and on Safari (segments have ranges).
- **Choosing a soundtrack in a container the browser already opens costs a
  copy, not a conversion** (`?mode=track`, `remuxTrack`, `startTrack` in
  `video.ts`): only Safari switches tracks on the element, so `startTrack`
  asks for the container copy wherever `opensDirectly` holds. A conversion
  would re-encode a playable VP9 picture (the MP4 rewrap refuses VP9) and
  transcode an Opus dub to AAC.
- **How much to convert turns on whether the picture is *proven*, not on
  what it is called** (`convertMode` in `playback.ts`, tested); its two
  callers mean opposite things, so never merge them. `trackMode` (playing,
  so decoded) converts only on a definite no, a codec with no type string
  being proven by its frames; `convertForContainer` (container refused,
  nothing decoded) copies only on a definite yes — `wmv2` (`decodesVideo` →
  `null`) copied into an MP4 yields nothing. A codec-name test
  (`vcodec === 'h264'`) fails VP9 in a refused container.
  **Safari asks for the rewrap too**, up to `REWRAP_WAIT_LIMIT` = 2 GiB
  (`rewrapWorthTheWait`): a disk-speed copy (~270 MB/s) gives the file
  itself (native seeking, its own clock, no held ffmpeg); past the limit
  HLS wins. A browser without native HLS waits at any size, its alternative
  being an unseekable pipe.
  - It is a file because iOS Safari opens media with `Range: bytes=0-1` and
    refuses a 200 without ranges; `http.ServeContent` serves it, with no
    timestamp corrections needed.
  - Not on the requesting context (Safari hangs up on its opening probe):
    callers wait on their own and the work continues. Scratch only — the
    database is the one thing worth keeping.
  - `Remuxer.Close` cancels copies in progress (no ffmpeg outlives a
    restart); `File` creates the context and cancel under the lock before
    publishing the entry, as `Close` skips an entry without one.
  - A copy finishing at shutdown is kept, not offered: never fold "closed"
    into "failed", which unlinks it under its caller. The waiter gets
    `ErrNoRemux` (a closed Remuxer cannot protect the file from pruning);
    the next run's `Adopt` takes it.
  - In-flight copies count against the budget (`Remuxer.pending`): `File`
    prunes against finished + writing + this film, `produce` releases the
    reservation under the lock; else concurrent admissions miss each other.
    The segmented converter's share is still uncounted (`Scratch` offers
    only `Excess()`, which then reads 0).
  - Both converters and the unpacker (`library.SetScratch`, under `unpack`)
    share one space and budget (`scratch.go`, `-tmp`, `-tmp-max`), each
    reporting its holdings and freeing its own least recently wanted.
  - A finished rewrap is counted, and the budget pruned, **before** waiters
    are released, or an immediate next request races the accounting (no
    entry is a candidate until `done` closes).
  - The prune **unlinks under the lock**: a deferred unlink could delete the
    copy a new `File` just wrote at the same identity-derived path. It holds
    the lock for a few `unlink`s only, since `File` refuses sources over the
    whole budget; those go segmented, and a lone over-budget session is
    never killed under its viewer.
  - `/api/convert/{id}` (the most recently asked-for conversion; with
    `?for=cast`, the subtitle read a cast is waiting on) is polled **only for
    a rewrap's wait**; segmented runs report nothing. The readout
    goes once something plays; a generation check keeps late polls out.
- `/api/hls/{id}/index.m3u8?t=&mode=` is the same conversion as segments,
  for Safari (chosen on `canPlayType` for `application/vnd.apple.mpegurl`;
  others keep the pipe), which refuses media without ranges; it plays after
  the first segment (0.31 s vs 56 s for a whole file).
  - **The session is in the URL path**: segment names resolve against the
    playlist URL minus its query, so `?t=`/`?mode=` cannot name it. Never
    redirect the playlist (browsers disagree on a redirected one's base);
    every name carries the session token.
  - **The playlist is the whole film from the first request** (`hlstable.go`,
    `hlsTable`), as a growing one plays as live: segments are decided up
    front, listed with real lengths and the end marker, and made **as
    asked**, a seek converting from its segment. Re-encodes cut on a
    four-second grid (`gridKeyframeExpr` forces keyframes); copies only on
    keyframes from the container index (`library.Keyframes`: Matroska cues,
    MP4 `stss` with the edit list applied as ffmpeg does; not a packet scan,
    19 s for 14 GB), boundary k being the first keyframe at or after k
    segment-lengths from the first, cumulative, one keyframe never two
    boundaries.
  - No readable index, pipe-only content or a disc title: the playlist grows
    from one conversion at the seek, with the pipe's clock arithmetic;
    `X-Media-Timeline` (read by `hlsClock`) says which shape, and with a
    table the element's clock is the film's.
  - **Every cut is verified; no landing is assumed.** Segments keep the
    film's clock (`mpegts_copyts=1`, `-copyts` from zero, *never*
    `-avoid_negative_ts make_zero`, which rebases it). A copy cuts relative
    to where the demuxer landed (ffmpeg takes 3/23 s, `hlsSeekLead`, off a
    reordered stream's seek), trying boundary + lead first, read back from
    one packet (`landing`, `tsFirstVideoPTS`), else a hair past, the lead-in
    discarded.
  - `-segment_list … csv` (`+live`: renamed whole, a line per closed file)
    records finished segments; each line is checked (`verify`; the first by
    its end only) and a wrong cut ends the session.
  - A re-encode checks its landing too (`gridSeek`, `seekLanding`): forced
    keyframes count from the first encoded frame, so it must land at or
    before the point, else it steps back (`gridSeekStep`, doubling) and
    trims in the graph (`conversion.trimTo`; an output seek resets the
    clock). Landing is read by framecrc (`framecrcFirstPTS`), which, unlike
    the transport-stream probe, sees a copied WMV picture.
  - Files are per run (`run<n>-seg<k>.ts`), never overwriting another run's;
    `done.txt` is the manifest a later process adopts.
  - An unmade segment waits on the run that will reach it if that is about
    as soon as a fresh start (`hlsWaitFor`: the run's pace, 1 s per segment
    until known), else a run starts there (`hlsFirstWait` bounds the wait);
    a second run ending without it gives up (`hlsRunFailures`).
  - Runs are judged by output, not exit status (the graphics engine errors
    flushing a run cut by `-to`; writing it off would send later runs to the
    processor).
  - A run stopping at an earlier run's segment (`until`, reading
    `hlsRunTail` past it) also lists the cut at `until` (`runCuts`), or a
    reordered picture overruns it and `verify` gives the session up.
  - A run from the very start of a reordered file comes out late by the
    reorder delay (`make_non_negative`) and is left so: inside the 0.1 s
    start tolerance at 24 fps, not for low rates or deep reordering; it
    wants measuring first.
  - The conversion is chosen from codecs, not decoding (a refused container
    decodes nothing); an H.264 picture is copied.
  - A finished playlist is relabelled VOD (ffmpeg leaves
    `#EXT-X-PLAYLIST-TYPE:EVENT`, read as live); EVENT stays while growing.
  - The master BANDWIDTH is the stream served, not the source
    (`convertBitrateGuess`, a description, not a budget), lest a thin link
    think it cannot afford a re-encode.
  - **Subtitles ride the playlist as renditions** (`hlssubs.go`), AirPlay
    giving a receiver only a URL: a film with any gets a master playlist,
    each subtitle (sidecars and embedded, listing numbering) an
    `#EXT-X-MEDIA` rendition named relative under the signed session path.
    One conversion serves all; `?sub=` marks the `DEFAULT`, the only
    `AUTOSELECT` (else system language shows subtitles unasked); `NAME`s are
    unique as written (players merge duplicates). Each is one unsegmented
    WebVTT rebased by the `?shift=` arithmetic; an adopted session reads
    identity and start from its key (`hlsSessionItem`), keeping captions
    across a restart.
  - There the **stream owns subtitle display**: the menu marks the choice
    via `markSubMenu`; the page's `<track>`s stay attached but disabled
    (textTracks order is uncontrolled; both would draw); a change reopens
    the stream in place.
  - **A failed conversion is forgotten, not cached** (`forget`, as in the
    Remuxer, both tested), or a transient failure sticks to that film and
    resume point for the process's life.
  - A session (one ffmpeg, one scratch directory, keyed by item, time and
    mode) is **kept until the space is needed**, not on a timer (going back
    must not reconvert). Running conversions are capped (least recently
    wanted stopped, segments kept); the budget evicts sessions least
    recently asked-for; quiet ones are reaped; shutdown removes them. Not on
    the request's context (a player comes and goes).
  - **Production is judged on disk, not by the ready gate** (which opens on
    a 150 ms tick; a run dying just after its first segment has produced):
    `hasSegment` is the test for `failIfEmpty`, the hardware retry and the
    watcher; `playable` asks only about the gate. `hasSegment` refuses an
    empty directory (else it tests `./index.m3u8` in the working directory).
  - A run its context stopped (converting cap, reaper, shutdown) records
    nothing (`attempt`'s error block stands down while the context is done),
    so `run` records a failure before opening the gate wherever
    nothing playable exists — the context's error, else "the conversion
    produced nothing" — or the key stays poisoned.
  - **A verdict is recorded once** (like `fail`, `finish`): the dying
    attempt's ffmpeg error stands, and `failIfEmpty` defers to it.
  - **A retry wipes the previous attempt**, so `run` allows one only when
    nothing was produced (else waiters lose a segment and, the gate open,
    the key serves an empty directory for good) — checked before the clear,
    covering every retry branch; the aspect verdict is still noted.
  - **The aspect verdict is read once per attempt**, `attempt` copying it
    into a local before planning (`startOverWithAspect`, shared with the
    pipe, tested): the thumbnailer writes it too, and a retry must ask what
    *this* run did.
  - **The scratch figure sums the live sessions at write time**
    (`reportLocked`, per-session `bytes`), never an old snapshot, and is
    written at run end, eviction and forget, not only on the 30 s reap tick;
    the Remuxer prunes on it. `forget` measures afresh via `account`, a last
    measurement missing a conversion still writing (a ReadDir per session:
    affordable there, not per segment request).
  - **Eviction compares the session, not the key** (`dropLocked`, the
    reaper, `forget`), or a retaken key drops a live newcomer nothing then
    manages.
  - HLS has a `closed` flag, as the Remuxer does: `session` checks it on
    both sides of its out-of-lock directory work and answers `errHLSClosed`,
    since a session made after the shutdown snapshot (context off
    `Background`, in neither map) could never be cancelled.
    `discard` returns what it could not remove (`cancel` only signals
    ffmpeg) for `account` and `forget` to log.
- **Every handler that serves an opening probes the same way** (`probed`,
  `server.go`): soundtracks, embedded captions and codecs come from the
  `EnsureCodecs` probe for the subtitle listing, cast, rewrap and HLS start
  alike; a television's links come from `mediaURL` (our address on the
  set's network, signed where there is a key).
- **Which soundtrack** is the viewer's choice, not the browser's. Tracks
  (`Item.Tracks`) come from the `EnsureCodecs` ffprobe on opening, guarded
  by `probed` alone: known codecs must not skip it, the tracks being
  persisted nowhere. A choice is served with `-map 0:a:<n>` (a browser
  cannot be told which stream to decode), so the HLS key and the rewrap's
  file name carry the track. `pickAudioTrack` (`playback.ts`, tested): this
  film's remembered choice, the last choice's language, the file's default,
  the first; a commentary only if alone.
  - **The pick is applied, not only marked** (`audioTrack`): `load` builds
    menu and pick before any route starts a stream (else `useKnownCodecs`
    starts the first track). `applyAudioChoice` goes cheapest first: the
    file's default or the element's `audioTracks` (Safari), then the rewrap
    where worth the wait (`remuxUrl` carries `?a=`), else a conversion (mode
    by `trackMode`). `appliedTrack` records what each source carries,
    keeping this idempotent. `a` cycles the menu, as `c` does subtitles.
- **A seek asks for the keyframe, not for the time**: a copied picture
  starts at the keyframe at or before the seek, re-encoded sound at the seek
  itself, so the client measures the keyframe (`/api/keyframe`), asks for
  it and starts its clock there. The pipe and table-less sessions add
  `-copyts` with `-avoid_negative_ts make_zero` (one shift for all streams,
  not each rebased to zero) as the belt for an unmeasured keyframe.
  **Not enough alone** (`landCopy`, `convert.go`, tested): ffmpeg takes
  `hlsSeekLead` off a reordered stream's seek and lands a keyframe early
  while the sound starts where asked, and the pipe's fragmented MP4 cannot
  start a track late, so sound and subtitles would lead throughout. The pipe
  and table-less HLS (`HLS.attempt`) land both streams on the keyframe:
  - Keyframe times are **read, not predicted** (`keyframeSeek`, one packet
    via `landsAt`): ffmpeg adds the file's start time to a seek, and an
    encoder-primed soundtrack (every Opus WebM) puts that below zero, so
    seeks use the film's clock (`-seek_timestamp 1`). If nothing lands on
    the time, the run stays as planned (logged at debug).
  - **Both streams are cut on the output side at the keyframe's *decode*
    time**: the input seek lands on it or the one before, ffmpeg's own sound
    trim off (`-noaccurate_seek`; it uses another clock), and an output `-ss`
    drops earlier picture packets and trims sound to that instant. Decode
    time because a reordered keyframe decodes early and the output clock
    starts there; reading from the previous keyframe supplies sound before
    it.
  - **Half a frame under that decode time**, which Matroska does not store
    and ffmpeg derives differently by where reading began (a cut a mere hair
    under can still drop the keyframe); half the probed packet's duration
    falls safely between the previous packet and the keyframe, and
    `landingSlack` stands in where the container gives none.
  - Accepted residue: the first frame shows tens of ms early; subtitles lead
    by ~0.1 s.
  - Seek times are sent to the millisecond (`seekTime` in `api.ts`):
    hundredths can fall before the keyframe.

- **A converted stream starts at a keyframe, not at `t`.** A copied picture
  starts at the last keyframe ≤ `t` (often 10 s early in 4K) with its clock at
  zero; `/api/keyframe/{id}?t=` says where and the player makes it `tcOffset`
  (readout, resume point, cues). It is measured with the same seek (`-copyts`,
  one packet, first pts) through the conversion's own input (`timeSeekInput`,
  pinned to `convertInput` by `TestKeyframeInputIsTheConversions`);
  position-read titles and pipe-only content get `t` back. Never "simplify" it
  into a keyframe listing or ffprobe scan: ffmpeg's seek is conservative in
  ways an index misses (a few ms past a keyframe rewinds to the previous one)
  and a scan is an order of magnitude slower. Full re-encode seeks accurately.
  A conversion has no ranges (`seekable` empty, `currentTime` clamps to zero):
  keyframe granularity with an honest clock is the contract. The seek bar
  follows the finger and commits once, on release; `castStep` (`playback.ts`,
  tested) interprets a set's poll and the transport's tick acts only on it; a
  set's volume is sent 150 ms after the slider settles; speed is disabled
  while casting; faults offer "Try again" (a disk that comes back needs no
  reopen).
- Cues are absolute while a conversion's clock starts at its keyframe:
  `/api/subs/{id}/{n}?shift=SECONDS` rebases them (`shiftVTT`) and each reopen
  **replaces** every `<track>` (`retimeSubtitles`); a re-pointed track keeps
  its old cues until the new file loads. `hideSubtitles` sets tracks to
  `disabled` across every source change (in `startSource` and before a new
  file's reset; restored on the newest source's `loadeddata`): Chromium keeps
  painting the last cue through the load, and `disabled` clears it at once.

### Subtitles, transfers and static files

- `/api/subs/{id}` lists sidecars (`subs.go`: subtitle extensions on the
  video's stem in its directory; label and language from the rest of the
  name); `/api/subs/{id}/{n}` serves one as WebVTT (`vtt.go`: BOMs, UTF-16,
  Latin-1). Sidecars are attachments, never items: kept in `subsByDir`,
  reconciled by scan and watcher.
- **Embedded text captions** (`Item.EmbSubs`, `embsubs.go`) reach a `<track>`
  only by extraction. Listed from the ffprobe run at open, text codecs only
  (`textSubCodecs`), they are numbered after the sidecars in one sequence;
  `SubtitlePath` answers only entries with a path (never `("", true)` for an
  embedded one), `EmbeddedSubStream` maps the rest to stream ordinals.
  Extraction demuxes the whole container, and everything about it follows
  from that cost:
  - **One read takes every text track the file carries**, each into a file of
    its own in a temporary directory (the only way one ffmpeg hands back
    several outputs), each bounded by `-fs`; where that run fails, the asked
    track is read alone. Track by track, a release's dozens of languages
    would be a whole-file pass apiece.
  - Cached per track by file identity (`embSubKey`; 64 MiB in all, a track
    admitted again replacing itself), since each seek re-asks with a new
    `?shift=`; deduplicated per file (`embSubFile`), an ask for any track
    waiting on a running read; counted as streaming; detached from the
    request, since seeks abandon the fetch. The outcome, data or error, is
    published on the in-flight entry before it closes, then the entry is
    deleted so failures are not cached.
  - Its own slots (`embSubReaders`, 2), never the thumbnailer's, which one
    read would hold for minutes; bounded by the file (`embSubBudget`: 4 min,
    or the file at 64 MiB/s where longer), as a fixed bound strands a large
    film or lets a wedged mount hold the slot as long as the largest needs.
  - It says how far it has got (`embSubs.progress`): bytes read over the
    file's size from the kernel's count (`bytesRead`, `rchar` in
    `/proc/<pid>/io`, counting a loopback socket like a file; nought where
    there is none), for a cast waiting on it (`castCaption`).
  Downstream it is a sidecar (`?shift=`'s parser accepts ffmpeg's hour-less
  timestamps).
- A stream that stops half way logs why: `ServeContent` swallows the copy
  error, so `recordingReader` keeps it; our failed read and the far end
  leaving look alike to a proxy ("upstream prematurely closed connection") and
  need different fixes. Cancelled requests are not reported.
- **Shutdown cuts transfers in flight** (`httpSrv.Shutdown`, 5 s; check
  restart times before calling a truncated transfer a bug). Spending all 5 s
  is normal, logged at Info: `Shutdown` cancels no request contexts and
  `handleEvents` waits on its own. Do not cancel request contexts from `main`:
  films would be cut at once. Converters read archived content via this
  server's `/api/stream`, which the drain waits for, so the rewrapper is closed
  before the drain (the defer stays as idempotent backstop; `Close` removes
  the unplayable `.part`) and `library.CloseScratch` stops the unpacker there
  too. The segmented converter deliberately is not: `HLS.Close` deletes
  directories players are still fetching from, so until a session's ffmpeg
  can be stopped with its files kept (`hlsSession.stopConverting`,
  unexported) a converting archived film spends the budget.
- With `-debug`, `access.go` logs each finished request (method, path,
  status, bytes sent, duration, requested range): the only record of how much
  left this process.
- **A file that never starts is diagnosed before it is converted.** A dead
  disk (EIO on every open, mount kept) fails every route at `OpenItem`, so
  `serveStream` answers **503** for a known item it cannot open (404 means "no
  such item") and the pipe answers 503, not an empty 200, when ffmpeg produced
  nothing. On the element's first error the player fetches one byte and reads
  the status (`readFault`, tested), matching **by id** since `refreshItem`
  replaces the item object when `/api/item` answers; the fault is drawn in the
  picture and stays (`giveUp`), and the file stays given up (`faulted`) so
  late codecs start no conversion. The 503 states the reason for the viewer
  (`openFault`, tested: dead disk vs a filesystem needing repair, e.g. XFS
  "structure needs cleaning"); other answers proceed to rewrap and
  conversion. The index keeps such files (an unreadable root is protected
  from reconciliation).
- **A transfer stopping half way is usually not this server**:
  `recordingReader` reports our failed reads, the proxy logs `upstream
  prematurely closed connection` when we closed first, and silence from both
  means the far end. Serving, ranges and the tunnel are already measured at
  full rate; a browser stopping a few hundred MiB in (then resuming with a
  `206`) while another pulls the file whole is a client fault.
- Media is streamed only by indexed ID (`http.ServeContent`, Range support);
  m3u entries resolve against the index, so playlists cannot expose paths
  outside the configured roots. Keep it that way.
- Static: hashed `/assets/*` are immutable; **everything else is
  `no-store`** (embedded files have no modtime to revalidate; `no-cache` keeps
  stale shells naming dead hashes). An `/assets/*` path not in the bundle is
  from a replaced build (UI state lives in the URL hash) and gets 404, not the
  SPA fallback (HTML for a `<script>`); so does an unmatched `/api` path, even
  one the mux cleans to nothing (ending in `..`): never the shell to a JSON
  caller.

### Thumbnails, previews, seek frames and deletion

- Thumbnails (`thumbs.go`): lazy, cached in the bbolt blob db
  (`internal/blob`, default `<data>/media.db`, `-db` to move, `off` to
  disable; it also holds enrichment metadata), keyed `(id, width)` with the
  source mtime+size in the value, so a changed file overwrites its entries;
  with no db (nil `ThumbStore`) every request regenerates.
  - Generation is deduplicated (followers share the leader's bytes, needed
    without a db). Failures are negative-cached **except cancellations**,
    which must surface as cancellation, never `ErrNoThumb` (the grid cancels
    routinely); the memory expires (`negTTL`, 10 min; `neg` maps key → time,
    checked by `recentlyFailed`), since it only stops retry storms and a dead
    mount's keys do not change when it returns. Publish the answer before
    dropping the in-flight entry, or a request in the gap (a bolt `Batch`
    with an fsync, on success) regenerates.
  - **One budget per item**: `plainThumbItemTimeout` (60 s) per plain video,
    after the ffmpeg slot, over the per-seek `plainThumbTimeout` (30 s);
    archived items have their own; stills (images, soundtrack art), sharing
    the single background slot during playback, get `stillThumbTimeout`
    (60 s), with `encodeResized` reading via a `ctxReader` (`io.ReadAll`
    ignores contexts), which bounds slow reads, not ones wedged in the kernel.
  - **An expired budget is a wrapped `DeadlineExceeded` (as from `runFrame`),
    never a verdict**, kept out of store and negative cache: keys never change
    for a stable file (`(id, mtime, size, width)`, immutable for a year), so
    whatever is written is permanent. This binds `fromAudio` (skipping
    undecodable art is fine; `ErrNoThumb` on expiry is not), `repairedFrame`
    (only a copy that merely fails answers nothing), `fromArchivedVideo`
    (`errSeekProducedNothing` is no verdict on the member; a frame landing at
    expiry is kept) and `makeSprite`.
  - Images: pure Go (EXIF orientation in `exif.go`, tested). Video: `ffmpeg`
    (optional) via a temp file (it needs a seekable output). Audio: embedded
    art, then a folder image.
  - **Video frames are taken a tenth of the way in** (`thumbOffsets`; plain,
    archived and disc titles alike), past front matter; fallbacks: the
    alternative offset, 3 s, frame 0 (opening bytes only: short clips, unknown
    durations, bounded prefixes). The key is width-only on purpose: **stored
    tiles are never remade for a recipe change** (older is not wrong); a new
    recipe reaches a file when it changes.
  - **Stills are un-squeezed before scaling** (`square`, tested): a JPEG
    cannot carry a DVD's pixel aspect, so the frame is widened to it and marked
    square (a no-op for square pixels). All stills go through `frameArgs`,
    differing only in accurate seeking and loopback reading.
  - **A bitstream declaring an impossible pixel aspect is repaired**
    (`aspect.go`, `repairedFrame`), since ffmpeg refuses it before scaling:
    once every offset fails, a couple of seconds are copied (no decode)
    through the bitstream filter that rewrites the declaration
    (`metadataFilter`, H.264 and HEVC only; nothing else is guessed at) into
    Matroska (MP4 keeps the bad ratio), and the frame comes from the copy; a
    failed copy is no verdict. Converters read the repair from a pipe
    (`startRepair`, chosen in `planConversion`; it does the seek, and closing
    the pipe kills and reaps it). The need is learned by one failure and
    shared for the run by identity (`aspects`) among thumbnailer and
    converters; a conversion hitting it retries at once (piped: the handler
    restarts, nothing written; segmented: reruns into the same session). Only
    ffmpeg's own complaint triggers it (`aspectRefused`, tested), or unreadable
    files would loop. Whether *this* attempt was repaired is run-local
    (`repaired`, seeded once and never re-read from the shared verdict, which
    the thumbnailer may set mid-run), decided by
    `startOverWithAspect(stderr, repaired, canRepair)` (tested).
  - Success is a frame coming out, not the exit status (0 with nothing
    written when a seek overran; non-zero after a good frame when a piped
    prefix ends).
- Archived video's frame is **seeked over the loopback URL to a fraction of
  the duration** (`seekThumb`), not picked from a prefix: ffmpeg's
  `thumbnail` filter favours logo and text cards (no uniformity test catches
  them), and front matter fills the first minute. `archiveThumbFraction`
  (10%), floored at `archiveThumbMinOffsetSec` (120 s), capped at half the
  length (front matter spans 0–240 s; 1–3% still hit it, deeper is darker).
  The piped prefix (`pipedThumb`, one keyframes-only pass) is the fallback only
  without a loopback address or a duration.
- An archive member's **store key never rotates** (its `(id, mtime, size,
  width)` are fixed once the set is complete) and `handleThumb` serves it
  `immutable`, so a bad frame is permanent in db and browsers (beyond
  `v=<mtime>` and `retryThumbs`). A frame is trusted only if (a) neither the
  caller's context nor the item's timeout fired (exit status cannot tell: a
  prefix ending mid-cluster fails a good run, a killed ffmpeg leaves a
  half-flushed JPEG), (b) the JPEG ends with its end-of-image marker, and (c)
  it is not near-uniform. A timeout counts as a cancellation, never
  negative-cached; plain files share these guards via `runFrame`. A follower
  of a cancelled leader leads the next attempt rather than inheriting the
  cancellation.
- Two luma-stddev thresholds, neither a classifier: under
  `archiveThumbRetryStdDev` (12.0) a frame earns the one retry (real frames
  16–67, black front matter ~1); under `archiveThumbMinStdDev` (3.0) nothing
  is stored, a "picture at all?" floor rather than a separator (black title
  text reaches 7.9, the darkest real frame 5.7). Both offsets flat →
  `ErrNoThumb`, retried next start, never a black tile kept.
- **The scrub sheet is ten seeks, not one pass** (`makeSprite`): an `fps`
  pass reads the whole film (> 5 min for 87 min of 1080p), ten input seeks
  (`-ss` before `-i`) take 3.5 s, run one at a time to spare playback's disk.
  Frames are tiled here, **one slot per offset** (`tileSprite`, tested with a
  gap): an overshot seek leaves a black cell instead of shifting later frames
  (the client times frames by the duration). The sheet has its own budget
  (`spriteTimeout` 90 s, `archiveSpriteTimeout` 3 min; vars for tests); an
  expiry leaving it **short of a frame** returns a wrapped `DeadlineExceeded`
  (no partial sheet stored, no `ErrNoThumb` remembered), but one on or just
  after the tenth seek is not refused, or the next hover redoes all ten. The
  per-frame `spriteFrameTimeout` may leave a gap (a black cell is older, not
  wrong). `spriteFrameWidth` is 320 for hover previews; `spriteCacheWidth` is a
  bucket slot, not a width, and must change with the sheet's shape or recipe.
- **Hover previews a film** (`preview.ts`): the sheet's ten frames stepped at
  `FRAME_MS` as a `background-position` on one div (nothing competes with
  playback for connections), fetched only after `DWELL_MS` (an unmade sheet
  costs ten seeks), one at a time, for the still-hovered tile; decoded sheets
  capped at `SHEET_CAP`. **A frame keeps its own shape** (the sheet's natural
  size gives `cols` × `rows`): fitted in pixels, never as a percentage of the
  tile, with black bars, inside **a window exactly one frame in size** within
  an element filling the tile, since a background is clipped only by its own
  element and neighbours would show beside a portrait frame (`fitFrame`,
  `format.ts`, tested). Inserted **before the badge**, which must stay
  visible; pointers only (`hover: hover`, never `pointerType === 'touch'`).
  The item is read on entry, never closed over at render (cells recycle);
  `grid.recycle` clears what renderers left via `scrubCell` (`cells.ts`), as
  handler properties (`onpointerenter`) and attributes like `title` survive an
  innerHTML wipe and renderers cannot be relied on to clear them. `holdsItem`
  (`cells.ts`) re-checks before fetching and mounting that the cell still
  holds the item, relying on keys starting with the item's id; key writing
  and reading live together in `cells.ts`, where the tests are, as
  `preview.ts` cannot be imported by the test runner (parameter properties
  defeat strip-only type removal).
- **Deleting from disk is two requests; the split is the safety**
  (`library/delete.go`, `library/delete_exec.go`, `server/delete.go`;
  `deleting.ts`, `deletedialog.ts`, tested). `POST /api/delete/plan` changes
  nothing and lists exactly what would go (whole folders, lone files, counts,
  size, container-mates); `POST /api/delete` with its token removes exactly
  that. Plans live 10 min and are single-use.
  - Execution re-checks: a file whose identity, mode, size or full mtime
    changed stays; folder plans snapshot every entry (sidecars and empty
    directories too) and `checkDeleteTree` rejects any addition, removal or
    replacement; directory handles are pinned with `os.Root` and confirmed
    entries removed one by one with `Remove`, never `RemoveAll`, so concurrent
    arrivals survive; roots are re-checked; a partial deletion is reported and
    reconciled with the index (`delete_snapshot_test.go`).
  - **What goes is what the thing is made of**: a file and its *own*
    subtitles (`Subtitles`; each goes with the video whose name it carries
    most of, `subtitleOfAnother`: `Film.Part2.en.srt` goes with
    `Film.Part2.mkv`, not `Film.mkv`); for content inside another file, the
    whole container (`rarVolumes`, `zipPartsOf`, the disc image, a DVD
    folder's files) and its other members, listed (`Others`); a release, show
    or season's tracks or episodes; a playlist release's tracks plus the list,
    or only the list if a track lives outside the playlist's folder (a
    mixtape). A release is taken only by its own id (`AlbumByID` also answers
    a track's).
  - **A folder goes whole only if what remains is furniture** (`folderGoes`,
    `leftover`): .nfo, checksums, cue sheets, logs, playlists, subtitles, OS
    litter, a release's sample (by name or Sample folder), and pictures
    **named or filed as artwork** (cover, folder, poster, scan, Covers/Scans
    folders). Anything else keeps it: another item, a document, an unindexed
    archive, a symlink, and above all **photographs** (`IMG_0001.jpg` is not
    artwork). Never a root, nothing outside the roots, no folder containing a
    root, nothing past `folderScanLimit` entries. A folder's parent is
    considered **only if the folder was part of a release** (`partOfARelease`:
    disc, season, `VIDEO_TS`, Sample, Subs), carrying a DVD up to its release
    and seasons up to the show, never a release into the folder it is filed
    in (an emptied `TV` folder stays). Files inside a folder going whole are
    still **in the plan** (`PlannedFile.InFolder`), or the second look takes
    them (a rar set's volumes) for strangers and keeps the folder.
  - **Only the whole library deletes** (`mayDelete`): no content-restricted
    face, path-confined caller or `-lock` server, since with no
    authentication a view given to someone must not delete the owner's files.
    `/api/info` reports `deletable`; the handlers enforce it. Both requests
    need a JSON body (a cross-origin preflight is refused), and only the
    plan's answer carries the token.
  - The index is updated at once (`Library.Remove` per path, covering
    containers and sidecars, notifying). Every deletion is logged at Info
    regardless of `-debug`, with what was kept and why (it cannot be undone).
  - Offered from a card's menu (right click, or a hand-timed long press since
    phone Safari sends no `contextmenu`; the lift's tap is swallowed until the
    next gesture), the player's delete button (next file, or close) and a
    release sheet's menu (closes it). The dialog captures keys at the window
    ahead of the player's document listener, so Escape answers it; Cancel has
    focus.

- **The seek bar shows the exact frame under the pointer** (`seekframe.go`,
  `GET /api/frame/{id}?t=&w=`; `seekframe.ts`, tested): seek to the prior
  keyframe, decode forward (`frameArgs` with `accurate`). Never stored
  (pixel→moment varies with width); immutable, mtime in the URL; own slot
  (`frameSem`, 2) so tiles never stand in front. Decoding stays in software
  (hardware is slower; 4K HEVC takes 2.5–4 s), so the client never waits:
  box and time follow every move; `SeekFrames` keeps one request in flight,
  asking for the latest moment as each lands; it shows the cached exact
  frame, else the last arrival dimmed (`.waiting`), and at rest (`SETTLE_MS`)
  aborts a request for a moment left, killing its ffmpeg. 32 frames per film
  as object URLs, released on eviction, new film, close and replacement; each
  decoded off-page before the swap (`showFrame`, generation-guarded) or the
  box blinks empty.
  - `previewBox`: a fifth of a wide player (160–320 CSS px), a quarter of the
    player's height for a tall picture (120–240), turned with the film,
    density capped at 2, rounded up to 32 so sizes share frames; the
    element's shape, else the library's. Centre the frame and turn it about
    its centre like `applyRotation`: a clipping box pins a turned image's
    overflow to its start edge.
  - Hovering pointers only; touch previews only while dragging. A DVD title,
    read by position (`library.SeekByte`), gets a frame near the moment;
    pipe-only content none.
  - `TestTheFrameIsTheMomentsToTheFrame` keeps its encoder off scene-cut
    keyframes and its tolerance under the three frames a non-accurate seek
    misses by, or it cannot tell them apart.

### State and the database

- `state.Store` (positions) lives in the shared blob database (one file to
  back up or delete), in memory (`Get`, `All`), flushed debounced: changed
  ids, one transaction. With `-db off` positions last the run — never a second
  store. Owner's data: no `(mtime, size)` stamp; `Prune` drops only ids the
  index lacks.
  - `Flush` clears the dirty set before its transaction (a save during the
    commit is the next flush's), so `Run` ends with `flushFinal`, bounded by
    `finalFlushRounds` (3) so a failing database cannot hold shutdown open.
  - `Flush` drops its lock before committing, so a failed flush's `restore`
    discards a removal whose id was marked dirty meanwhile (else one commit
    writes and deletes it); a put whose record was since deleted is skipped by
    `Flush`'s existence check.
  - Writes go through a one-method `writer` interface so tests can fail them;
    `Load`'s nil check stays outside the struct literal (a nil pointer in an
    interface field is non-nil).
- The database **epoch** (`blob.initEpoch`, minted when the file is created,
  served by `GET /api/info`) is in every thumbnail and sprite URL: they are
  `immutable` and otherwise versioned only by source mtime, so a new database
  must change them and the same one keeps the cache. `main.ts` awaits
  `/api/info` before the first listing; the live stream opens earlier, so its
  change handler refetches nothing before the first draw (`booted`), or the
  default query would fetch the whole library under a link naming a search.

### Docker and frontend tests

Docker: the Go stage outputs to `/out/mediator`; never build to a path Alpine
ships as a directory (`/media` → `/media/media`, breaking the COPY).

Frontend tests (`web/src/*.test.ts`, `npm test` in the image build): node's
test runner, types stripped, no dependency. They pin the pure predicates that
pick a playback route, against real user-agent strings and file-name shapes.
Lift pure logic out of the DOM into testable modules: `playback.ts`
(`playButtonIcon`), `query.ts` (the hold-over decision; the strip-only loader
refuses `sources.ts`'s classes), `audiopref.ts` (soundtrack memory, over
`remember.ts`'s guarded storage). A module the tests load runtime-imports only
with an explicit `.ts` extension (node resolves imports as written;
`allowImportingTsExtensions` is on); type-only imports need none.

## Testing an ordering, which is not testing a race

Never pin a concurrency fault with goroutine stress — a test that only
usually fails against the unfixed code protects nothing. Hold one side still:

- Open a read/write window on purpose. `enrich.go`'s seams `afterRead`
  (between reading a file and asking whether it is still that file) and
  `beforeWrite` (between that answer and the write) are nil in production,
  called before any lock, and separate so a test opens exactly one; a guard
  deleted with its seam leaves its test passing having tested nothing.
  `analysisTimeout`, `plainThumbTimeout` and kin are vars so a test can spend
  a budget up front, proving a timeout is not a verdict; `probeFilters`,
  `casting.discover` and the state store's `writer` stand in for a child
  process, a network and a database so they can fail on command.
- For a lock scope, take the lock the slow work would wait on, start the
  work, and sample whether the lock it must have released is still held. A
  handshake proves only that the call was reached (a descheduled goroutine
  gives a silent false pass), so `waitParkedIn` opens the window only once
  the runtime's goroutine dump shows one parked inside the function under
  test, and fails if none ever is.
- Prove each such test by reverting its fix and watching it go red.

## Server-complete, UI-pending

Complete and tested on the server, deliberately not yet in the UI (the grid
has no selection model): the hidden/favourite flags (`PUT /api/flags`, whose
batch form is faced like the single one, else it bypasses the restriction and
is an existence oracle; the `hidden=`/`fav=` listing filters), the seeded
shuffle (`sort=random` + `seed=`) and the m3u export helper (`playlistUrl` in
`api.ts`). Not leftovers: the player writes rotation and nocrop through that
endpoint.

## Documentation is part of the change

Every change updates, in the same commit, `README.md` (features, flags,
endpoints, layout) and `AGENTS.md` (how it works and why). A flag without its
table row, or an endpoint without its API-table line, is unfinished: nobody
would know it exists.

## License headers

MIT (`LICENSE`). Every source file starts with `SPDX-License-Identifier: MIT`
in its own comment syntax, a new one included: above a Go file's `//go:build`
line and kept off its package comment by a blank line, after an HTML doctype or
a shell script's shebang. `cmd/gen-ts` writes it into `types.gen.ts`.
`SECURITY.md` sends reports to GitHub's private vulnerability reporting (on for
the repository); `CITATION.cff` carries the version and date of the latest
release, so a release updates both. `CONTRIBUTING.md`, the issue forms and the
pull request template (`.github/`) repeat a few rules from this file — docs in
the same commit, the header, no library names — and change when they do.

## Writing about this project

**Nothing committed may name what the library holds**: not docs, commit
messages, tests, code comments or artifacts; screenshots, recordings, images,
sample media and binary fixtures are never committed (a grid screenshot is a
page of real names). Describe the pattern ("a language appended to the
video's name"); illustrate only with obviously invented examples.

**Tests invent their names** (fixtures are as public as prose) but keep the
**shape** under test: punctuation, season marker, group tag, the invalid
UTF-8 byte, the accented letter under the wrong alphabet
("Harbour.Lights.S01E01.1080p.BluRay.x264-GRP"). Describe a real file's shape
in the comment, never the file; keep the measurement, lose the name. These
are names too:

- **Site tags**: invent the site ("example.com", "clipsite.example").
- **Non-Latin text and mojibake pairs**: harder to catch, no less
  identifying. Invent a phrase and compute its bytes
  (`str.encode('tis-620')`, `encode('cp1251')`); the encoding property
  survives any such text.
- **Famous titles**: they get copied into the next table among unfamous ones.

Scrub a find in place, keeping the byte shape the test pins, across the whole
repository: a name travels between a test, its comment and the `AGENTS.md`
paragraph citing the measurement. `make names` (`scripts/names.sh`) lists
name-shaped text in tracked files — personal paths and mounts, hostnames
beyond the project's own dependencies, release-shaped names and season
markers, non-Latin text, every capitalised phrase quoted in a test — as
questions for a reader, not verdicts (invented fixtures share the shape). Run
it before committing fixtures or measurements, and read its last section in
full: that is where names hide.

## Environment note

`tag.ReadFrom` (dhowden/tag) can panic on corrupt files; every call site wraps
it in `recover` (`enrich.go`) — keep that for new tag reads.

## Review invariants

- Apply exclusions while restoring persisted items, before the first scan.
  `-lock` uses command-line roots even when the database has saved preferences.
  Root-level startup tests run the real listener and cancel through `run`'s
  context; the Docker Go-source stage must copy `main*.go`, including tests.
- Every HLS child resolves the current scoped item and matches its ID to the
  session. A live session snapshot and a restored session key grant no access.
  Media responses vary on both restriction headers; collection validators
  include a per-server epoch. Subtitle validators hash the converted bytes.
- Stream ZIP directory records through a bounded buffer and count records
  actually read. Required ZIP64 values must be present and fit signed bounds;
  local method/flags must agree with the directory. An unpacker must cap bytes
  written before checking for excess output, not after filling the disk.
- MP4 and EBML children stay within their parent's header and payload bounds.
  Check lengths by subtraction before adding offsets. Sample dimensions and
  clocks belong to their own records, never adjacent bytes. Keep the synthetic
  MP4/ZIP fuzz targets alongside the parser regression tests.
- DVD timestamp readers share the archive seek validator. Reject negative
  offsets and reads beyond EOF before rounding to sectors or allocating, and
  clamp the sector end without overflowing the file offset.
- Bound sidecar and extracted subtitles to 16 MiB before conversion, including
  ffmpeg stdout. Bounded writers must not inherit a `ReadFrom` that bypasses
  `Write`. HLS labels are single quoted-string attributes with unique names,
  including collisions between natural names and generated suffixes.
- Create new databases with `0600`; derived crop records participate in pruning.
  Existing database permissions are preserved and documented for upgrades.
- DLNA relative control URLs use an explicit URLBase or the final description
  URL after redirects. Reject oversized, incomplete and non-SOAP responses;
  a SOAP fault is a failure even if the device sends HTTP 200. Resolve the
  receiver host from the transport control URL.
- Cast polling has one request in flight and discards answers predating a
  local control or item change. Queue replies follow the latest request, and
  a playing receiver's zero position is meaningful. Media-key callbacks keep
  their owner as `this`. Initial lightbox loads, steps and swipes invalidate
  superseded navigation; stale preloads do not start image requests.
- `http.go` owns cross-origin write protection, response security headers and
  request-scoped parsing of the proxy restrictions. `contentOf` and `pathsOf`
  reuse that value, including through signed routing, rather than parsing CSV
  for every item in a batch. Invalid nonempty restrictions return 403 before
  routing; `PathFilter` also denies all paths when used directly with invalid
  input. `NoKinds` distinguishes an empty allowed set from zero's unrestricted
  default. Content and path restrictions both deny directory preferences through
  `fullLibrary`; `/api/info.confined` reflects both so the UI hides the control.
- JSON mutations use `decodeJSON`: a bounded, single, non-null object is read
  completely before publishing the decoded value. Keep response reads separate
  from this helper. Go's `CrossOriginProtection` permits media GETs and non-browser
  clients while denying cross-origin mutations. Proxy the original Host and port.
  Request reads are bounded to 30 seconds, idle connections to two minutes;
  long streaming responses intentionally have no write timeout.
- Stream responses carry a CSP sandbox: SVG and misnamed HTML are untrusted
  documents, even if displayed safely in an image element elsewhere. Signed
  credentials are redacted from request and redirect paths in the access log.
- Scan and watcher entry points check regular-file status before dispatching to
  subtitle or container readers. Reconciliation drops ordinary files replaced
  by special files while retaining DVD directory containers. A FIFO named as
  media must neither enter the index nor block a reader during discovery.
- A newer listing page retires the prior page generation and cancels outstanding
  requests. A version drop on a request started at the current version is a
  server restart and is accepted; an overtaken response is discarded. Explicit
  refreshes reset the comparison too. Source errors remain observable until retry;
  a failed first load shows an error and retry control rather than an empty
  library. Preference edits wait for the initial read and disable controls
  while saving; closed dialogs ignore late answers. Stored soundtrack indices
  must be nonnegative safe integers, with blank storage treated as no preference.
- Parse only stdout when checking ffprobe's structured output. Diagnostics on
  stderr differ by tool version and must not be counted as stream records.
- Every listing order ends with the item ID as a tie-breaker, including files
  with identical display paths under different roots. Clamp pagination before
  adding the limit so an oversized offset produces an empty page, never a panic.
  Exact-file removal uses the path index without walking the whole library.
- Clearing resume state preserves likes and play history (`ClearPosition`).
  Restricted state responses omit unknown IDs as well as disallowed items.
- `LibrarySource` and `CollectionSource` cancel superseded requests and ignore
  their results. Page zero must refetch even when the previous total was zero.
  Failed loads clear pending placeholders; repeated redraws have a five-second
  retry backoff. Tests exercise the sources themselves with deferred responses.
- `FedSource` cancels and releases its response reader on all exits. SourceBuffer
  errors reject pending appends; stopping during an append settles its waiter
  without reporting a playback failure. MP4 box and codec parsing reject short
  or overflowing records, and initialization buffering is bounded.
- `modal.ts` owns dialog focus and native-control key handling; `scrollhold.ts`
  owns the shared scroll lock. Overlay callers acquire and release each once.
- Playlist exports carry signed URLs. Album ZIPs use the real release directory
  for multi-disc albums; restricted views get permitted tracks only. ZIP entry
  names remain unique even when an existing name resembles a generated suffix.
  Hidden directories are skipped. Walk errors and entry/depth limits fail the
  request before streaming; copy errors abort it instead of closing a partial ZIP.
- Watched thresholds come from `watched.go` through `cmd/gen-ts`, alongside the
  API types. Keep the generated file current with `make generate`.
- Malformed subtitle format lines must not panic. Validate all ASS column
  indices and honor the Events section; normalize SRT timestamps to WebVTT.
  Archive seek errors preserve the old position and reject integer overflow.
- `make test` runs frontend tests/build, vet and Go race tests with ffmpeg present.
  Host coverage can run separately; combining race and coverage instrumentation
  makes the audio fingerprint tests substantially slower.
