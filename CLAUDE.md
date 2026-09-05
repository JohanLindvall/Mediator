# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A self-contained web media browser: a Go backend (module `github.com/JohanLindvall/Mediator`)
that scans/watches directories and streams video, images and music to a
TypeScript frontend embedded into the binary via `go:embed`.

## Commands

Every make target runs the build **inside Docker** (BuildKit) — the host
needs Docker only, no local Go/Node/npm packages:

```sh
make docker           # runtime image (mediator:latest): gen-ts → npm → go build, all in Docker
make build            # same build; extracts the static linux binary to ./mediator
make generate         # regenerate web/src/types.gen.ts via the Docker gen stage
make test             # go vet + go test -race ./... inside the image build
make vet              # go vet only

./mediator -listen :8080 -data data DIR [DIR...]   # run against media directories
./mediator -open DIR                            # free loopback port + open a browser
```

Optional flows that DO need a local toolchain (never required to build):

```sh
cd web && npm run dev                           # Vite dev server on :5173, proxies /api to :8080
go test ./internal/library -run TestAlbums      # single test (needs web/dist to exist — see below)
```

Dockerfile stage layout and gotchas:

- Stages: `gosrc` (Go sources) → `gen` (writes types.gen.ts) → `web`
  (npm ci + tsc + vite) → `build` (go build with dist embedded) → `vet`/`test`;
  `bin` and `types` are `scratch` stages that `make build`/`make generate`
  extract from via `docker build --target ... --output`.
- `web/dist`, `web/node_modules` and `web/src/types.gen.ts` are dockerignored:
  the image can never embed stale local artifacts — everything is rebuilt from
  source in the stages above.
- `gosrc` copies Go sources explicitly (`main.go`, `cmd/`, `internal/`) so
  frontend edits don't bust Go layer caches — **a new top-level Go package
  needs a COPY line in the Dockerfile**.
- If you do run `go build`/`go test ./...` locally: it must be from the repo
  root, and `web/dist` must exist with the current bundle — `main.go` embeds
  `all:web/dist` (the Docker targets leave no local dist; produce one with
  `cd web && npm run build`).

## Generated TypeScript API model — single source of truth

The wire format is defined only by Go structs. `cmd/gen-ts` reflects over
their `json` tags and writes `web/src/types.gen.ts` (checked in; wired to
`go generate ./...` via the directive in `main.go`).

When touching the API:

1. Do not return anonymous structs from handlers — add/extend a named type in
   `internal/server/apitypes.go` (or the library/state types themselves).
2. Register any new root type in `rootTypes` in `cmd/gen-ts/main.go`
   (the generator panics on unregistered struct references).
3. Run `make generate` (runs `cmd/gen-ts` inside Docker and exports the file)
   and commit both sides. Image builds regenerate `types.gen.ts` from the Go
   types on every build — the checked-in copy is dockerignored and exists only
   for the `npm run dev` flow.

`json:"-"` fields are omitted; `omitempty` becomes an optional TS field; the
`Kind` union is derived from `library.AllKinds`.

## Architecture

The index is mirrored into the blob db (`persist.go`, `items` bucket) so a
restart serves the library before the walk finishes (36k files: 0.7 s versus
1 m 41 s). `SetMetaDB` starts change tracking, `LoadFromDB` restores,
`PersistLoop` batches writes every 2 s (a write that fails leaves its records dirty for the next tick rather than losing them), and `PruneDB` — run only after a
*completed* scan, and never on an empty index, or an unmounted root would
wipe the cache — deletes index records, metadata and thumbnails for files
that no longer exist. The state store's positions, counts and verdicts are
pruned by the same live set (`LiveIDs`, nil for an empty index) right after
it, and both run after **every** completed scan — the first, the periodic
rescan and a change of roots — through one `pruneAll` in `main`; the
periodic rescan used to skip both, so what a file left behind stayed until
the next restart. Shutdown waits for the state store's final flush as it
waits for the persist loop's, since either landing after the deferred
`db.Close` is a write to a closed database. Items whose content lives inside another file — a rar
member, a DVD title — are deliberately not persisted: their content is byte
offsets into volumes or into an image, and stale offsets would serve garbage,
so the scan re-derives them.

Scale invariants (measured at 150k files; keep them true):

- `List` serves pages from a cached, per-(query, version) sorted result —
  only the first request of a new query or after a change pays the filter
  and sort. Never add per-request work that walks the index.
- **How far things have been watched** is the library's (`watched.go`), not
  the state store's: a listing filters under the index's own lock and cannot
  be reaching into another store to do it. So the positions are pushed in —
  in bulk at startup, and one at a time as the player saves them — and
  reduced to the one bit a listing needs: started, finished, or neither. The
  rule is the one the grid draws with (under five seconds is not a start,
  past `WatchedFraction` is finished, which is also where the player stops
  offering to resume), because a chip that counted one thing while the tiles
  showed another would be worse than no chip at all. The client reads the
  same pair (`START_FLOOR_S`, `WATCHED_FRACTION` in `playback.ts`) through
  one function, `watchState`, tested — the grid's tick and `resumeStart`
  used to differ by a percent, so a film at 96.5% wore the tick on its tile
  and still resumed from there.
  A position is saved every few seconds while something plays, so this gets a
  **counter of its own** (`watchVersion`) rather than bumping the library's
  version: only a query that filters on watching has to be rebuilt when a
  position moves, and every client is not made to refetch the world because
  somebody is watching a film. The two totals are cached per (version,
  watchVer) and computed from the watch map, which holds only what has ever
  been played, intersected with the index — positions outlive the files they
  belong to until the next prune.
- `Counts` is O(1): per-kind totals are maintained by `upsert` and
  `dropItem` — the one door into the index and the one door out, so nothing
  else has to remember `countKind`; five places used to spell the removal
  out and the fifth forgot a step — and the album, artist, genre and show
  totals ride on their caches, fed by the builds. `CountsFor` (`counts.go`) is
  the one that cannot be: "how many albums match this?" is not a running
  total, so a narrowed view costs one pass over the index — cached per (search,
  flags, version) exactly like the sorted listing beside it, so paging
  through results or moving between views pays nothing more. The last eight
  answers are kept, not one: a faced or confined caller's listing asks two
  questions per page — its own totals and the search's — and a single slot
  missed on both, every page. It is cheaper
  than the listing's own walk: no sort, no copies, one integer per item.
  Items are counted as items and albums and artists as collections, which is
  what each chip opens. `CountQuery` carries the artist as well as the
  search, because drilling into a performer narrows the view and the chips
  have to say what is in front of the viewer: their tracks, their releases,
  and the one artist. Items are theirs by their artist tag, which is why a
  film or a picture is never one — the video chip reads nought while their
  music is on screen, and that is the truth of it.
  **Only the view on screen writes those numbers.** Every source refetches on
  a library change, including the one behind the view nobody is looking at,
  and that one still holds an older question — usually one with no search in
  it. Letting it answer made the chips flick between the hits and the whole
  library and back, once per change event. They also keep the last hits while
  the next answer is fetched, since dropping to the totals for a moment is
  the same flicker by another route.
  **But only while the question has not changed.** Holding them across a
  *drill-down* is the same fault wearing the other hat: clicking a genre put
  the narrowing in force before any answer about it existed, the fallback was
  the library's own totals, and the chips read "85,118 videos" for as long as
  the request took and then settled on nought. So `countsToShow` (`query.ts`,
  tested) has three states rather than two — totals when nothing is narrowed,
  the matching counts when the narrowing has been answered, and **no numbers
  at all** while it is still being answered, since there is no honest one to
  show. What separates the two cases is `countKey`: the narrowing the
  *server* was asked about, not everything on screen. `narrowed`
  (`query.ts`, tested beside `countKey`, and pinned to agree with it on
  every narrowing) says whether anything is narrowed at all — the
  performer, the genre, the show, and what sounds like one thing, which it
  once left out, so a like-this listing read the library's totals under a
  chip naming one performer's neighbours. A search is absent from
  it deliberately, which is what keeps the hits up while somebody types; and
  a view that fetches nothing shares the key of the one that did — the
  seasons view is derived from the show list already in hand, and keying it
  on "a show is open" would blank its chips for good, nothing ever arriving
  to unblank them. Every drill-down and every way out of one goes through
  this, not the genre alone.
  `List` carries the answer as `Matching` beside the
  unfiltered `Counts`, and the album and artist endpoints carry it too since
  those views fetch no items — the chips would otherwise go on counting the
  whole library above a search. Both are needed: an empty result with a full
  library is "no matches", not "the library is empty". The broadcast loop is the
  one place those lists are rebuilt after a change (`RefreshCounts`, at
  most once per coalesced event); main seeds them once after the initial
  scan because an unchanged warm start emits no event.
- Enrichment buffers its results (`queueMeta`) and the persist loop writes
  them in bulk; a synchronous db write per track (commit + fsync each)
  caps a cold pass at a few hundred items/s — batching took 135k items
  from 10m42s to ~20s.
- Videos whose duration the native parsers read get no eager ffprobe (one
  process per file across the library); `EnsureCodecs` fills codecs in
  when the item is opened, which is the first moment they matter. It runs
  at most once per item per process, and the flag that says so is
  `Probe.Probed` — "ffprobe answered", never "a probe produced a duration"
  and never "we tried". Plenty of containers give up codecs and no duration
  ever, so a guard demanding one can never be satisfied and every request
  re-probes; and a run that never happened (no ffprobe on PATH, an
  unreadable member, a killed process) is not a verdict either — recording
  it as one spends the item's one second chance on nothing. The flag is fed
  by `ffprobeResult.answered`, which is set only when the process ran and
  printed a document that parsed. It takes the caller's context (the
  request is waiting for it) and a slot from `probeSem`; it deliberately does **not** wait for playback to go quiet
  the way the background sweep does — this probe is what tells the player
  whether the browser can decode what it is already playing. Nothing is
  written down when the context expires: interrupted is not answered.
- The album/artist endpoints send `ETag: W/"v<version>"` +
  `Cache-Control: no-cache`, so browsers revalidate for free; `writeJSON`
  only defaults to no-store when the handler set no policy. The frontend
  additionally throttles those full-list reloads to one per 2 s during
  change bursts, and `LibrarySource` evicts listing pages beyond 64 so an
  end-to-end scroll cannot pin the whole library in browser memory.

Change propagation is the core loop:

- What counts as media is `extKind` (`scan.go`), and every entry in it was
  measured against real disks rather than guessed at. The transport streams
  are the ones worth knowing about: `.ts` is what a stream capture is written
  as, and at 356 files and 110 GB it was by far the largest thing the library
  could not see — while `remuxable` already answers yes for the H.264/AAC
  inside one, so those play by lossless copy and needed no other code. Every
  one of those files begins with the transport-stream sync byte, which is
  worth recording because `.ts` is also a source-code extension: on a media
  disk it never was one. Left out deliberately: `.dat`, which is a VCD's
  MPEG only when it sits in a `MPEGAV` folder and is otherwise anything at
  all; and `.tif`, which would cost a decoding dependency for two files.
- **A file whose name says nothing gets its first bytes read** (`sniff.go`).
  `extKind` is how this library decides what it holds and that is right
  nearly always — but some downloads arrive with **no extension at all**, and
  three members of one release, hundreds of megabytes each and plainly MP4
  from their first eight bytes, were invisible to a library that reads only
  names.
  Two guards make it affordable, and both are measured rather than chosen.
  Only files with **no extension whatsoever** are opened: one that says
  `.nfo` or `.par2` has told us what it is, and sniffing those would be a
  read per file for an answer already given. And only from `sniffMinSize`
  up: across these disks there are 9,024 extensionless files of which **37**
  are a megabyte or more, so the floor turns nine thousand opens into
  thirty-seven — and an extensionless file under a megabyte is far likelier
  to be a repository object or a lock file than anything anybody wants to
  watch. Of those 37, all but a handful were MP4, so this is a recurring
  shape rather than one release.
  `kindOfMagic` is a table of signatures at fixed offsets, pure and tested;
  nothing scans, guesses or falls back on statistics, because a wrong answer
  indexes a disk image as a film and hands it to a player. An ISO brand it
  does not know reads as video — a film is likelier than a song, and
  `EnsureCodecs` settles what is really inside the moment it is opened.
  The file is **not renamed**: `Name` stays what the filesystem says, as it
  does everywhere here. Playback needs no more — `opensDirectly` lets an
  unknown container try, which for an MP4 is right, and `mimeFor` falls back
  on the kind. The kind is in the mirrored record, so a warm start restores
  it rather than reclassifying a nameless file as nothing.
- `internal/library` holds the in-memory index (`map[id]*Item`,
  ID = `sha1(abs path)[:16]`). It is populated three ways that all converge on
  `upsert`/`removePath`: the initial recursive walk (`Scan`, which also
  reconciles deletions), the fsnotify watcher (`watch.go`, installs watches on
  every directory including newly created ones), and a periodic full rescan
  (`-rescan`, default 10m, safety net for missed events).
  **A new directory is walked more than once**, and that is not belt and
  braces — it is the one window a watcher cannot see into. The watch on a
  directory can only be installed once the directory has been *reported*, and
  whatever is created inside it before that is announced to nobody. Measured
  on a real arrival: a torrent client preallocating a release made the
  directory at 12:07:42.825 and all thirteen of its files at 12:07:42.829 —
  four milliseconds later — and the release sat unindexed until the rescan
  found it nine minutes on. `walkNew`'s immediate walk is what covers a
  directory moved in whole and cannot cover this: at that instant the files
  do not exist yet. So `settleWalks` looks again at 5 s, 30 s and 2 min, the
  last deliberately minutes out — a set of archive volumes is not indexable
  until the last byte of the last volume has arrived, and a download takes as
  long as it takes. `maxSettles` bounds the timers, since a tree moved in
  wholesale is one Create per directory; past the cap they are left to the
  rescan, which is what it is for.
  **A file that changed under its item forgets what was read from it**
  (`forgetContent`): the duration, the codecs, the shape, the soundtrack and
  caption lists, and the marks that say they were looked for. Both upserts
  share it — a member replaced inside a volume set used to keep the old
  file's codecs and languages, where a replaced plain file forgot them — and
  a DVD title's declared length is put back afterwards, since the disc
  outranks anything read (`declaresDuration`).
  **Attribute events count too.** Every tool that preserves timestamps stamps
  them after the copy, and the mtime is part of what an item *is* here — it
  keys the metadata cache, the grid cell and the thumbnail URL — so a
  `Chmod` event re-reads the file rather than being dropped.
  **Reconciliation stats outside the lock**: the paths a walk did not see are
  gathered under a read lock, checked against the disk with no lock held —
  a mass disappearance on a slow or unmounted disk used to stall every
  reader for its duration — and dropped under the write lock after a
  recheck. A rar set or a disc image reconciles its members against the
  list it produced last time (`members`), not against the whole index.
  **Scans serialize** (`scanMu`): the rescan ticker starts counting at boot,
  so it can fire while a cold initial walk of a big library is still
  running, and a prefs change starts one of its own. Two walks at once each
  clear `byInode` and reconcile against their own `seen` — hard links
  mis-detect as duplicates and files one walk indexed vanish under the
  other's reconciliation (self-healing on the next pass, which is exactly
  the kind of fault nobody can reproduce).
  **The watcher's enrichment is debounced** (`enrichAfterQuiet`, 2 s): a
  file being written — a torrent landing — emits a stream of Write events,
  and a tag-read goroutine per event was an unbounded number of readers on a
  file that was about to change again anyway. The read happens once, after
  the writer goes quiet, and still notifies when it lands, which is the
  watcher paths' publish contract.
- Every mutation calls `notify()` → version bump → `BroadcastLoop` coalesces
  bursts (400 ms) → subscribers get an `Event` → `/api/events` SSE →
  the frontend drops its page cache and refetches the visible window. Version
  numbers let clients skip no-op refreshes; the grouped views — albums,
  artists, genres, shows — are each recomputed lazily and cached per version
  through one arrangement (`perVersion` in `cache.go`), which also carries the
  total the chips read. They sort through one rule too (`orderBy`): a thing
  that carries the sort key beats one that does not whichever way the sort
  runs, then the name, then the id — four copies of that closure had begun to
  drift. The keys the four grouped views share — modified, size, tracks,
  popularity, length — are compared once too (`collection`,
  `compareCommon`), each view adding only its own.
- Listing (`List`) filters + sorts + pages under `RLock` and returns *copies*
  of items — background enrichment (`enrich.go`: audio tags incl.
  track/year/genre, plus durations for audio and video) mutates items under
  the write lock, so never hand out `*Item` across the lock boundary.
  Results persist in the blob db (`meta` bucket, keyed by id + mtime/size)
  so restarts skip the file reads. Durations come from `duration.go`: pure
  Go header parsing for mp4/m4a/mov, mp3 (Xing/VBRI/CBR), flac, ogg/opus and
  wav; one ffprobe call (optional, like ffmpeg) supplies both the codecs
  and any missing duration for video. Archived video has no path for
  ffprobe to open, so it is pointed at this server's own loopback stream
  URL instead (`probeItem`, see the loopback entry below) — measured over
  15 archived members, that returned the same duration and codecs as a
  32 MiB piped prefix every time, reading 5.2 MiB in 0.06 s, and it gets
  there by ranging to the container index at the *end* of the file, which
  no prefix can ever contain. `ProbeMedia` takes the caller's context:
  `EnrichNow`/`EnrichSoon` route through it from inside requests that are
  waiting, and a hardcoded background context let one probe outlive a
  200 ms deadline by 5 s.
- A release's `Sample`/`Samples` subdirectory is skipped when the release it
  belongs to is present (`isRedundantSampleDir` → `releaseNear`), and kept
  when it is not — then the excerpt is all there is. **The release is looked
  for below the parent as well**, to `sampleReleaseDepth` (2): a film split
  over discs keeps nothing in the release directory itself — CD1, CD2, an nfo
  and the Sample folder — so a rule that read only the parent's own files
  found no release, concluded the excerpt was all there was, and indexed a
  one-minute clip beside two 735 MB discs. Two levels also covers a disc that
  is itself a folder of parts. Deeper stops being "the release this sample
  belongs to" and becomes "some video somewhere underneath", and the search
  is bounded because it happens once per sample directory during a scan.
  Another sample folder never counts as the release: one excerpt does not
  make another redundant. The same excerpt
  arranged the other way — a `Sample.mkv` lying beside the film rather than
  under a folder — is skipped by `isRedundantSampleFile` on the same terms,
  and never on the name alone: it must be a **video**, since "sample" means
  something else entirely in a folder of music, and the directory must hold
  a release at least `sampleRatio` (5×) its size, or an archive set. A scene
  sample is a percent or two of the feature, so that bar is generous by an
  order of magnitude — a session of takes where one happens to be called
  "sample" keeps all of them. `namedLikeASample` matches the word
  **wherever it falls** so long as punctuation sets it off — scene samples
  are named "group-sample.film68.avi" as readily as "film-sample.avi", and a
  rule that only looked at the two ends missed the middle. It splits on
  punctuation and never on a space, which is what still keeps a film called
  "Sample Text" out of it: by this reckoning that name is one word, and a
  title that merely begins with the word is a title. Because the walk
  skips it deliberately rather than because it vanished, reconciliation
  consults `stillIndexable` before keeping a path that is still on disk;
  the same check drops a path that has become a duplicate.
- Duplicate paths are collapsed (`dedup.go`): a file is identified by
  `(device, inode)` — inode alone collides across volumes — and only the
  first path claiming it is indexed, except that a real file supersedes a
  symlink to it whichever order the walk finds them in. `Scan` clears
  `byInode` before each walk: claims are only reconciled with the disk
  *after* the walk, so a claim left by a since-deleted path would otherwise
  block its surviving hard link and both would be dropped. `upsert`
  therefore returns `(changed, dup)`, and a duplicate is deliberately kept
  out of `seen` so that a path demoted to duplicate is also removed.
  `statEntry` follows symlinks: `DirEntry.Info` is an lstat, which would
  otherwise record the link's own few bytes as the media's size.
- Search is tokenized (`search.go`): items index space-joined lowercase
  word runs of name + **absolute** path + tags; every query word must be a
  substring of that text. Rebuild `lower` via `searchText` whenever fields
  feeding it change — all four doors into the index do (`upsert`,
  `upsertRar`, `setMeta`, and `LoadFromDB`, which a warm start comes
  through). The path is the absolute one and not `Rel`, because `Rel` starts
  at the root's own base name: a library rooted several levels inside a
  mount point could be searched by what is under the root and never by where
  the root is. Token for token the absolute path is a superset of the
  display path, so indexing it instead costs only the prefix. Albums index
  name/artist/genre/year plus where the release is kept — the directory, or
  the playlist file for an m3u album (`fillAlbum`) — so a query that answers
  in the file listing does not come back empty one view across; they keep a
  separate `sortName`, since sorting must not depend on the search text.
  Artists are deliberately left out of this: a performer is not a place, and
  the albums they group may sit anywhere.
- **A release spread over discs is one release.** A directory named like a
  disc (`CD2`, `disc 3`, `disk-4`, and the common `CD 1-…` form that names
  the disc as well) is folded into the one above it, so five subdirectories
  come out as the album they are. `discPattern` wants the number to be a
  number on its own, which keeps "CD 1990s Hits" and "Discography" out of it,
  **and it reads a number spelled out as well as written**: a release split
  into "DISC ONE" and "DISC TWO" came out as two albums under those names,
  belonging to nobody. Ten is as far as the words go — a release in double
  figures is counted in digits by everybody, and every word added is another
  that could begin a title. The same whole-word rule is what keeps "Disco Lantern" out, "disc" there running straight into a letter. `discSuffix`
  takes the spelled forms too, and has to: where the folder folds and the tag
  does not, the discs disagree about the album's name and the fold comes out
  under the directory's name instead of the release's.
  `foldDiscs` folds only where a release has **more than one** — a lone
  directory named like a disc, sitting among unrelated albums, would
  otherwise pour every one of them into a single heap. One level only:
  a release is discs inside a release, never discs inside discs. Tracks sort
  by disc before track number, or the second disc's opener lands between the
  first disc's one and two — every disc numbers from the start. And the vote
  on the album's name is taken over titles with the disc marker stripped
  (`albumTitle`), which is what lets "Anthology (CD1)", "(CD2)" and "(CD3)"
  agree on being *Anthology*.
- `dropDuplicateAlbums` removes a directory album when a playlist lists
  exactly the same tracks (releases commonly ship both). The playlist wins:
  it carries the running order. Identical track sets only — a partial or
  cross-directory playlist is its own collection. Deduplication runs before
  artists are grouped, which is why they can count every remaining album
  without double counting.
- **How often a thing has been played** is counted by the client, not the
  server (`POST /api/plays/{id}`, `plays.go`). Only the client knows what a
  play is: a file opened and shut after two seconds is not one, and the
  response carrying the bytes cannot tell the difference — a seek opens
  another, and a range request opens six. So the players count it after
  `PLAY_AFTER` seconds of **playing time**, measured on the element's own
  clock so a paused track never creeps over the line, and against how far it
  has run since the file was loaded so resuming a film at ninety minutes does
  not count one the instant the clock is read. It is the same five seconds
  that already decide whether something counts as started, deliberately: two
  floors for one idea would drift apart. Handing a film or a track to a
  television counts at once instead — the set is playing it, and this page
  has no clock on it to wait for.
  The count lives in the state store beside the position, in one record,
  because they are the same kind of fact with the same lifetime and the same
  pruning; `Set` therefore reads before it writes, or saving a position every
  few seconds would reset the count for as long as anything played.
  **A play bumps the library's own version**, where a position has
  `watchVersion` instead. That looks inconsistent and is the point: a
  position is written every few seconds while something plays, and making
  every client refetch the world for it would be absurd — a play happens once
  per track. Bumping the version is what keeps everything else honest for
  free: the album, artist and genre lists are cached per version and sum
  their plays at build time, the endpoints carrying them are revalidated by a
  version ETag, and the broadcast loop refreshes the chips. The counts
  themselves are pushed into the library like the watch states, because a
  listing sorts and filters under the index's own lock and must not reach
  across into another store to do it, and they are stamped onto the copy a
  listing hands out rather than onto the indexed item — the count is the
  owner's, and an item is rebuilt by every walk that sees it.
  **Popular** is a listing of what has been played at all, most played first,
  and it is a filter rather than only a sort: ordering the whole library by
  plays would bury the handful that matter under the untouched majority. The
  aggregation the question actually wants is already in the app's grammar —
  Albums, Artists and Genres each sort by plays, which is popularity by
  release, by performer and by genre.
  **A verdict outranks the count** (`likes.go`, `POST /api/like/{id}`, the
  thumbs on the music bar). The owner's like or dislike travels exactly as
  the play count does — kept in the same state record (`Position.Like`),
  pushed into the library at startup and on every change, stamped onto the
  copies a listing hands out, bumping the version so the collections sum it
  when they are built; the two are one type in the library, `ownerCounts`,
  a map under its own lock with a generation the affinity cache compares
  against — and the popular orders sort on one number,
  `popularity(like, plays)`: the verdict shifted forty bits above the count,
  so a liked track stands above anything merely played however often, a
  disliked one sinks below the untouched, and the count decides only among
  equals. A collection's verdict is the net one over its tracks, likes less
  dislikes, so a release, a performer, a genre and a show rank the same way.
  The sort key is `popular` everywhere the views offer it — "Most played"
  became "Popular" — and `plays` is kept as its old name so an address
  minted before the rename still sorts. The Popular listing, and the chip
  that counts it, are what has been played *or judged*: a liked track
  nobody has played yet belongs there, and a disliked one sits at its foot.
  Pressing the lit thumb withdraws the verdict; a state record left with
  nothing in it is dropped rather than kept empty. The tile carries the
  verdict beside the play count, because a listing ordered by something
  invisible looks wrong.
- **How the music sounds is read, once, in the background** (`analyze.go`,
  `features.go`), as the fourth and lowest tier of background work: below
  playback, thumbnails and tag reading, one track at a time, only while
  `busy()` says nothing streams and no thumbnail is being made and while no
  tag pass runs (`enriching`), and it sleeps a minute between passes. Each
  audio item is decoded by ffmpeg — three twenty-second windows at a
  quarter, a half and three quarters of the way through (`analysisOffsets`,
  front matter left out for the thumbnail's reason), by path or over the
  loopback URL — and described by `extractFeatures`: fifty-six numbers, a
  pure-Go radix-2 FFT under mel cepstral coefficients, spectral centroid,
  bandwidth, rolloff and flatness, chroma, loudness and dynamics, tempo by
  onset autocorrelation with a prior toward 120, and three speech cues. The
  vector is written to the `features` bucket stamped with mtime and size and
  a recipe version (`featuresVersion`, the same idea as `shapeVersion`),
  restored at startup (`LoadFeatures`), pruned with the item, and never read
  twice for an unchanged file. Measured on the live library: 0.7-1.1 s per
  track, 22k tracks a working day of one core, once. `-analyze=false` turns
  it off; without ffmpeg it does nothing. A decode that fails is remembered for the run and not retried every
  pass (the loop used to run ffmpeg on the same broken file every minute);
  a cancellation is never remembered. A silent track's empty vector is
  restored at startup as "analysed, nothing to say" rather than read again.
  The three windows are described separately and their statistics merged
  (`extractFeaturesFrom`): concatenated, the frame straddling two windows
  was a spurious onset and a seam in the envelope the syllable and tempo
  cues read. Flatness takes one log per run of eight bins rather than one
  per bin, which was the largest cost after the FFT. A track whose length
  nobody measured is sampled at fixed marks and, where those return
  nothing, read from the start. The FFT, the tone, noise, click
  train and speech-cue tests pin the arithmetic on synthetic signals.
  **Similarity is cosine over scaled vectors** (`similar.go`): each column
  has the library's mean taken out and its spread divided away — a tempo in
  beats per minute and a cepstral coefficient count alike — then unit length,
  so the dot product is the resemblance. Brute force, deliberately: fifty-six
  by a hundred thousand is milliseconds, and an index would be a second thing
  to keep right. `Similar` keeps the n best as a short sorted slice a
  candidate is slotted into only when it beats the last, rather than sorting
  every candidate to keep twenty, and the releases and the performers that
  sound like one are ordered by one generic rule (`nearest`). `Similar` is "more like this" and radio (`of=similar` on
  `/api/tracks`); a release's or a performer's **sound** is the mean of its
  tracks' vectors (`sounds`, cached per version and features generation),
  which is what `near=` on the album and artist listings orders by, the
  copies stamped with `Similarity` and the chips counting what is listed
  (`countsOfAlbums`, `countsOfArtists`), the sort select reduced to "Similarity" — whose
  direction is still the viewer's: nearest first, or turned round. The
  listing opens most alike first whatever way the list it was reached from
  ran (`showNear` sets the direction), since a performer list sorted A to Z
  is "ascending" and carrying that in opened on the least alike, with a
  toggle that then did nothing because the server ignored it. **Affinity**
  is what the owner's verdicts say about every analysed track: the most it
  resembles anything liked, less the most it resembles anything disliked,
  graded -2..2 (`affinityBucket`, 0.35 and 0.6 on a scale where unrelated
  tracks sit near zero) and stamped on the copy with `Akin`, the title it
  was measured against — stamped but **not drawn**: a faint thumb on a tile
  the owner never touched read as their own verdict, and the owner asked for
  no mark at all, so the resemblance ranks and the tile shows only real
  verdicts. The fields stay on the wire for a view that can say it better. `trackPopularity` is verdict, then affinity, then
  plays; the collections have no affinity and keep `popularity`. Rebuilt
  only when a verdict or a vector changes (`likesGen`, `featuresGen`). And when the album build's release verdicts change, since those decide
  which tracks are speech and the affinity keeps speech and music apart.
  A listing page is stamped from one `stamper` — the counts, the verdicts,
  the resemblances and the release verdicts snapshotted once — rather than
  seven locks per item, and so are the similar tracks and the queue.
  **Speech is kept apart everywhere.** `spoken.go` reads a score off the
  stored vector, so the rule can be tuned without analysing anything again,
  and the rule was set against real files rather than reasoned out: pauses
  between sentences separate a reading from music by a wide gap, the
  voiced/unvoiced alternation and the missing tempo help, syllable-rate
  movement of the envelope turned out to mark drums rather than voices and
  is read the way the data says, and pitch-class entropy said nothing. The
  measured spreads and the weights are in the file's own comment, which is
  the one place they are written down; the threshold sits in the middle of
  the measured gap. A track nothing has read is music: a book taken for
  music is filed where it always was, where a song taken for a book vanishes
  from the music.
  **Two false positives set the rest of the rules.** A grindcore release of
  ninety-nine tracks with a median length of four seconds: its long songs
  read as music and its four-second ones as speech, since three seconds of
  shouting with silence at both ends has "pauses", no tempo a window that
  short can measure, and a harsh voice's alternation — so pauses are counted
  only between the first and last sound (column 52), a track needs one full
  window of sound to be judged at all (column 55, `spokenMinSound` — nineteen
  seconds, a twenty-second window measuring 19.99 once its tail frame is
  dropped; `spokenVerdict`; the unjudged are music), a release votes by playing time
  rather than by count, and a genre tag naming an audiobook in the spellings
  these libraries carry (`spokenGenre`) shelves it outright. Then a black
  metal EP, shelved while only its first track had been read: an ambient
  intro whose quiet gaps sit at the same -48 to -58 dBFS as a reader's
  pauses, so no level floor tells them apart, outweighed four to one once
  its songs were read — so a release gets no verdict from the analysis until
  half its playing time has been judged (`markSpoken`), and a track's own
  mark follows its release (`byRelease`, set by the album build and read by
  `spokenOf` without forcing one, since it is asked under the index's lock
  where a build would wait on itself): an intro on a record is music for the
  tile, for radio and for the similarity listings, a chapter is a reading,
  and only a track in no release keeps its own verdict. Both verdicts are
  taken on the **raw** vector — the scaled one has the library's mean taken
  out of every column, the seconds of sound included, which is how the first
  version judged nothing at all. A release whose judged playing time is
  mostly speech, or whose tag says so, is an **audiobook**: it has a chip of
  its own (`audiobooks=1`, `Counts.Audiobooks`, kept out of `Counts.Albums`
  so the two add up), it leaves the Albums, Artists and Genres views and the
  queue-all groupings — a narrator is not a performer — it is found by the
  word "audiobook", and `Similar` answers a song with music and a chapter
  with readings only. The analysis bumps the version every `analysisReport`
  tracks and at the end of a pass (`Touch`), since the collections are
  cached per version and a shelf that filled only at the end of a six-hour
  pass would look broken; every track would be a rebuild a second.
  **Radio** (`audio.ts`): the bar's toggle, remembered, tops the queue up
  with a batch of similar tracks whenever fewer than `RADIO_AHEAD` follow the
  one playing — on every track change and when the queue runs out, where
  the appending finds the player parked and moves it on. `append` is the
  enqueue without its toasts, shared by both.
  **The queue has no limit worth the name** (`QUEUE_CAP` and `maxQueue`, a
  million): the whole library goes in and is shuffled there. Two things made
  that true. A spread over a hundred thousand arguments is more than a call
  stack takes, so the queue and the order are extended by loop; and the
  panel draws a **window** — one tall spacer, the rows in view laid over it
  by position (`paintQueue`, `Q_ROW` pinned in the stylesheet) — since
  building every row on every track change is what the browser did not
  handle. The grid's "play from here" takes the same one answer the
  queue-all does rather than paging.
- **A genre tag is one field doing several jobs**, and both of them are
  normalised at the single door metadata enters. Whitespace folds first
  (`cleanGenre`): a tag reading "Black  Metal" — two spaces, or a
  non-breaking one, neither of which anything on screen can distinguish —
  was a second card of one album standing beside a card of nine hundred.
  Then the numeric-genre doubling collapses in both the forms taggers write
  it, "Black Metal Black Metal" and "Black Metal/Black Metal".
  **A tag can also name several genres**, and `splitGenres` reads it as the
  list it is — but only on a pipe, a semicolon or a comma. **A slash is not a
  separator**, and that is the whole judgement: measured over this library, 8
  tags use a pipe or semicolon and 11 a comma, all plainly lists, while 48
  use a slash and there most are one compound name. "Black/Death Metal" is
  blackened death metal, not black metal and death metal, and splitting it
  would invent a genre called "Black" — which happens to be a truncation this
  library already suffers from. Guessing wrong on 48 tags to tidy 19 is the
  wrong trade. An album therefore carries `Genres` as well as `Genre` (the
  first, for sorting and the one-line caption), the grouping walks all of
  them, and the drill-down matches any of them. Measured: 142 cards became
  138, the duplicate went, Black Metal went from 916 albums to 1002, and
  Viking Metal, Heavy Metal and Pagan Metal became genres of their own
  instead of being buried inside a six-genre string.
- **Television is read out of the names** (`series.go`), because nothing else
  says it: an episode carries no tag worth the word, and what a release
  actually tells you is in its path. Measured over this library: of 85,609
  videos, 3,302 carry a season or episode marker, and they arrive in two
  shapes of roughly equal size — half name it in the file, half sit in a
  "Season 5" folder whose parent is the only thing naming the show.
  **The series name is taken from the shallowest directory that marks a
  season and has something in front of the marker**, and that ordering is the
  whole design. A release whose file is called "grp.hl.s01.e01.1080.mkv"
  names the *group*; only the directories name the show, and a parser that
  read the file first would file six seasons of one programme under six
  release groups. The file is asked last, and a "Season 5" folder with
  nothing in front of its marker hands the question one level further up.
  **A file may only claim to be an episode if its own name says which
  episode it is**, and two forms are a directory's alone: the bare season
  pack ("S02", which must also be two digits) and "Series 2" as a synonym for
  a season. A file that merely mentions a season identifies nothing — that is
  a season pack's folder speaking — and in a file name the word "series"
  turns up in the middle of descriptions far more often than it names one.
  Each of those rules was written against a real thing that appeared on
  screen as a show: a clip ending "-s2_hd_x265_aac.mp4", one called
  "…Season 5 Full Footage.webm", and a sentence with "series 1" in the
  middle of it.
  **A pair of numbers in the "1x02" form is checked against being something
  else**: "03x00" is a time and "9x16" is an aspect ratio, so both numbers
  must be real and the pair must not be a screen shape. A vertical clip is
  not season nine.
  **And a series needs more than one episode.** One is not a series; it is an
  episode — and that is where the mistakes collect, since every false
  positive found in practice was a lone clip whose name happened to carry a
  marker. Nothing is hidden by it: the file is still in the listings, still
  searchable, still a video. It simply does not get a shelf of its own for
  being alone on it. Measured on this library: 72 shows before these rules,
  42 after, and the ones that went were clips.
  **Every marker must be a whole token** — a separator or the end of the name
  on both sides. That is not fussiness: half a library of downloads ends in a
  random identifier, and a pattern that matches inside a word reads
  "clip-S74tb48v.mp4" as season 74 of a series named after the rest of the
  file. Measured before the rule was tightened, twenty such against a hundred
  and twenty real shows; after it, 72 shows and no junk. The loosest form
  ("1x02") additionally insists both numbers are real, because "flash at
  03x00" is a pair of timestamps.
  It is parsed at **all four doors into the index** — the two upserts, and the
  warm start, which is the one that matters: the mirrored record has no field
  for a series, so without it a library that had been restarted knew about no
  television at all, which is to say nearly always.
  Television is **video's own**, the way albums are music's: a face without
  video is shown no shows. And a restricted face counts them rather than
  reading the running total, like everything else it counts — the grouped
  list is of the whole library, so a face restricted to films, or to part of
  the disk, has its own answer. Counted from what the caller can actually
  see, with the same rule the grouping applies: more than one episode in
  front of them, or it is not a series to them either.
  **The listing has to be grouped the same way, and for a while it was not**:
  `SearchSeries` took the restriction and ignored it, so a face restricted to
  one directory drew forty-two shows under a chip that said thirty-two, with
  shows from elsewhere on the disk among them. `AllowedSeries` regroups from
  what the caller can reach, which is more than a filter over the cached list
  and has to be: a show is a show only where more than one of its episodes is
  in front of the viewer, and its episode count, running time and seasons are
  the ones that can actually be reached. Filtering afterwards would put a
  shelf of one episode on screen under numbers from a library the caller
  cannot see. Paid only when the header is set, exactly as the albums are.
  **The face of a show is chosen by a total order**, and it has to be. The
  grouping walks the index's own map, which arrives differently every time; a
  show whose episodes all parsed as the same season and number — a folder of
  unnumbered episodes, which is common, and one measured show had twelve —
  had nothing to order them by, so the first one seen won. The list is
  rebuilt on every change to the library, which is every few seconds while
  anything is being written, so those tiles cycled through episodes on
  screen and blanked whenever the newest choice had no thumbnail made yet.
  `before` compares the id after the season and the number, and the season's
  own cover breaks its modification-time tie the same way — files copied
  together share an mtime. The albums have always done this (`ID` after
  `lower`); this is the same rule in the place that missed it.
  A show's **seasons travel inside it** rather than behind an endpoint of
  their own: (and a show or a season shows a running time only when every episode is
  measured, as an album does, since half a show's length under a whole
  show's name is a wrong number) a programme has a handful, they are wanted the instant one is
  opened, and a second request to learn "Season 1, Season 2" is a request to
  learn almost nothing. So the chip drills twice with one fetch — shows,
  then seasons, then the episodes themselves, which are an ordinary item
  listing sorted by episode. A listing rather than a sheet, deliberately: it
  is what the player steps through, so going on to the next episode is
  something it already does.
- **Genres** (`genres.go`) are grouped from albums exactly as artists are,
  and for the same reason — a view grouped from tracks could disagree with
  the album view about what a release is filed under. A genre carries the one
  number an artist has no counterpart for: **how many performers are in it**,
  which is what a viewer scans the card for and what the artists chip must
  then agree with, so it is deduplicated on the same lowercase key
  `buildArtists` uses. A release with no genre tag joins none: there is
  deliberately no "Unknown" bucket, which would be the largest card in the
  view on most libraries and is not a genre anybody is looking for. The
  search text is the genre's name plus the performers in it — the words
  somebody remembers about a genre are the bands in it — and pointedly *not*
  the release titles, of which a genre has hundreds.
  **Both drill-downs clear each other.** `state.artist` and `state.genre`
  cannot both be set: the album view would be asked for one performer and one
  genre at once and would answer with whatever their intersection happened to
  be, under a chip naming only one of them. Leaving either goes through
  `setModeForce`, since stepping out of a drill-down into the very chip it
  was reached from is not a change of mode and would otherwise be ignored.
  Inside a narrowing the two collection chips answer about *what is in front
  of the viewer*, and neither answer is in the artist or genre lists — which
  know nothing of the other narrowing. `CountsFor` therefore gathers the
  performers and genres of the matching releases as it counts them: inside a
  genre, the Artists chip is how many performers are in it; inside a
  performer, the Genres chip is how many genres they span.
- Artists (`artists.go`) are grouped from **albums**, not from tracks, so
  the artist and album views cannot disagree: every artist has albums, and
  `SearchAlbums`'s artist filter shows exactly the ones counted. Playlists
  are excluded or their tracks would be counted twice. Cached per version
  like albums, and searched and sorted by the same rules.
- **A file name is bytes, not text** (`name.go`). Windows-1252 and Latin-1
  names are common on disks that have been carried between systems, and Go
  hands those bytes back as they are — which is right for opening the file
  and wrong for showing it, since `encoding/json` silently replaces every
  invalid byte with U+FFFD and the browser draws a diamond where the letter
  should be. So `displayText` decodes the **display name, the display path
  and the search text**, reading anything that is not valid UTF-8 as
  Windows-1252 (byte by byte, so a mostly-UTF-8 name keeps its good part),
  and nothing else is decoded: `Path`, the id hashed from it, and the record
  written to the database stay the filesystem's own bytes. Decoding those
  would name a file that does not exist and change every id in the library.
  Two consequences worth keeping: `Subtitles` matches stems against real
  directory entries, so it uses `filepath.Base(it.Path)` and never `it.Name`;
  and `blob.Item` carries such a path in `PathBytes` beside the string
  (base64, written only when needed), because a path marshalled through a
  JSON string comes back naming nothing — the warm start would restore an
  item that cannot be opened, under an id that hashes to something else.
  Records written before that field existed are already damaged, so `upsert`
  repairs any item whose stored path differs from the one just walked: the id
  is the hash of the path, so a disagreement means the record came back from
  somewhere lossy. Without the repair those files did not merely look wrong —
  the walk found the id already present and left it alone, and reconciliation
  then dropped the item outright, because nothing had walked the path it
  claimed. A file with an umlaut in its name disappeared from the library on
  the next restart.
- Thai is the other encoding these libraries carry, and it is not
  Windows-1252: TIS-620 puts the whole alphabet in the high half of the byte
  range, so read as Latin-1 every letter becomes an accented Roman one and a
  track of the shape "ฝนตกหนัก" arrives as "½¹µ¡Ë¹Ñ¡". The two cannot be
  told apart byte by byte, so `looksTIS620` goes by shape: **a run of four or
  more Thai-range bytes with nothing ASCII between them**. Thai is written
  that way throughout and European text is not — its accents come one or two
  at a time among Roman letters, and nobody writes four in a row. Anything in
  0x80-0xA0 settles it the other way, since TIS-620 leaves that unassigned
  where Windows-1252 keeps its quotation marks.
- **A letter can also survive as the wrong letter entirely.** CP1251 puts
  the Russian alphabet exactly where Latin-1 keeps its accented vowels —
  0xF6 is ö in one and ц in the other, 0xE4 is ä and д, 0xE5 is å and е,
  0xF8 is ø and ш — so a tagger that guessed Russian on a Nordic release
  writes the shape "Fyrsnц" where "Fyrsnö" was meant, **in UTF-16**, and
  nothing downstream
  can tell from the bytes that anything went wrong. `reinterpretCyrillic`
  puts it back, and what it goes by is company: a Cyrillic letter with a
  Latin letter against it is one byte read twice differently, while a
  Cyrillic word standing on its own is a word. That distinction is
  load-bearing here — this library holds 501 items of genuine Russian
  against about fifteen damaged Nordic tags, and a repair that could not
  tell them apart would do far more harm than the damage. Letters only: a
  Cyrillic character whose CP1251 byte is punctuation in Latin-1 is left
  alone, since turning one into a pilcrow is a different kind of wrong.
  Because `setMeta` is the single door metadata enters the index through,
  the repair reaches values already cached in the database as well — no
  rescan, no re-read of the files.
  What cannot be repaired is a name that has been through this **and** had
  its accent folded off afterwards: two files here carried the shape
  "MA¶rknatt" — ö double-encoded to "Ã¶" and then stripped to "A¶". Valid
  UTF-8, so
  nothing marks it as damaged, and the lead character could have been any of
  Ã Á À Â Ä Å. Those were renamed on disk instead, which is the honest fix
  for two files and would be a guess as a rule.
- `cleanTag` decodes the same way, for the ID3v1 frames that are never
  UTF-8, and `setMeta` trims tag whitespace. It also puts Thai back
  (`reinterpretThai`): a tag reader has already turned the frame into
  Latin-1 letters by the time it reaches us, so there the bytes have to be
  reconstructed from the string before they can be read properly — which
  works only because every one of them is below U+0100. Padded values are common in real files,
  and an artist stored with a leading space both sorts to the top and
  becomes a second artist of its own; normalising at the single point where
  metadata enters the index cleans up cached values too.
- **A name on a card is a way to everything else like it.** The performer
  under a release and the genre beside it are both the answer to a question
  the card raises — what else is theirs, what else is like this — so both are
  click targets wherever either appears: on a release, on a track, on a
  performer's card, and in the album sheet, which closes onto the result
  because a drill-down behind an open sheet is a change nobody can see. They
  look like the caption they replaced until they are pointed at; a card whose
  every fact is a visible button is a card nobody can read. `viaArtistLink`
  and `viaGenreLink` match on the link class as well as the attribute,
  because `data-artist` also names the artist line in the music bar, which is
  not a link.
- **A music-only face opens on the artists** (`defaultMode`). The file
  listing there is a thousand tracks in the order they were written to disk,
  which is the least useful thing to be shown first; a mixed library has no
  one shelf to open on, and a face of films has no grouping to open into. It
  is only the default — a link that names a view still opens that view.
- **A card says what is known about the thing on it.** A release shows its
  performer, year and genre; a performer shows their tracks, running time,
  the span their dated releases cover and what most of them are filed under
  (`Artist.Genre`, `FromYear`, `ToYear`, grouped in `buildArtists` with ties
  broken by name so two builds of one library cannot disagree); a track
  shows its artist, year and genre. A performer is not one genre and not one
  year, but a line that says nothing at all is worse than one that says what
  is mostly true — and the full line is in the `title` attribute, since a
  card is narrower than the facts.
  The tags are **part of the cell's key** for music, because they arrive
  after the file does: enrichment reads them in the background, and a cell
  keyed only by id, mtime and size kept showing the file name under a title
  the library had since learnt.
- `cleanGenre` undoes one thing and only for genres: ID3v1 numbered its
  genres and ID3v2 kept the number as a reference in front of the text, so a
  frame reading "(138)Black Metal" means genre 138 — which *is* Black Metal
  — with its name spelt out after it. A reader that expands the number and
  keeps the text hands over both, and sixteen releases here were filed under
  "Black Metal Black Metal". Only an exact doubling is collapsed, and only
  for genres: a title may legitimately say a thing twice.
  It is applied through `cleanGenreTag` at **both** doors metadata comes
  through — `setMeta` for the metadata cache, `LoadFromDB` for the mirrored
  index — because a rule applied at one of them is a library that tidies
  itself when re-read and not when restarted.
- **A year in a release's name is moved to where a year belongs**
  (`liftYear`). Directories are called "2018 - Some Release (Single)" because a
  file listing has nowhere else to put the year; a card does — it shows the
  year beside the genre — so the name was saying it twice and the second
  time took the room. It is dropped from the name and kept as the release's
  year where the tags gave none; the tags outrank it, someone having typed
  the folder while a tag came off the record. Measured over this library:
  2228 of 2318 releases end up dated, and exactly one name still opens with
  a year — a release actually *called* `1995`, which is left alone because
  stripping it would leave nothing behind.
  `splitYear` wants a separator after the year, or a bracket around it, so
  "2018Something" is a word that begins with digits and "44 Winters" keeps
  its number. `splitTrailingYear` insists on brackets: a name ending in a
  bare number is far more likely to mean it. Nothing is lost either way —
  the directory is indexed for search exactly as it is spelt on disk, so the
  year still finds the release — but note that sorting albums *by name* no
  longer sorts a performer's releases chronologically by accident, which is
  what the year sort key is for.
- **A release nothing tagged is filed under the directory above it, where
  that names somebody the library already knows** (`artistFromParent`). Music
  is filed as performer, then release, then tracks, so the answer to "whose is
  this?" is usually sitting one level up — and a release ripped or downloaded
  without tags has nothing else to go on at all. But a parent directory is not
  always a performer, and taking it on trust is how the artists view fills
  with rubbish: measured over this library's untagged releases, the folder
  above them was as often "complete", "Unreleased_Unofficial", "EP, Single,
  Demo" or an interview archive as it was somebody's name — more often, in
  fact.
  So the name is **corroborated** rather than trusted: it counts only when
  some other release, properly tagged, has already established that performer.
  The set is gathered on the same walk that groups the tracks, so it costs
  nothing extra. The rule can then never invent a performer, which is the
  whole of its safety — the worst it can do is what happens without it, which
  is nothing. Measured on the same set: five releases correctly filed and
  every container directory refused, including two that were really disc
  folders whose fold had failed.
  **One voice is enough where nothing contradicts it.** The majority test
  above it settles a *disagreement* between tracks, and where there is none
  there is nothing to settle: a release with one track tagged and eleven blank
  is not an anonymous release, it is that performer's with the tagging left
  half done. Two names and no majority is a real disagreement and still reads
  "Various Artists".
  Three limits, each load-bearing. It fires only where **nothing tagged the
  release at all** — tracks that disagree are a different thing and say
  something of their own, and a release most of whose tracks name somebody has
  already answered. It is for **directory albums only**: a playlist's tracks
  may live anywhere, so the folder holding the file says nothing about who
  made them. And the spelling handed back is the **tagged** one, matched
  case-insensitively, since a directory shouts where a tag does not and a
  second spelling would be a second performer.
- `fillAlbum` also derives `Genre`, `Year` (majority of tagged tracks) and
  `Duration` (sum, left 0 unless *every* track is measured, so the UI never
  shows a half-counted album length).
- **What a file is, technically, is read from its own header** (`shape.go`,
  `mp4box.go`). A listing says how big a photograph is and what a film was
  encoded with, on hover, for everything on the screen at once — which rules
  out ffprobe, that being a process per file across a hundred thousand of
  them. So: a still's dimensions come from `image.DecodeConfig`, which reads
  the header and stops (a few hundred bytes against the several megabytes a
  photograph is), and a film's picture size and both its codecs come from the
  box tree, a handful of reads to the sample entries. Everything else already
  pays for ffprobe when its duration cannot be read natively, and takes its
  shape from that same answer. `codecOfSampleFormat` translates the
  four-character code into the name a probe would have used, so a film
  described by the box tree and one described by a probe read alike; a code
  nothing recognises answers nothing, a guess being worse than the silence.
  The frame rate comes from the same walk, out of the table saying how long
  each sample lasts: samples over the time they take, both summed, so a file
  whose frames differ in length comes out at its average — the only single
  number there is for one. The table is read to `sttsMaxEntries` and no
  further: a well-behaved file describes its whole timeline in one entry, and
  one needing thousands is telling us its rate varies, which the first few
  hundred say as well as all of them. It is worth more than the tooltip it
  was added for — `hwWorthIt` decides on pixels per second, and a film whose
  duration was read natively had no rate to give it.
  **It is written down** (`blob.Meta`: `Width`, `Height`, `FPS`), because it
  is read once and wanted every time the file is listed — and with it a
  `Shape` marker, which the fields themselves cannot replace: plenty of files
  have no picture to measure, and a record written before there was anywhere
  to keep one looks exactly like a file nobody could measure. Without it
  every start would read the whole library again to learn what it already
  knew.
  **A container with no native reader is probed, once.** Matroska, AVI and the
  transport streams keep their shape where only ffprobe can reach it, so the
  reading falls back to one where the box tree answered nothing. That is a
  process per file — the thing this whole arrangement exists to avoid — and it
  is allowed here for one reason: it is a process per file *once*, against a
  record that will be read for the life of the library, where the objection is
  to paying it per page or per request. Measured before it was written: about
  thirteen thousand such files against ninety thousand videos, the library
  being overwhelmingly ISO base media. A probe the context killed is not
  written down, or the silence would be permanent — the key never changes for
  a stable file.
  It is a **number rather than a flag**, and that is what makes it survive
  its own extension: raising `shapeVersion` is how a new fact read from the
  same header reaches the files already read. The frame rate was the first
  such fact, added within the hour of the size, and without the number it
  would have reached only files the library had never seen.
  The marker is in **both** places a file's state is remembered — the
  metadata record and the mirrored index (`blob.Item.Shape`) — and
  `needsEnrich` consults it. **Every writer of the record carries it**:
  `EnsureCodecs` writes the record again when a film is opened, and a version
  that left the shape and the marker out of that write put every film ever
  opened back to "shape unread" on each restart — a header to re-read apiece,
  and for a Matroska file a probe to re-run (`TestEnsureCodecsKeepsTheShape`). That is what makes it reach a library examined
  for tags before any of this existed: such items come back from a restart
  marked finished and would otherwise never be read again. It costs one
  header per video and per still, once per reading.
- Enrichment is priority-driven: `EnrichNow` (blocking, bounded by the
  caller's context) and `EnrichSoon` (one background pass at a time) read
  what the browser is showing before the rest — the listing page, the
  opened album's tracks, the item behind `/api/item/{id}`. Unlike the
  background sweep these ignore the busy gate; the user is waiting.
  `needsEnrich` keys off an `enriched` flag set whatever the outcome, so a
  file with no metadata is not re-read by every later pass; the flag is
  persisted, so restarts do not redo it either.
- Enrichment must publish: `EnrichMeta` notifies in batches, and the
  watcher paths (`AddFile`, `reindexRarSet`) notify **after** their
  `enrichOne` goroutine finishes. Without that trailing notify the tags
  reach the index but never the clients.
- **A member the set holds but cannot serve is reported, not passed over.**
  A compressed member parses without error and yields nothing, so the set
  looks exactly like a release nobody ever walked — and the question "why is
  this not in the library?" could only be settled by reading the archive by
  hand, twice. `parseRarSet` returns `[]rarSkip` beside the entries and
  `indexRarSet` logs each one with its reason: compressed (with the method),
  encrypted, or incomplete (with how many bytes of how many arrived, which is
  the ordinary state of a download and not an error). **One line per set,
  once per process, naming no members**: how many cannot be served, how many
  can, and the reasons grouped by kind with the first member's own wording
  (`skipReasons`). It used to be a line per member per rescan — the rescan
  finds the same set every ten minutes, and one set of three hundred
  compressed pictures was three hundred lines per rescan for the life of the
  process. `Library.once` keeps the memory, keyed by the set and the kinds
  of reason, so an incomplete member is not news again for every byte that
  arrives and a set is reported afresh only when what is wrong with it
  changes. The parse failure of a whole set is said once the same way. Measured across the
  disks: 244 rar sets, 174 yielding media and 70 yielding nothing — most of
  those being software, subtitle archives and other things that were never
  media, which is exactly why the reason has to be in the log rather than
  inferred from the count.
  **Compressed members stay unsupported**, deliberately and after being
  asked: serving one means decompressing from the first byte for every seek,
  which for a film is the whole archive per scrub of the bar. A scene DVDR
  release stored with "fastest" saves about five percent on an image that is
  already compressed, and costs all of that.
- Rar support (`rar.go`): store-method members of RAR4/RAR5 volume sets are
  indexed as virtual items (`Path` = "<rar>\x00<member>", `Archived()`
  true) and read via `OpenItem`, which stitches segments into a seekable
  reader. That reader is not the rar's — `storedEntry` is a name, a size and
  a list of byte ranges in files we do not own, and a DVD title is the same
  thing (see disc images below), so both go through it and `Archived()` means
  "the bytes are inside something else", not "this came out of an archive".
  The index of where each segment starts lives on the *reader*, built by
  `newStoredReader`: it is what `readAt` binary-searches, and putting it on
  the entry meant every producer of one had to remember to build it — which
  the second producer did not, and the reward was a panic in the reader
  rather than an error anywhere near the mistake.
  Anything opening item content must go through
  `library.OpenItem`, not `os.Open`. ffmpeg can't reach archived bytes by
  path, so everything that has to read them is pointed at this server's own
  stream URL instead — the thumbnail, the metadata probe, and all three
  converters (see the loopback entry below). The pipe survives only as the
  fallback for when there is no loopback address, and it cannot seek: a seek
  then costs reading the file from the beginning, which for a disc image is
  gigabytes before the first frame. Scrub sheets
  are made for archived members now. The recipe stopped being the obstacle
  when the sheet became ten seeks — the same technique the archived thumbnail
  uses — and the cost turns out to be smaller than the estimate that had held
  it back: **measured on a 1.17 GB member, 60 ranged reads, 164.5 MB, 2.9 s**
  for the whole sheet.
  What remains is that this is not free, and **what decides is the app's own
  priority order, not what the content is stored in.** The gate used to
  refuse a `hover=1` sheet for anything archived, on the strength of that 4K
  measurement — but a DVD title is archived by the same definition and
  measures 1.2-1.4 s and 210-270 MB over six titles, MPEG-2 keyframes being
  dense enough that a seek lands almost at once. The container was the wrong
  variable. What all those reads have in common is the disk, and the disk is
  what playback needs: so a hover, which is the most speculative work in the
  app, stands down while anything is streaming (`Library.Streaming`) and goes
  ahead otherwise. It is paid once — the sheet is stored and served immutable
  afterwards — a sheet already made is served either way, since that costs
  nothing but the read from the database, and a refusal is not remembered by
  the client, so the next hover asks again.
  A second member under a name the set already holds is refused and reported
  with the other skips — one path cannot serve two files — and legacy
  numbering runs past `.r99` into `.s00…`, letter by letter, up to
  `rarMaxVolumes`. A zero-length segment is dropped by the stitched reader
  rather than read for ever. A reader holds at most `rarMaxOpenFiles` (4) volume descriptors, closing
  least-recently-used first — a 100-part set must never cost 100 fds per
  viewer — and is mutex-guarded so parallel `ReadAt` is safe as the
  `io.ReaderAt` contract requires.
- **Interlaced video is deinterlaced wherever a picture is made**
  (`deinterlace.go`). A PAL disc is 576i: every frame is two fields taken a
  fiftieth of a second apart and combed together, which a television
  separated again and a browser does not — measured on a real disc, every
  frame flagged interlaced, top field first, and the combing plain on screen
  the moment anything moved. Nothing can be done about it in a copy, the
  fields being in the bitstream; but wherever a picture is *re-encoded* or a
  frame *extracted* it costs one filter, so both converters, the thumbnail,
  its piped fallback and the sprite frames all go through `videoFilter`.
  Three decisions in it. `bwdif` rather than yadif — motion-adaptive, the
  best ffmpeg ships without hardware behind it. **`deint=interlaced`**, which
  is what makes it safe to apply unconditionally: only frames the container
  flags are touched and a progressive file pays a frame copy, so nothing
  needs to decide in advance whether a file is interlaced. And **the
  deinterlacer goes first, before any scale** — scaling an interlaced frame
  blends the fields into rows belonging to neither, and no filter afterwards
  can take them apart, so the combing becomes a permanent smear instead. That
  order is what the test pins.
  `send_frame` is the mode: one picture out per picture in, keeping a PAL
  disc at 25 fps. `send_field` moves more smoothly on footage really shot at
  fifty and costs only a tenth more processor (measured: 13.24 s against
  11.86 s for twenty seconds of a disc), but it doubles the frames in a
  stream a phone is often pulling over a mobile connection — and both modes
  end the combing, which is the complaint. Measured cost of the filter
  itself: 6.57 s to 11.86 s for the same twenty seconds, still comfortably
  ahead of playback.
  `crop.go` is deliberately left alone: black bars are black in both fields,
  so detection sees the same borders either way, and a filter there would be
  paid on every crop for nothing.
- **DVD-Video** (`disc.go`) is the other thing on these disks that is media
  held inside something else, and it is deliberately not a second reader. A
  DVD's picture and sound live in VOB files — MPEG-2 program streams, stored
  plainly and split at a gigabyte because that is what the format allows.
  Inside a disc image they are runs of bytes at known offsets; unpacked into
  a `VIDEO_TS` folder they are ordinary files. Either way that is a name, a
  size and a list of byte ranges, which is exactly `storedEntry` — so a disc
  needs a *parser* and nothing else, and the reader, the loopback input, the
  rule against persisting derived offsets, the thumbnails and the sprites all
  come for free.
  What is indexed is a **title**, not a file: `VTS_01_1.VOB` … `VTS_01_4.VOB`
  are one film and are stitched in the order the disc numbers them, not the
  order the directory happens to list them in — sorting by offset would start
  a film that was written back to front half way through. `VTS_nn_0.VOB` and
  `VIDEO_TS.VOB` are menus and are left out, and so is anything under
  `discTitleFraction` (a twentieth) of the biggest title on the disc: a DVD
  carries the feature and then a scatter of one-second title sets — the
  distributor's logo, the copyright notice, what the menus animate with —
  and indexing those is a dozen tiles in the listing for every film. A
  twentieth is six minutes against a two-hour feature, which keeps an extra
  worth watching.
  The ISO9660 parse is a few sector reads (descriptors from sector 16, the
  root record inside the primary one, then two directories), and it is the
  parse that decides, not the extension: `.iso` and `.img` are only a
  prefilter, so an installer image or a boot ramdisk costs those reads and is
  then left alone. An image whose only filesystem is UDF answers nothing,
  which is deliberate — DVD-Video images are written as a bridge with an
  ISO9660 tree describing the same data, and the ones that are not are
  Blu-ray, whose streams are not VOBs at all. A partially downloaded image
  has its directory long before its data, so the ranges are clipped to what
  is actually there.
  **A disc image is usually inside something else**, which is how a DVD
  release ships: one image split across seventy rar volumes. A title is then
  two mappings deep — a range of the image, and the volume files that range
  crosses — and `placeInStored` is the second of them, which is all that was
  needed: the parse reads through the same stitching reader, and what comes
  out is segments like any other. `discsInside` does the replacing, at
  1 ms per set measured across nine releases. Such a title is named after the
  **directory** the set is in rather than after the member, which is a name
  the packer chose and usually says nothing ("GIS_7.img"); a set holding more
  than one image falls back to the member's stem, since one name for two
  discs would be one item.
  `.vob` is a video kind, which is what lets a title classify as one — and
  the consequence is that a DVD's own VOBs would otherwise be a tile per
  gigabyte with the menus among them, so the scan folds a folder holding them
  into its titles (`isDVDStructure`, once per folder per walk) and
  `stillIndexable` agrees, or reconciliation would put back what the walk
  deliberately skipped. A `.vob` under any other name is an ordinary video.
  A title is named after the release, which for an unpacked disc is the first
  directory at or above the VOBs that is not DVD furniture (`VIDEO_TS`,
  `AUDIO_TS`, `dvd`) — and the title number is added only where the disc
  really does offer more than one, since a number nobody has to read is a
  number worth leaving out.
  **How long a title is, only the disc knows.** An MPEG-2 program stream
  carries no duration; ffprobe estimates one from the timestamps it can see,
  and on a measured disc that came out at 26:26 for a title that decodes to
  50:43 — out by a factor of two, which is not a cosmetic error: it puts a
  wrong length on the tile, takes the thumbnail from the wrong place, and
  tells the player a film is finished half way through. So `ifoDuration`
  reads `VTS_nn_0.IFO`, and the unit that matters there is the **cell**, not
  the programme chain: a title set's VOBs store each cell once however many
  chains reference it, and the two measured discs need both halves of that —
  one lists two chains of different lengths whose cells are distinct, where
  the total is the sum, and the other lists the same 45 minutes twelve times
  over, where it is not. Summing the distinct cells came to within a frame of
  decoding the whole thing (3042.80 s against a measured 3042.76 s). It rides
  on the `storedEntry`, so `upsertStored` has it at index time and
  `nativeDuration` returns it. Both doors metadata comes through then have to
  honour it (`declaresDuration`, consulted by `setMeta` and `setProbe`), or
  the estimate wins anyway: one comes back from the metadata cache at
  enrichment, and `EnsureCodecs` produces another when the film is opened —
  which is how a listing and the item endpoint came to disagree by a factor
  of two about the same film. The rest of a probe is still worth having; it
  is only the duration that is outranked.
  **A DVD is seeked by position, not by time** (`library.SeekByte`), and that
  falls out of the same table. ffmpeg will not seek past the end it believes
  in, and what it believes is that half-length estimate — so on a measured
  title everything after 26 minutes of 50 returned no picture at all, and a
  film's last hour was simply unreachable. Each cell says which sectors it
  occupies, and those address the stitched VOB stream directly: on five of
  six measured titles the cells covered it *exactly*, to the byte, with no
  gaps. So the index is time → byte offset, and ffmpeg's http input is given
  `-seekable 0 -offset <n>` instead of `-ss` — which is why this works only
  over the loopback URL, a pipe being seekable by nothing. **Both flags are
  the seek**: given a seekable input the demuxer rewinds to the start of the
  stream before it reads, and the offset is silently undone — measured, three
  different seeks into a film all returned its first frame. Told the input
  cannot be seeked, ffmpeg reads forward from where it was put, which is all
  a conversion ever does. The sixth title is why the index is
  kept only when it accounts for the whole title: a disc whose cells do not
  add up is one this does not understand, and a wrong index puts every seek
  somewhere else. Between cell boundaries it interpolates, so landing is
  close rather than exact — four or five minutes of variable-bitrate picture
  is the error bar, against a seek that did nothing at all.
  `convertInput` (`remux.go`) is the one place that decides, because the
  input and the seek are one decision: all three converters go through it,
  and `-ss` is added only where the seek was *not* already done by position.
  Above it, `planConversion` (`convert.go`) is the whole of what the pipe
  and the segmented converter share — the seek with `-copyts`, the hardware
  decision, the picture copied or encoded, the soundtrack — and each adds
  only its delivery; the two used to spell all of it out separately, and the
  two diverging once is how a fault got in. It is tested on its own. `handleKeyframe` needs no change — it already answers with the
  time asked for whenever the content is inside another file.
  What the disc does **not** record is where one episode ends and the next
  begins: a measured TV release is one title of 2h39m with thirty chapter
  stops about five minutes apart, and both its part-of-title table and the
  video manager's own title table say one title. There is nothing to split on
  and guessing at thirds would be wrong, so it is one item.
  **A downloaded title has its clock straightened** (`ps.go`). A disc is
  authored from separate pieces and each starts its own clock near zero, so
  a title's timestamps run forwards inside each cell and jump backwards at
  every join — measured on one release: 0.25 s at the start, 2501 s a
  gigabyte in, 1140 s at two, 733 s at four. A player takes the last minus
  the first and calls it the duration (5:40 for a title of 2h57m) and can
  seek to nothing. **This is not the stitching**: a plain `cat` of ordinary
  extracted VOB files, no server involved, does the same — 4207 s reported
  for a title of 9569 — because on the disc the stream's clock never had to
  be continuous. The player followed the IFO's cell table, which said what
  played when; hand someone the stream alone and that ordering is simply
  gone. So the rewriter puts it back, out of the same cell table.
  Three things make it a rewrite rather than a conversion. Every pack is one
  **2048-byte sector** — a DVD guarantee, verified to hold across a stitched
  title at four points spanning four gigabytes — so there is no scanning for
  start codes. The fields are **fixed width** (SCR in the pack header, PTS
  and DTS in the PES headers, 33 bits with markers between the pieces), so
  the file that comes out is the same length as the one that went in, which
  is what keeps `Content-Length` honest and Range requests working. And the
  joins are **known rather than guessed**: the cell table gives each one, so
  the correction is arithmetic where looking for backward jumps would be a
  heuristic that puts a whole cell in the wrong place when it is wrong.
  Applied to the **download only**. What this server plays it seeks by byte
  position and times from the IFO, so it needs none of this, and the
  playback path should not depend on a per-sector rewrite. Navigation packs
  (stream 0xBF) are left alone: their PCI and DSI times matter to a player
  walking a disc's menus and mean nothing in a bare title file.
  Measured after: the download reports 10665.24 s against the disc's own
  10665.28, its clock climbs 0.25 → 2501 → 5077 → 10092 s, and a seek asked
  for 10000 s lands at 9999.61 where the raw stream lands at 340 s whatever
  it is asked. The alternative was ffmpeg `-c copy` into MKV: measured at
  9.5 MB/s here, about seven minutes and four gigabytes of scratch per
  download, no resuming, and no longer a `.vob`.
  Nothing decodes MPEG-2, so `opensDirectly` and `decodesVideo` are both told
  about it (`vob` → `video/mpeg`, `mpeg2video`/`mpeg1video` → `video/mpeg`):
  the conversion starts at once instead of after a black picture and a stall,
  and — this is the one that matters — the browser is never handed the file
  to chew through looking for something it can play, which for a four-gigabyte
  image is the whole disc off the disk.
- **The loopback input** (`loopback.go`) is how archived content is read by
  ffmpeg and ffprobe. `/api/stream/{id}` already serves item content with
  Range support over `OpenItem`, which is the only *seekable* view of a
  member there is, so `run` calls `library.SetLoopback` with the bound
  listener's address (`LoopbackAddr`: unspecified or loopback IP → a base
  URL; a listener bound to one specific LAN interface → `""`, and
  everything falls back to the pipe). Measured over 22 archived members and
  140 frame extractions: every seek landed, 6 range requests, 9.3-28.7 MiB
  and 0.09-1.91 s each, at most one volume descriptor open at a time in an
  89-volume set — against 64-66 MiB and 6-9 s for a piped prefix that could
  only ever reach the first minute.
  Internal requests carry `X-Media-Internal: <per-process random token>`
  (`library.InternalHeader`/`InternalToken`). `handleStream` uses it to skip
  `StartStream`, because a thumbnail fetch that registered as playback would
  make the thumbnailer throttle against its own read and pause enrichment
  for as long as tiles are being made. The marker authorises nothing: it
  only declines the flag and accepts `internalStreamCap` (64 MiB per
  response, against a measured 28.7 MiB for a whole extraction). That
  ceiling is per response and nothing accumulates across them, so what
  actually bounds a runaway reader is the tile's own time budget and
  ffmpeg's `-rw_timeout`; the cap catches the single pathological response.

Frontend (`web/src`, no framework, no runtime deps):

- `grid.ts` is a windowed virtual grid driven by a `GridAdapter`; `main.ts`
  swaps adapters between the items view (`LibrarySource`, 200-item pages keyed
  by a query generation counter) and the albums/artists views (fetched
  whole). The grid only ever asks `need(a, b)` for the visible range — keep it
  that way; that is what makes 10k+ files smooth. Cells are reconciled by
  `itemKey` (identity), never by index: in a newest-first listing an insertion
  shifts every index, and index-owned cells would repaint the entire screen,
  while identity-owned cells just move with their DOM and decoded thumbnails
  intact — verified by asserting the same img nodes survive an insertion.
- **A tile says what the file is, technically, on hover** (`mediaShape` in
  `format.ts`, tested): the codecs, the picture's size, the frame rate, and
  for a track the format and what it averages. It is a tooltip rather than a
  line on the card because it is what somebody occasionally wants and nobody
  wants in the way — a card is already narrower than the facts it carries.
  It is set on the cell root, which is exactly what `scrubCell` exists for:
  unscrubbed, it survives recycling into another view.
  Every part is optional and every absent one is dropped: a library learns
  these as it goes, and a line of separators around nothing is worse than no
  line. The one figure computed rather than read is a soundtrack's bitrate,
  its size over its playing time — an average rather than a nominal number,
  which is the truth of a variable-rate file where the nominal one is a
  fiction, and it costs nothing to know — for a film it counts the soundtrack
  and the container along with the picture, which is what every player means
  by a file's bitrate and the only figure obtainable without reading the
  stream. Megabits once there are enough of them: "12.2 Mbps" is a film's
  rate at a glance where "12200 kbps" has to be counted. Codec names are
  spelled the way
  they are written rather than the way a probe says them ("h264" is nobody's
  spelling of H.264), and one nothing recognises is shown as it came:
  wrong-looking, but never a lie.
- Grid cells are keyed by id **plus mtime and size**, and thumbnail URLs
  carry `v=<mtime>`. Both matter for files that change after first sight —
  a torrent still downloading: the key re-render retries a thumbnail that
  failed against the incomplete file, and the URL version defeats the
  immutable HTTP cache, which would otherwise pin the thumbnail of the
  file's first readable state forever. Never key a cell by id alone.
- `LibrarySource` keeps serving its previous pages until the refetched ones
  land — on a live update (`invalidate`) and on a **new query**
  (`setQuery`) alike, both through `holdOver`. A query does bring different
  rows, but blanking the grid to announce that flashed the whole screen on
  every keystroke of a search, and the answer is one page and a few
  milliseconds away. So the outgoing rows stay, the total stays with them so
  the grid keeps its shape, and when the first page lands reconciliation
  hands whatever the two queries have in common its existing cell. The one
  total not held is 0: it means the last query matched nothing, so there is
  nothing to hold, and `fetchPage`'s past-the-end guard would refuse the very
  fetch meant to replace it. A total of -1 still means nothing has ever
  arrived — a first load has no rows to keep and shows skeletons, because
  there the wait is real.
- **Nothing is refetched behind an open viewer.** A library being written to
  sends a change event every few seconds, and each one had the listing
  fetched again — two hundred items at a time, for a screen covered by a
  film, competing with the playback that is the one thing that matters
  then. The change is remembered and applied when the viewer closes, which
  a `MutationObserver` on `body.viewing` notices; the viewers already own
  that class, so nothing new has to be plumbed through them. On-demand
  fetches are untouched: a drag still resolves its neighbours.
- What is held over is decided by `sameSubject` (`sources.ts`): the rows stay
  up only while the next query is asking about the same things. Narrowing by
  a word or reordering leaves most of them in place, which is what makes a
  search read as the listing settling. Stepping from one performer to
  another, or between two particular kinds, does not — no film is a picture —
  and holding those up for the moment it takes to answer is not a crossfade
  but the wrong listing, under a chip that already says whose it is not. To
  or from "everything" is still the same subject, since one set contains the
  other.
- Correspondingly the grid distinguishes a new *view* from a new *query*:
  `setAdapter` hard-resets only when the adapter actually differs, and
  otherwise `rewind`s — back to the top with every cell still mounted.
  Cells mounted by an update that data drove (`refresh`) fade in via
  `.cell-in`; ones mounted by scrolling do not, since those are rows the
  viewer is travelling towards and should simply be there. The animation is
  opacity only — the cell's own transform is what places it in the grid.
- Grid thumbnails load through `thumbs.ts` (`loadThumb`/`cancelThumb`): at most
  3 *recent* fetches in flight (a request older than SLOW_MS stops counting,
  so one slow generation cannot wedge every slot — that produced tiles that
  only filled in when scrolling recycled the cell), oldest visible cell
  first (newest-first starved tiles under a stream of arrivals), aborted
  when the cell is recycled (the grid's `release` hook) — an abort also cancels server-side
  generation via the request context. Never assign thumb URLs straight to
  `img.src` in grid cells: the browser has ~6 connections per origin, one is
  the SSE stream, and unbounded thumb loads starve `/api/stream` — cold-start
  video playback stalls for minutes. A load that produces no image is
  remembered and retried while its cell stays on screen (`retryThumbs`,
  also called on library changes): cells are no longer re-rendered just
  because the library changed, so without a retry one failure left a
  permanent grey tile. Renderers must not delete the `img` on error — the
  retry needs it, and it is invisible without `.ok` anyway.
- Track titles in the album panel lose the number the file carries
  (`withoutTrackNumber`, tested): the list numbers its rows already, so every
  line otherwise reads "1  01. …". Only where a separator says the digits are
  a number and not the title — "44 Winters" and "1979" keep theirs.
- The album panel renders from tags first (ID3 title, tag track number,
  duration; filename/size only as fallback) and is re-fetched on every SSE
  change via `reloadAlbumPanel` — a panel opened during a scan would
  otherwise stay a pre-enrichment snapshot of filenames and byte sizes. It
  diffs its own HTML and restores scroll, so the frequent change events
  during enrichment do not disturb reading.
- Overlays (`video.ts`, `lightbox.ts`, `albumpanel.ts`) are self-contained
  classes that mount into `#overlays`, own their document-level key handlers,
  and clean up on close. `audio.ts` is a singleton bottom bar with a
  queue/shuffle model (`order[]` of queue indices). Its toggles say their state
  (`press`: the class and `aria-pressed` together), a verdict pressed twice
  quickly keeps only the latest answer (`rateGen`), and the pure queue rules
  — what follows under repeat and shuffle (`nextPosition`), where a chosen
  track goes in a rebuilt order (`placeFirst`), which rows the panel's
  window holds (`windowRows`), whether play would continue rather than
  restart (`resumable`) — live in `queue.ts` with tests. Below 720 px the
  spectrum, radio and link fold into one "⋯" menu so the title keeps its
  room, and the sheet keeps Download and Link under a "⋯" of its own; the
  queue panel holds the listener's scroll unless the current row leaves
  view, and repaints its window on a frame rather than per scroll event.
  **A release can be put after everything already queued** (`enqueue`,
  the sheet's "Add to queue"). The arithmetic is `appendToOrder` in
  `queue.ts`, pure and tested, because "at the end" is the whole promise of
  the button and shuffle is where it is most easily broken: the new entries
  are shuffled *among themselves* and appended, never dealt into the order
  that was already there. Three states of the queue, three outcomes: nothing
  loaded means there is no end to add to, so the release plays and the toast
  says so; played out means the player is parked on its last track under
  `exhausted`, and it moves on to the first new track at once — to where the
  appended segment begins, not one step on, since a shuffle from the bar
  rebuilds the order under a parked player and the position is then
  anywhere; still going means only the order changes, and a television that
  had been handed nothing to follow the current track is handed the first of
  these (`queueAhead`). The queue keeps its owner throughout: the sheet that
  started it is still playing its collection, with more after it. The cap
  is `QUEUE_CAP`, the same one `playItems` applies.
  **The sleeve is never another release's** (`sameRelease` in `cover.ts`,
  tested). An `<img>` keeps its picture until the next one has loaded, so at
  a change of release the bar showed the old sleeve under the new title for
  as long as the thumbnail took — measured at 0.8 s server-side with the
  machine busy, the bar's own stream having collapsed thumbnail generation
  to one job. Two things follow. The next track's sleeve is fetched when the
  next track is (`prefetchArt`, from the deck preload and from the
  television handover), and since thumbnails are served immutable the
  boundary then finds it in the cache. And where it is not there yet the
  sleeve is hidden behind the placeholder until it lands rather than left
  standing — unless the incoming track is the same release, when the picture
  coming is the one already up and hiding it would be a blink at every
  boundary. One release is one directory, or one album tag under one
  performer: the disc folders of a split release agree on the tag and
  disagree on the directory, and two releases that merely share a title are
  told apart by who made them.
  **A whole view can be queued** (`queueSource` in `content.ts`, tested;
  `handleTracks` and `library/tracks.go` on the server). The toolbar's
  button asks `/api/tracks?of=` for the tracks behind the view in one
  answer, because the grid holds tiles and the queue needs tracks: every
  release listed, in the listing's own order; every release of every
  performer listed, each played through from their first (`ReleasesOf`,
  matching the performer the way the artists view groups them); every
  release in every genre listed, each once however many of the genres it
  carries and performer by performer within a genre (`ReleasesIn`, the
  uncredited last); or the tracks of the music listing, the query forced to
  audio so a mixed listing gives up its films. `TracksOf` flattens releases
  under one read lock and hands a confined caller only the tracks it may
  see — a release is kept for one allowed track, and its others may lie
  outside. Capped at `maxQueue`, which is the bar's `QUEUE_CAP` under
  another name; the two have to agree, and the answer says when it was cut.
  The button is offered where the listing is music's — the three grouped
  views, the music chip, and the mixed listings only on a face that shows
  nothing but music — and the listing's words for a query are one function
  (`listFilters` in `query.ts`) that the grid, the m3u export and this all
  use. The bar words every outcome of an enqueue itself, since the sheet
  and the toolbar both stand over it.
- **A new file starts where *it* was left, not where the last one was.**
  Until the element has been handed this file's source, `video.currentTime`
  still reads the previous file's position — and a container the browser
  will not open never reaches the element at all, so its rewrap or
  conversion asked the element where it was and got the answer for the file
  before it. Stepping back to a shorter video therefore opened it past its
  own end, where it ended at once and rolled on to the next: moving to the
  previous video looked like it did nothing at all, and the position saved
  meanwhile was the wrong file's. `load` works out `startAt` from the file's
  own resume record, `sourced` says whether the element is showing this file
  yet, and `switchAt` picks between them for every route that changes source.
  It also means a converted file honours its resume point, which it never did
  before — the conversion started whenever the element said, which for a
  fresh player was zero.
- `resumeStart` (`playback.ts`, tested) decides that position: nothing in the
  first few seconds, and nothing at or past the end — judged against the
  length stored with the record *or*, when that is missing, the one the
  library measured. The second source is what disarms the records already
  written by the bug above, which carry a position with no length beside it.
- The player and the picture viewer both step through the listing they were
  opened from (`ItemSource` + `findKind` in `sources.ts`): swipe left/right,
  and for video also when the file ends. Each steps between items of its own
  kind, passing over whatever is filed in between — the viewer has nothing to
  show for the other kinds. The player switches file **in place** rather than
  reopening: closing the overlay would drop fullscreen, which is where a
  swipe is most likely to come from. `load()` is therefore the single point
  where per-file state is cleared, and `startSource` the single point where the element is handed a source —
  the file, a rewrap, the sound-fix file or a conversion — carrying the seek,
  the track it plays and the decode-check settle, so no route can forget
  the settle again — the conversion that file needed, its
  subtitle tracks, its resume point — while volume, speed and the chosen
  subtitle language belong to the viewer and stay. Rotation is the one thing
  `load` *restores* rather than clears: **it is the file's, not the
  viewer's.** Footage shot sideways is sideways every time it is opened, so
  the correction is kept beside the other things the owner has said about an
  item (`library.Flags`, in the blob database) rather than in one browser's
  storage — turned upright at the desk, it is upright on the phone. The
  picture viewer's rotation stays a viewing aid and is not persisted: a
  photograph with EXIF orientation is already the right way up, and turning
  one is a look rather than a correction.
- **Black borders that are in the file** are found and pushed off the screen
  (`crop.go`, `/api/crop/{id}`). A portrait clip padded into a landscape
  frame is mostly black pixels, and the player can only fit what the file
  says its size is — so it letterboxes that frame into the window and the
  viewer gets black on all four sides. ffmpeg's `cropdetect` is run at four
  points **across** the film, never the opening, where the fades and the
  title cards are and which alone would crop a scene away; the samples are
  **unioned**, not averaged, because a dark moment detects as smaller than it
  is and the safe way to be wrong is to trim less. The answer is a property
  of the file, so it is kept in the blob database stamped with mtime and size
  like a thumbnail — 0.7 s to find, 12 ms thereafter — and an empty answer is
  stored too, "no borders here" costing exactly as much to discover. Two viewers
  opening one film run one detection, not eight ffmpegs: the runs are
  deduplicated per file and each seek takes the thumbnailer's slot, and the
  frame size comes from the header reading where the library has it rather
  than from ffmpeg's log.
  Applying it is a scale about the element's centre, composed into the same
  transform as the rotation, with the element clipped. That works only
  because real borders are symmetrical; `cropScale` refuses an off-centre box
  rather than half-correcting it, and refuses a detection that would trim
  nearly everything. The picture never grows past the box either: the limit
  is whichever edge it reaches first, so what goes is the *smaller* of the
  two borders and nothing real is pushed off — some black can be left, and
  that black is the window's.
  It is also measured in the coordinates the **element** uses, not the ones
  the frame is stored in. Anamorphic video — every DVD — codes a 16:9 picture
  in a 4:3 grid and stretches it on display, so the detection is used only as
  fractions of the frame and the geometry is done against
  `video.videoWidth/videoHeight`. Done in coded pixels it over-scaled a DVD
  by a fifth and cut the sides off. Those dimensions are zero until the
  metadata is in, so `loadedmetadata` asks again. **It answers for the screen, not just the file**: a
  portrait picture in a landscape window is already as large as it can be,
  and the black beside it belongs to the window — turn the phone upright and
  the same file has a great deal to give back. The button appears only where
  there is something to gain, and `Flags.NoCrop` remembers a viewer who wants
  the borders kept, since a film framed at 2.39:1 has them on purpose.
- `swipe.ts` defines what a gesture is for both viewers, so one cannot come
  to mean two different things. The drag itself is one `SlideDeck` there
  too — the neighbours resolved while the finger is down, the damped
  travel, the layer run home before the file changes, a release taken to
  the previewed neighbour, the fallback for a release before the search
  answered — with `arriveEarly` the one thing the viewers differ in: the
  picture viewer renders under the layer, the player shows its poster and
  only then opens. Both used to carry all four drag methods, line for line. `watchDrag` follows a drag as it happens
  (the player up and down, the picture viewer sideways) and `watchSwipes`
  reports a flick once it is over — both, because a flick fast enough to
  arrive with no touchmove between start and end never becomes a drag, and
  the gesture still has to work. They coexist by `watchSwipes` standing down
  when `ev.defaultPrevented` says the drag already dealt with it; stepping
  twice for one movement is what that prevents.
  The `ignore` selector disowns a gesture where it *starts*: a finger
  travelling along the seek bar is scrubbing, and one on a zoomed picture is
  panning — by the time either lifts it looks like any other travel. A
  recognised gesture calls `preventDefault` on the touchend, which is the
  only way to cancel the click the browser synthesises from it; without that
  the tap lands on the video and toggles playback, or on the backdrop and
  closes the overlay.
- Switching files used to be a black rectangle for as long as the next one
  took to open, because an element goes black the moment its source changes
  and stays that way until a frame is decoded. Two layers fix it and they are
  different things. The **poster** (`.vo-poster`) is the incoming item's
  thumbnail held over the picture from `load()` until `loadeddata`, so every
  switch is covered — a drag, the roll-on at the end of a file, the first
  open. The **slides** (`.vo-slides`, `.lb-slides`) are what a drag moves:
  the frame being left, captured off the video into a canvas, with the
  neighbours' thumbnails a screen away on either side. On release the layer
  runs the rest of the way, the poster goes up showing the very picture the
  layer carried into place, and only then does the layer come down — that
  order is what keeps a frame of the outgoing file from appearing.
  The neighbours are resolved while the finger is still down and the release
  goes to *that* item rather than searching again, so a listing that changed
  mid-gesture cannot make what slid in differ from what opened. A drag
  released before its search answered falls back to the ordinary step, which
  has no preview but is better than swallowing the gesture.
- The player and the picture viewer are placed on the **visible** viewport,
  not the layout one (`viewport.ts` measures it, `--app-vw/-vh/-top/-left`).
  iOS lays a fixed element out against the layout viewport, which is taller
  than the screen while Safari's bars are showing and wider than it after a
  pinch: first the controls ended up under the bottom bar, then the picture
  and the close button ran past the right-hand edge. Sizing a viewer to less
  than the layout viewport leaves the strip around it showing the grid, so
  the app behind is hidden while one is open (`body.viewing`) — simpler than
  compensating each piece of furniture separately, and complete.
  The player also takes `touch-action: none`: nothing in it scrolls, so every
  gesture over it is the player's. The picture viewer keeps `manipulation`,
  because an enlarged picture is panned by dragging it and that panning is
  the browser's.
- The performer's name under a release is a click target of its own
  (`.link-artist`, `data-artist`), read out of the event by `viaArtistLink`
  the way the play badge is by `viaPlayBadge`: one delegated click on the
  cell, three possible meanings, each decided by where it landed. **Every**
  adapter that draws one has to read it, the track cells included — a click
  the item view did not check for fell through to the track, which queues
  everything the current listing holds, so pressing a performer's name under
  a song played the whole library instead of going to them. The name
  looks like the caption it replaced until it is pointed at, or every card
  grows a button in the middle of its text.
- **Resolution and bitrate are two orders, not one** (`pixels` and `bitrate`,
  offered on the videos view alone since nothing else has both). Resolution
  sorts on the **area**, because a listing ordered by width alone puts a tall
  clip from a phone above a film and one ordered by height does the reverse;
  the area is what somebody sorting by resolution means, and it leaves a
  turned picture tied with its landscape twin, which is right. Bitrate is the
  file over its playing time — the whole file, that being the only rate
  obtainable without reading the stream — and it is deliberately not a sort by
  size: the same bytes over twice the length is half the rate, which is the
  distinction the order exists to draw. A file whose shape has not been read
  yet counts as nothing and lands at the unsorted end, rather than in the
  middle of the order under a number nobody measured.
- The sort select is refilled from `sortOptions(mode)` by **every** path that
  changes the view, and that includes drilling into an artist — which is a
  listing of that performer's releases, so it sorts by the album keys (year,
  artist, genre) and not by the artist ones it arrived with. A key that does
  not survive the change falls back to the key the new view **opens on**
  rather than being sent to a server that would quietly ignore it —
  `openingSort`, which lives in `sorts.ts` beside the option table, both
  pure and tested. That is the first row of the table except where a view's
  subject is an order: the popularity listing opens on plays, and **a
  performer's releases open by year**, a discography being read in the order
  it was made where by name it is one shelf shuffled. A genre's releases are
  many performers' and stay by name. The drill-down sets the key outright
  rather than only when the old one no longer fits, since the name key
  survives the change from the artists view and would otherwise carry over;
  an address that names a sort keeps it, and the direction is the viewer's
  own — newest first under the default. Every way into a
  view — a chip, a drill-down, the brand, the address — goes through
  `enterView`, so none of them can forget that step; the four grouped
  sources are wired to the screen by one `bindCollection`, and which of
  them is on screen is answered by one `collectionOnScreen` — the refetch
  after a change (`reloadGroupedView`) and the guard that keeps a listing's
  stale counts out of a grouped view both ask it, where each used to keep a
  list of views of its own and the audiobook shelf was in neither, and the
  five collection cards — release, performer, genre, show, season — are one
  `renderCollectionCell` with the handful of fields they differ in.
- UI state (mode/search/sort/artist) is serialized in the URL hash
  (`readHash`/`writeHash` in `main.ts`). A change of view is **pushed** onto the
  history and a keystroke or a sort **replaces** the entry, so Back returns
  from a drill-down — on a phone the back gesture used to leave the app —
  while a search does not leave a hundred entries behind; arriving by
  address or shortlink replaces too. Grid cells take focus and open on
  Enter or Space, since a grid nothing can reach from a keyboard is not
  reachable at all. The tile's play count, verdict and spoken-word marks
  are one `.marks` row beside the watched tick, styled like the duration
  pill; they used to sit in normal flow with the page's default icon size.
  The audiobook shelf and a like-this listing say *why* they are empty
  while the analysis has not reached them, since "nothing found" there
  reads as a fault.
- **A shortlink is that hash under a short name** (`links.go`, `blob.PutLink`,
  `web/src/links.ts`). Everything here was already addressable — the view has
  been in the fragment all along — but a fragment naming a performer, a genre
  and a search is a paragraph, and a paragraph is not something anybody pastes
  into a message.
  **What is stored is the fragment, and the server never parses it.** That is
  the whole design: a performer, a genre, a programme, a search and one
  particular film are then the same feature, and a view invented later needs
  nothing in the backend at all. The validation is about *shape* — printable
  ASCII, no `#`, bounded length — and deliberately not about meaning, since a
  server that refused a key it did not recognise would be a feature that
  breaks the next time the app learns one.
  **It has to be a redirect.** A fragment is never sent to a server, so
  `/s/{code}` cannot render the view itself; it answers 302 to `/#<target>`
  and the app reads it on the way up as it always did. A code nobody minted
  goes to the library rather than to an error — a link mistyped, or outliving
  the database it was kept in, should land somewhere usable.
  **The same place gives the same link**, which is why both directions are
  stored (`c`/`t` prefixes in one bucket, written in one transaction).
  Pressing the button twice means "give me that link", not "give me another
  name for it", and without the reverse index the database fills with
  synonyms for one view.
  **The item is arrival state, not view state** (`i=`, and `al=` for a
  release). `readHash` reads them and `writeHash` never writes them, because
  an overlay is not a view — the thing behind it is — and a player that
  rewrote the address on every swipe would be describing what is in front of
  the listing rather than the listing itself. `openLinked` clears them as it
  reads them, or closing the viewer and searching for something else would
  put it back.
  A linked item opens on a **listing of one** (`justThis`). The viewers step
  through the listing they were opened from and a link was not opened from
  one: it named a single film or photograph, which is what whoever sent it
  meant. Closing the viewer leaves the listing behind it, which is why the
  item is carried *on top of* the view rather than instead of it — and why
  `viewParams` drops any existing `i=` before adding one, since a page opened
  from a shortlink still holds that link's item in its address and would
  otherwise mint an address naming two.
  **A link belongs to the name it was made under**, and the hostname is the
  first component of both stored keys (`linkKey`, NUL-separated — neither a
  hostname nor a target can hold one, so no pair can spell another's key).
  One server answers under several names, and until this was so the hostname
  in a shortlink was decoration: a code sent to somebody for the music face
  opened the films the moment they changed the name in front of it. Scoped by
  host, the same view on two faces is two codes, and a code offered to a
  third name reads as one nobody minted — which from there is what it is, so
  it lands in the library like any other unknown code rather than on a page
  saying no.
  The host is a **key component and not part of the code**, deliberately: a
  marker inside the code would be a second mechanism for one guarantee, and
  a short marker could collide between two hostnames and let exactly the
  links this prevents cross after all.
  What this depends on is the proxy passing the name the visitor asked for.
  `linkHost` reads `X-Forwarded-Host` first and falls back to `Host` — and
  the m3u export's absolute stream URLs go through it too (`requestBase`),
  for the same reason and with the same nginx line to get right — because
  nginx sends the **upstream address** by default — so a deployment that
  forgets `proxy_set_header Host $host` has every face looking like one host
  and the links crossing again, silently, which is the worst way for this to
  fail. It is the same trap the content header has, and the mint is logged
  with the host it was scoped to for the same reason the access log records
  the face. Lower-cased, since a hostname is not case-sensitive; the port is
  kept, since two servers on one machine are two libraries.
  **There is no authentication on one**, as there is none on anything else
  here: it names a place in a library that whoever can reach the port can
  already browse, so guessing a code reveals nothing that visiting the address
  would not. That is also why the redirect is not filtered by face — the
  enforcement is where it always is, on the request for the thing itself, and
  a link to a film opened on a music face fetches the item, is refused, and
  says so.
  The alphabet leaves out `i`, `l`, `o`, `0` and `1`: a short code exists to be
  written down and read aloud, and those are the characters that get that
  wrong.
- The listing's scroll position is held by the app, not by the browser
  (`scrollhold.ts`): every overlay takes it on the way up and puts it back on
  the way down. `#scroller` is `overflow: hidden` for as long as one is open,
  and coming out of iOS's native fullscreen player — which is where a
  converted file is watched, since that is the only fullscreen an iPhone
  offers a `<video>` — WebKit re-lays the document out and scrolls elements
  into view on its own account, leaving the grid back at the top. The restore
  re-asserts once on the next frame and once shortly after, because iOS moves
  the page *after* the event that closed the viewer; it only fires when the
  listing has been sent to the very top, so a reader who has scrolled since is
  left alone.
- Text fields that take a touch keyboard are 16px (`@media (pointer:
  coarse)`). iOS Safari zooms the page in when a smaller field takes focus,
  and nothing can zoom it back out: with the search box focused the layout
  stood wider than the screen and the right-hand column of the grid sat off
  the edge until the page was panned sideways. `#scroller` also refuses
  horizontal overflow outright — the grid wraps, so there is never anything
  out there to reach.
- The preferences list truncates a long directory from its head, which is
  done by making the box right-to-left — so the path inside it is wrapped in
  a `<bdi>`. A leading `/` is bidi-neutral and otherwise takes the box's
  direction, which drew every root without its first slash and with a
  spurious trailing one. The `title` sits on the same element, since a
  tooltip inherits directionality from whatever carries it.
- **The grid takes the whole window, from the top left.** Nothing is capped
  and nothing is centred — not the grid, not the top bar, not the transport.
  A capped grid was tried and is worse on a wide screen twice over: the first
  card stands hundreds of pixels inside chips that are at the edge, so the
  page looks displaced rather than margined; and every column that would have
  fitted in those margins is a row of the library that is not on screen.
- **The chips are reconciled, not rebuilt.** The counts move constantly — a
  library being written to sends a change event every few seconds, and every
  listing answer carries new numbers — and rebuilding the bar's markup on
  each of those destroyed the button under the pointer. A browser dispatches
  a click only when the press and the release land on the same element, so a
  chip replaced in between swallowed the click: pressing one did nothing,
  intermittently, which is the worst way for a control to fail. The flicker
  that came with it was the same rebuild, visible. The structure is now
  redrawn only when the structure changes — a different set of chips, or a
  drill-down arriving or leaving — and the numbers and the lit state are
  written into the elements already there. The same reasoning as the grid
  owning its cells by identity rather than by index.
- The filter chips wrap onto as many rows as they need, at **every** width,
  instead of scrolling sideways: a horizontal scroller hides the later
  filters — albums and artists, the two that are not merely a kind of file —
  behind an edge with nothing to hint at it. That was first noticed on a
  phone and fixed only there, but a window merely narrow rather than small
  does it too: six chips and their counts want more room than a laptop
  leaves beside the sort controls, and what the viewer sees is the last chip
  sliced off under them. The top bar has no fixed height, so it simply grows
  and the grid shrinks.
  On a phone it also gives back what it can, because every vertical pixel of
  bar is a row of the library not on screen. The chips container dissolves
  (`display: contents`, ≤560px) so the chips and the sort controls share one
  wrapping flow: the sort pill sits at the end of the last chip line
  whenever it fits — which it usually does, the last line being short —
  instead of always costing a row of its own, and wraps to a right-aligned
  row of its own only when the line is genuinely full, which is the layout
  this replaced. Dissolving the container is also why `space-between` goes
  on that width: with the chips loose in the row it would spread each line
  across the full width, so chips pack left and the toolbar's own margin
  pushes it right. The controls share one height there too — a merged line
  must read as one row of controls, not two heights fighting.
  Packing was still not enough for the mixed face, whose eleven chips carry
  six-digit counts, so two more things happen on **coarse pointers only** —
  width is not the question, since a narrow desktop window still has a wheel
  and a bar that ducked on every tick would be a twitch, not a saving.
  **The counts compact** (`chipCount`, tested): five digits and up rounds to
  thousands — "210k" says what a chip's number is for, telling the shelves
  apart at a glance, while under ten thousand the exact figure is no wider
  than the rounding and stays. And **the whole bar leaves while the viewer
  browses**, sliding back on the first upward flick or at the top
  (`barhide.ts`, a pure reducer with the thresholds pinned by tests;
  `main.ts` only wires it to a body class). This is not the horizontal
  scroller the chips refused: nothing is behind an edge, everything returns
  on one gesture. The hiding is `grid-template-rows: 1fr → 0fr` on the bar
  with the padding moved to an inner element (`.bar-inner`) — that pair is
  what animates height without anybody measuring it, and padding left on the
  outer element would survive the collapse as a blank strip. Three rules in
  the reducer earn their tests: downward travel accumulates so slow scrolls
  hide too, a move bigger than a finger makes in a frame is an overlay
  restoring scroll and decides nothing, and the top outranks that guard —
  a view switch resets the scroll in one assignment, and a bar hidden at the
  top would be stuck, there being no further up to reveal it from.
  The player's control row wraps for the same reason and had the same bug —
  fullscreen sat past the right-hand edge of a phone, reachable only by
  dragging the whole page across. The overlay also takes
  `touch-action: manipulation`, because double-tapping the picture asks for
  fullscreen and must not also be the browser's zoom; a zoomed overlay pans,
  which is what put the controls out of reach in the first place.
- **Playing to a receiver** (`airplay.ts`) is offered as a button of our
  own, because the browser's own picker lives in the controls we replaced.
  There are two routes behind that one button and the module hides the
  difference: WebKit's prefixed AirPlay API, and the Remote Playback API,
  which is the same idea standardised and is what Chrome has. Both are
  feature-detected, and the button is shown only once the browser says a
  receiver exists (`webkitplaybacktargetavailabilitychanged`, or
  `remote.watchAvailability`), since a button that opens an empty picker is
  worse than none. `watchRemoteState` merges the two connection events for
  the same reason.
  The two availability questions are not the same question, which matters.
  WebKit says whether *any* target is on the network, once it has looked;
  Chrome says whether one can play **what this element is holding**, which is
  nothing at all until a file is loaded — a watch armed on a player's empty
  element is answered about nothing and never asked again, so it is armed
  afresh on every `loadedmetadata` and the previous one cancelled. It also
  means the answer differs by viewer on the same network: a speaker is a
  receiver for the music bar and not for a film, and a television found over
  DIAL is one for neither. The bar therefore keeps a set of which decks can
  reach something rather than one flag, or the idle deck — which holds no
  file and can reach nothing — would take the button away from the deck that
  is playing; and its decks are appended to the bar rather than left
  detached, since an element the document does not hold is a poor thing to
  ask a browser about.
  **A browser that will not answer has not answered no.** Chrome rejects
  `watchAvailability` where it cannot monitor, and treating that as "no
  receiver" hides the button and with it the picker, which does know. So a
  rejection shows the button and Chrome's own dialog becomes the authority;
  only an explicit `false` hides it.
  Both routes hand over a **URL**, not pixels — which is why a signed one is
  what makes either work behind a password, and why what arrives is the
  file's own quality. Casting a *tab* is the other thing entirely and is the
  browser's, not ours: it re-encodes what is on screen.
  What neither route can reach is a set that is merely *discovered*. Chrome
  finds an LG or Samsung television over DIAL and reports it as "available
  for specific video sites" — DIAL launches a named app there (YouTube,
  Netflix) and carries no URL of ours, so such a set never appears in
  `remote.prompt()` and no amount of page code changes that. From a desktop
  the ways to that television are AirPlay from an Apple device, or DLNA,
  which is a server-side protocol and not the browser's to speak.
  **What is handed over has to be decodable by the receiver**, which never
  says what it can decode and cannot be asked. So when playback moves to one
  (either event), a file in a codec a
  television is unlikely to have is converted at once rather than arriving as
  sound with a black screen behind it — which is exactly what AV1 does: a
  phone from the last two years plays it perfectly and the set it is being
  sent to has no decoder for it at all. `playsOnReceiver` is the short safe
  list (H.264, HEVC) and it leaves an unprobed file alone, converting on the
  chance of trouble being the more expensive way to be wrong. Coming back
  from the television is not switched back: the conversion plays here as well
  as the file did, and changing source twice for one gesture is a stall each
  way.
  What still bounds it, once the URLs are signed (below), is two things
  neither of which is ours. The picker offers a route for *one element*, and
  an element whose output has been **moved** into a Web Audio graph has none
  left to give — so on a browser with no way to copy a deck's sound (see the
  spectrum below), opening the spectrum ends AirPlay for the decks until the
  page is reloaded. And the receiver has to be able to reach the address the page
  came from, which an older television that cannot agree with a modern
  certificate cannot; that one shows as the file's name and a spinner.
- **A television is not a browser receiver, and that is why it can be
  reached** (`internal/dlna`, `internal/server/cast.go`, `web/src/cast.ts`).
  AirPlay and the Remote Playback API both hand a *browser's element* to a
  receiver; a set discovered over DIAL is offered to neither picker and no
  page code changes that. A DLNA renderer is driven from the server instead:
  SOAP over HTTP, `SetAVTransportURI` and `Play`, and the set fetches the
  file itself. What plays is the file — measured against a 2160p HEVC
  release, the set decoded it in HDR, which is not something any browser
  route here could have delivered, and the 1080p H.264 beside it started in
  7 s and seeked to the exact second asked for.
  Three things about it are load-bearing.
  **The search goes out of every interface**, not the default route's: a
  machine running containers has a fistful of bridges, and a multicast sent
  out of one of those finds nothing. It goes out twice per interface, UDP
  being lossy and nobody retrying. IPv4 only, deliberately: the search, the
description fetches and the address handed to a set (`LocalIPFor`) all
speak IPv4, and a set reachable only over IPv6 is not found. The fetches
go through one client with a dial timeout and a short idle life, since a
set that has gone away should be given up on in seconds, and no more than
`describeAtOnce` descriptions are in flight at once on a noisy network.
  **The address handed over is ours on the renderer's network**
  (`LocalIPFor`, the mirror of the loopback address the library uses for its
  own reads). Loopback names a server the television cannot see, and the
  hostname the page came from may be a tunnel on the other side of the
  world — while the set is in the room with the server. It is signed like
  every other media link, since the set answers no password.
  **`SetAVTransportURI` gets a budget of its own** (45 s against 8 s for
  everything else): a set opens the URL before it answers, and timing out at
  the ordinary budget reported a failure for a film that was already
  playing.
  **A container has more than one name, and a set knows the ones its makers
  chose.** Before anything is copied, `castAlias` looks for another name the
  *same bytes* can honestly be handed over under — not a conversion and not a
  guess, but one container that two names describe, so a set demuxing what it
  is given finds exactly what the name promised. Measured against a real
  television: it lists `video/x-matroska` and refuses both spellings of WebM,
  and a WebM **is** a Matroska file — a constrained profile of one — so a
  video downloaded from a video site was being turned away at the door over
  what it was called. Offered as Matroska the same set played it, VP9 and
  Opus and all, its clock running and the full duration reported. The alias
  runs **one way only**: an arbitrary MKV named as WebM would promise a set
  VP8 or VP9 and hand it anything at all. The others are the spellings of
  AVI (`video/avi`, `video/msvideo`, the registered `video/x-msvideo`) and a
  transport stream's general name (`video/mpeg`).
  Whether the set can *decode* what is inside stays unanswerable — a renderer
  says which containers it takes and nothing about the codecs in them — so
  letting it fail on screen is better than refusing on its behalf, which is
  the same judgement made for a set that will not say what it accepts at all.
  **What a file is called comes from our own table, not the system's.**
  `mimeFor` reads the map registered in `server.go`'s `init`, and `.webm` had
  to be added to it because Go's *built-in* table calls it `audio/webm` — the
  right name for a WebM holding only sound, and the wrong one for a film. It
  overrules `/usr/share/mime/globs2` and `/etc/mime.types`, both of which say
  `video/webm` on the machine this was found on, so nothing downstream can
  put it right. A television decides from that name alone, and was told a
  film was a soundtrack.
  What is offered is the file, or the rewrap where the set does not list our
  container among the things it plays — a real file with ranges, which is
  what a renderer wants. The live conversion is never offered: no ranges and
  no length, and a renderer given one refuses it or plays it once from the
  top. A device that will not say what it accepts is taken to accept
  everything; it refusing on screen is a better failure than this server
  refusing on its behalf.
  **A television playing music shows what the DIDL says and nothing else.**
  It has been handed one file and has no library to look anything up in, so
  a document carrying only the URL leaves a set showing its own logo on a
  black screen. It is sent the tagged title, the artist (under both spellings
  — sets read one or the other and rarely both), the album, the genre, the
  year, the track number, the file's size and bitrate, and the cover: our own
  thumbnail at `castArtWidth`, since a grid cell's hundred pixels would be a
  smear across a room. That is why `thumb` joins the signed paths — the set
  fetches the sleeve exactly as it fetches the music, with no credentials,
  and a thumbnail says nothing about what else is in the library, which is
  the line that list draws. Artwork is music's alone: a set playing a film is
  showing the film.
  UPnP's `bitrate` is **bytes** per second, not bits — a wart in the
  specification, and writing bits there tells a set the file is eight times
  the size it is.
  **Which soundtrack is not sayable over DLNA.** The protocol hands over a
  URL and the set decides what is inside it — a release carrying six
  languages plays the one its file marks default, which is how a Nordic
  release comes out in Danish on a television as readily as it did in the
  browser. There is no `-map` to send, so the choice is made by handing over
  a file that has only one soundtrack in it: `Remuxer.File` now takes the
  track, and it is part of the **key and the file name** (`-a<n>`), because
  a film rewrapped for one language is a different file from the same film
  rewrapped for another and serving the second under the first's name would
  hand a viewer the wrong language with no way to tell. Names from before
  that no longer parse, so `Adopt` deletes them rather than serving a file
  whose soundtrack nothing records.
  The rewrap is produced **before** the URL is handed over, or the set would
  sit on a request while ffmpeg copied a film underneath it. It is only done
  where the viewer chose something other than the first track — that being
  what a set picks anyway — and where the file cannot be rewrapped at all
  the original is sent and the set chooses, which is worth more than
  refusing. Measured: a 5.3 GB release copied for one of its six soundtracks
  in about 19 s, disk speed, no frames touched.
  **A dubbed download is the case that copy could not serve**, and it is now
  the common one: a video shipping automatic dubs arrives as Matroska holding
  VP9 and one Opus track per language, whose streams belong to no MP4 — so
  `remuxable` said no and the viewer's choice was silently dropped. `remuxTrack`
  is the third kind: both streams copied through with the **container left as
  it was**, holding only the chosen soundtrack. Lossless, a read rather than a
  re-encode, and it comes out as something the set already accepts, because it
  is the same container it accepted the file in. `trackContainer` allows only
  Matroska (`.webm` → `webm`, `.mkv` → `matroska`): an MP4 needing one
  soundtrack taken out is already `remuxable`, and anything else is a container
  a browser will not open and a set is unlikely to list, where a copy would not
  be the thing that was wrong. The name carries the container now
  (`remuxExt`), since it is no longer always an MP4, and `remuxKeyFromName`
  cuts whatever extension it finds rather than `.mp4` alone.
  **The soundtrack is settled before the name is**, and that ordering is the
  whole of what was wrong. `castSource` used to be one `switch`: a file whose
  container the set did not list took the branch that renames it and `break`,
  and never reached the soundtrack case at all — which is *every* dubbed
  download, the one kind of file where the choice is the entire point. A copy
  may also come out in a different container from the file it was made from,
  and it is the copy the set has to be told about. So the file is decided
  first and named afterwards, and `castAlias` is keyed by **type** rather than
  by extension, since what needs naming may not be the item's own container.
  **The player's own choices flow into the handover.** The subtitle the
  viewer picked travels as its index (`?sub=`), and the numbering is the
  listing's — sidecars then embedded — so an embedded caption reaches a
  television exactly as a sidecar does, extracted and converted on the same
  endpoint. Both cast handlers run `EnsureCodecs` first, because the indexes
  were numbered against the probed listing and a cast resolved without it
  counts a shorter list — the wrong subtitle, or none. The soundtrack choice
  is named only where it differs from the file's default
  (`castAudioChoice`, tested): naming one costs a copy of the film, and the
  default is what the set would have picked anyway. The rule this replaced
  sent the track whenever it was non-zero — so track 0 chosen over a
  non-zero default was dropped, the one language asked for being the one
  that never arrived, and choosing the default (not track 0) paid for a
  copy that changed nothing.
  **Subtitles are handed over as a URL too**, and in three places at once
  (`sec:CaptionInfoEx`, `sec:CaptionInfo`, and a `text/srt` `res`): sets
  differ over which they read, and one that reads none ignores all three
  without complaint. The namespace is Samsung's, which is what LG reads as
  well — sidecar subtitles are not in the UPnP vocabulary at all. They are
  converted to SubRip (`ToSRT`, which goes through `ToVTT` so the encoding
  work — BOMs, UTF-16, the Latin-1 files that dominate — is not written
  twice): WebVTT is what the browser insists on and what a television will
  not read. A set draws one subtitle or none and has no menu to change it,
  so the viewer's choice travels with the request (`?sub=`, `off` for none)
  rather than the server picking; without a choice it sends the first, which
  is what the player defaults to as well.
  **A set's refusal reaches the viewer as a sentence** (`castFault`): "did
  not accept the file", "would not start playing it", "did not answer in
  time", naming the set, with the SOAP fault itself in the log — a fault
  code on the screen said nothing to anybody.
  **A set that has not answered has not necessarily failed.** Measured on a
  television with another session already open, the reply to
  `SetAVTransportURI` did not come inside any budget worth waiting on —
  while the film was already on screen. Reporting failure then also lost the
  seek that follows, so the film started from the beginning. `showing` asks
  the set what it is holding instead of believing the silence, and the seek
  goes ahead.
  **The queue is this side's business.** A renderer knows nothing of what
  follows what it was given, so the bar hands it the next track *in advance*
  (`SetNextAVTransportURI`, optional in the specification): the set opens it
  before it needs it and moves on by itself, and the bar finds out by seeing
  the URI it reports become the one that was queued. Where a renderer refuses
  that, the next track is sent when the poll reports the last has ended —
  which is why the bar polls more often than the player (`CAST_POLL` 3
  against 4) and asks every second near a track's end (`CAST_END_WINDOW`):
  a gap between songs is heard, where a few seconds at the end of a film is
  not.
  Measured at a boundary, a set about to move on **says STOPPED for a second
  or two first** — which is also exactly how it says a track has ended. Read
  as the second, the bar would send the next track into a set already
  starting it and the two would race. So with something queued a stop waits
  `CAST_HANDOVER` polls — seconds, that close to the end — to become the
  handover it probably is, and only then is taken at its word. `load` splits into `loadDecks` and `castLoad` for the
  same reason the transport forks: what the bar *shows* is the same wherever
  the sound is coming from.
  **A set stopped short of a track's end is somebody with the remote**, and
  the bar stops driving it — it used to read every stop after playing as
  the track finishing and send the next one, so the music could not be
  stopped from the set's own remote at all. Where the set gives no duration
  the two cannot be told apart, and the old reading stands: the next track
  is sent, since a queue that halts after every song is the worse failure.
  **The spectrum is of what this page is playing**, so the bar disables it
  while a television is: the decks are silent, the graph would draw a flat
  line, and routing an element into Web Audio cannot be undone — it would
  spend the deck's own AirPlay route to draw nothing. The button is dimmed
  rather than removed, a control that vanishes and comes back reading as a
  fault.
  **A set that has not started reports STOPPED, and so does one that has
  finished.** The two are told apart by having seen it play first (`seen`),
  without which the player would stop a film it had just started and the bar
  would run through a queue as fast as it could ask — one measured start
  took 7 s, which is well inside the poll.
  **All of that is one transport** (`CastTransport`, `casting.ts`), and the
  bar and the player are two adapters over it. The one-second tick, the
  clock carried forward while playing and corrected by each answer, the
  poll cadence, the seen/ended/stopped reading through `castStep`, the
  handover wait and the generation guard that drops an answer in flight
  after a stop were spelled out twice — in `audio.ts` and `video.ts`, each
  with its own fields and its own timer — and the two had already begun to
  differ in what a stop meant. The transport touches no DOM and takes its
  timers as arguments, so `casting.test.ts` drives it against a fake set:
  the clock, the cadence, the end window, the handover, the URI match that
  says the set moved on by itself, and the answer that arrives after the
  poll was stopped. What differs between the two players is what they do
  about an answer, and that is the hooks: the player persists its position
  on every poll and rolls the season on at an ending; the bar clears its
  error streak when the set plays, follows it into the track it queued in
  advance, and sends the next track at an ending.
  In the player, casting is a fork in the transport rather than a second
  player: `curT`/`totT`/`seekTo`/`togglePlay` answer for the set while it
  holds the film, so the seek bar, the clock, the resume point and the media
  keys go on working unchanged. The element is paused and holding nothing,
  which is why the poster stands in for the picture — a black rectangle with
  a running clock over it looks like a fault. The clock is carried forward a
  second at a time and corrected every fourth (`CAST_POLL`), counted in
  ticks and never in the position, which comes back with fractions in it and
  would make a modulo fire by accident or never at all. **A season rolls on
  where a set is playing it** as it does here: `goTo` hands the next file to
  the set (`castItem`) instead of the element, after the same per-file
  reset `load` does (`beginFile`, shared, since the two used to differ in
  nothing but who is given the file and a reset spelled out twice is one
  that drifts) — so the next episode's soundtrack is picked by the
  remembered language and its subtitle by the remembered language, exactly
  as here, and both travel in the handover. A set says STOPPED for a film
  that ended and for one stopped with the remote, and the two want opposite
  things, so `endedOnSet` (`playback.ts`, tested) tells them apart by where
  the clock had got to: ended rolls on, stopped ends the casting. The poll
  stands down during a handover, or the last film's STOPPED would read as a
  second ending. A season is played from its card's badge (`playSeason`):
  the player is handed the season's episodes as a listing of its own, in
  order. Closing the player stops the set, since a film
  left running with nothing on screen to stop it needs the television's own
  remote.
  There is deliberately **no authentication** on any of this, as there is
  none on anything else here: whoever can reach the port can start something
  playing on a television in the house. That is the same posture as being
  able to point the library at any directory.
- **Media URLs carry their own permission** (`sign.go`). Anything that
  fetches media without a browser behind it — an AirPlay receiver above all,
  which is handed a URL and goes and gets it — has no credentials and no way
  to be given any, so behind a proxy asking for a password it is refused
  before the request arrives here. The television then sits on a spinner
  while the access log stays empty, which is what makes it look like a fault
  in this server.
  So `/api/info` hands the page a token and the page puts it in front of
  every media path: `/api/signed/<token>/stream/<id>`, and the same for
  `hls`, `remux`, `transcode` and `subs` — a film that must be converted is
  fetched by the receiver too, and only signing `stream` left exactly that
  case broken. One route (`{rest...}`) answers for all of them rather than a
  signed twin apiece, which also settles the segments for nothing: a playlist
  names them relative to itself, so a session opened under a signed URL
  resolves its segments under the same one and the receiver never learns any
  of this exists. `signedPaths` is the allowlist and it is media only — the
  library cannot be listed with a token, which is not a login.
  The token says nothing but when it expires. That is what lets the page
  build URLs itself: a round trip before every track would be felt on a
  phone, and a per-file token would change with every request and defeat the
  caching thumbnails depend on. The cost, stated plainly, is that a leaked
  URL is worth a leaked token — the streaming half of the library until it
  runs out (`signTTL`, 12 hours). It replaces no password; it is how the
  things that cannot answer one fetch what they were pointed at. The key
  lives in the blob database (`blob.SignKey`), so links outlive a restart and
  deleting the database invalidates every one of them; with `-db off` it
  lasts the run.
  **The proxy has to be told**, or none of this changes anything: one
  location for `/api/signed/` with authentication off, carrying the same
  `X-Media-Content` header as the rest of that face — or a music face would
  serve films through it. The README has the block.
- **Pausing fades** (`FADE_MS`, `audio.ts`). Stopping an element outright
  cuts the waveform wherever it happens to be, and a step in a waveform is a
  click — the same fault `ROUTE_FADE` exists for in the spectrum, heard every
  time anybody pressed pause. The deck ramps to silence over 150 ms and is
  paused at the end of that, not at the start of it; resuming starts silent
  and climbs back to whatever the volume control says, read as the ramp goes
  so moving the slider mid-fade lands on the new level.
  The ramp is on the **element's own volume**, not a Web Audio gain: the
  graph is built only if somebody opens the spectrum, routing a deck into it
  cannot be undone, and pausing is not a good enough reason to spend that.
  It is driven by **two clocks and that is deliberate**. The animation frames
  make it smooth; the timeout makes it *happen*. A browser runs no frames for
  a hidden tab, so a pause pressed on a headset with the tab in the
  background would fade half way and never stop the music at all. Whichever
  arrives first applies the endpoint once, under a generation counter.
  A fade is cancelled outright at a track boundary and on close, and both
  decks are put back to full level when it is — a boundary is gapless by deck
  swap and fading it would open a hole, and a deck left holding a third of
  the volume is the one that plays the next track, quietly, for no reason
  anybody could see.
- **The player has the spectrum too**, on the film's own soundtrack, and it
  is the same `Visualizer` — `attach` takes media elements rather than audio
  ones, which is the whole of what that needed — inside the same
  `SpectrumPanel` (`visualizer.ts`), which owns the markup, the open and
  close, and the lit button; the two players each used to build all of that.
  Everything else about it is where it is shown and what it costs.
  It is **part of the control furniture**, prepended to `.vo-controls`, so it
  sits directly above the seek bar however many rows the buttons have wrapped
  onto and fades with them. A fixed offset from the bottom was tried first
  and is wrong: the row wraps on a phone, which is exactly the width with
  least room to spare. And it is **gone in fullscreen** — that is where a
  film is watched rather than inspected — with `syncViz` stopping the
  painting to match, rather than drawing every frame into something covered.
  What it costs, on a browser that cannot copy a deck's sound, is the
  element's **AirPlay route**, since moving an output into a graph cannot be
  undone; the button says so before it is pressed, `showReceiverButton` then
  drops the receiver button rather than leaving one that can no longer work,
  and `routed` is set from what the tap actually did (`movedOutput`) rather
  than from what was predicted. Where the sound was copied instead, none of
  that is paid and the button stays. A television driven over DLNA is untouched, the
  set fetching the file itself — but the button is dimmed while one is
  playing, because this element is then paused and holding nothing and the
  graph would draw a flat line. `dispose()` closes the context when the
  player closes: the bar's visualiser is a singleton and has nothing to give
  back, while a viewer opening the spectrum on film after film would use up
  the handful of contexts a browser allows.
- **The spectrum reads a copy of the sound, and only moves it where it
  must** (`visualizer.ts`, the rule in `tapChoice`, tested). This is the
  whole safety of the view. `createMediaElementSource` does not copy an
  element's output, it **moves** it — once, for the life of the element,
  with no way back — so everything the graph afterwards fails to do is
  silence with no repair, and the element's own AirPlay route goes with it.
  Measured the hard way: a viewer opened the spectrum early in a film that
  was still being converted, the player then replaced `video.src` behind
  playback (the sound-fix file landing), and the source node went on holding
  a file that was no longer there — the analyser read zeros, the film was
  mute, and the element could not be routed a second time, so only reloading
  the page brought the sound back. It also set `videoAudioIsReadable`, which
  is how one such film took the spectrum button away for the rest of the
  session.
  `HTMLMediaElement.captureStream` (`mozCaptureStream` in Firefox) is a copy
  instead: the element goes on playing exactly as it did, the tap can be
  dropped and taken again, and a tap that yields nothing costs an empty
  panel rather than a silent film. So the choice is copy where the browser
  has one, **wait** where it has one that is not ready yet — a capture taken
  before the deck has its soundtrack carries no track, and falling back to
  the move there would spend the film's sound to save a moment of empty
  panel — and move only where there is no capture at all (Safari), which is
  the difference between a spectrum and none. `loadeddata` is what re-takes
  a tap, asked of the deck rather than of the several places that set a
  source, since a fact spelled out in several places is one that goes wrong
  quietly — and this one went wrong silently. `release()` gives the copies
  back when the panel closes; a moved output is kept, there being nothing to
  give.
  Three consequences worth keeping. The **analyser is a leaf**: what it
  reads has already reached the speakers by its own route, so connecting it
  to the destination would play a captured deck twice — it is given a
  silent gain instead, because a node with nothing downstream is not
  guaranteed to be pulled at all and a graph nobody pulls reads zeros. A
  moved deck reaches the speakers through its **own** gain, connected before
  the analyser is so much as mentioned, which is what leaves `giveUp` with
  nothing to repair. And the graph is still built the first time the view is
  opened and never before, since on the browsers that have to move the
  output the cost is unrecoverable.
  Each moved deck fades in over `ROUTE_FADE`, because taking a *playing*
  element's output off the speakers between one sample and the next is a
  step in a waveform, which is a click — what opening the view used to sound
  like. The context is resumed before anything is tapped rather than after,
  so a suspended one cannot leave a moved deck silent in between.
- **What it draws is a quarter-octave analyser**, and four decisions carry it.
  **The bands are musical, not arithmetic.** Bins are linear in Hz and hearing
  is not, so bars are spaced by ratio: 36 bands of 0.2516 octaves — a minor
  third — from 30 Hz to `min(16 kHz, sampleRate * 0.45)`, dropping to 24 below
  a 288 px canvas, which is not another named interval but the count that
  still reads when 36 would be four pixels wide. The power curve this replaced
  gave its first three bars bin 0 — DC, which carries no sound at all — and
  put the first bin with music in it under a single bar. That is why the bass
  end moved as one block: a kick and a bass note were the same reading. `fftSize` is 4096 for
  the same reason — seventeen bins under 200 Hz where 1024 gave four — and not
  8192, whose 170 ms window is slower than the attack envelope and would smear
  the transients the caps exist to report. The range stops at 16 kHz because a
  sixth of the old panel drew 16-24 kHz, which is silent in every lossy file.
  **The display is tilted** 3 dB per octave, the slope of pink noise, about the
  geometric centre of the range. Music's own spectrum slopes down, so an honest
  plot is a ramp into the floor with a dead right-hand third; tilted, a
  well-mastered track reads roughly flat and a hi-hat moves something. It is a
  chosen lie and it is said out loud: what is on screen is no longer raw
  magnitude. It is also why `minDecibels` is -100 rather than something nearer
  the floor — the top band is lifted 13.2 dB, so a shallower range would leave
  it standing a tenth of the way up the panel in digital silence. Verified
  arithmetically at 32/44.1/48/96 kHz: silence lands at -0.03 and clamps to the
  floor, full scale reaches 1.06 and clamps to the ceiling.
  **A band is its loudest bin, not its average.** A mean drags a real tonal
  peak down toward its quiet neighbours, so a clean sustained note comes out as
  a low broad hump. The two edges are interpolated between bins, which hides
  the staircase at the bass end without pretending to resolve what is not
  there — at 48 kHz exactly one band contains no whole bin centre, and a reader
  who believes the bottom two octaves are resolved will make a bad decision
  about `fftSize`.
  **Movement is measured in seconds.** `smoothingTimeConstant` is applied per
  read rather than per second, so the shipped 0.8 smoothed twice as fast on a
  120 Hz screen as on a 60 Hz one — the same music drawn differently by a
  better display. It is turned down to 0.25, just enough to damp bin noise
  before the per-band maximum goes looking for it, and the real smoothing is
  `1 - exp(-dt/tau)` against the frame clock: 20 ms up so a transient lands,
  150 ms down so it can be read. The caps hold `PEAK_HOLD` then accelerate
  under `PEAK_GRAVITY`, and they are the only thing on screen reporting
  dynamics rather than instantaneous level.
- **The panel is the picture.** No heading, no close button, six pixels of
  padding: with a spectrum on screen, a line of chrome saying "spectrum" is a
  row of bars nobody gets to see, and the button that opened it always closed
  it too — so it lights while the panel is up (`markViz`), which is what says
  the way in is the way out. What the heading said goes on the canvas's own
  `aria-label` (`Spectrum analyser, 30 Hz to 16 kHz`), where a screen reader
  still finds it and no pixels are spent. The canvas took the heading's row:
  the panel is about the size it was and almost all of it is bars now.
  The one line of text that survived is the fault report, and it survived
  because it is the only one that ever *says* anything — `.viz-note` is empty
  the rest of the time and `:empty` keeps it out of the layout entirely.
- **Silence is a designed state, not an absence.** Nothing moves when nothing
  sounds: no idle shimmer, no resting waveform. What is left is the resting
  floor and nothing else, and the floor dims when the context is not running,
  which is the whole report — a badge over a panel would be worse. The next instinct here will be to add an idle animation; this
  paragraph is the refusal — and the showpiece layer below was built inside
  it, every term of it zero at silence, deliberately.
  The **still-frame skip** is what makes that free: once every bar and cap is
  under `STILL_EPS` — and the energy and the kick have decayed with them —
  and one settled frame has been painted, the frame returns before the whole
  paint: the clear, the fills and the composites alike. It deliberately still reads the analyser
  every frame — about 0.4% of a frame's budget — because that is what makes the
  first sound after silence appear in the very next frame instead of a quarter
  of a second later, which is what a timer-based idle throttle would cost.
- **But an audio path that never arrived is not silence, and has to say so**
  (`deafStep` in `playback.ts`, tested; `watchForSilence` in `visualizer.ts`).
  A browser can accept the routing and pass nothing: WebKit on a phone does
  exactly that for a **video** element — `createMediaElementSource` succeeds,
  `attach` returns true, the film goes on playing out of the speakers as
  though nothing had happened, and the analyser reads digital silence for
  ever. Measured on one: native playback of a WebM, no conversion in the
  chain, the panel drawn and every band at the floor. From inside the page
  that is indistinguishable from a quiet passage — which is precisely why the
  designed silence above, left alone, reads as a broken feature.
  So it is measured rather than assumed of any browser, and the caption says
  it: after `DEAF_AFTER` seconds of something **actually playing** — unmuted,
  volume up, which is what tells a pause apart from a dead path — with not one
  band having moved since the graph was built, the range in the header is
  replaced by "no audio reaches the page". Three rules make that safe, and are
  why it is a reducer with tests rather than a flag: once anything has been
  heard it is never said (a film with a silence in the middle of it must not
  accuse the browser that has been drawing it); only playing time counts; and
  it is **taken back** if sound arrives later, so nothing is claimed that
  turned out to be untrue.
  **It is the element kind that decides**, which is why no feature test can
  ask about it beforehand. Measured on one phone, in one page, at one minute's
  distance: the player's spectrum drew nothing while the music bar's — same
  browser, same graph, same code — drew every band. WebKit gives a page an
  `<audio>` element's audio and will not give it a `<video>`'s. Nothing on
  this side changes that: `captureStream()` does not exist in Safari, and a
  second element fetching the same film to be analysed would double the
  bytes for a decoration.
  So the button is offered **once and then dropped**: `onDeaf` fires the
  moment it is measured, and `videoAudioIsReadable` keeps the answer for the
  session so a later film is not offered a control this browser cannot
  honour — the judgement the receiver button already makes. It is offered
  until then because the panel is where the explanation is written, and a
  viewer should get to read it once rather than be told nothing. The film
  that measured it keeps its panel open; taking it away mid-sentence would be
  the fault this exists to end. Remembered for the session only, not written
  down: this is a limit a WebKit release could lift without telling anybody.
- **The panel's own box was wrong and everything else depended on it.** The
  canvas carried `padding` under the app's global `border-box`, so the bitmap
  was sized from the padding box while the drawing surface was the content
  box — a 398x120 element holding a 374x98 image, resampled 0.94 by 0.82 on
  every composite. Every device-pixel snapping decision below it was being
  stretched unevenly afterwards. The padding now belongs to a `.viz-body`
  wrapper. Sizes come from `getBoundingClientRect`, not `clientWidth`, which
  truncates to an integer on a canvas that is routinely fractional.
  The **resize check is on device pixels inside the frame**, not on a resize
  listener: dragging a window to a display with a different ratio changes the
  ratio with no resize event at all, and both gradients are built in device
  space. Moving it onto an event reintroduces a stale gradient stretched across
  the wrong width — easy to miss, hard to explain. The ratio is capped at 2:
  a three-times display would have us clearing 420,000 pixels a frame for a
  decoration, on exactly the device where playback is tightest.
- **The hue runs along the frequency axis, not up the bars.** A vertical ramp
  in canvas space is only ever sampled as far up as the tallest bar reaches, so
  the cyan half of the app's accent pair was unreachable at ordinary listening
  levels and every quiet passage was a flat indigo wall. Horizontal, the whole
  ramp is on screen at every level, and all 36 bodies still go into one path
  and one fill. What replaces the level channel that gives up is a wash toward
  the panel's own background — full strength at the baseline, gone by two
  fifths of the height — so a short bar recedes into the surface and a tall one
  stands clear of it. It is the same path filled twice: `fill()` does not clear
  the path, so the second pass costs one fill and no geometry. On top of each
  bar is a crown at 22% of `--text`, constant rather than level-scaled, which
  adds nothing to a bright tall bar and is the lit edge that keeps a
  six-pixel one reading as an object. The colours are read from the app's own
  tokens at build time; this module used to be the only place in the frontend
  that restated a design token as a literal.
- **The showpiece layer is driven by the sound and by nothing else.** The
  panel was honest and restrained; it is now honest and theatrical, and the
  line it has always drawn holds: at silence every term below is zero, the
  panel is perfectly still, and the still-frame skip ends painting. The
  spectacle lives inside that rule rather than instead of it.
  Five pieces, each priced. **The bloom** is the panel redrawn into a canvas
  `BLOOM_DOWN` times smaller and composited back over itself with `lighter` —
  bilinear filtering is the blur, a few drawImage calls buy the neon halo a
  hardware analyser has, no filter() and no shader. It blooms the
  **highlights**, not the panel: compositing the whole picture back adds
  light in proportion to what was already there, so every dull pixel
  contributes — measured on the transfer, a mid-tone of 0.30 was adding 0.20
  at a peak, which is not a glow but a fog, and the panel read as smudged.
  The small canvas is multiplied by itself first, which squares every
  channel: the background falls by four to fourteen times while a bright bar
  keeps two thirds of its halo. That is the bright-pass every real bloom
  starts with, and here it costs one drawImage on an image a twenty-fifth of
  the size. The radius came in with it (eight to five), a very small canvas
  scaled back up being blotchy rather than soft. **The glass floor**
  (`REFLECT`) mirrors the strip above the baseline below it, bloom and all,
  faded destination-out so the panel's own background shows through — the one
  piece that costs bar travel, and the piece that makes the block stand on a
  stage rather than sit on a chart. **The stage light** is a radial wash from
  the baseline whose alpha is the smoothed energy. **The hue flows through
  the bars**: a canvas path is baked in device space when built while a
  gradient maps through the transform at fill time, so translating between
  build and fill slides the colour through fixed geometry for free — the
  gradient is periodic (accent → accent-2 → accent, twice over a double
  width) so the phase wraps without a seam, and the phase advances with the
  energy, so a quiet passage drifts, a loud one streams, silence freezes.
  **The sparks** flash a crown whose band leapt more than `SPARK_DELTA` in a
  frame — the attack made visible, one batched path like everything else.
  **The kick is the one that moves a head.** The bottom `KICK_BANDS` bands
  are watched as one signal, and an onset — a kick drum landing — slams the
  light, the bloom and the hue flow. Level alone cannot say this: a
  sustained bass line is loud without hitting, and the hit is what a head
  nods to. What counts as an onset is a rise of `KICK_DELTA` above the
  signal's own **recent floor**, not its average, and the difference is
  blast beats: at seven hits a second in a wall-of-sound mix an average
  climbs to the middle of the oscillation and the hits stop clearing it —
  the detector went quiet on exactly the music with the most beat. A floor
  follows dips instantly and climbs slowly, so the brief dip between two
  hits re-arms it however dense the mix; the rise must also be a rise, and
  a kick still burning past half strength refuses to re-fire, which keeps
  one hit one flash.
  **And the whole panel keeps time** (`pace`). The spectral flux — how much
  the bands rose this frame, per second — is a tempo the analyser can read
  without ever finding a beat: blast beats churn it, a ballad barely stirs
  it. Smoothed over `FLUX_TAU` and mapped onto `[1, PACE_MAX]`, it divides
  every **release** — the bars' fall, the caps' hold and gravity, the kick's
  decay, the floor's climb — so fast drums get a panel that falls fast
  enough to show every hit, while a slow song keeps the slow read that
  suits it. Attack times are left alone: a transient should land at any
  tempo. Reduced motion pins the pace at 1 and stills the flow, the sparks
  and the kick, keeping the light and the glass.
  Deliberately not done: album-art palette extraction (tens of milliseconds of
  quantisation plus a canvas readback per track, in an app whose whole priority
  order exists to keep that class of work off the critical path — and it would
  be the one surface that stops matching the accents), per-bar fill styles
  (breaks the single batched path), and frequency hairlines (below the visible
  threshold on a phone, and three unlabelled lines say a scale exists without
  saying what it is — the header carries the range as text instead).
- The tab says what is playing (`nowplaying.ts`): the player's file if one is
  open, else the bottom bar's track, else the app's name. The player wins
  because it is in front of everything, and closing it hands the tab back to
  whatever the bar is still holding.
- The media keys are **claimed, not registered** (`mediakeys.ts`): a page
  has one set of handlers and two things that want them, so the video player
  takes them while it is open and hands them back on close, leaving the
  bottom bar in charge again. Actions nobody claims are cleared rather than
  left behind — a key answered by a bar hidden behind an open film is worse
  than a key that does nothing. The player deliberately does not take
  previous/next: a film is not a track list. Each
  action does what it is named rather than toggling — a desktop control that
  believes the music is playing and a player that has already paused would
  otherwise argue, and whichever arrives second wins. `stop` is handled as a
  pause that keeps its place, so the same key starts it again; without a
  handler the key did nothing at all. `playbackState` is published on every
  play and pause, which is what keeps the desktop's own controls offering the
  right button. A *mute* key is not one of these: it is the system mixer's,
  never delivered to a page, and nothing here can see it. The player's own keys are one table
  (`PLAYER_KEYS` in `playback.ts`, tested for a label each and no key twice)
  that `?` draws as a card in the player, so the list cannot drift from the
  handler.
- The queue panel renders only while it is visible, so it must be unhidden
  *before* it is filled — filling it first left an empty box on screen.

Background work is priority-ordered: **playback > thumbnails > tag
enrichment**. `handleStream` marks active media responses
(`Library.StartStream`/`Streaming`) — except its own process's internal
reads, which carry `library.InternalHeader`; while one is live, thumbnail generation
collapses to a single job (`bgSem` in `thumbs.go`) and `EnrichMeta` pauses
between files (its `busy` callback also covers in-flight thumb generation via
`Thumbnailer.Generating`). Client-side, overlays call
`holdThumbs`/`releaseThumbs` so an open video/lightbox aborts and later
re-queues grid thumbnail fetches. Keep this hierarchy in mind when adding any
background I/O — on cold start (scan + enrichment + cold thumb cache) the
disk is the bottleneck and playback must win.

**One library, several faces** (`internal/server/content.go`). A request
carrying `X-Media-Content: music` (or `videos`, or `images`, or a
comma-separated combination) is shown that and nothing else: not in listings,
not in counts, and not if it asks for a video by id. Indexing is untouched —
one scan feeds every face, and removing the header shows everything again
with nothing to rebuild.

The header is set by whatever sits in front, a reverse proxy giving one
hostname to each face, and **never by the page**: a browser cannot put a
header on the requests that matter here, since an `<img>` fetching a
thumbnail or a `<video>` fetching a stream send what they like. That is
precisely why the filtering is in the backend. `/api/info` tells the client
what it may see — and nothing that depends on that answer is drawn before it
arrives (`contentKnown`), since the live stream opens first and its first
event renders the chips, which on a restricted face meant all six of them
for a moment and then the three it has — so it can leave out views it would
get nothing from
(`content.ts`, tested: one class shows All plus that class's grouped views,
since a chip for the only class there is would just repeat All; two classes
bring the chips back), but that is a courtesy — the enforcement is
`Server.item`, which every by-id handler resolves through — the album sheet
and its zip included (`albumFor`): a caller confined by paths is handed only
the tracks of a release it may see, and 404 where none remain, and the
positions listing and the by-id position are faced the same way, since a
record of how far a film got is a record that the film exists — plus
`content.kinds()` on every listing and `content.mask()` on every set of
counts. Albums and artists are music's own, so they are empty for a face
without it. Music covers playlists as well as audio: a release's own running
order is not a different kind of thing.

**A restricted face counts; it does not mask.** Masking after the fact works
for the per-kind totals and cannot work for the three that run *across*
kinds: started, finished and played are one number each, so a videos face was
being told how much music somebody had been playing. Worse, the live stream
and the listing arrived at their numbers by different routes — the stream
masked the running totals, the listing did too — and any disagreement between
them shows as a chip that displays one number and settles on another half a
second later, because the stream answers first and the listing corrects it.
Both now go through `CountsFor` with the face's `Kinds` in the query whenever
anything is restricted, so they are the same numbers counted the same way;
an unrestricted caller still takes the O(1) running totals, which is what the
`q.Kinds == 0` in the early-out is for. `mask` stays as the belt.
This is also why the debug access log records the **face** every request
arrived as. A restriction is set by the proxy, and its failure mode is one
location block that forgets it: everything works, and one endpoint quietly
answers for the whole library. That is invisible from the outside — the
answer is well formed, just not this face's — and a chip flicking to a number
from another library is exactly what it looks like.

**A caller can also be restricted to part of the library**
(`X-Allowed-Paths`, `internal/library/paths.go`). The header names
directories, separated by commas — or newlines, for a path with a comma in
it — and the request then sees only what lives under them: not in a listing,
not in a count, not by asking for one thing by id, and not through the
collections. Absent or empty is the whole library, exactly as an absent
content header is every class.

**The two compose**, and they do so by each being applied where it belongs
rather than either knowing about the other: `Server.item` asks both, the
listing carries both in its query, and a request holding both sees the
intersection. Verified against a real library: one band's directory answers
101 items, 92 audio, 13 albums, 3 artists and 1 genre, and the same
restriction on a *videos* face answers nothing at all.

Three things about it are load-bearing.

**It matches the absolute path**, not the display path. `Rel` starts at a
root's own base name, so two roots can produce the same display path and a
restriction written against it would let through files the operator never
named. An archived member's path is the archive's own with the member after a
NUL, so it is under the same directories the archive is and needs no special
case — pinned by a test, because the day that stops being true it would go
wrong silently.

**The prefix test is component-aware.** `/srv/mediax` is not under
`/srv/media`, and a plain string prefix hands over a directory nobody
allowed.

**It is a value type, and it is in every cache key** — the listing cache, the
counts cache, and the HTTP ETag. The first two are the trap `KindSet` exists
for: without it one caller's restricted answer is served to the next caller
who asks the same question unrestricted, which a test pins down. The third is
the same trap one layer out — two faces of one library are at the same
version and hold different answers, so `versionTag` hashes the restrictions
into the tag and sends `Vary: X-Media-Content, X-Allowed-Paths`, or a client
that changed faces (or a shared cache serving both) keeps the answer it had.

The collections need more than a filter: albums, artists, genres and shows
are grouped and cached per version, of everything. A restricted caller gets the
albums whose **tracks** it may see — any track, since a playlist album is a
file in one place naming tracks in several — and the artists and genres are
grouped from *that* set rather than filtered afterwards, which would count
performers and genres the caller cannot see. That regrouping is only paid
when the header is set. **Every grouped endpoint passes the restriction on** through one prologue
(`groupedView`, `listOrNear`: the tag, the face, the query and the paths
once, and a `near` listing or the ordinary one by one rule — which also
answers a list and never `null` where nothing is analysed yet),
and a handler test pins all of them (`TestPathsRestrictEveryCollection`):
the albums endpoint once left it out, answering with every release in the
library under chips that counted only the allowed ones — the one fault a
restricted face must never have, and invisible from outside, the answer
being well formed.

`library.KindSet` is what carries the restriction into a query
(`Query.Kinds`, distinct from `Query.Kind`, which is the view the *viewer*
chose). It is a value type so it sits in the listing cache's key — without
that, one caller's restricted answer would be served to the next caller who
asked the same question unrestricted.

**What is running is on the screen** (`about.go`, the foot of the
preferences sheet). Two questions that are easy to answer wrongly from the
outside: a binary that has been rebuilt and not restarted looks exactly like
one that has, and hardware conversion either happens or does not, silently,
with the difference between a film that plays and one that stalls hanging on
a driver and a group membership. `/api/info` already carries what the page
needs before it draws anything, so it carries this too.
The version is stamped by the linker because the image is built from copied
sources and has no repository for Go's own version stamping to read — the
Makefile passes what it can see. A build made from a checkout stamps itself
through `debug.ReadBuildInfo`, and that wins: it cannot be stale. `modified`
is worth reporting on its own, being the difference between a build somebody
can go and read and one that existed on one machine for an afternoon.
Capabilities report only what is *established*, and the box names only what
is **missing**: a list of things that work is a list nobody needs to read,
while the absent one is the line that explains whatever the reader came
wondering about. The hardware search runs at startup (`FindHardware`, in a
goroutine — a broken driver can take the whole probe budget to say so) rather
than lazily, so the answer is there before anybody asks and no viewer waits
for it in the middle of a conversion. A conversion asked for before the search has
finished converts in software rather than waiting on it; the next one takes
the hardware if it was found. The answer is read through `chosen()`,
under a lock: `/api/info` can ask before the search has finished, and a
conversion asks after `sync.Once` has settled it — only the second of those
was safe by construction.

The directories to index can be changed while the server runs
(`internal/server/prefs.go`, `/api/prefs`). Three things move together or
they disagree until the next restart, which is why one callback in `main`
owns the whole change: the list, the filesystem watches (`Watcher.Reset`,
because a watch left on a removed directory reports it and the watcher puts
back exactly what the scan is about to take out), and the index — where the
load-bearing part is that `stillIndexable` now asks `underRoots`. A removed
directory leaves its files exactly where they were, so "still on disk" cannot
be the whole test, and without that check reconciliation would keep
everything under it for the life of the process. Stored roots outrank the
command line, which seeds a first run rather than being the setting; with
`-db off` there is nowhere to write them, so a change lasts the run and
`/api/prefs` reports `persisted: false` rather than letting it look permanent.
Directories nested inside another on the list are dropped (the enclosing one
already walks them, and every file under one would be a duplicate of itself).
**A caller confined to part of the library is refused the preferences
outright**, reading as well as writing. Changing them is the obvious half —
it is the one call that could hand somebody the whole disk — but reading them
matters as much: the list names the directories the library is *rooted* at,
and a caller allowed one branch of that tree has no business learning what
the others are called. Everything else such a caller is told is filtered to
what they may see, and this would have been the one place that was not. The
refusal is checked before the `-lock` question, since a confined caller may
not learn whether this server would otherwise have allowed it either.
`/api/info` carries `confined` so the page can leave the button out rather
than offering one that answers 403 — the same courtesy `content` is, with the
handler as the guarantee.

**There is no authentication in front of this**, as there is none in front of
anything else here: whoever can reach the port can point the library at any
directory the process can read. That is the deliberate posture for a personal
server on a trusted network.

`run` binds the listener itself (rather than `ListenAndServe`) so `-listen`
port 0 can be resolved to the real port for `browse.URL` and for
`library.SetLoopback`, and so bind errors surface before anything claims to
be listening. `-open` without an explicit
`-listen` rewrites the address to `127.0.0.1:0`: an ad-hoc browser session
must not collide with a running instance or expose the library to the LAN.

Serving details worth knowing before "fixing" them:

- **Some conversions run on the graphics hardware** (`hwaccel.go`), and the
  rule for which is the whole of it. A phone's 4K video is the case software
  cannot do at all: measured on a 24-second iPhone clip — 3840x2160, 60 fps,
  10-bit HEVC — the software recipe produced 20 s of output in 49 s, which is
  0.41 of real time, so the player drains its buffer and stalls for the whole
  film. Nothing in the encoder settings fixes that: `ultrafast` was still
  0.61, and **decoding alone**, with no encoder in the chain, was 0.78.
  But **hardware is not simply better**, which is why it is not used
  everywhere. On a DVD, software converts at three times real time and spends
  **0.8 Mbit/s**; the graphics hardware manages ten times and spends 4.3 —
  five times the bytes, for speed nobody needed, down a connection somebody is
  watching over. Fixed-function encoders are that much less efficient, and at
  a matched bitrate they are visibly worse (SSIM 0.945 against 0.964 on one
  clip). So `hwPixelRate` draws the line at a little over 1080p60: 4K of any
  frame rate goes to the hardware, everything else stays where the picture is
  better. Measured after: the same 4K clip converts at 2.3 times real time and
  6.3 Mbit/s, against 0.3 and 12.1 before.
  Three things it turns on. `hwaccel_output_format` is load-bearing — without
  it every frame is copied back to system memory for the filters and the
  copying costs more than the hardware saved (5.5 times real time with the
  frames left alone, 0.37 with them brought back). The **codec list is short
  on purpose**: hardware that cannot decode something produces *no frames at
  all*, silently — a DivX file through Intel's video engine produced none —
  and those old codecs are the ones most likely to need converting. And the
  deinterlacer has to be the hardware's own, since bwdif works on frames in
  system memory, which these are not; `auto=1` is the same judgement
  `deint=interlaced` makes in software, verified on an interlaced disc at
  166 frames of 166 progressive.
  Four backends are defined — VAAPI, QSV, NVENC, VideoToolbox — and **only
  VAAPI has been measured**. That is safe because of how one is chosen: each
  is *proved* by running a real conversion through it before it is ever used,
  so a backend whose arguments are wrong fails its own test and is never
  picked, and the machine converts on its processor exactly as it did before.
  The probe is a real encode because every cheaper question has a wrong answer
  available: the device node exists on machines with no driver, the driver
  loads on hardware that cannot encode, and ffmpeg lists encoders for hardware
  it has never seen.
  The **binary is what this is for**. The container image now carries
  `intel-media-driver`, so it works when given `--device /dev/dri`, but it is
  given no device by default and finds nothing.
  One thing that had to be fixed to make the rule possible: `EnsureCodecs`
  read the picture's shape and rate and threw them away. It is the probe that
  runs when a film is *opened*, which is the moment something decides how to
  convert it, so `Item` now keeps `Width`, `Height` and `FPS` from it — the
  decision turns on pixels per second and had nothing to go on without them.
- `/api/transcode/{id}?t=SECONDS[&mode=audio]` converts videos the browser
  cannot decode: fragmented MP4 on stdout, no ranges, seek = reopen with
  `t`; capped at 2 concurrent ffmpeg runs and counted as streaming for the
  background-work gate. `mode=audio` copies the video stream and only
  re-encodes audio — mandatory for 4K HEVC, where a full re-encode runs far
  slower than realtime (measured: ~17 MB in 20 s versus 1.4 GB in 35 s).
  The client picks the mode in `checkDecodes`: no decoded video frames →
  full; frames but no decoded audio bytes while `item.acodec` says the file
  has a soundtrack → audio (this is the E-AC3-in-MKV case, which produces
  picture with silence and never fires an error event).
  Its input is `it.Path` for a plain file and the loopback stream URL for
  content inside another file, the same three-way switch the rewrap and the
  segmented conversion use; the stdin pipe is what is left when there is no
  loopback address, and a seek on a pipe means reading from the beginning.
  Where the codecs are known, though, the soundtrack is decided *before*
  anything plays (`useKnownCodecs`, `decodesAudio` in `playback.ts`): a
  browser asked about `ac-3` or `ec-3` gives a definitive answer, and acting
  on it saves a stall a few seconds in and a jump back to the nearest
  keyframe. It also settles *which* soundtrack is heard, which the decode
  check cannot: a file offering an AC3 track and an AAC commentary hands
  Chrome one it cannot decode and one it can, so Chrome plays the commentary
  — reasonably enough — and the check then sees decoded audio and concludes
  all is well. Converting from the track the file leads with is the whole
  fix. Codecs with no settled type string to ask about (`wmav2` and the
  like) are left to the decode check, and the cinema formats — DTS, TrueHD —
  are refused outright, having no type string and no decoder anywhere. Audio mode may
  escalate to full if the copied picture turns out to be undecodable too.
- `/api/remux/{id}` rewraps rather than converts, for the files where the
  container is the only thing the browser will not open — an FLV or an MKV
  holding H.264 and AAC holds exactly what every browser decodes. `-c copy`
  into a faststart MP4 rewrote a 337 MiB FLV in 1.5 s where a full re-encode
  would have touched every frame, lossily. `remuxable` in `remux.go` answers
  for two faults with the same cure. One is a container the browser will not
  open around streams it decodes: the allowlist for that is deliberately
  short, since moving HEVC into an MP4 to escape such a container would only
  trade it for a codec another browser cannot decode. The other is a file
  **already** in the right container whose picture is labelled in a way the
  browser refuses — HEVC written as `hev1` instead of `hvc1`. Apple's
  decoders take only `hvc1` and ffmpeg writes `hev1` unless told otherwise,
  so an iPhone that decodes the stream in hardware would not start it, and
  the player, seeing nothing decoded, re-encoded every frame of the film to
  correct four bytes. That rewrap adds `-tag:v hvc1` and copies. Which case
  applies is read from the file itself (`library.VideoSampleFormat`,
  `mp4box.go`): the video sample entry's four-character code, a handful of
  reads down the box tree in pure Go rather than an ffprobe, since the answer
  is what turns a re-encode into a copy — and it goes through `OpenItem`, so
  a member of a rar set answers as readily as a plain file. The player asks
  for the rewrap on a decode failure too, not only when the container is
  refused, but only where `canPlayType` says the browser has HEVC at all
  (`decodesHEVC` in `playback.ts`): one that has none would wait through a
  copy of the whole file and still have nothing it could decode. The decode
  check is re-armed for the rewrapped stream — it was asked for because
  nothing decoded, so it has to show that now something does — but not at
  once: the frame counter starts again at zero for a new source, and a check
  landing between the seek and the first decoded frame reported failure about
  a file that was about to play, and sent it to be re-encoded after all.
  `checkAt` holds it off for a moment of *playback*, which is the clock that
  says the pipeline is running; a resume sets it the same way, from where the
  file actually starts rather than from zero.
  404 means "rewrapping would not help", which is what sends the player on
  to the converter — so there is nothing to ask first.
- **A stream can lie about how far its frames are reordered, and then a
  faithful copy is exactly the wrong thing** (`reorder.go`). H.264 declares
  `max_num_reorder_frames`: how many decoded frames a player must hold before
  it can present them in order. A stream putting more B-frames between its
  references than that is lying, and what follows depends on how forgiving the
  player is — ffmpeg and VLC buffer generously and show it correctly, while a
  browser taking the declaration at its word emits frames early and drops the
  ones arriving behind what it has already shown.
  Measured on the file this came from: three B-frames between references
  against a declaration of one, and Chrome reporting *"Dropping frame with
  timestamp 0.48 s, which is earlier than the last rendered frame (0.52 s)"*
  once every four frames — 25 fps arriving as nineteen with a limp. Everything
  about the copy was verified faithful first: the first forty frames identical
  to the source and in the same order, every presentation gap exactly 40 ms
  across three thousand, both streams the same duration, the whole file
  buffered before playback and the server silent throughout. Nothing was wrong
  with the copy. The stream is wrong, and a copy preserves it exactly.
  So the cure is the one thing a copy is not — re-encoding, which rewrites the
  frame structure and the declaration together. Verified: re-encoded, the
  timestamps come out strictly in order and the stutter is gone.
  **It is found by reading the opening**, not by guessing from the container:
  one ffprobe over `reorderProbeFrames` frames, and the decision is
  `reorderVerdict`, pure and tested against the numbers real files produce.
  Two signals, either sufficient. Frames **emitted with timestamps running
  backwards** are the fault observed directly — ffmpeg's decoder honours the
  declaration exactly as a browser does — needing two, since an edit-list
  oddity at the start of a file is one inversion and not a lie. A **B-run
  longer than the declaration** is the structural signal, trusted only where
  the declaration is under two: three consecutive B-frames under a
  declaration of two is an ordinary B-pyramid, the middle frame itself a
  reference, and the first version of this rule — which compared run length
  against the declaration unguarded — accused every modern encode in the
  library of lying and sent an honest HEVC rewrap to the converter. Caught
  by an existing test the Docker image had been skipping for want of an
  encoder.
  Remembered for the run, keyed by the file's identity — the same shape of
  cost `EnsureCodecs` already pays at the moment a film is opened, and for the
  same reason. **No answer is not an accusation**: no ffprobe, a pipe that
  cannot be read twice, a timeout or an unparsable document all leave the file
  exactly as it was, because being wrong the other way costs a film every
  frame re-encoded.
  It is applied at **all three places a picture is copied** — the rewrap
  (which answers 404, the thing the player already acts on), the piped
  soundtrack conversion, and the segmented one. The client cannot make this
  decision: it reads a stalled picture, never a header, and cannot see the
  status behind a `<video>` source.
  **And to native playback**, which is where it was missing: a browser
  handed the file itself trusts the declaration exactly as it trusts a copy
  of it. Measured on a phone, two MP4s from one muxer (Lavf 58.29) stuttered
  for their whole length played as they were — declared 2 against runs of 6
  and 3, with 22 and 50 inversions in the opening frames — and nothing had
  asked, since the check guarded only the copies. So `/api/item` runs the
  look when a film is opened, with the probe's own budget and once per film
  per process, and stamps `Reencode` on the copy it hands out; the player's
  `plannedRoute` (`playback.ts`, tested) then converts rather than plays,
  and `tryRemux` stands down, a copy being exactly the thing that cannot
  help. A look that could not finish is not a verdict and is **no longer
  cached as one**: with the short budget an item fetch gives it, a film
  would otherwise have been excused for the life of the process, for the
  conversions with their own budget as much as for the next open. And it is deliberately **not** asked of a
  television: a set fetches the file and decodes it with the same generosity
  VLC has, so re-encoding a film for a player that was never going to drop a
  frame would be paying the whole cost for nothing.
  AVI is where this turns up, that container having no way to express
  reordering at all, so whatever its encoders wrote went unchecked.
- **A soundtrack conversion becomes a file while it is being watched**
  (`?mode=audio` on the same endpoint, `remuxSound`, `upgradeToSoundFix` in
  `video.ts`). This is the one case the pipe serves worst, and it is also the
  commonest: a picture every browser decodes beside a soundtrack none of them
  does, which is most of a television release and every 4K one. The pipe
  answers no range request, so a browser managing its own buffer disconnects
  when it is full and reconnects asking for the byte it wants next — and is
  answered with the conversion from byte zero, every time. Measured over one
  viewing of a 4K release: ten connections, **963 MB across the link to move
  the stream 167 MB**, at 97 Mbit/s on a link whose headroom over the film's
  own 12.6 Mbps was 7.7x and so nearly all spent. One of those ten was asked
  for byte 118 M, delivered 93 MB, never reached it and was abandoned whole.
  **That waste cannot be tuned away, because it is not a constant**: every
  reconnect re-reads from the beginning, so it grows with the playback
  position and is worst at the end of a film. Pacing the output or widening
  the buffer only moves where it becomes intolerable.
  What fixes it is the file, for the same reasons the rewrap is one. What
  makes the file affordable is that **it is made behind the playback rather
  than in front of it**. Measured on a 22-minute 4K release: the picture
  copies in 2 s (it is a copy, and the disk is the only cost) and the
  soundtrack encodes in 37 s, so the whole file arrives in about a
  thirty-sixth of the film's own length — which is to say inside the first
  three per cent of playback, which is precisely the stretch the pipe carries
  cheaply. The two failure modes cancel: the pipe is only ever asked for what
  it is good at, and everything after the switch is an ordinary file — a
  measured **6 ms** to answer a range 90% of the way in, where the pipe would
  have re-read 1.8 GB to reach it.
  Four things about it are load-bearing.
  **`-aac_coder fast`, and only here.** It is the whole of the wait: measured
  on the same release, 61 s of encoding with the default coder against 37 s
  with the fast one, where the copy beside it is 2 s. The live conversion
  keeps the default — it has only to stay ahead of playback, and it has five
  times over.
  **The kind is in the key and in the file name** (`-aac`), for the reason
  the soundtrack is: a film whose sound has been converted is a different
  file from the same film merely rewrapped, and serving one under the
  other's name hands a viewer a film they cannot hear with nothing on screen
  to say why. The plain copy is marked by nothing at all, so names written
  before there was a second kind still parse and are still adopted rather
  than converted again.
  **`-tag:v hvc1` is applied on the codec as well as on the source's own
  label.** For a file already in an MP4 the mislabelling *is* the fault being
  fixed; for HEVC being copied out of a Matroska container there is no label
  to be wrong yet, and ffmpeg would write the one Apple's decoders refuse —
  so the fix for one case would newly break the other.
  **The player asks by polling, not by waiting.** The ask is what starts the
  file being made and the server goes on making it after the asker has gone,
  so a short wait costs nothing but the retry — where one long request would
  hold one of the handful of connections the page has, for minutes, against
  the playback that is running meanwhile, and could be dropped silently by
  anything in between. It stands down on a 404 (a copy would not help), on a
  file whose picture turns out to need converting too (the copy would deliver
  that same picture, later), and on Safari, which is given segments instead —
  a segment is already an ordinary file with a length and ranges, so there is
  no re-read to save, and changing source under a film in iOS's own
  fullscreen would cost more than it bought.
- **Choosing a soundtrack in a container the browser already opens costs a
  copy, not a conversion** (`?mode=track`, `remuxTrack`, `startTrack` in
  `video.ts`). Only Safari can switch tracks on the element; everywhere else
  the choice means a new source, and until this the new source was a
  conversion — for a dubbed WebM, a *full re-encode*: the MP4 rewrap answered
  404 (VP9 belongs to no MP4), the error path asked `convertForContainer`,
  and that decided on the codec being H.264 rather than on whether the
  browser could decode it. A picture Chrome plays perfectly well was being
  re-encoded frame by frame to change which language was heard, and the Opus
  dub was transcoded to AAC beside it — a generational loss on a soundtrack
  that was already what the browser wanted. `startTrack` now asks for the
  container copy wherever `opensDirectly` says this browser opens the file.
- **How much to convert turns on whether the picture is *proven*, not on what
  it is called** (`convertMode` in `playback.ts`, tested). The two callers ask
  the same words and mean opposite things, and collapsing them into one rule
  broke a WMV within the hour.
  `trackMode` is asked while the file is **playing**: the browser has
  demonstrably decoded this picture, so anything not known to be undecodable
  is copied through, and a codec with no type string to ask about is proven by
  the frames on screen.
  `convertForContainer` is asked when the browser **refused the container**
  and nothing has decoded at all. Nothing is proven there, so only a picture
  positively known to play may be copied. `wmv2` has no type string, so
  `decodesVideo` answers `null`; read as "not known to be undecodable" it was
  copied into an MP4 that cannot even hold that codec, and ffmpeg answered
  with **nothing at all** — measured, 0 bytes against 1.6 MB for the same
  file converted properly. What the viewer saw was "this format cannot be
  played by your browser", and then the audio-mode watchdog escalating eight
  seconds later and the film starting after all.
  So: proven → convert only on a definite no; unproven → copy only on a
  definite yes. The old rule (`vcodec === 'h264'`) was right for the WMV and
  wrong for VP9 in a container Chrome will not open, which is what the
  collapse was trying to fix; this is right for both.
  **Safari asks for the rewrap too**, up to a size (`rewrapWorthTheWait`,
  `REWRAP_WAIT_LIMIT` = 2 GiB in `playback.ts`). It used to go straight to
  HLS on the grounds that a segment plays in a third of a second while a copy
  plays only when it is finished — but the copy runs at disk speed (measured:
  a 405 MiB FLV in 1.5 s, a 351 MiB MP4 relabelled in 2 s, both about
  270 MB/s), and what it buys is the file itself: native playback, byte-range
  seeking anywhere in it at once, its own clock and quality, nothing asked of
  the server afterwards, and it is still there the next time the film is
  opened. A live conversion gives none of that and holds an ffmpeg open for
  as long as the film lasts. Past the limit the wait becomes the bigger
  cost — a 17 GiB member would be minutes — and HLS is the better trade. A
  browser with no native HLS waits whatever the size, since its alternative
  is a pipe it cannot seek in at all.
  The reason it writes a **file** and not a stream is delivery: iOS Safari
  opens a media URL with `Range: bytes=0-1` and will not play a resource
  that answers 200 with no ranges, which is all a live conversion of unknown
  length can answer. The rewrap goes out through `http.ServeContent` — real
  ranges, a length, seeking the browser does itself — and needs none of the
  conversion's corrections, because the timestamps are the file's own.
  The rewrap does **not** run on the requesting context: Safari hangs up on
  its opening probe, and work tied to that request would be cancelled every
  time it was started. Callers wait on their own context; the work carries on
  for whoever asks next. What is written is scratch — reproducible in seconds,
  so the database stays the one thing worth keeping or deleting.
  `Remuxer.Close` cancels the rewraps still being written, so a restart does not leave an ffmpeg filling the scratch space behind it. Both converters share one working space and one budget (`scratch.go`,
  `-tmp` and `-tmp-max`), because the disk is one disk: each reports what it
  holds, asks whether the two of them are over, and frees its own least
  recently wanted. A finished rewrap is counted, and the budget pruned,
  **before** its waiters are released: announced first, a caller that found
  its film ready and asked for the next one at once raced the accounting,
  and the older file was still on disk a moment after the newer one was
  there. The entry cannot prune itself — only finished files are candidates,
  and it is not finished until `done` closes.
  A source larger than the whole budget is left to the
  segmented converter, which holds only what it has produced so far; a
  segmented session larger than the budget on its own is left alone rather
  than killed under whoever is watching it, since that is a budget set too
  small for one film and stopping playback is not the way to say so.
  `/api/convert/{id}` reports how far a conversion has reached, for the item's most recently asked-for conversion, and the
  player polls it **only where there is a wait**: the rewrap writes a whole
  file before a single byte is playable, which for a film is tens of seconds
  of nothing and indistinguishable from a failure. The segmented conversion
  has no such wait — it plays after its first segment and runs on ahead — so
  it reports nothing, and a badge over a picture that is already playing is
  only something in the way. The readout also stands down the moment there is
  something to watch, whatever the server is still doing, and a poll already
  in flight when it is dismissed cannot put it back: without that generation
  check the last late answer left it on screen for good, since no further
  poll was coming to take it away.
- `/api/hls/{id}/index.m3u8?t=&mode=` is the same conversion delivered as
  segments, and it is what Safari plays. That browser opens a media URL with
  a byte-range request and will not play a resource that cannot answer one,
  which a conversion piped down a single response of unknown length never
  can — so on a phone a file needing conversion did not start at all. HLS
  makes every request a request for an ordinary finished file, and playback
  begins after the first segment rather than after the last: measured on a
  2.2 GiB film whose audio had to be re-encoded, **0.31 s to a playable
  playlist** against 56 s to write the same conversion as one file, and
  never for the pipe. The client picks it on `canPlayType` for
  `application/vnd.apple.mpegurl`, which is Safari and nothing else; every
  other browser keeps the pipe, since none of them plays HLS unaided.
  Everything downstream is unchanged, deliberately: it is the same seek, so
  the clock still starts at the keyframe `/api/keyframe` measured and the
  subtitles are still rebased onto it, and seeking still reopens the
  conversion at a new time.
  **The session is in the URL path, and that is load-bearing.** ffmpeg writes
  plain segment names into the playlist and a player resolves them against
  the playlist's own URL, which drops the query — so with the session
  identified by `?t=`/`?mode=`, every segment request arrived asking for a
  different conversion than the playlist described. The first one answered
  (quietly starting a second ffmpeg) and the rest did not line up, which the
  player reported as an unplayable format. Asking for the playlist now
  redirects to one under an opaque session token, where a relative name can
  only resolve to that session's own segments.
  Which conversion is needed is decided from the file's codecs, not from what
  was decoded: a container the browser will not open decodes nothing, so
  there is no black picture or silent soundtrack to go on. H.264 is decoded
  everywhere, so when that is the picture only the container and the sound
  are in the way and the picture is copied — the difference between a
  conversion that runs far ahead of playback and one that re-encodes every
  frame of a picture the browser could always have shown.
  The playlist is relabelled on the way out: ffmpeg writes
  `#EXT-X-PLAYLIST-TYPE:EVENT` throughout and leaves it that way after adding
  the end marker, so a player goes on treating a finished conversion as a live
  event — iOS's own fullscreen shows LIVE where the clock should be, and a
  running time of what was converted rather than of the film. The end marker
  is what says it is complete, and a complete presentation is VOD. While it is
  still growing EVENT is the honest description and is left alone.
  **Subtitles ride the playlist as renditions** (`hlssubs.go`), because
  AirPlay hands a receiver a URL and nothing else — no caption field, no
  metadata document — so the only way captions reach one is inside what the
  URL describes. Where the film has any, the start endpoint serves a
  **master** playlist instead of the media one: every subtitle (sidecars and
  embedded tracks, the listing's own numbering) as an `#EXT-X-MEDIA`
  rendition, the media playlist beside them, all named relative so they
  resolve under the session's path with the signed prefix exactly as
  segments always have. The session and its segments are untouched — one
  conversion serves every choice, and `?sub=` only picks which rendition is
  marked `DEFAULT`. `AUTOSELECT` rides with it and is off on the rest:
  autoselect everywhere would put subtitles on screen by system language
  that nobody asked for, and the menu is the offer. `NAME` is unique within
  the group, counted under the name as it is written: three tracks labelled
  alike used to come out as one name twice, and a player folds those into
  one entry. Each rendition is a
  one-entry playlist naming a single WebVTT for the whole run — subtitles
  are kilobytes, and four-second pieces would be a thousand requests for
  nothing — with the cues rebased onto the session's clock by the same
  arithmetic `?shift=` has always done. An adopted session has no item
  snapshot, so `hlsSessionItem` reads identity and start back out of the
  session key, which is what lets a conversion kept across a restart still
  serve its captions.
  On this path the **stream owns subtitle display**: Safari draws the
  rendition inline, in its native fullscreen and on the receiver, one menu
  everywhere. The player's own menu still marks the choice, through the
  same `markSubMenu` the `<track>` path uses — the HLS branch once lit the
  entry with a class the stylesheet does not know, so on Safari the menu
  showed no choice at all. The page's own `<track>` elements stay attached but disabled —
  the element's textTracks then hold rendition tracks and ours in an order
  nothing controls, so showing by index would light a random one, and
  doubling the stream's rendering is the other way to be wrong. Changing
  subtitles there reopens the stream where it is, the same cost a
  soundtrack change pays.
  **A failed conversion is forgotten, not cached.** The Remuxer has always
  deleted a failed entry — "a later ask deserves a fresh attempt rather than
  a cached refusal" — and the sessions now do the same (`forget`): one whose
  ffmpeg produced nothing playable leaves the map the moment its waiters
  have their error. Before that, a transient failure (a busy disk, a mount
  hiccup) sat in the map under its key and answered every later ask for the
  same film at the same resume point with the same error, for the life of
  the process, unless the budget happened to evict it. Both behaviours are
  pinned by tests now, since the two diverging once is how the bug got in.
  A session is one ffmpeg writing into one scratch directory, keyed by item,
  time and mode. What it produced is **kept until the space is needed**:
  going back to a film should not convert it again, so nothing is removed on
  a timer or a count. Two things are bounded separately — the number of
  conversions actually *running* (the least recently wanted one is stopped,
  and its segments stay), and the disk, which is the budget's job. A film
  nobody is watching stops being converted for the same reason: there is no
  sense spending the processor on the rest of it, and no sense throwing away
  the part already done. It deliberately does **not** run on the requesting context —
  a player fetches the playlist, goes away to fetch a segment and comes back,
  and work tied to the first request would be dead before the second. Live
  sessions are capped (least recently asked-for evicted), reaped after going
  quiet, and removed with their directories on shutdown.
- **Every handler that serves an opening probes the same way** (`probed`,
  `server.go`): the soundtracks, the embedded captions and the codecs come
  from the probe `EnsureCodecs` runs, and the subtitle listing, the cast, the
  rewrap and the HLS start all used to spell out the same three lines to get
  at it. The television's links are built by one function too (`mediaURL`):
  our address on the set's network, and the signed token where there is a
  key to sign with.
- **Which soundtrack** is the viewer's choice, not the browser's. A film
  with several — a Nordic release carrying four languages, a disc with a
  commentary — hands the browser a set of streams and it picks one on its
  own reasoning, which is how a Swedish film comes out in Danish. The
  soundtracks are listed in `Item.Tracks`, filled from the ffprobe that
  already runs when a video is opened (`EnsureCodecs`, whose guard is now
  `probed` alone: the codecs used to be enough to skip it, but the tracks
  come from the same call and are written down nowhere, so a film restored
  from the cache would never offer the choice it carries). Choosing one is
  served by conversion with `-map 0:a:<n>`, because a browser cannot be told
  which stream to decode — so the choice costs a conversion, and the HLS
  session key and the rewrap's file name carry the track for the same reason
  they carry the mode. `pickAudioTrack` (`playback.ts`, tested) decides where
  to start: this film's remembered choice, then the language of the last
  choice made anywhere — pick Swedish once and the next Nordic release starts
  in Swedish, which is the whole of the "smart" part — then the file's own
  default, then the first. A commentary track is never any of those unless it
  is the only thing there.
  **The pick is applied, not only marked.** The menu marks `audioTrack`, so
  the stream on the element has to be brought to it or the menu lies —
  which it did: the pick was computed after `useKnownCodecs` had already
  started a conversion carrying the first track, and on a file that played
  natively it was never applied at all. `load` therefore builds the menu
  (and with it the pick) before any route starts a stream, and
  `applyAudioChoice` settles the rest in cost order: agreeing with the
  file's own default is free, the element's `audioTracks` (Safari) are
  free, and only then does the choice cost a source — the rewrap first
  where it is worth the wait (`remuxUrl` carries `?a=` now; the server
  always did), else a conversion whose mode `trackMode` picks by whether
  the picture decodes. `appliedTrack` is what the current stream actually
  carries; every route that sets a source records it, which is what makes
  the whole thing idempotent. The `a` key cycles the menu from the
  keyboard, as `c` does for subtitles.
- **A seek asks for the keyframe, not for the time.** With the picture
  copied, ffmpeg starts it at the keyframe at or before the seek — but the
  soundtrack is re-encoded and starts exactly where it was told, so asking
  for the time itself puts the two however far apart the keyframe was. That
  is heard as the subtitles agreeing with the sound (both being where the
  clock says) while the picture runs seconds behind. The client therefore
  measures the keyframe first (`/api/keyframe`) and asks the conversion for
  *that*, which is also where it has told its own clock the stream begins.
  Both converters also pass `-copyts` with `-avoid_negative_ts make_zero`,
  which shifts every stream by the same amount instead of rebasing each to
  zero on its own — the belt for a seek whose keyframe could not be measured.
- **The converted stream does not start where you asked.** With the video
  copied, ffmpeg can only begin on a keyframe, so it begins at the last one
  at or before `t` — ten seconds early is ordinary for a 4K release. The
  element's clock restarts at zero there, so a client that assumes the stream
  starts at `t` misplaces the position readout, the saved resume point and
  every subtitle cue by that much. `/api/keyframe/{id}?t=` answers where it
  will really begin, and the player makes that answer its `tcOffset`.
  The answer is measured, not predicted: it runs the same seek with the same
  tool (`-copyts`, one packet, first pts). A keyframe listing looks like it
  would do and does not — ffmpeg's seek is conservative in ways an index scan
  does not reproduce (a time a few ms past a keyframe rewinds to the one
  before), and it is an order of magnitude slower to obtain. Do not "simplify"
  this into an ffprobe frame scan. Full re-encode mode seeks accurately and
  needs none of it.
  Landing on the exact frame is not possible from the client: the conversion
  is a live stream with no ranges, and the browser reports it unseekable
  (`seekable` is empty even where it has buffered), so setting `currentTime`
  is clamped to zero. Keyframe granularity with an honest clock is the
  contract. The seek bar follows the finger on every route
  and asks a set or a conversion once, on release; it used to commit a
  converted stream on the first touch and ignore the rest of the drag.
  What the television's poll means — still opening, playing, paused, ended,
  stopped by hand — is `castStep` in `playback.ts`, tested, and the shared
  transport's tick only acts on its answer. The set's volume is told 150 ms after the slider
  settles rather than per pixel, the speed button is disabled while a set
  plays since the element's rate changes nothing anybody hears, and the
  fault badge offers "Try again", because a disk that comes back should not
  need the player closed and reopened.
- Subtitle cue times are absolute in the file while a converted stream's
  clock starts at its keyframe, so `/api/subs/{id}/{n}?shift=SECONDS` rebases
  them (`shiftVTT`); the player re-points every `<track>` whenever the
  conversion reopens. Without it, subtitles on a converted video are wrong by
  wherever the viewer last seeked.
- `/api/subs/{id}` lists external subtitle files (`subs.go` matches
  subtitle extensions against the video's stem in its own directory,
  deriving label and language from the part of the name that follows the
  stem);
  `/api/subs/{id}/{n}` serves one converted to WebVTT (`vtt.go`, which also
  handles BOMs, UTF-16 and the Latin-1 files that dominate in the wild).
  Subtitle files are attachments, never library items — they are held in
  `subsByDir`, reconciled by the scan and the watcher like items are.
- **The captions inside the file are offered too** (`Item.EmbSubs`,
  `embsubs.go`), because that is how a television release ships them: no
  sidecar, one MKV, the text muxed in beside the picture — the episode that
  reported this had a SubRip stream and not a subtitle file anywhere on the
  disk. A browser will not surface embedded text from a stream it is
  playing, and once playback is a conversion there is nothing to surface it
  from; the only way to a `<track>` is extraction.
  They are read by the ffprobe that already runs when a video is opened —
  the same call that fills the soundtrack menu, so the listing costs
  nothing new — text codecs only (`textSubCodecs`): a DVD's or Blu-ray's
  bitmap subtitles are pictures, and listing them would offer a menu entry
  that can never draw a line of text. The listing appends them **after the
  sidecars under one continuous numbering**, so the client picks an index
  and never knows which kind it chose; `SubtitlePath` answers only for
  entries that have a path — an embedded one answered `("", true)` for one
  afternoon, which sent the handler off to read a file called nothing —
  and `EmbeddedSubStream` resolves the rest to the stream ordinal.
  **Extraction reads the whole container** — ffmpeg has to demux everything
  to collect every cue, a couple of gigabytes for a hundred kilobytes of
  text — so three things follow. It is **cached** by file identity, because
  the player re-points its `<track>` at a new `?shift=` on every conversion
  reopen, which is every seek, and the shift is arithmetic applied to the
  cached cues. It is **deduplicated**, a second ask waiting on the first
  rather than reading the film beside it. And it is **counted as
  streaming**, since the read races the viewer's own playback. Everything
  downstream is a sidecar's path: the same WebVTT out, the same SRT for a
  television, the same `?shift=` — whose parser takes ffmpeg's hour-less
  timestamps as readily as a sidecar's full ones.
- A stream that stops half way is logged with the reason. `ServeContent`
  swallows the copy error, so the reader is wrapped to keep its last one
  (`recordingReader`): from the outside a transfer that dies looks the same
  whether our read failed or the far end let go — a reverse proxy reports
  both as "upstream prematurely closed connection" — and the two want
  different fixes. A cancelled request is not reported, since that is the
  client leaving and says nothing about the file.
- **Shutdown cuts transfers in flight.** `httpSrv.Shutdown` is given five
  seconds, so restarting the server ends any download that is running. That
  is worth knowing before reading a truncated transfer as a bug: check the
  restart times first.
- With `-debug` every request is logged as it finishes (`access.go`): method,
  path, status, bytes sent, duration, and the range that was asked for. That
  log answers the one question no other hop in the chain can — how much of
  what was requested actually left this process.
- **A file that never starts is asked about before it is converted.** A
  disk that has stopped answering — a filesystem that shut itself down keeps
  its mount and returns EIO on every open — fails every route at the same
  `OpenItem`: the stream, the rewrap, the segments, the pipe. Measured on
  one, the player spent ten seconds failing through all four and then said
  "this format cannot be played" over an ordinary MP4. So `serveStream`
  answers **503** for an item it knows but cannot open — a 404 is "no such
  item", and the two must be told apart — the pipe answers 503 rather than
  an empty 200 when ffmpeg produced nothing (which read as success in the
  access log), and the player, on the element's first error for a file, asks
  the stream for one byte and reads the status (`readFault`, tested): a
  fault is written into the picture and stays there (`giveUp`), where a
  toast would vanish and leave a spinner saying nothing. The answer is
  matched to the film **by id**: `refreshItem` replaces the item object for
  the same film when `/api/item` answers, and that answer raced the probe —
  three times out of four it landed first, and an identity check dropped
  the fault as though the viewer had moved on. Once given up on, a file is
  given up on (`faulted`): the codecs arriving a moment later must not start
  a conversion of a file the disk will not hand over. The 503 carries the
  reason in the viewer's words (`openFault`, tested), because a disk that
  has stopped answering and a filesystem that needs repair — XFS's
  "structure needs cleaning", which a remount after a crash leaves behind —
  are different things to be told. Anything but a
  fault goes on to the rewrap and the conversions exactly as before. The
  index keeps such files deliberately: a root that fails to read is
  protected from reconciliation, and they play again when the mount does.
- **A transfer that stops half way is usually not this server.** A remote
  viewer's bytes cross reader → handler → tunnel → reverse proxy → browser,
  and three of those hops speak up when they break: `recordingReader` warns
  when our read failed, a reverse proxy logs `upstream prematurely closed
  connection` when we closed first, and when neither says anything the far
  end ended it. Already measured, so it need not be measured again: the
  handler serves a 17 GiB archived member over loopback in seconds; deep
  ranges into a multi-part set are answered instantly and a sequential read
  holds its rate to the last byte; a tunnel carried that same file whole at
  line rate; the link stayed clean with both directions loaded. One browser
  stopping a few hundred MiB into a file that another pulls whole at
  50 MB/s over the same path is a fault on the client — the `206` it then
  issues to resume itself is the tell — and nothing on this side fixes it.
- Media is streamed only by indexed ID (`http.ServeContent`, Range support);
  m3u entries resolve against the index, so playlists cannot expose paths
  outside the configured roots. Keep it that way.
- Static: hashed `/assets/*` are cached immutable; **everything else is
  `no-store`** because embedded files have no modtime → clients cannot
  revalidate, and `no-cache` leads to stale app shells referencing dead asset
  hashes. A path under `/api` that matched no handler answers 404 rather
  than the shell — including one the mux cleaned into nothing, which is what
  a request ending in `..` becomes — because handing JSON callers an HTML
  document tells them all is well and moves the failure somewhere unrelated.
  That pairing is the whole cache-busting story — a rebuild changes
  the hashes, and the shell that names them is never cached — so an
  `/assets/*` path that is not in the bundle is a request from a replaced
  build, not a client-side route (UI state lives in the URL hash). It gets a
  404 rather than the SPA fallback, which would answer a `<script>` with
  HTML and surface the failure as a parse error somewhere unrelated.
- Thumbnails (`thumbs.go`): lazy, cached in the bbolt blob database
  (`internal/blob`, default `<data>/media.db`, `-db` to move it or `off`
  to disable; the same db persists enrichment metadata). Keys are
  `(id, width)`; the value embeds the source mtime+size,
  so a changed file overwrites its stale entries instead of accumulating.
  With the db off (nil `ThumbStore`) every request regenerates. Concurrent
  generation is deduplicated (followers share the leader's bytes — required
  for the no-db mode) and failures are negative-cached in memory, except
  context cancellations, which say nothing about the item — a generator must
  therefore report a canceled request *as* cancellation and not fold it into
  ErrNoThumb. The grid cancels routinely (a recycled cell, an overlay taking
  the screen), so getting that wrong turns one badly timed scroll into a grey
  tile that lasts as long as the process. Images are pure Go
  (EXIF orientation via the minimal parser in `exif.go` — orientation formulas
  are covered by tests); video frames require `ffmpeg` on PATH (optional at
  runtime, present in the Docker image) and go through a temp file because
  ffmpeg needs a seekable output; audio uses embedded tag art, then a
  folder image beside the tracks.
  **A video's frame is taken a tenth of the way in**, wherever it lives —
  plain file, archive member or disc title alike — because the opening of a
  film is its front matter, and for a television episode that is the
  distributor's logo almost without exception: a shelf of shows read Netflix,
  Paramount, Showtime, one after another, none of them saying which programme
  it was. It is the offset archived video has always used (`thumbOffsets`,
  measured across 15 releases), applied now to everything. Behind it, in
  order: the alternative offset, three seconds, and frame 0 — which needs
  nothing but the opening bytes and so covers a clip too short to seek into,
  a duration nobody has measured, and an archived one fed a bounded prefix
  the seek would land past the end of.
  The store key stays the width alone, deliberately: a change of recipe
  reaches files that change on disk and leaves the rest as they are. Remaking
  every video still in a library is an ffmpeg seek apiece, and the tiles
  already made are not wrong — only older.
  Every still — a tile, a scrub-sheet frame, over a path or over loopback —
  is taken by one recipe (`frameArgs`), the four builders that used to spell
  it out being wrappers over it now; what differs is only whether the seek
  is accurate (a sheet on a plain file decodes forward to the moment) and
  whether the read is over loopback (the internal marker and a socket
  timeout).
  Whether a frame came out decides the outcome, not ffmpeg's exit status:
  it exits 0 having written nothing when the seek overran its input, and
  non-zero once the frame is safely out when a piped prefix ends under it.
- Archived video gets a thumbnail too, and it is **taken at a fraction of
  the duration over the loopback stream URL** (`seekThumb`), not from a
  prefix. The old piped recipe sampled the first minute and let ffmpeg's
  `thumbnail` filter score the samples; measured on six archived 2160p
  releases, four of six stored a distributor logo or a text card. The
  filter steps past *black*, not past a card — it is attracted to one (a
  studio logo card measures luma stddev 41.69) — and no uniformity test can
  ever catch that. Widening the prefix or the window did not help either:
  front matter is where the first minute *is*.
  So the frame comes from `archiveThumbFraction` (10%) of the duration,
  floored at `archiveThumbMinOffsetSec` (120 s) and capped at half the
  length. Measured over 15 members at 1/2/3/5/10/20/30/50%: 1-3% still
  returned front matter for two or three of them, 5% and 10% returned a
  real scene for all 15, and deeper is worse because climactic scenes are
  dark (worst tile 16.20 at 10%, 10.76 at 20%, 5.70 at 50%). Front matter
  is an absolute-time phenomenon, measured at 0-240 s, so 10% of anything
  40 minutes or longer clears it.
  The piped prefix survives as the **fallback**, for exactly two cases:
  no loopback address, or no duration to take a fraction of (`pipedThumb`,
  one pass, keyframes only — measured 1.44 s against 6.13 s for decoding
  every frame, and it picked the better frame too).
- What is written down there has to be earned, because for an archive
  member **the store key never rotates**: `(id, mtime, size, width)` are
  fixed once the volume set is complete, and `handleThumb` serves stored
  bytes `immutable` — so a bad image is permanent in the db *and* in every
  browser that saw it, out of reach of the frontend's `v=<mtime>` buster
  and of `retryThumbs`. Hence a frame is trusted only when (a) neither the
  caller's context nor the item's own timeout fired — the exit status can't
  be used, a prefix ending mid-cluster fails a run that produced a good
  frame, and a killed ffmpeg leaves a half-flushed JPEG that the old code
  returned with `err=nil`; (b) the JPEG ends in its end-of-image marker;
  and (c) the frame is not near-uniform. A timeout is treated exactly like
  a cancellation, i.e. never negative-cached: the disk being busy is what
  the timeout is *for*, and writing the item off for the life of the
  process would leave a permanent blank tile. That holds for a plain file as much as
  for an archived member now: both frames come out of one `runFrame`, which
  is where the torn-frame and the deadline guards live — the plain path used
  to be a near-copy that had neither, and stored a half-flushed JPEG as the
  tile for good. A follower waiting on a generation whose leader was
  cancelled — a cell scrolled away under the player's poster request —
  leads the next attempt itself rather than inheriting the cancellation as
  its answer.
- Two luma-stddev thresholds, and neither is a classifier. Below
  `archiveThumbRetryStdDev` (12.0) the frame is flat enough to spend the one
  retry on — measured, accepted frames scored 16.31-66.64 across 20 items
  and the black front matter the offset avoids scored 0.84-1.13, and the
  retry never actually had to fire. Below `archiveThumbMinStdDev` (3.0)
  nothing is stored at all: this is a "is this a picture?" floor and *not*
  a gap, because the populations overlap around it — a measured black
  title-text frame scored 0.84 on one release and 7.90 on another, while
  the darkest real frame measured 5.70. The offset does the work; this only
  refuses to make something permanent out of a flat colour. If both offsets
  are flat the item gets `ErrNoThumb` — a blank tile the next start
  retries, never a black one remembered forever.
- **The scrub sheet is ten seeks, not one pass** (`makeSprite`). It used to
  be a single ffmpeg invocation with an `fps` filter — elegant, and it reads
  the entire film: measured on an 87-minute 1080p release, **over five
  minutes against its own two-minute timeout**, so the sheet never appeared
  for exactly the long films a scrub bar is for, and the failure was
  negative-cached for the life of the process. Ten independent input seeks
  (`-ss` before `-i`, so ffmpeg jumps to the keyframe at or before the mark
  and decodes forward) produce the same ten positions in **3.5 s**, each
  reading a few megabytes around its own timestamp. They run one at a time:
  ten concurrent ffmpegs on one disk is how a preview competes with the
  playback it is meant to sit beside. The frames are tiled here rather than
  by ffmpeg, so a seek that lands past the end of an overstated duration
  leaves its cell black instead of shifting every later frame into the wrong
  timestamp — the client derives each frame's moment from the duration
  alone. That takes a **slot per offset**: appending only the frames that
  came back did exactly that shift, and `tileSprite` is tested with a gap.
  `spriteFrameWidth` is 320 because the sheet is now what a **hover preview**
  animates, not only what the seek bar shows in a corner, and 160 upscaled to
  a grid tile is a smear. `spriteCacheWidth` moved with it: it is a slot in
  the thumbnail bucket rather than a width, and it changes whenever the
  sheet's shape or recipe does, or a stored sheet from the old one would be
  served under the new convention at half the resolution the client expects.
- **Hovering a film shows what is in it** (`preview.ts`): the sheet's ten
  frames stepped at `FRAME_MS`, which is a five-second tour of the whole film
  rather than motion — that is what a preview is for, and the user asked for
  no sound and was content with a low rate. It is a `background-position` on
  one div: no video element, no stream, no decoder, nothing that could take
  one of the handful of connections an origin gets from the playback that
  needs them.
  It is deliberately cheap to *not* use. Nothing is asked for until the
  pointer has stayed `DWELL_MS`, since crossing a grid is not a request for
  ten previews and a sheet that does not exist yet costs the server ten
  seeks. Only one is fetched at a time, and only for the tile the pointer is
  still on — one that has moved on leaves the sheet for whoever asks next,
  it being cached from then on. Decoded sheets are capped (`SHEET_CAP`), or
  a long scroll would hold every tile the pointer ever crossed.
  **A frame keeps its own shape.** Sizing the background as a percentage of
  the tile forces every frame into the tile's proportions — invisible on a
  wide film, grotesque on a clip shot on a phone, where a 0.56 frame was
  drawn at 1.52. The sheet's natural size says what shape the frames are (it
  is one image of `cols` by `rows` of them), so the frame is fitted inside
  the tile at that shape, in pixels rather than percentages, and what is left
  over is painted black. Bars rather than a distortion — and rather than a
  crop, which is what the still beside it does: a preview is for seeing what
  is in the film.
  The element is inserted **before the badge**, so it covers the still and
  nothing else: the play badge is what says a tile can be opened and must not
  vanish at the moment the pointer is on it. And it is for pointers only
  (`hover: hover`, and never `pointerType === 'touch'`) — a phone has no
  hover, and wiring this to touch would put a preview in the way of the tap.
  Entering is where the item is read, because the grid recycles cells: a
  listener closing over the render's item would, three scrolls later,
  preview a film that is no longer under the pointer.
  **Recycling is the whole difficulty here**, and it caught this twice.
  The first was the pool: the grid recycles cells, and handler properties
  survived it — so a tile that had been a film became an album card *still
  carrying the film's `onpointerenter`*, and hovering the album played the
  film across the sleeve. The fix is in the one place elements are reused:
  `grid.recycle` clears everything a renderer may have left, because a
  renderer that sets something cannot be made to remember to clear it — the
  first version of this fix asked six renderers to, which is the same bug
  waiting for a seventh. The clearing is `scrubCell` in `cells.ts`, typed
  over its own small surface so it is testable where the DOM is not — and it
  has now caught the same fault twice: after the preview handler came the
  hover **tooltip**, a `title` being an attribute that outlives the
  innerHTML wipe exactly as handler properties do, so a stale one named one
  performer's file over another performer's release.
  The second is time: the dwell and the fetch are together the better part of
  a second, and a scroll inside that window hands the cell to something else.
  So the cell is asked whether it still holds the item — before the sheet is
  fetched and again before it is mounted (`holdsItem`, `cells.ts`). That the
  key *begins with the item's id* is what makes the question answerable, and
  both halves of it — the writing of the key and the reading of it — live in
  `cells.ts` together, since a fact spelled out in two files is one that goes
  wrong quietly. It is also the only pure part of this, so it is where the
  test is: `preview.ts` cannot be imported by the test runner at all, its
  constructor using parameter properties, which strip-only type removal
  refuses.
- `state.Store` (playback positions) lives in the same blob database as
  everything else — thumbnails, metadata, the mirrored index, flags — so
  there is one file to back up and one file to delete for a clean slate. It
  is held in memory (both hot paths want it there: `Get` as playback starts,
  `All` served whole to the client) and flushed debounced, writing only the
  ids that changed since the last flush in one transaction; `Flush()` is
  called on shutdown. With `-db off` there is nowhere to put them, so they
  last the run and are not written down — a JSON file beside the database
  would be the second store this arrangement exists to avoid. Positions are
  the owner's data, not the file's: no `(mtime, size)` stamp, and `Prune`
  drops them only for items the index no longer has.
- The database also carries an **epoch**, minted when the file is created
  (`blob.initEpoch`) and served by `GET /api/info`. The client puts it in
  every thumbnail and sprite URL. Without it, deleting the database changed
  nothing a browser could see: those images are served `immutable` for a
  year and versioned only by the source file's mtime, so the server would
  regenerate everything while the browser went on showing what it had. A new
  database means new URLs and a refetch; the same database means the cache
  still stands, which is what keeps thumbnail loads off the connections
  playback needs. `main.ts` awaits `/api/info` before the first listing,
  because a URL built a moment too early is the old URL.

Docker: multi-stage (go gen → node → go → alpine, see Commands above). The Go
stage outputs to `/out/mediator` because Alpine already has a `/media`
**directory** — building to `/media` silently produced `/media/media` and
broke the COPY, which is why the output has always lived under `/out`; the
binary is `mediator` now, but the rule stands for anything named after a
directory Alpine ships.

The frontend has tests of its own (`web/src/*.test.ts`, run by `npm test` in
the image build). They use node's test runner and strip types on the fly — no
framework, no dependency — and cover the pure predicates that decide which
playback route a viewer takes, against real user-agent strings and file
names. That is where they belong: both have been wrong, and a wrong answer
there is not a cosmetic fault but a file that will not play. Anything pure
enough to test that way should be lifted out of the DOM into `playback.ts`
rather than left inside a class — `playButtonIcon` is the worked example: the
music bar's toggle showed a triangle while music played for as long as the
mapping lived inline, and became untestable-wrong the moment it did.
`query.ts` (the hold-over decision, out of sources.ts, whose classes the
strip-only loader refuses) and `audiopref.ts` (the soundtrack memory, over
the guarded storage helpers in `remember.ts`) are tested the same way. One
mechanical rule: node resolves imports exactly as written, so a module the
tests load may only *runtime*-import with an explicit `.ts` extension
(`allowImportingTsExtensions` is on for tsc); type-only imports erase and
need nothing.

## Server-complete, UI-pending

The hidden/favourite flags (`PUT /api/flags`, the batch form, the
`hidden=`/`fav=` listing filters), the seeded shuffle sort (`sort=random` +
`seed=`) and the m3u export helper (`playlistUrl` in `api.ts`) are complete
and tested on the server and deliberately not yet surfaced in the UI — the
grid has no selection model to hang a cull or a favourite toggle on yet.
They are not leftovers to clean up; the video player already writes two of
the flag fields (rotation, nocrop) through the same endpoint.

## Documentation is part of the change

Every change updates the docs in the same commit — `README.md` for what the
thing does and how it is run (features, flags, endpoints, layout), `CLAUDE.md`
for how it works and why it was built that way. A flag added without a row in
the table, or an endpoint without a line in the API table, is an unfinished
change: the next person to look has no way to know it exists, and neither does
the next session.

## Writing about this project

**Nothing committed may name what the library holds.** That covers every
form a name can take and every place a commit can put one: README.md,
CLAUDE.md, commit messages, tests, code comments, and any artifact —
screenshots, recordings, images, sample media, binary fixtures are never
committed at all, a screenshot of the grid being a page of real names in one
file. In prose, describe the pattern instead ("a language appended to the
video's name", "a multi-part archive set"); where an illustration is worth
more than a description, use an obviously generic invented one.

**Tests are held to the same rule**, and it is not a smaller matter there. A
fixture is committed, shared and searchable exactly as prose is, and a table
of real release names is a record of what somebody's library holds — the very
thing this rule exists to keep out of the repository. So a test invents its
names.

What a test must preserve is the **shape**, which is the whole of what is
being checked: the punctuation, the season marker, the group tag, the byte
that is not valid UTF-8, the accented letter written under the wrong
alphabet. "Harbour.Lights.S01E01.1080p.BluRay.x264-GRP" tests everything the
real name tested and tells nobody anything. Where a case came from a real
file, say what the *shape* is in the comment above it and leave the file out
of it. Keep the measurement, lose the name: "measured over this library,
48 tags use a slash" is knowledge; which tags they were is nobody's.

Three shapes that have slipped through and are worth naming as shapes:

- **A site tag is a name.** Files scraped from the web carry the site in the
  filename, and a fixture holding one records where somebody downloads from
  as surely as a title records what. Invent the site too ("example.com",
  "clipsite.example").
- **A non-Latin fixture is still a name.** A Thai title, a Cyrillic sentence,
  a mojibake pair — each came from a real file, and being unreadable to most
  reviewers makes it *less* likely to be caught, not less identifying.
  Invent a phrase and compute its bytes (`str.encode('tis-620')`,
  `encode('cp1251')`): the encoding property under test survives any text
  written in that alphabet.
- **A famous title is still a title.** A world-known song as the canonical
  example identifies little, and gets copied into the next table anyway,
  where its neighbours will not be famous. Invent from the start.

When one is found, scrub it in place, preserving the byte-level shape the
test pins — and check the whole repository, not the file it was noticed in:
a real name travels between a test, the comment above it, and the CLAUDE.md
paragraph that cites the measurement.

**How to look, and what the last look found.** `make names` runs
`scripts/names.sh`, which lists what in the tracked files has the shape of
a name — personal paths and mounts, hostnames in prose and fixtures outside
the ones the project depends on, release-shaped names and season markers,
text in another alphabet, and every capitalised phrase quoted in a test —
for a reader to judge. It cannot know a name when it sees one, so a hit is a
question and not a verdict: an invented fixture has the shape too, which is
the whole point of a fixture. Run it before committing anything that adds
fixtures or measurements, and read the last section in full; that is where
the names hide. The scrub of 2026-09-05 read all of it and found, past the
earlier scrub, a performer in a year-lifting fixture, a famous song and a
famous album used as fixtures, a famous film used for a trailing year, a
surname in a byte-decoding fixture, a famous title on a personal mount path
in the search fixture, and mojibake pairs that were real Nordic and Russian
titles — each replaced with an invented name of the same shape, the byte the
test pins included. Commit messages since the earlier scrub were read too and
carried none.

## Environment note

`tag.ReadFrom` (dhowden/tag) can panic on corrupt files; all call sites wrap
it behind `recover` (`enrich.go`) — preserve that when adding tag reads.
