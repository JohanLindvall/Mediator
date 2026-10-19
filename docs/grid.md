# The grid and its sources

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
  - `LibrarySource` and `CollectionSource` cancel superseded requests and
    ignore their answers: a newer page generation retires the older one's.
    An answer whose version is below the one its request started at means
    the server restarted, and is taken; an overtaken answer is dropped, and
    an explicit refresh resets the comparison. Page zero is refetched even
    where the previous total was zero. A failed load clears its placeholders
    and stays visible until a retry (redraws retry at most every five
    seconds); a failed first load shows an error and a retry control, never
    an empty library. Tests drive the sources themselves with deferred
    answers.
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
- The performer's name under a release is its own click target
  (`.link-artist`, `data-artist`), read by `viaLink` as the play badge is by
  `viaPlayBadge`: one delegated click per cell, meaning set by where it lands.
  **Every** adapter drawing one must check it, track cells included, or the
  click falls through and queues the whole listing. It is styled as caption
  until pointed at; the names that are links, and what they open, are in
  [collections](collections.md).
