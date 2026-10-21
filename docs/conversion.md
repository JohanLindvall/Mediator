# Playback routes and conversion

## Before converting

- **A file that never starts is diagnosed before it is converted.** A dead
  disk (EIO on every open, mount kept) fails every route at `OpenItem`, so
  `serveStream` answers **503** for a known item it cannot open (404 means "no
  such item"). The pipe, when ffmpeg produced nothing, asks the disk the same
  question (`conversionFailed`, `readsAtAll`): 503 with the reason where the
  file will not be read, else 500, never an empty 200 — the player words a
  503 as a file it cannot read, which said of a film ffmpeg failed to convert
  sends its owner to a healthy disk. On the element's first error the player
  fetches one byte and reads the status (`readFault`, tested), matching **by
  id** since `refreshItem` replaces the item object when `/api/item` answers;
  the fault is drawn in the picture and stays (`giveUp`), and the file stays
  given up (`faulted`) so late codecs start no conversion. The 503 states the
  reason for the viewer
  (`openFault`, tested: dead disk vs a filesystem needing repair, e.g. XFS
  "structure needs cleaning"); other answers proceed to rewrap and
  conversion. The index keeps such files (an unreadable root is protected
  from reconciliation).

## Converting

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
    refuses to retry once a segment exists that waiters may be reading
    ([hls](hls.md)).
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
  - Off-ladder rates are refused (`parseQuality`). The rung is the last part
    of `hlsKey`, so the start stays the fourth field `hlsSessionItem` reads;
    the master `BANDWIDTH` is rung plus soundtrack. The choice lasts the
    session (`chosenKbps`, `video.ts`), never persisted (the link is the
    day's), and is disabled while a set plays (it fetches the file itself).
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
  - `FedSource` cancels and releases its response reader on every exit. A
    SourceBuffer error rejects the appends pending; stopping during an append
    settles its waiter without reporting a playback failure. The box and
    codec parsing refuses short or overflowing records, and the
    initialisation buffer is bounded.
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
- **How much to convert turns on whether the picture is *proven*, not on
  what it is called** (`convertMode` in `playback.ts`, tested); its two
  callers mean opposite things, so never merge them. `trackMode` (playing,
  so decoded) converts only on a definite no, a codec with no type string
  being proven by its frames; `convertForContainer` (container refused,
  nothing decoded) copies only on a definite yes — `wmv2` (`decodesVideo` →
  `null`) copied into an MP4 yields nothing. A codec-name test
  (`vcodec === 'h264'`) fails VP9 in a refused container. Which browsers wait
  for a rewrap first is in [rewrap](rewrap.md).

## One plan for every converter

- `convertInput` (`remux.go`) decides input and seek together for all three
  converters (`-ss` only where position did not seek); `planConversion`
  (`convert.go`) is all the pipe and the segmented converter share (the seek
  with `-copyts`, hardware, picture, soundtrack) — keep it one tested
  function, as copies drift. `timeSeekInput` refuses a title read by
  position, so `handleKeyframe` answers the time asked for.
- **Every handler that serves an opening probes the same way** (`probed`,
  `server.go`): soundtracks, embedded captions and codecs come from the
  `EnsureCodecs` probe for the subtitle listing, cast, rewrap and HLS start
  alike; a television's links come from `mediaURL` (our address on the
  set's network, signed where there is a key).
- **Interlaced video is deinterlaced wherever a picture is made**
  (`deinterlace.go`; browsers do not separate fields and a copy cannot): both
  converters, thumbnails, the piped fallback and sprite frames use
  `videoFilter` — `bwdif` with **`deint=interlaced`**, so it touches only
  flagged frames and is safe on every file; **before any scale** (scaling
  merges the fields for good; test-pinned); **`send_frame` stated
  explicitly**, the default `send_field` doubling every re-encode's frame
  rate. `crop.go` has none: bars are black in both fields.
- **The film's picture is ffmpeg's `V:0`, never `v:0`** (`pictureMap`,
  `pictureStream`, `convert.go`): a cover the file carries (a WMV's embedded
  picture, an MP4's artwork, a Matroska attachment) is a video stream to
  ffmpeg, and a WMV lists it ahead of its film. Taken for the film, it was
  every still, and every conversion failed before writing a byte (the
  re-encoded cover keeps its attached-picture mark, and an MP4 stores a cover
  only as JPEG, PNG or BMP: "Could not find tag for codec h264"). Conversions,
  rewraps, landings, keyframes, the aspect repair, the reorder look and every
  still map it, and the probe passes covers over too
  ([enrichment](enrichment.md)). Crop detection maps nothing (ffmpeg's own
  choice ranks covers last); a cover is a tile only where no film is there
  ([thumbnails](thumbnails.md)). `mediatest.CoverFirst` writes the shape for
  tests: an MP4 with its artwork box moved ahead of the tracks (ffmpeg's ASF
  writer keeps no cover, and its MP4 writer puts one last).

## Soundtracks

- **Which soundtrack** is the viewer's choice, not the browser's. Tracks
  (`Item.Tracks`) come from the `EnsureCodecs` ffprobe on opening, guarded
  by `probed` alone: known codecs must not skip it, the tracks being
  persisted nowhere. A choice is served with `-map 0:a:<n>` (a browser
  cannot be told which stream to decode), so the HLS key and the rewrap's
  file name carry the track. `pickAudioTrack` (`playback.ts`, tested): this
  film's remembered choice, the last choice's language, the file's default,
  the first; a commentary only if alone. A remembered index must be a
  nonnegative safe integer, blank storage meaning no preference
  (`audiopref.ts`).
  - **The pick is applied, not only marked** (`audioTrack`): `load` builds
    menu and pick before any route starts a stream (else `useKnownCodecs`
    starts the first track). `applyAudioChoice` goes cheapest first: the
    file's default or the element's `audioTracks` (Safari), then the rewrap
    where worth the wait (`remuxUrl` carries `?a=`), else a conversion (mode
    by `trackMode`). `appliedTrack` records what each source carries,
    keeping this idempotent. `a` cycles the menu, as `c` does subtitles.

## Seeking

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
  keyframe granularity with an honest clock is the contract.
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
