# Reading files: shape and enrichment

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
  - The box readers (`mp4box.go`; Matroska's in `keyframes.go`) keep every
    child within its parent's header and payload bounds, checking a length by
    subtraction before adding it to an offset; sample dimensions and clocks
    come from their own records, never adjacent bytes. The synthetic fuzz
    target (`FuzzMediaBoxBounds`) stays beside the parser's regression tests.
- Enrichment results persist in `meta` (id + mtime/size). `duration.go` reads
  mp4/m4a/mov, mp3, flac, ogg/opus and wav headers in pure Go; one optional
  ffprobe adds video codecs and missing durations. Archived video is probed
  over the loopback URL (`probeItem`), which can range to an index at the
  file's end that no piped prefix holds. `ProbeMedia` takes the caller's
  context, never a background one (`EnrichNow`/`EnrichSoon` call it from
  waiting requests).
- Natively timed videos get no eager ffprobe; `EnsureCodecs` probes on open,
  once per item per process. `Probe.Probed` means ffprobe answered
  (`ffprobeResult.answered`: ran, printed a parseable document), never "found
  a duration" (many containers never do) or "tried" (no ffprobe, unreadable
  member, killed run). It takes the caller's context and a `probeSem` slot,
  deliberately not waiting for playback (the player needs the answer). Nothing
  is recorded on context expiry, ffprobe's own ceiling (`Probe.Interrupted`),
  or a changed identity.
  - The document is stdout alone: stderr's diagnostics differ by tool version
    and never count as stream records. Stderr is read only for the not-media
    phrases below (`unreadableInput`).
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
    (`judged`, in memory, reset with the content): only a verdict or a
    length is written down, so an examined record holding neither would
    otherwise never be asked. Films are left listed: the player says what is
    wrong with one.
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
- Enrichment buffers results (`queueMeta`) for bulk writes; a commit+fsync per
  track caps cold passes at a few hundred items/s (135k: 10m42s vs ~20s).
- `tag.ReadFrom` (dhowden/tag) can panic on corrupt files; every call site
  wraps it in `recover` (`enrich.go`) — keep that for new tag reads.
