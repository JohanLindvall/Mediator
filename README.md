# Mediator

A fast, self-contained web media browser: point it at your directories and get
a slick, responsive UI for your videos, photos and music — served from a single
Go binary with the TypeScript frontend embedded.

## Features

- **Play on a TV, the way that actually reaches one** — the button in the
  player *and in the music bar* lists the DLNA renderers on the network,
  which is what a smart television is. Choosing one hands the **server** the
  job: it tells the set what to play over SOAP, the set fetches the file from
  this machine's LAN address and decodes it itself, and the player becomes a
  remote control — seek, pause, volume, and the position saved as it goes. A
  queue sent to a set keeps going there: where the renderer allows it the
  next track is handed over *while the current one plays*, so the boundary
  costs nothing, and where it does not the next is sent when the last is seen
  to end. The
  spectrum is dimmed while it plays there — it draws what *this* page is
  playing, and this page is playing nothing. What plays is the
  file: no conversion, no re-encode, no browser in the path, so a 4K HEVC
  release plays in HDR on a set that can decode it, which no browser route
  can offer. It also works from a phone that is nowhere near either the
  server or the television, since only the instructions come from the page.
  See [Playing to a television](#playing-to-a-television).
- **AirPlay and Cast** — the same button also carries the browser's own
  receivers: a button appears in the player and in the music bar
  once a receiver that can play *that* is on the network — a speaker is one
  for music and not for a film — and films that need converting play on the
  television as readily as ones that do not. It is AirPlay where Safari
  offers it and the Remote Playback API where Chrome does; both hand the
  receiver a URL rather than a picture, which is why the quality is the
  file's own and not a re-encoded copy of a browser tab. A television Chrome
  describes as *available for specific video sites* is not a target for
  either: it was found over DIAL, which can launch a handful of named apps
  there and nothing else. The
  receiver fetches the media **itself** and sends no credentials, so media
  URLs carry a signed token and the proxy is told to let that path through —
  see [Signed media URLs](#signed-media-urls), which is the one piece of
  configuration this needs. A film in a codec the television is unlikely to
  have — AV1, most often — is converted the moment playback moves there,
  since otherwise it arrives as sound with a black screen behind it. Older sets that cannot agree with a modern
  certificate will still sit on a spinner; that part is theirs.
- **Hover a film and it moves** — the pointer resting on a tile plays ten
  frames from across the film in place of the still, a five-second tour of
  what is in it. It is the same sheet the seek bar scrubs with, so it costs
  one small image and no stream at all; the play badge stays on top of it,
  the still comes back when the pointer leaves, and a touch screen — which
  has no hover — is left alone.
- **Video player** — custom controls: play/pause, seek with buffer display,
  ±10s skip, volume/mute, playback speed, rotation for sideways footage
  (`r`/`R`, remembered per file and shared across devices, since a clip shot
  sideways is sideways everywhere), trimming of black borders that are baked
  into the file — a portrait clip padded out to a landscape frame fills the
  screen on a phone instead of sitting in the middle of it, found
  automatically and remembered per file if you would rather keep them —
  picture-in-picture, fullscreen (`f`), keyboard shortcuts
  (`space`/`k` play, `←`/`→` or `j`/`l` skip, `home`/`end`, `↑`/`↓` volume,
  `f` fullscreen, `m` mute, `c` to cycle subtitles, `a` to cycle
  soundtracks, `r`/`shift+r` to turn the picture, `?` for the list, `esc`). The controls and the pointer withdraw
  after a few seconds of quiet and return on any movement or keypress.
  Playback position is saved server-side, so videos resume where you left
  off — progress bars and watched checkmarks appear on the grid. Drag up or
  down to move between videos: the picture follows your finger, with the
  frame you are leaving and the one you are reaching either side of it, and
  a finished file rolls on to the next by itself.
- **Image lightbox** — full-resolution viewing, fullscreen (`f`), arrow-key
  navigation, neighbour preloading, double-click zoom, rotation (`r`/`R`),
  download. Drag sideways to move between pictures — both are on screen
  during the drag — and flick down to dismiss.
- **Music** — mp3/flac/m4a/ogg files are tagged (ID3 & friends, read in the
  background) and grouped into **albums** by directory; `.m3u`/`.m3u8`
  playlists become albums too. A persistent bottom player provides
  queue/shuffle/repeat, a queue panel, and OS media-key integration, a thumb up and a thumb down for the playing track, radio, the spectrum
  and a link to the track — the last three under a "⋯" menu on a phone, so
  the title keeps its room —
  play/pause, stop (which pauses where it is, and resumes when pressed
  again), previous and next. An album
  or playlist opens as a sheet listing its tracks, numbered once rather than
  twice — the "01." a file carries in its name is dropped, unless the digits
  are the title; because the sheet covers
  the bottom bar, it carries its own transport: the button pauses and resumes
  the queue that sheet started, and otherwise plays the album from the top.
  Whichever track is loaded is marked in the list, wherever it was started
  from, and tapping a track starts the album at that track. The play badge
  on an album or playlist card starts it without opening it. Tracks are
  crossed without a gap: the next one buffers in a second element while the
  current plays, so the boundary is a change of deck rather than a fresh
  request — and the preloading stands aside whenever the track being
  listened to still has fetching left to do. The sleeve is fetched ahead in
  the same way, so it changes with the track; and another release's is never
  left standing under a new title while the next one loads. A spectrum view draws what is
  sounding as a quarter-octave analyser: 36 bands spaced by musical interval
  from 30 Hz to 16 kHz rather than by arithmetic, tilted 3 dB per octave so a
  well-mastered track reads roughly flat instead of sloping into the floor,
  with peak caps that hang and then fall — the gap between a bar and its cap
  is how hard that hit was. It is built to be watched: a neon bloom over the
  bars, a glass-floor reflection under them, a stage light that breathes with
  the loudness, colour that streams through the bars with the music's energy,
  crowns that spark on a transient — and a beat detector on the bass that
  slams the light on every kick, so the panel lands on the beat rather than
  describing it. The whole display keeps time with the music: how busy the
  bands are sets how fast everything falls, so blast beats read as distinct
  hits instead of a wall, and a ballad keeps its slow decay. All of it is
  driven by the sound alone: silence is still. Silence is a designed state: a dim row of pills,
  and nothing moving. It reads a **copy** of what is playing wherever the
  browser can give one, so opening it can never take the sound away and the
  file keeps its own AirPlay route; only a browser with no way to copy has
  its output moved into the graph, which is where that cost is paid and
  where the panel warns before it is pressed. Where a browser will not pass
  the audio to the page at all — Safari on a phone does this for video,
  playing the sound perfectly well while giving the page nothing to read,
  though it hands over a music track's audio quite happily — the panel says
  so rather than sitting empty, and the button is not offered again for the
  rest of the session. That is measured while something is actually playing,
  and withdrawn the moment any sound arrives. The browser tab says
  what is playing — the video if one is open, otherwise the track.
- **Metadata** — tags (title/artist/album/genre/track/year) and playing
  time are read in the background (pure-Go header parsing for mp4/m4a/mov,
  mp3, flac, ogg/opus and wav; `ffprobe` for the rest when available) and
  persisted in the blob database, so restarts don't re-read the files.
  What you open is read first — the page of the grid you are looking at,
  the album you just opened, the file you started playing — so metadata
  appears where your attention is instead of in scan order.
  **Hovering a tile says what the file is**: the codecs, the picture's size
  and frame rate for a film, the dimensions of a photograph, the format and
  average bitrate of a track. Those are read from the file's own header — a
  still's from the few hundred bytes that describe it, a film's from its box
  tree — rather than by running a probe per file across the whole library,
  and they are written to the database so they are read once. The containers
  with no reader of their own here (Matroska, AVI, the transport streams) do
  cost an `ffprobe`, but once each rather than per page, and the result is
  kept with the rest.
  Album and track views prefer tags over the filesystem: ID3 titles instead
  of filenames, tag track numbers, per-track and total playing time instead
  of byte sizes, and the album's year and genre in its header. Anything not
  tagged falls back to the filename and size. A release that carries **no
  tags at all** is filed under the directory above it — music is kept as
  performer, then release, then tracks, so the answer is usually sitting one
  level up — but only where that name is a performer the library already
  knows from tags elsewhere. That keeps folders called "complete" or
  "EP, Single, Demo" from becoming artists, which is what a folder above an
  untagged release turned out to be more often than not. Panels stay live — metadata
  appears as background enrichment reads it, without reopening. Tags are
  normalised where they enter the index: trimmed of whitespace and of the
  byte-order mark a UTF-8 file leaves at the front of a value, and a year
  frame holding a whole date is cut back to the year, so sorting by year
  does not put a release from the year twenty million at the top.
- **Shortlinks** — a button on the top bar, in the player, in the picture
  viewer, on the music bar and in the album sheet makes a short address for
  exactly what is on screen: a performer, a genre, a programme and season, a
  search, one film, one photograph, one track, or one release. It is
  `https://your-host/s/k7m2qp4`, short enough to paste into a message or read
  aloud — the alphabet leaves out the characters that get read wrongly (i, l,
  o, 0, 1). On a phone the button opens the system share sheet; elsewhere it
  copies to the clipboard, and where the browser allows neither (a page served
  over plain http to something that is not localhost) it shows the address to
  be copied by hand.
  What is stored is the address the app already keeps in the URL fragment, so
  a link to one film also carries the listing it was made from — closing the
  film leaves the recipient somewhere rather than nowhere. Asking twice for a
  link to the same place gives the same link back. They do not expire;
  deleting the database invalidates all of them, exactly as it does signed
  media URLs and thumbnails.
  **A link belongs to the hostname it was made under.** One server answers
  under several names — a face of music, a face of films, the whole library —
  and a code offered to a different one is a code nobody minted, so it opens
  that library rather than crossing between them. The same view on two faces
  is therefore two links. This needs the proxy to pass the name the visitor
  asked for (`proxy_set_header Host $host`, or `X-Forwarded-Host`): without
  it nginx sends the upstream address and every face looks like one host.
  A shortlink is a name for somewhere in the library, **not a key to it**:
  there is no authentication on one, as there is none on anything else here,
  and it grants a visitor nothing that reaching the address would not. Opened
  on a face that cannot show what it names — a film on a music-only face, or
  something outside the directories a caller is confined to — it lands in the
  library and says so, because the filtering is applied where it always is,
  on the request for the thing itself.
- **Files with no extension are recognised by content** — some downloads
  arrive with no name to go on, and a few hundred megabytes of plainly MP4
  bytes should not be invisible. Only files that named nothing at all are
  opened, and only from a megabyte up, so it costs a few dozen header reads
  across a whole library rather than one per file.
- **Plays anything** — a file the browser will not open is converted, and
  how much of it gets converted depends on what is actually in the way.
  A container it cannot open but whose streams it decodes perfectly well
  (H.264 with AAC in Matroska, FLV, AVI) is **rewrapped** rather than
  re-encoded — a copy, not a conversion, and lossless. Except where a copy is
  the wrong thing: some old files declare that their frames need holding back
  by less than they really do, and a browser believing them drops one frame in
  four while VLC plays the same file perfectly. That is read out of the opening
  of the file, and such a picture is re-encoded rather than copied — the only
  thing that rewrites the frame order and the declaration together. So is an MP4 holding
  HEVC labelled the way ffmpeg writes it by default (`hev1`): iPhones and
  Safari decode that picture in hardware but accept only the other label
  (`hvc1`), so such a file is relabelled by copying instead of being
  re-encoded — which is what an x265 script's output would otherwise cost on
  a phone. Archived files get the same treatment, being read straight out of
  their volumes. A soundtrack it
  cannot decode (AC3, E-AC3, DTS, TrueHD — the usual cause of "video plays
  but there is no sound") costs an audio conversion with the picture passed
  through untouched, decided from the file's own codecs before playback
  begins rather than after a stall. That also settles which soundtrack you
  hear: a film carrying its real audio in AC3 and a commentary in AAC gives
  a browser one track it cannot decode and one it can, and left to itself it
  plays the commentary. Only a picture it cannot decode (old MPEG-4/DivX,
  often HEVC, and the MPEG-2 on a DVD) is re-encoded.
  That soundtrack conversion **becomes a file while you watch it**. It starts
  as a live stream, which plays at once but cannot answer a range request —
  so a browser managing its own buffer disconnects when it is full and
  reconnects asking for the byte it wants next, and is answered from the
  beginning every time. Measured on a 4K release over one viewing, 963 MB
  crossed the link to move the stream 167 MB, and it gets worse the further
  into a film you are, since every reconnect re-reads more. So the same
  conversion is written to a file behind the playback and the player moves
  onto it when it lands — the picture is copied at disk speed and only the
  soundtrack is encoded, at around thirty-six times real time, which puts the
  switch inside the first few per cent of the film. From then on it is an
  ordinary file: seeking anywhere at once, no server work at all, and it is
  still there the next time the film is opened.
  A browser is never handed a container it has already said it cannot open:
  it does not fail fast on one, it downloads it looking for something to
  play, which on a phone is minutes and gigabytes before anything happens.
  Where a rewrap will do, it is preferred on every browser — including
  Safari, as long as the file is small enough that the copy is quick: a
  couple of seconds of waiting buys the film itself, playing natively and
  seekable throughout, instead of a conversion that has to be reopened at
  every seek.
  **On Safari a conversion is served as HLS** — short segments and a
  playlist, which is the only shape that browser will play, since it opens a
  media URL with a byte-range request and a conversion of unknown length
  cannot answer one. Playback starts after the first segment rather than
  after the whole file: a two-hour film whose audio needs converting is
  playing in under a second. Everything else is streamed straight from the
  converter as before, and seeking reopens it at the new position. Where the
  film has subtitles the playlist is a master carrying them as renditions —
  which is what puts them on an **AirPlay receiver**, that route handing
  over a URL and nothing else, and gives Safari one native subtitle menu
  inline, in fullscreen and on the receiver alike. One conversion serves
  every choice; picking a subtitle only changes which rendition the
  playlist marks default.
  Converted files are kept — until the space is needed, and across a restart
  — so a film converted once is not converted again.
- **Soundtracks** — a film carrying several (a Nordic release with four
  languages, a disc with a commentary) gets a menu in the player: pick from
  it, or cycle with `a`. The choice
  is remembered for that film, and the language is remembered for every
  other: pick Swedish once and the next release carrying Swedish starts in
  it. Left alone it takes the file's own default, and never a commentary
  track unless that is all there is. Because a browser cannot be told which
  stream to decode, choosing one costs nothing where the browser can switch
  its own tracks (Safari), a lossless rewrap where a copy will do, and the
  converter otherwise.
- **Subtitles** — external `.srt`, `.vtt`, `.ass` and `.ssa` files sitting
  next to a video are offered in the player: pick one from the CC menu, or
  cycle with `c`. A language
  or flag appended to the video's name — before the subtitle extension —
  becomes a labelled, language-tagged track. Everything is converted to
  WebVTT on the fly, and non-UTF-8 files (Latin-1 is still common) are
  transcoded so accents survive. The chosen language is remembered. Cue
  times follow a converted stream, which starts at a keyframe rather than
  where you seeked, so subtitles stay in step after skipping around.
- **Split RAR support** — uncompressed ("store") rar volume sets, the
  classic multi-part set holding one huge video, are indexed
  and streamed directly out of the volumes with full seeking — nothing is
  extracted, and a viewer keeps only a handful of volume files open no
  matter how many parts the set has. RAR4 and RAR5, plain and split.
  Compressed or encrypted members are skipped — serving one would mean
  decompressing from the first byte on every seek — and `-debug` says which
  member was passed over and why, so a release that does not appear can be
  told from one that was never scanned.
- **Formats** — video: mp4, mkv, webm, mov, avi, m4v, mpg/mpeg, wmv, flv,
  3gp, vob, the transport streams a capture or a camcorder writes (ts, mts,
  m2ts), and the older wrappers (divx, f4v, ogv, rm, rmvb); images: jpg,
  jpeg, jfif, png, gif, webp, bmp, svg, avif; audio: mp3, m4a, flac, ogg,
  oga, wav, aac, opus, wma; playlists: m3u, m3u8. Whatever the browser
  cannot open is converted (below), so the list is about what is *found*,
  not about what plays.
- **Spectrum analyser** — a quarter-octave analyser for the music bar and
  for the video player alike, shown above the player's controls and fading
  with them. Opening it on a film ends AirPlay for that film until it is
  reopened, which the button says before you press it; casting to a
  television is unaffected.
- **Downloadable DVD titles** — a disc is authored from pieces that each
  start their own clock, so a title downloaded as one file reports minutes
  instead of hours and cannot be seeked. (That is the disc, not the export:
  a plain `cat` of the VOBs does the same.) The timestamps are corrected as
  the file goes out, using the disc's own cell table — same bytes, same
  length, ranges and resuming intact.
- **Hardware conversion where it is needed** — a phone's 4K video is more
  than a processor can convert while you watch it (measured: 0.41 of real
  time, which stalls for ever), so those go to the graphics hardware
  instead — 2.3 times real time, and at half the bitrate. Everything a
  processor handles comfortably stays with it, because fixed-function
  encoders spend far more bytes for the same picture: on a DVD, five times
  as many. VAAPI, Quick Sync, NVENC and VideoToolbox are all tried, each
  proved by a real conversion before it is used, and a machine with none of
  them works exactly as before.
- **Thumbnails from the film, not its titles** — a video's tile is a frame
  from a tenth of the way in, wherever the video lives, because the opening
  is the distributor's logo and the title card. Tiles already made are kept
  as they are; new and changed files get the better frame.
- **Deinterlacing** — a PAL DVD is 576i, and a browser shows those combed
  fields as horizontal teeth on anything that moves. Wherever the picture is
  converted or a frame extracted for a thumbnail, interlaced frames are
  deinterlaced; progressive files are untouched, since only the frames the
  file itself flags are processed.
- **DVD images and folders** — a disc image (`.iso`, `.img`) holding
  DVD-Video, an unpacked `VIDEO_TS` folder, and an image that is itself
  inside a rar volume set — which is how a DVD release ships — are indexed as
  the **titles** on them rather than as the files they are made of: the feature arrives as
  four one-gigabyte VOBs and appears as one film, playable and seekable with
  nothing mounted and nothing extracted. Menus and the one-second title sets
  a disc is padded with are left out, and an image that is not a DVD — an
  installer, a boot ramdisk — is recognised as one and ignored. Hovering one
  previews it like any other film. Anamorphic
  video is handled where it shows: a DVD stores a widescreen picture in a
  square-ish grid of pixels, and de-letterboxing measures the picture as the
  screen lays it out rather than as the file stores it. MPEG-2 is a
  codec no browser decodes, so a title is converted on the way out, and how
  long it runs is read from the disc's own information file rather than
  guessed from the stream — a measured title that ffprobe put at 26 minutes
  is 50, and a length that wrong marks a film watched half way through. The
  same table says which sectors each part of the title occupies, so seeking
  is done by position — without it ffmpeg refuses to move past the length it
  guessed, and the second half of a film cannot be reached at all.
- **One library, several faces** — a request header (`X-Media-Content:
  music`, `videos`, `images`) restricts everything a caller is shown to that,
  so one server and one scan can be a music library on one hostname and a
  video library on another. Enforced in the backend, down to a direct link;
  see [One library, several faces](#one-library-several-faces).
- **What is running** — the foot of the preferences shows the build (version,
  commit, when it was made) and what this machine turned out to be able to
  do: whether video is converted on the graphics hardware or the processor,
  and anything missing that would explain odd behaviour — no ffmpeg, no
  database, no loopback address.
- **Preferences** — the directories being indexed can be changed from the
  UI without restarting: add one and it is walked and watched, remove one
  and everything under it leaves the index. The choice is remembered, and
  outranks the command line, which seeds the first run. `-lock` makes the
  dialog read-only for a server that should not be reconfigured over the
  network — there is no authentication in front of any of this.
- **Download a release** — an album's sheet offers the whole thing as one
  zip: the audio, the sleeve art, the notes — a release is a directory, not
  a track list. A playlist has no directory of its own, so for one of those
  it is the entries it names.
- **Add to queue** — the sheet can also put a release after everything
  already queued: in order, or shuffled among themselves when the bar is
  shuffling, and never mixed in among what was already waiting. With
  nothing playing it simply plays; a queue that had run out moves on to it
  at once.
- **Queue all** — a button in the toolbar of every music view puts the whole
  view after what is already queued: every release listed, every release of
  every performer listed (each played through from their first), every
  release in every genre listed (each once, performer by performer), or
  every track the music listing shows. One request — the server flattens the
  view into its tracks — and there is no limit worth the name: the whole
  library goes into the queue and is shuffled there, the queue panel drawing
  a window of it rather than every row.
- **How the music sounds** — in the background, below everything else the
  server does, each track is decoded once (three twenty-second windows from
  the middle of it) and described by fifty-six numbers: timbre, brightness,
  harmony, loudness and dynamics, tempo, pauses. Written down beside the
  thumbnails, read again only when the file changes, about a second of one
  core per track. Three things are read off it:
  - **Radio, and similar tracks.** The bar's radio button keeps the queue
    going with the tracks that sound most like the one playing, fetching a
    batch whenever fewer than a handful remain.
  - **Similar releases and performers.** A release's sheet offers *Similar*,
    the releases that sound most like it, each saying how alike; a performer,
    once drilled into, offers *Similar artists*. Nearest first, a chip back.
  - **A verdict reaches what sounds like it.** A track that sounds like one
    you liked is lifted in the popular orders, above anything merely played,
    and one that sounds like a dislike is sunk. Your own verdicts still
    outrank it, and the tiles show only those: the resemblance ranks, it is
    not drawn.
- **Audiobooks** — a reading is told from music by the sound of it: a reader
  pauses between sentences where a band plays through, alternates voiced and
  unvoiced sounds, and keeps no tempo. The rule was set against real files
  and the two sit far apart; a track needs a full window of sound (nineteen
  seconds) to be judged at all, a release votes by playing time once half of it has been
  read, an intro on a record is music by its release's word, and a genre
  tag that says audiobook (in the usual spellings) shelves it outright.
  Audiobooks get a
  chip of their own, leave the Albums, Artists and Genres views (a narrator
  is not a performer), are never offered as "similar" to a song, and are
  found by the word *audiobook*.
- **Pause and resume fade** — a tenth of a second or so either way, because
  cutting an element mid-waveform is a click, not a silence.
- **What you actually play, and what you think of it** — every item counts
  its plays, and the music bar has a thumb up and a thumb down for the track
  that is playing. Both add up the shelf: a release is what its tracks have
  been played and how they were judged, a performer what their releases
  have been, a genre what is in it. The verdict outranks the count: a liked
  track stands above anything merely played however often, a disliked one
  sinks below anything untouched, and plays decide only among equals.
  **Popular** lists everything that has been played or judged, and every
  view sorts by *Popular* — which is popularity by release, by performer, by
  genre and by show. A play is counted after five seconds of real playing
  time, the same floor that decides whether a film counts as started, so a
  queue skipped through is not twenty plays; a verdict is remembered until
  the lit thumb is pressed again.
- **Play a season** — the badge on a season card plays it from the first
  episode, and a finished episode rolls on to the next by itself; on a
  television driven over DLNA the set is handed each episode in turn. The
  soundtrack and subtitle language chosen once hold for the whole season,
  here and on the set alike, matched by language in each file.
- **Series** — television grouped by show, with its seasons folded under it:
  the chip lists the programmes, one opens its seasons, and a season opens
  its episodes in order. There is nothing to tag and nothing to configure —
  the show, the season and the episode are read out of the names, taken from
  whichever part of the path actually says them, which is usually not the
  file (a release names the group as often as the programme).
- **A phone-sized top bar** — on touch screens the chip counts compact
  ("210k" rather than "209,857"), the sort controls share the chips' last
  row, and the whole bar slides away while you browse, returning on the
  first upward flick. Nothing is ever hidden behind an edge; it is one
  gesture away.
- **Grouped views** — the same library seen as **Albums**, **Artists** or
  **Genres**, each grouped from the tags rather than the folders, searchable
  and sortable in its own terms. Genre tags are normalised on the way in —
  whitespace folded, so "Black  Metal" is not a second genre — and a tag that
  names several ("Death Metal | Viking Metal") files the release under each.
  A slash is left alone, since "Black/Death Metal" is one genre's name and
  not two. The performer under a release and the genre
  beside it are click targets wherever they appear — on a card, on a track,
  in the album sheet — and each opens everything else that is theirs. A face
  showing only music opens on the artists rather than on a listing of every
  track in write order.
- **Cards say what is known** — a release card carries its performer, year
  and genre, and a year written into the directory name ("2018 - Release")
  is lifted out of the title and shown as the year instead of twice; a performer carries their tracks, running time, the years their
  releases span and what most of them are filed under; a track carries its
  artist, year and genre. Whatever is untagged is simply left out rather than
  leaving a gap or a stray separator.
- **Names as they were meant** — file names and tags that are not UTF-8
  (Latin-1 and Windows-1252 are common on disks that have travelled between
  systems; Thai in TIS-620 is common in music tagged on Windows) are decoded
  for display and for search, so a release with an umlaut in it reads
  properly and can be typed into the search box instead of showing black
  diamonds. A tag that was written under the wrong alphabet entirely is put
  back too: CP1251 keeps Russian letters where Latin-1 keeps its accented
  ones, so a Nordic title tagged by a program guessing Russian arrives with a
  Cyrillic letter inside a Latin word. That, and only that, is repaired — a
  Cyrillic *word* is left exactly as it is. The file itself is still opened
  by the exact bytes on disk; nothing is renamed.
- **Less clutter** — a release's sample is skipped when the full version
  sits beside it, whether it is a folder or a lone `Sample.mkv` next to the
  film, so short excerpts stay out of the library. Never on the name alone:
  the real release has to be there and has to dwarf it, or the sample is
  kept — sometimes the excerpt is all there is.
- **No duplicates** — a file reachable by several paths (hard links, or a
  symlink beside its target) is indexed once. Identity is the file's
  device + inode, not its contents, so genuinely separate copies still both
  appear. Symlinks are measured by their target, and a real file always
  wins over a link to it. On a library that hardlinks torrents into place,
  this removed 355 duplicate entries.
- **Live library** — directories are scanned recursively and watched with
  inotify; new/changed/deleted files stream to the UI over server-sent events.
  A periodic rescan (default 10 min) acts as a safety net.
- **One entry per record** — a release spread over `CD1`, `CD2` and `CD3`
  is one album, with its tracks running disc by disc rather than interleaved
  by track number, and named without the disc marker its tags carry. And a
  release that ships its own playlist used to
  appear twice, once as a directory and once as the playlist. When both
  hold exactly the same tracks only the playlist is kept, since it carries
  the intended running order; a playlist covering part of a directory, or
  spanning several, remains a collection of its own.
- **Artists** — a view grouping music by performer, derived from the same
  tags the albums view uses, so the two always agree. Each card shows their
  album and track counts and total playing time; opening one narrows the
  albums view to that performer, with a chip to step back out. The name
  under a release is a way in as well — click it and you have the rest of
  their work. The view is part of the URL, so a performer's albums can be
  linked to directly.
- **Pick up where you left off** — videos you have started appear under an
  **In progress** chip and ones you have watched through under **Watched**,
  both counted like the rest and filtered server-side, so they page and sort
  like any other listing. The same rule draws the progress bar and the
  checkmark on the tiles: five seconds in is a start, and past 96% is
  watched, which is also where the player stops offering to resume.
- **Search / filter / sort** — the filter chips count what you are looking
  at, not what the library holds: type a word and each chip says how many
  videos, images, tracks, albums and artists answer to it, and inside one
  performer's releases they count only theirs. Word-based search that
  matches while you type: the query is split into words and every word must appear somewhere
  in the filename, tags (title/artist/album/genre/year) or the file's full
  path, in any order, so a half-remembered title in the wrong order still
  finds the track. The whole path counts, not just the part below the
  scanned directory, so typing the mount point a library sits on narrows the
  listing to it. Albums match the same way
  on their name, artist, genre and year, so "melodic death 1993" finds the
  album, and on where the release is kept, so the same query works in either
  view; performers match on the releases and genres they carry as well
  as on their own name. Results arrive in place: the listing stays up while
  the next one is fetched and only what differs is redrawn, so typing narrows
  the grid instead of flashing it, and anything the two searches have in
  common keeps its tile and its loaded thumbnail. Type filter chips (all / videos / images / music /
  albums / artists). What can be sorted on follows the view: files by
  modified date, name, size, date added or playing time; **videos also by
  resolution and by bitrate**, which are two different questions — the
  biggest picture and the heaviest file are rarely the same release; albums
  also by artist, year, genre, track count and length; artists by how many
  albums, how many tracks and how long. A release that carries the value sorts ahead
  of one that does not, whichever way the order runs. Opening a performer
  lists their releases by year, newest first unless the order has been
  turned round. UI state lives in the URL hash. A change of view goes onto the history, so Back returns from a
  drill-down. The grid can be worked from a keyboard: Tab to a tile, Enter or
  Space to open it. Sizes are shown in thousands, as a file manager shows
  them.
- **Thumbnails** — generated on demand and cached in a single-file blob
  database ([bbolt](https://go.etcd.io/bbolt), `<data>/media.db` by default;
  relocate with `-db PATH` or skip caching with `-db off`): images
  are downscaled in pure Go (EXIF orientation respected), videos use `ffmpeg`
  when available, audio uses embedded cover art, a folder image next to the
  tracks, or the artwork a release ships under its own name or in an
  artwork subfolder. The UI fetches thumbnails a few at a time — visible ones first,
  off-screen requests canceled — so streaming and browsing stay snappy while
  a cold cache fills.
- **Fast at scale** — a windowed virtual grid plus server-side paging, a
  per-query result cache and O(1) counts keep six-figure libraries smooth:
  at 150k files, page requests answer in under a millisecond at any offset
  and sort order (the one-off build for a new query costs ~150 ms), and
  metadata for a cold 135k-track library is read in ~20 seconds. The album
  and artist lists revalidate with ETags, so an unchanged list costs a
  304 and no body. The index is mirrored into the database, so a restart
  serves the whole library in under a second instead of waiting for the
  walk; the scan still reconciles in the background, and anything that
  disappeared is dropped along with its cached thumbnails and metadata.
- **Streaming** — all media is served with HTTP Range support
  (`http.ServeContent`), so seeking is instant and nothing is buffered
  whole-file.

## Quick start

```sh
make build            # builds everything inside Docker and extracts ./mediator
./mediator ~/Videos ~/Pictures ~/Music
# open http://localhost:8080

./mediator -open ~/Videos    # or: free port on localhost, browser opens itself
```

The entire build — TypeScript API model generation, frontend bundle, Go
binary — runs inside Docker (BuildKit). The host needs only Docker; no Go,
Node or npm installation is required.

Flags:

| Flag       | Default            | Description                                                    |
| ---------- | ------------------ | -------------------------------------------------------------- |
| `-listen`  | `:8080`            | HTTP listen address; port `0` picks a free one                 |
| `-open`    | `false`            | Open the UI in your browser once listening (see below)         |
| `-data`    | `data`             | Directory for playback state + the default blob database       |
| `-db`      | `<data>/media.db`  | Blob database (thumbnails + metadata); `off` disables caching  |
| `-rescan`  | `10m`              | Full reconciliation rescan interval (`0` disables)             |
| `-analyze` | `true` | Read how the music sounds in the background (ffmpeg needed), for similar tracks, radio, similar releases and performers, and audiobook detection; `-analyze=false` turns it off |
| `-version` | | Print the build (version, commit, time, toolchain) and exit |
| `-tmp`     | system temp        | Where converted files are kept (see below)                     |
| `-tmp-max` | `8G`               | How much converted material may be held at once; `off` (or `0`) for no limit |
| `-lock`    | `false`            | Refuse changes to the scanned directories: the preferences become read-only |
| `-exclude` | —                  | Glob of paths to keep out of the index; repeatable             |
| `-debug`   | `false`            | Log every request — method, range, status, bytes, duration     |

Positional arguments are the directories to scan and watch (recursive) — the
first run's, at least: once directories are chosen in the preferences those
are what is indexed, and the command line is only the seed. `-lock` keeps the
command line in charge.

Converted files (rewraps and HLS segments) live under `-tmp` in a fixed place
rather than a fresh one per run, so a restart finds what was already
converted instead of doing it again. `-tmp-max` is a budget shared by both,
and it prunes: least recently wanted first, never taking something that is
being watched — being over a limit you chose is a smaller wrong than deleting
a film out from under a viewer. A file too large for the whole budget is left
to the segmented converter, which only holds what it has produced.

`-exclude` matches the file or directory name when the pattern has no slash
in it, and the whole path when it does.

`-open` launches your desktop's URL handler (`xdg-open`, with `gio`,
`gnome-open`, `kde-open`, `wslview` and `x-www-browser` as fallbacks; `open`
on macOS, `rundll32` on Windows). On its own it also switches the listen
address to `127.0.0.1:0` — a free port on loopback — so an ad-hoc session
never collides with a running instance and is not reachable from the
network. Pass `-listen` explicitly to override that; the server still opens
whatever address it ends up bound to. The chosen URL is logged either way,
and if no URL handler is found the server logs a warning and keeps serving.

## Signed media URLs

Anything that fetches media without a browser behind it — an AirPlay
receiver, a casting device, a player handed an exported m3u — is given a URL
and goes and gets it with no credentials at all. Behind a proxy asking for
HTTP Basic authentication it is refused before the request reaches this
server, and the television sits on a spinner while the access log stays
empty.

So the page builds its media URLs with a token in them:

```
/api/signed/<token>/stream/<id>   instead of  /api/stream/<id>
/api/signed/<token>/hls/<id>/…    instead of  /api/hls/<id>/…
```

— and the same for `remux`, `transcode` and `subs`. A film that has to be
converted is fetched by the receiver too, segments and subtitles and all;
the segments are named relative to the playlist, so they follow it under the
same token without anything on the receiver knowing. Media paths only: the
library cannot be listed with a token, which is not a login.

The token comes from `/api/info` — fetched the ordinary, authenticated way —
and lasts twelve hours. The server checks it on that path and serves nothing
without a valid one, so the proxy can let the path through:

```nginx
server {
  server_name music.example.com;

  auth_basic           "mediator";
  auth_basic_user_file /etc/nginx/.htpasswd;

  # Signed links carry their own permission, and the server refuses them
  # without a valid one. This is what lets an AirPlay receiver fetch a file.
  location /api/signed/ {
    auth_basic off;
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;              # shortlinks are scoped by it
    proxy_set_header X-Media-Content music;   # if this face is restricted
  }

  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Media-Content music;
  }
}
```

What that trades: a leaked URL is worth what a leaked token is worth — the
streaming half of the library, until the token expires. It is not a
replacement for the password in front; it is a way for the things that
cannot answer one to fetch what they were pointed at. The key lives in the
blob database, so links survive a restart and deleting the database
invalidates all of them.

## Playing to a television

A television is not a Cast receiver and usually not an AirPlay one either.
What it almost always *is* is a **DLNA renderer**: a device that will play a
URL it is handed. The browser cannot hand it one — a set discovered over DIAL
appears in no picker, and nothing in a page changes that — so this server
does it instead.

Choosing a set from the player's TV button:

1. finds renderers with an SSDP search, out of every interface that could
   carry it (a machine running containers has several that cannot);
2. hands the set the item's URL on **this machine's address on the network
   the set is on** — not loopback, and not whatever hostname the page was
   loaded from, which may be on the other side of the world;
3. drives it from then on: play, pause, seek, volume, and a poll that keeps
   the player's clock and the saved resume point honest.

What the set is told about the file is sent with it — title, artist, album,
genre, year, track number, size, and the cover art for music — because a
television has been handed one file and no library, and shows what that
document says and nothing else. Without it, music plays behind the set's own
logo on a black screen.

Subtitles carried **inside** the file are offered alongside the sidecars —
one menu, one numbering. That is how a television release ships its
captions: no `.srt` beside the file, one MKV with the text muxed in. They
are found by the same probe that lists the soundtracks, extracted once and
cached, and served through the same conversions a sidecar gets.

Subtitles go with a film, converted to SubRip on the way out, since that is
the format sets read and WebVTT is not — sidecars and the tracks inside the
file alike, under the same numbering the player's menu uses. The set draws
one or none — it has no menu to choose from — so the one the player is
showing is the one that is sent, and turning them off in the player sends
none. The soundtrack choice travels the same way: the one the player is
using is the one the set is given, as a copy of the film carrying only that
track, made only when the choice differs from the default the set would
have picked anyway.

**Which soundtrack** cannot be said to a television at all: DLNA hands over a
URL and the set decides what to play out of it, which is how a release
carrying six languages comes out in the one its file marks as default. So the
choice is made by handing over a file with one soundtrack in it — a copy at
disk speed rather than a re-encode. It is only done when the viewer has chosen
something other than the file's first track, since that is the one a set would
have picked anyway; measured on a 5.3 GB film, the copy took about 19 seconds
before the television began playing it.

That copy keeps the file's own container where it can. A video that ships
**automatic dubs** — one audio track per language in a single Matroska file,
which is what a downloader produces for one — holds streams that belong in no
MP4, so moving them would be the wrong container for both the set and the
codecs, while a copy into their own kind is lossless and costs a read. The
same copy is what the browser gets: only Safari can switch soundtracks on the
element, so everywhere else a change of language means a new source, and for
a file the browser already opens that source is a copy rather than a
conversion.

The set fetches the file and decodes it — so what it plays is the file, at
its own quality. Where the container is not one it lists as playable, the
first thing tried is **another name for the same bytes**: a container often
has more than one name in circulation, and a set knows the ones its makers
chose. A WebM — what a video downloaded from a video site arrives as — is a
profile of Matroska, so a television that lists `video/x-matroska` and
refuses both spellings of WebM can be handed exactly the same file under the
name it does list, and plays it. Nothing is copied and nothing re-encoded.
The same goes for the spellings of AVI, and for a transport stream offered
under the general MPEG name. Failing that the rewrap is offered (a real file
with ranges, which is what a renderer wants); a live conversion never is,
since it answers no ranges and has no length. The URL carries a signed token like every other media link, so a
proxy asking for a password does not stop it.

Two things this needs, both properties of the network rather than settings:
the server and the television have to be on the same one, and multicast has
to reach between them. Nothing else is configured, and there is no flag to
turn it on — the search happens when a client asks what is out there. It is
IPv4 only: the search, the description fetches and the address the set is
handed all speak IPv4, so a set reachable only over IPv6 is not found.

A set that is off or in standby does not answer the search, so the button is
absent until the television is on. That is not a fault to look for: the list
is what answered, and nothing answered.

Note the posture, which is the same as the rest of this server: **there is no
authentication**. Whoever can reach the port can start something playing on a
television in the house.

## One library, several faces

A face can also be restricted to **part** of the library with
`X-Allowed-Paths`, naming absolute directories separated by commas (or
newlines, for a path with a comma in it). The request then sees only what
lives under them — in listings, in counts, in the albums, artists and genres,
and when asking for anything by id — and such a caller is refused the
preferences outright, since the list of scanned directories names the roots
of a tree they have been given one branch of. Unset means the whole library, and the two
headers compose: a request carrying both is shown the intersection.

    location / {
        proxy_set_header X-Media-Content music;
        proxy_set_header X-Allowed-Paths /srv/media/music,/srv/archive/live;
        proxy_pass http://127.0.0.1:8080;
    }

Both are matched against the **absolute path on disk**, and the match is by
path component: `/srv/mediax` is not under `/srv/media`. Both also form part
of the cache key, in the server's own caches and in the `ETag` it sends, so a
client that changes faces — or a shared cache serving two of them — cannot be
handed the wrong library.

Note what this is not: the header is the whole of the permission, so anything
that can set it can see anything. It restricts a *face*, in the same way and
with the same trust model as the content header.


The same server can answer as a music library on one hostname and a video
library on another. A request carrying `X-Media-Content` is shown only what
it names — `music`, `videos`, `images`, or several separated by commas — in
its listings, its counts, its search and its downloads alike; a direct link
to anything else answers 404. Nothing is indexed differently: one scan feeds
every face, and a request without the header sees the whole library.

The header is meant to come from a reverse proxy, because a browser cannot
set headers on the requests that matter (an `<img>` fetching a thumbnail, a
`<video>` fetching a stream). With nginx:

```nginx
server {
  server_name music.example.com;
  location / {
    proxy_set_header Host $host;
    proxy_set_header X-Media-Content music;
    proxy_pass http://127.0.0.1:8080;
  }
}
```

`proxy_set_header Host $host` is worth setting on every face: without it
nginx sends the **upstream** address instead, so every face looks to the
server like one host and the shortlinks below stop telling them apart.
`X-Forwarded-Host` is honoured too, for a proxy that prefers to say it that
way.

The UI adapts: a music face offers the library, albums and artists, a video
face just the library, and a link to a view that is not there opens the
listing instead of an empty screen.

## Docker

```sh
docker build -t mediator .
docker run --rm -p 8080:8080 \
  -v /path/to/your/media:/library:ro \
  -v mediator-data:/data \
  mediator /library
```

Multiple libraries: mount them and list them —
`docker run … -v /x:/movies:ro -v /y:/music:ro mediator /movies /music`.

The image answers Docker's health check from `/api/info`, which is served
the moment the listener is bound, before any scan. It includes `ffmpeg` for
video thumbnails. Watching relies on inotify;
for very large trees raise `fs.inotify.max_user_watches` on the host.

## Troubleshooting

**A download or a stream stops part way.** Run with `-debug`: every request
is logged when it finishes, with the range asked for, the status, the bytes
actually sent and how long it took. If that shows the whole file went out,
the transfer was ended downstream. The server also warns on its own when a
stream stops because *it* could not read further, naming the file and how
far it got, so "we failed" and "the other end let go" are never confused.

Two things that look like bugs and are not. Restarting the server ends every
transfer in flight — shutdown waits five seconds, not for a 16 GB download —
so check the restart times before reading a truncated file as a fault.
And browsers differ: if one truncates a large file that another downloads
whole over the same path, that is the browser, and one that quietly reissues
the request as a range to carry on where it stopped is telling you so.

Behind a tunnel or a reverse proxy, read its log too. `upstream prematurely
closed connection` means this server closed first; no entry at all means it
did not. On the same network the server's own address bypasses the proxy
entirely, which is the quickest way to separate the two.

**A release inside a rar set is missing.** With `-debug` the log says why,
once per set and not per member or per rescan: `rar set holds members it
cannot serve`, with how many and the reason — compressed (only stored
members can be served; decompressing from the first byte for every seek
would be the whole archive per scrub), encrypted, or incomplete, with how
many bytes of how many have arrived.

**A film stutters in the browser and plays smoothly in VLC.** Usually the
stream lies about how far it reorders its frames: it declares a depth the
encoder did not keep to, ffmpeg and VLC buffer past it, and a browser that
takes the declaration at its word shows frames early and drops the late ones.
The server reads the opening of every film when it is opened and, where it
finds this, has the player convert the film instead of playing the file as it
is — the log says `picture must be re-encoded rather than copied`. That costs
a re-encode for exactly those files and nothing for the rest.

**A film never starts, and the player says the file cannot be read.** That
is the disk, not the server: the log carries `stream open failed … err="…:
input/output error"` (or `permission denied`, or `no such file`), and the
same error from the conversions that tried afterwards. A filesystem that has
shut itself down after an error — XFS does this — keeps its mount and answers
every access with an I/O error until it is unmounted and repaired; the rescan
logs `scan error path=/mnt/… input/output error` every ten minutes while it
lasts. The files on it stay in the listings on purpose: a root that fails to
read is protected from reconciliation, so nothing is forgotten while the disk
is away, and it all plays again the moment the mount answers.

## Development

All build/check targets run inside Docker as well:

```sh
make generate   # regenerate web/src/types.gen.ts from the Go API types
make test       # frontend tests + go vet + go test ./...
make vet        # go vet only
cd web && npm test      # just the frontend's own tests (needs Node)
cd web && npm run dev   # Vite dev server on :5173, proxies /api to :8080 (needs Node)
```

The TypeScript API model (`web/src/types.gen.ts`) is **generated** from the Go
types via `cmd/gen-ts` — the Go structs are the single source of truth for the
wire format. Image builds regenerate it from scratch every time; the checked-in
copy only serves the `npm run dev` flow.

### Layout

```
main.go               flags, embedding (web/dist), lifecycle
cmd/gen-ts/           Go → TypeScript API model generator
internal/library/     index, scanner, fsnotify watcher, albums, artists,
                      tokenized search, tag/duration enrichment, rar volume
                      reading, loopback reads for archived members
internal/server/      HTTP API, SSE, streaming, rewrap and HLS conversion,
                      the scratch budget, thumbnails, preferences, request
                      log, embedded SPA
internal/blob/        bbolt store: thumbnails, probed metadata, the mirrored
                      index, flags, playback positions, configuration
internal/browse/      default-browser launch + URL derivation
internal/state/       playback positions, held in memory and flushed to the
                      database
internal/rartest/     spec-correct rar fixtures, shared by the tests
web/                  Vite + vanilla TypeScript frontend (no runtime deps);
                      web/src/*.test.ts run under node's own test runner
```

### API

| Endpoint                                  | Description                              |
| ----------------------------------------- | ---------------------------------------- |
| `GET /api/library?kind&q&sort&order&offset&limit` | Paged, filtered, sorted listing  |
| `GET /api/albums?q&artist&genre&sort&order` | Albums (directory + m3u), narrowed to one performer or one genre; `audiobooks=1` lists the audiobook shelf instead |
| `GET /api/artists?q&sort&order`           | Artists, grouped from album tags         |
| `GET /api/genres?q&sort&order`            | Genres, grouped from album tags          |
| `GET /api/tracks?of&…`                    | The tracks behind a view (`of` = albums, artists, genres or items, with that view's own parameters), in the order a queue plays them; `of=similar&id=…&n=` the tracks that sound most like one (`n` at most 200) |
| `GET /api/albums?near={id}`               | The releases that sound like one, nearest first (`order=asc` turns it round), each with `similarity`; `audiobooks=1` lists the audiobook shelf instead of the records |
| `GET /api/artists?near={name}`            | The performers that sound like one, nearest first (`order=asc` turns it round) |
| `GET /api/series?q&sort&order`            | Television, read out of the file and directory names; each show carries its seasons |
| `POST /api/plays/{id}`                    | Count one play; returns the new total    |
| `POST /api/like/{id}`                     | Record a verdict (`{"like": 1 / -1 / 0}`); returns what is stored |
| `GET /api/albums/{id}`                    | Album detail with tracks                 |
| `GET /api/stream/{id}`                    | Media bytes (Range supported, `?dl=1`)   |
| `GET /api/signed/{token}/…`               | The media paths again, reachable with a signed token instead of the proxy's credentials — see [Signed media URLs](#signed-media-urls) |
| `GET /api/thumb/{id}?w=360`               | Cached JPEG thumbnail                    |
| `GET /api/item/{id}`                      | One item, with its metadata read first if it has not been |
| `GET /api/remux/{id}?a=&mode=`            | The same streams in a container the browser opens, served as an ordinary seekable file, keeping soundtrack `a`; `mode=audio` copies the picture and converts the soundtrack instead; 404 when copying would not help |
| `GET /api/transcode/{id}?t=0[&mode=audio]` | Live fMP4 conversion from t seconds (`mode=audio` copies the video) |
| `GET /api/hls/{id}/index.m3u8?t=&mode=`   | The same conversion as HLS — what Safari plays; redirects into a session |
| `GET /api/convert/{id}`                   | How far a conversion has reached, while something is waiting on one |
| `GET /api/keyframe/{id}?t=` | Where a copied conversion seeking to t really begins |
| `GET /api/crop/{id}`                      | Where the picture sits inside the file's own black borders, found once and remembered |
| `GET /api/albums/{id}/zip`                | The release as one download                |
| `GET /api/sprite/{id}`                    | Scrub sheet: ten frames across a video, taken by ten seeks (3.5 s for an 87-minute film) |
| `GET /api/playlist.m3u?…`                 | The current query as an m3u                |
| `GET /api/info`                           | What the client needs before anything else: the thumbnail epoch, and which classes of media this face may show |
| `POST /api/links`                         | Mint a shortlink to a view or to one item; asking twice for the same place returns the same code |
| `GET /s/{code}`                           | Follow one, redirecting to the address it names; an unknown code opens the library |
| `GET /api/prefs`, `PUT /api/prefs`        | The directories being indexed              |
| `PUT /api/flags/{id}`, `PUT /api/flags`   | Hidden / favourite, one item or a batch    |
| `GET /api/subs/{id}`, `GET /api/subs/{id}/{n}?shift=&format=` | External subtitles: list, and one as WebVTT — or as SubRip (`format=srt`), which is what a television reads |
| `GET/PUT/DELETE /api/state/{id}`, `GET /api/state` | Playback positions, filtered by face and paths like everything else |
| `GET /api/renderers`                      | The DLNA renderers on the network (`?fresh=1` searches again) |
| `GET /api/renderers/{rid}`                | Where that set has got to: transport state, position, duration |
| `POST /api/renderers/{rid}/play/{id}?t=&sub=&audio=` | Play an item on it, from t seconds, with one sidecar subtitle (`sub=off` for none) and one soundtrack |
| `POST /api/renderers/{rid}/next/{id}?audio=` | Queue what follows on the set itself, so a track boundary costs no silence; 501 where the renderer will not |
| `POST /api/renderers/{rid}/control`       | `{action: play\|pause\|stop\|seek\|volume, seconds, volume}` |
| `GET /api/events`                         | Server-sent library change events        |

#### Request headers

| Header             | Values                          | Effect                                                       |
| ------------------ | ------------------------------- | ------------------------------------------------------------ |
| `X-Media-Content`  | `music`, `videos`, `images` — comma-separated for more than one | Restricts everything this request is shown to those classes: listings, counts, search, the grouped views, and anything asked for by id (404 otherwise). Absent or unrecognised means the whole library. Set it in a reverse proxy, not in the page — see [One library, several faces](#one-library-several-faces). |
| `X-Allowed-Paths` | absolute directories, comma or newline separated | Restricts the request to what lives under them — listings, counts, collections and every by-id request alike. Absent or empty is the whole library. Composes with `X-Media-Content`: a request carrying both sees the intersection. Set by the proxy, never by the page, for the same reason as the header above. |
| `X-Media-Internal` | a token the server mints for itself | Marks the server's own loopback reads — ffmpeg fetching an archived file's bytes through `/api/stream` to make a thumbnail or read its codecs. It keeps those from registering as playback, which would make the thumbnailer throttle against its own reading. Not something a caller sets: it grants nothing, and a request carrying someone else's guess at it is treated as any other request. |

Notes: media is only ever served by indexed ID, so playlists cannot reach
outside the configured roots. This server implements **no authentication** of
its own — a deployment that needs it puts a proxy in front. Anything
fetching a URL without a browser (an AirPlay receiver, a casting device, an
external player handed an m3u) cannot answer a password challenge, which is
what [signed media URLs](#signed-media-urls) are for. As shipped: whoever can reach the port can browse, stream and — unless the server
was started with `-lock` — point the library at any directory the process can
read. That is the posture for a personal server on a network its owner
trusts. Thumbnails for `.avif` fall back to an icon
(no pure-Go decoder); the full-size view still renders in the browser.
