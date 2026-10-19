# The player and the picture viewer

- Overlays (`video.ts`, `lightbox.ts`, `albumpanel.ts`) are self-contained
  classes mounting into `#overlays`, owning their document-level key handlers,
  cleaning up on close. `modal.ts` owns dialog focus and native-control key
  handling, `scrollhold.ts` the shared scroll lock ([views](views.md)); an
  overlay acquires and releases each once.
- **A new file starts where *it* was left**: until the element holds its
  source, `video.currentTime` is the previous file's, and a container the
  browser will not open never reaches the element, so rewraps and conversions
  must not ask it. `load` computes `startAt` from the file's resume record,
  `sourced` says whether the element shows this file, `switchAt` picks for
  every source change. A source with no start position is told 0
  (`sourceStart`, tested), as a `currentTime` set before metadata survives a
  source change until metadata consumes it.
- `resumeStart` (`playback.ts`, tested): no resume in the first few seconds or
  at/past the end, judged by the record's stored length or else the library's.
- Player and picture viewer step through the listing they came from
  (`ItemSource` + `findKind`, `sources.ts`) — swipes, and a video's end —
  within their own kind; the player switches file in place (closing drops
  fullscreen). Subtitles and soundtracks are listed on every route,
  conversions included (a pick unlike the running conversion costs a reopen).
  `load()` is the one place per-file state is cleared, `startSource` the one
  place the element gets a source (file, rewrap, sound-fix file, conversion),
  carrying seek, track and the decode-check settle so no route forgets it;
  volume, speed and subtitle language are the viewer's. Rotation is the
  file's: `load` restores it from `library.Flags` (blob db, so devices agree);
  the picture viewer's is a viewing aid, not persisted (EXIF already uprights
  photos). In the picture viewer the first load, a step and a swipe each
  supersede the navigation before them, and a stale preload starts no image
  request.
- **Black borders in the file** (`crop.go`, `/api/crop/{id}`): `cropdetect` at
  four points across the film, never the opening (fades, title cards), samples
  unioned, not averaged (dark scenes detect small; trim less).
  - Stored in the blob db stamped with mtime and size, empty boxes too, but
    only measured ones: `detectCrop` returns `(box, measured)`, as an empty
    box means either and the key never changes. No ffmpeg, no duration,
    archived without loopback, every sample failing, or an interrupted run is
    unmeasured; a sample counts only where `cropdetect` printed a box. A
    record goes with its item in the prune ([persistence](persistence.md)).
  - The handler runs `probed` (the player asks before the item request reads
    the length) only where `it.Duration <= 0`: `EnsureCodecs` does not dedupe
    in flight and `probeSem` holds two, so probing unconditionally doubles
    ffprobe per open.
  - Per-file dedup hands waiters the answer (box and `ok`, set under the mutex
    before the channel closes), not a bare close, or waiters re-run the seeks
    when storing fails (`-db off`, failed `PutCrop`); a not-ok leader is not
    inherited. Seeks take the thumbnailer's slot; the run marks
    `Library.StartStream` (a viewer waits on it); frame size comes from the
    header reading.
  - Applied as a scale about the element's centre, composed with the turn and
    the shape (`place`), element clipped; `cropScale` refuses off-centre boxes and near-total trims
    and stops at the first edge reached, so only the smaller border goes.
    Geometry uses `video.videoWidth/videoHeight`, detection as fractions
    (coded pixels over-scale an anamorphic DVD by a fifth), redone on
    `loadedmetadata`. It answers for this screen: the button shows only where
    the window gains; `Flags.NoCrop` keeps borders (a 2.39:1 film has them on
    purpose).
- **A picture's shape is the owner's to correct** (`Flags.Aspect`, the
  player's shape menu; `shapeRatio`, `shapeChoices`, `fitShape` in
  `playback.ts`, tested): a widescreen title converted into a 640×480 frame
  declares square pixels in stream and container alike, so every player
  shows it squeezed and nothing in the file says otherwise — never guessed
  from the picture. Kept per file with the owner's flags, as the turn is
  (`FlagChange`; `ValidAspect` takes "W:H" in whole numbers to 99, neither
  side over four times the other, else 400), and stamped on items. The menu
  offers the file's own shape (the element's metadata; the library's coded
  size is wrong for a DVD) and each of `PICTURE_SHAPES` (4:3, 16:9) the file
  is not. `place` (`video.ts`) draws element and poster alike: a reshaped
  picture is centred at the largest box of its shape with `object-fit: fill`
  and turned like a quarter-turned one; the trim measures it at that shape
  (`cropScaleNow`), the seek preview takes the shape (`reshaped` fills its
  box) and the drag layer draws the frame stretched. A television plays the
  file itself, which the shape cannot reach.
- `swipe.ts` defines gestures for both viewers: one `SlideDeck` drag
  (neighbours resolved while down, damped travel, layer home before the file
  changes), differing only in `arriveEarly` (the picture viewer renders under
  the layer; the player shows its poster, then opens). `watchDrag` follows
  drags (player vertical, viewer sideways); `watchSwipes` catches flicks with
  no touchmove, standing down on `ev.defaultPrevented` (no double step).
  `ignore` disowns a gesture by where it starts (seek bar, zoomed picture). A
  recognised gesture `preventDefault`s the touchend, the only way to cancel
  the synthesised click.
- A file switch is covered by the **poster** (`.vo-poster`: incoming thumbnail
  from `load()` to `loadeddata`, turned and shaped as the item is by `place`,
  a thumbnail being the file as stored — the neighbour's mid-swipe; the
  picture during a cast) and the **slides** (`.vo-slides`, `.lb-slides`:
  current frame on a canvas, neighbour thumbnails either side). On release:
  layer home, poster up with the same picture, then layer down — that order
  hides the outgoing file. Release goes to the neighbour resolved during the
  drag, never a new search; before that answers, the plain step.
- Viewers sit on the **visible** viewport (`viewport.ts` →
  `--app-vw/-vh/-top/-left`), as iOS lays fixed elements on the layout
  viewport (taller under Safari's bars, wider after a pinch); the app is
  hidden while one is open (`body.viewing`), or the grid shows around it. The
  player is `touch-action: none`, every gesture over it being the player's (a
  double tap is fullscreen, never zoom); the picture viewer keeps
  `manipulation` for browser panning.
- The player's control row wraps, so `keepMenuOnScreen` measures each menu
  (anchored to its button's right edge) as it opens and slides it on screen.
- Every await in the player is guarded by the film's id (`refreshItem`,
  `loadSubs`), or a swipe mid-request repoints it; `load` shares one
  in-flight item request per file; `menuShift` (`playback.ts`, tested) places
  menus against the visible viewport.
- The seek bar follows the finger and commits once, on release; faults offer
  "Try again" (a disk that comes back needs no reopen).
