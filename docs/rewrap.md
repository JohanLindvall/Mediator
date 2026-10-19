# Rewraps and the scratch space

- `/api/remux/{id}` rewraps (`-c copy` into a faststart MP4, no frame
  touched) where `remuxable` (`remux.go`) finds: a container the browser won't
  open around decodable streams (a short allowlist — HEVC moved into MP4 only
  meets a browser that cannot decode it); HEVC tagged `hev1`, which ffmpeg
  writes unless told while Apple takes only `hvc1` (copy with `-tag:v hvc1`);
  or `moov` after `mdat`, as phones record — not progressive, so Safari hunts
  through it, fetching many times the file; `+faststart` cures it
  (`wantsFaststart` in `playback.ts`, from the listing or `useKnownCodecs`;
  the wait bounded as for any rewrap).
  - The case is read from the sample entry's four-character code
    (`library.VideoSampleFormat`, `mp4box.go`) in pure Go via `OpenItem` (rar
    members too), not ffprobe.
  - A decode failure asks for the rewrap only where `canPlayType` has HEVC
    (`decodesHEVC`), else a copy is waited for in vain. The re-armed decode
    check waits for some *playback* (`checkAt`) — the frame counter restarts at
    zero and an early check would re-encode a file about to play; a resume
    counts from the file's real start.
  - 404 means "rewrapping would not help" → converter; nothing is asked first.
- **A soundtrack conversion becomes a file while it is being watched**
  (`?mode=audio` on `/api/remux/{id}`, `remuxSound`, `upgradeToSoundFix` in
  `video.ts`) — the commonest case, a decodable picture with an undecodable
  soundtrack. The rangeless pipe re-sends from byte zero on each reconnect,
  waste that grows with position and no pacing or buffering removes; the file
  is made behind playback (4K, 22 min: 2 s copy + 37 s encode).
  - `-aac_coder fast` here only, being the whole wait (37 s vs 61 s); the
    live conversion keeps the default.
  - The kind is in key and file name (`-aac`), or a plain copy can be served
    as the sound-fixed one and the film is silent; plain copies carry no
    marker, so any name without one adopts as a plain copy.
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
- **Safari asks for the rewrap too**, up to `REWRAP_WAIT_LIMIT` = 2 GiB
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
