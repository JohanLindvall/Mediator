# AGENTS.md

Guidance for coding agents working in this repository. This file holds what
every change needs; how each part works, and why, is in the chapters under
[`docs/`](docs/), listed under [Chapters](#chapters). Read the chapter for the
code you are changing before changing it.

## What this is

A self-contained web media browser: a Go backend (module `github.com/JohanLindvall/Mediator`)
that scans/watches directories and streams video, images and music to a
TypeScript frontend embedded into the binary via `go:embed`. It has no
authentication of its own, deliberately: it is a personal server for a
trusted network, where whoever reaches the port may browse, stream, change
the indexed directories, delete from the disk and play to a television.

## Commands

Every make target builds inside Docker (BuildKit); the host needs only Docker:

```sh
make docker           # runtime image (mediator:latest): gen-ts → npm → go build, all in Docker
make build            # same build; extracts the static linux binary to ./mediator
make generate         # regenerate web/src/types.gen.ts via the Docker gen stage
make test             # frontend tests and build; go vet (Linux, macOS, Windows); go test -race ./... with ffmpeg
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
- `gosrc` copies `main*.go` (`main_test.go` included), `cmd/` and `internal/`
  explicitly so frontend edits keep Go layer caches: **a new top-level Go
  package needs a COPY line**.
- Local `go build`/`go test ./...` must run from the repo root with a current
  `web/dist` (`main.go` embeds `all:web/dist`; build it with
  `cd web && npm run build`).
- `vet` also vets for macOS and Windows, tests included, since nothing runs
  there to notice a break: what only Linux has (`EUCLEAN`, `/proc`, inotify)
  lives in a `_linux.go` file with a `!linux` twin (`fsfault`, `procio`,
  `fswatch`), what only Unix has in a `_unix.go` one (`inode`), and a test
  needing a FIFO is `//go:build unix`.
- The Go stage outputs to `/out/mediator`; never build to a path Alpine ships
  as a directory (`/media` → `/media/media`, breaking the COPY).

## Chapters

How each part works and why, one file per part. Each entry names the code
the chapter covers: read it before changing that code, and change it in the
same commit.

The library (`internal/library`, `internal/blob`, `internal/state`):

- [persistence](docs/persistence.md): the index mirrored in the blob
  database, its prunes and their repair, the state store, the database's
  epoch, the final flushes (`persist.go`, `live.go`, `internal/blob`,
  `internal/state`, the stores in `main.go`).
- [scanning](docs/scanning.md): what counts as media, the walk and the
  watcher, reconciliation, containers, duplicates and sample folders,
  `-exclude`, changing the indexed directories (`scan.go`, `sniff.go`,
  `watch.go`, `fswatch*.go`, `dedup.go`, `inode_*.go`, `server/prefs.go`).
- [listings](docs/listings.md): the scale invariants — the cached listing,
  the two versions, counts and chips, change events (`library.go`,
  `cache.go`, `counts.go`, `annotate.go`, `watched.go`, `mtime.go`,
  `web/src/query.ts`).
- [names](docs/names.md): file names as bytes, text encodings and their
  repair, natural order, search and its ranking (`name.go`, `natural.go`,
  `search.go`).
- [enrichment](docs/enrichment.md): tags, lengths and technical shape read
  from the files, probes and their verdicts (`enrich.go`, `shape.go`,
  `mp4box.go`, `duration.go`).
- [collections](docs/collections.md): releases, discs, performers and
  genres, and what their cards show (`albums.go`, `artists.go`, `genres.go`,
  `web/src/format.ts`).
- [television](docs/television.md): shows and seasons read from names,
  intros and credits found in the sound (`series.go`, `fingerprint.go`,
  `skipdetect.go`, `server/skips.go`, `web/src/skips.ts`).
- [music](docs/music.md): plays, verdicts and owner flags; how music sounds
  (analysis, tempo, similarity, speech, audiobooks); radio and artist radio
  (`plays.go`, `likes.go`, `ownercounts.go`, `analyze.go`, `features.go`,
  `tempo.go`, `similar.go`, `spoken.go`, `station.go`).
- [archives](docs/archives.md): rar and zip sets, unpacking, DVD-Video and
  the loopback input (`rar.go`, `archive.go`, `zipset.go`, `pack.go`,
  `unpack.go`, `openitem.go`, `disc.go`, `ps.go`, `loopback.go`).

The server (`internal/server`, `internal/dlna`, `main.go`):

- [serving](docs/serving.md): build info, the listener, request handling
  and its limits, streaming, shutdown, the logs, static files and downloads
  (`server.go`, `http.go`, `about.go`, `access.go`, `clientlog.go`,
  `static.go`, `albumzip.go`, `internal/browse`).
- [restrictions](docs/restrictions.md): faces (`X-Media-Content`) and path
  restrictions (`X-Allowed-Paths`), where they are enforced and keyed
  (`content.go`, `library/paths.go`).
- [conversion](docs/conversion.md): how a file reaches the element — live
  conversion, the graphics hardware, the bitrate ladder, the page-fed
  stream, reordering, soundtracks, seeking (`convert.go`, `hwaccel.go`,
  `hdr.go`, `reorder.go`, `deinterlace.go`, `keyframe.go`, `timeparam.go`,
  `fsfault_*.go`, `web/src/playback.ts`, `video.ts`, `mse.ts`).
- [rewrap](docs/rewrap.md): copies into a container the browser opens,
  sound fixes and soundtrack copies, the scratch space (`remux.go`,
  `scratch.go`).
- [hls](docs/hls.md): the segmented conversion Safari plays (`hls.go`,
  `hlstable.go`, `hlssubs.go`, `library/keyframes.go`).
- [subtitles](docs/subtitles.md): sidecars, embedded captions, WebVTT and
  shifted cues (`library/subs.go`, `subtitles.go`, `vtt.go`, `embsubs.go`,
  `boundedbuffer.go`, `procio_*.go`).
- [thumbnails](docs/thumbnails.md): tiles, scrub sheets, hover previews and
  seek-bar frames (`thumbs.go`, `aspect.go`, `exif.go`, `seekframe.go`,
  `web/src/preview.ts`, `seekframe.ts`, `cells.ts`).
- [receivers](docs/receivers.md): AirPlay, DLNA casting and signed media
  URLs (`internal/dlna`, `cast.go`, `sign.go`, `web/src/airplay.ts`,
  `cast.ts`, `casting.ts`).
- [deletion](docs/deletion.md): deleting from disk (`library/delete*.go`,
  `server/delete.go`, `web/src/deleting.ts`, `deletedialog.ts`).

The page (`web/src`, no framework, no runtime dependencies):

- [grid](docs/grid.md): the virtual grid, its sources, cells, hovers and
  thumbnail loading (`grid.ts`, `sources.ts`, `thumbs.ts`, `trackcard.ts`,
  `format.ts`).
- [views](docs/views.md): views and sorting, the address and shortlinks, the
  chrome, and what the server offers that the page does not yet (`main.ts`,
  `sorts.ts`, `content.ts`, `links.ts`, `barhide.ts`, `scrollhold.ts`,
  `prefs.ts`, `style.css`, `server/links.go`).
- [player](docs/player.md): the video player and the picture viewer —
  starting a file, borders, shape, swipes (`video.ts`, `lightbox.ts`,
  `swipe.ts`, `viewport.ts`, `modal.ts`, `server/crop.go`).
- [music-bar](docs/music-bar.md): the music bar, its queue and decks, fades,
  queue-all and media keys (`audio.ts`, `audiodecks.ts`, `queue.ts`,
  `cover.ts`, `mediakeys.ts`, `nowplaying.ts`, `library/tracks.go`).
- [spectrum](docs/spectrum.md): the spectrum analyser (`visualizer.ts`).

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
derives from `library.AllKinds`. The watch thresholds (`START_FLOOR_S`,
`WATCHED_FRACTION`) come from `watched.go` the same way.

## Background work

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
it on its own context, not the first asker's.

**Turns, busy gates and locks are always released by `defer`** (a turn is
`Server.owning`'s, [music](docs/music.md)), in a closure so the turn returns
before the answer is written: net/http recovers panics, so a plain release
leaks for the process's life. A leaked stream count reads as playback for
ever, throttling all background work; a stuck stripe wedges every write
hashing there.

## Tests

`make test` runs the frontend's tests and build, `go vet` for three
platforms and `go test -race ./...` with ffmpeg present, all inside the image
build. Coverage is best run on the host separately: race and coverage
instrumentation together make the audio fingerprint tests far slower. The
root package's startup tests (`main_test.go`) run the real listener and
cancel through `run`'s context.

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

## Documentation is part of the change

Every change updates, in the same commit, `README.md` (features, flags,
endpoints, layout) and the chapter under `docs/` that covers it (how it works
and why), or this file where the rule holds everywhere; a new part gets a
chapter and a line under [Chapters](#chapters). A flag without its table row,
or an endpoint without its API-table line, is unfinished: nobody would know
it exists. The docs say what the code does now, and why, in the present
tense; how a rule came to be, what an earlier version did and how a fault was
found belong in the commit message.

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
repository: a name travels between a test, its comment and the `docs/`
paragraph citing the measurement. `make names` (`scripts/names.sh`) lists
name-shaped text in tracked files — personal paths and mounts, hostnames
beyond the project's own dependencies, release-shaped names and season
markers, non-Latin text, every capitalised phrase quoted in a test — as
questions for a reader, not verdicts (invented fixtures share the shape). Run
it before committing fixtures or measurements, and read its last section in
full: that is where names hide.
