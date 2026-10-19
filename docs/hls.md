# HLS: the segmented conversion

`/api/hls/{id}/index.m3u8?t=&mode=` is the same conversion as segments,
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
- A run from the start begins at the picture's first frame, past nought
  where the sound leads it (an MP4 whose picture track opens on an empty
  edit). Forced keyframes and the segment muxer's cuts both count from
  that frame (the muxer adds `reference_stream_first_pts`), so
  `pictureStart` reads it (one packet, `firstPicture`) and both are
  offset: `gridKeyframesFrom` via `keyframesFrom`, and a
  `-segment_time_delta` taking it back off (`hlsTableArgs`). A picture
  beginning before nought needs nothing (`make_non_negative` shifts the
  whole run); one beginning a segment or more late still fails, its first
  segments holding no picture to cut at.
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
  unique as written (players merge duplicates), a natural name colliding
  with a generated suffix included, and every label is a single
  quoted-string attribute. Each is one unsegmented WebVTT rebased by the
  `?shift=` arithmetic; an adopted session reads identity and start from its
  key (`hlsSessionItem`), keeping captions across a restart.
- **Every request under a session resolves the item afresh** for its caller
  (face and paths, [restrictions](restrictions.md)) and matches its id to the
  session's: neither a live session's snapshot nor a restored session's key
  grants access.
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
