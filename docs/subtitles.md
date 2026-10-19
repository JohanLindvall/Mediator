# Subtitles

- `/api/subs/{id}` lists sidecars (`subs.go`: subtitle extensions on the
  video's stem in its directory; label and language from the rest of the
  name); `/api/subs/{id}/{n}` serves one as WebVTT (`vtt.go`: BOMs, UTF-16,
  Latin-1). Sidecars are attachments, never items: kept in `subsByDir`,
  reconciled by scan and watcher.
- A subtitle is bounded to 16 MiB (`subtitleMaxBytes`) before conversion,
  sidecar or extracted, ffmpeg's stdout included; a bounded writer
  (`boundedBuffer`) has no `ReadFrom` for `io.Copy` to bypass `Write`'s limit
  with. A malformed format line never panics: every ASS column index is
  validated and the Events section honoured, and SRT timestamps are
  normalised to WebVTT's. The answer's ETag hashes the converted bytes.
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
- Cues are absolute while a conversion's clock starts at its keyframe:
  `/api/subs/{id}/{n}?shift=SECONDS` rebases them (`shiftVTT`) and each reopen
  **replaces** every `<track>` (`retimeSubtitles`); a re-pointed track keeps
  its old cues until the new file loads. `hideSubtitles` sets tracks to
  `disabled` across every source change (in `startSource` and before a new
  file's reset; restored on the newest source's `loadeddata`): Chromium keeps
  painting the last cue through the load, and `disabled` clears it at once.
