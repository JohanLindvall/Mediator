# Listings, versions and counts

## Scale invariants (measured at 150k files; keep them true)

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
  - Every listing order ends with the item id as a tie-breaker (two roots can
    hold files with one display path). The offset is clamped before the limit
    is added, so an oversized one answers an empty page, never a panic.
  - `removePath` drops an indexed file through `byPath`, never a walk of the
    whole index; only a directory or container walks for what lies under it.
- Watch progress is the library's (`watched.go`), since listings filter under
  the index lock: positions are pushed in, reduced to started/finished/neither
  (under five seconds: no start; past `WatchedFraction`: finished, no resume).
  The client reads `START_FLOOR_S`/`WATCHED_FRACTION`, generated from
  `watched.go` by `cmd/gen-ts` and re-exported by `playback.ts`, via one
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
- Album/artist endpoints send
  `ETag: W/"<epoch>-v<group version>.<watch version>-<face>"` +
  `Cache-Control: no-cache` (`versionTag`: the epoch is the process's own, as
  versions start again at every start; the face hashes both restriction
  headers, [restrictions](restrictions.md)); `writeJSON` defaults to no-store
  only without a handler policy. The frontend throttles those reloads to one
  per 2 s in bursts; `LibrarySource` evicts pages beyond 64 so a long scroll
  cannot pin the library.

## Change propagation

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
  (`tracksOfAlbum`), not a `Get` per track.
