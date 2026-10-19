# Thumbnails, scrub sheets and seek frames

- Thumbnails (`thumbs.go`): lazy, cached in the bbolt blob db
  (`internal/blob`, default `<data>/media.db`, `-db` to move, `off` to
  disable; it also holds enrichment metadata), keyed `(id, width)` with the
  source mtime+size in the value, so a changed file overwrites its entries;
  with no db (`NewThumbnailer` given a nil store) every request regenerates.
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
  - Archived members get scrub sheets too: **priority gates a `hover=1` sheet,
    not the container** — it stands down while anything streams
    (`Library.Streaming`); a stored sheet is always served, and the client
    does not remember a refusal.
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
    owner's shape, else the element's, else the library's. Centre the frame
    and turn it about its centre like `place`: a clipping box pins a turned image's
    overflow to its start edge.
  - Hovering pointers only; touch previews only while dragging. A DVD title,
    read by position (`library.SeekByte`), gets a frame near the moment;
    pipe-only content none.
  - `TestTheFrameIsTheMomentsToTheFrame` keeps its encoder off scene-cut
    keyframes and its tolerance under the three frames a non-accurate seek
    misses by, or it cannot tell them apart.
