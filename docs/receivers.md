# Receivers: AirPlay, DLNA and signed URLs

- **Playing to a receiver** (`airplay.ts`) is our own button (the browser's
  picker belongs to the native controls, which the player replaces) over two
  feature-detected routes,
  WebKit's prefixed AirPlay API and the Remote Playback API, shown only once a
  receiver is reported (`webkitplaybacktargetavailabilitychanged`,
  `remote.watchAvailability`; an empty picker is worse than none).
  `watchRemoteState` merges the two connection events.
  - `watchAirPlay` attaches WebKit's listener (system route detection, a
    documented power cost) and Chrome's watch only while the document is
    visible (WebKit answers at once on re-attach); a chosen route is
    unaffected. A phone or tablet (`maxTouchPoints > 1`) is never asked: its
    picker always has routes, so the button is offered outright and the system
    scans only while the picker is open.
  - WebKit answers whether *any* target exists, Chrome whether one can play
    **what this element holds**, so its watch is re-armed (the old cancelled)
    on every `loadedmetadata`; the bar keeps the set of decks that can reach
    something, not one flag (or the idle deck hides the button), and appends
    its decks to the bar rather than leaving them detached.
  - A rejected `watchAvailability` shows the button and lets Chrome's dialog
    decide; only an explicit `false` hides it.
  - Both routes hand over a **URL** (hence signing). A set merely discovered
    over DIAL never appears in `remote.prompt()` (DIAL launches a named app);
    it is reached by AirPlay from an Apple device, or by DLNA.
  - When playback moves to a receiver, a codec a television likely lacks is
    converted at once (else sound over black): `playsOnReceiver` is the safe
    list (H.264, HEVC); an unprobed file is left alone (converting on a hunch
    costs more). Coming back does not switch back (a stall each way).
  - Limits: an element whose output was **moved** into Web Audio has no route
    left (where a deck's sound cannot be copied, opening the spectrum ends
    AirPlay until reload); the receiver must reach the page's address (an old
    set rejecting a modern certificate just spins).
- **A television is driven over DLNA from the server** (`internal/dlna`,
  `internal/server/cast.go`, `web/src/cast.ts`): SOAP (`SetAVTransportURI`,
  `Play`), the set fetching the file itself.
  - Discovery searches out of every interface (container bridges hide sets
    from the default route), twice each (UDP is lossy), waiting 2.5 s (`MX`
    2); IPv4 only, deliberately (search, descriptions, `LocalIPFor`).
    Descriptions use one client with a dial timeout and short idle life, at
    most `describeAtOnce` in flight, through a fixed pool fed from a channel
    (`describeAll`), not a goroutine per location, as other hosts decide the
    datagram count. Locations are deliberately uncapped (never drop a real
    renderer).
  - `Discover` runs on handler contexts with no deadline, so folding in
    `ctx.Deadline()` is not enough: `endReadsWhenDone` winds the socket
    deadline forward on cancellation (waking a blocked read) and returns its
    teardown, registered after `defer conn.Close()` so LIFO runs it first (else
    an abandoned search holds a socket and goroutine per interface and the
    search mutex). Nothing is cached where `ctx.Err()` is set (a dead context
    empties every describe, which would hide every set for the TTL).
  - A relative control URL resolves against an explicit `URLBase`, else the
    description's final URL after redirects; the receiver's host is read from
    the transport control URL. An oversized, incomplete or non-SOAP response
    is refused, and a SOAP fault is a failure even under HTTP 200.
  - `casting.known` keeps every set ever reported (refreshed per search) and
    resolves ids clients hold (`renderer()`), so a lost reply never strands a
    playing set; one really off times out (45 s `SetAVTransportURI`, 8 s poll)
    as "did not answer". `known` is unbounded on purpose (a handful;
    UDN-derived ids survive lease changes). The picker lists sets seen within
    `rendererMemory` (5 minutes), this round's first, since a search misses an
    answering set ~1 time in 72 (cause unknown); `DiscoverReport` returns sets
    that answered but could not be described and `Server.search` logs them (a
    lost datagram and a failed description want different cures).
  - Handed-over URLs use our address on the set's network (`LocalIPFor`;
    loopback and the page's hostname are unreachable from it), signed.
  - The calls a set answers only once the film is open get `openTimeout`
    (45 s against 8 s; `budgetFor`): `SetAVTransportURI`, and `Play`, which an
    LG holds until the picture is up (1.4–7 s for an ordinary film, past 8 s
    for a 4K one). Timing either out at the ordinary budget reports a failure
    for a film that is about to play, and the page then stops it.
  - `castAlias` first seeks another honest name for the *same bytes*: WebM as
    `video/x-matroska` for a set refusing WebM (**one way only**: an MKV named
    WebM promises VP8/VP9), AVI's spellings (`video/avi`, `video/msvideo`,
    `video/x-msvideo`), `video/mpeg` for a transport stream. It is keyed by
    **type**, not extension (a copy may change container).
  - Codecs cannot be asked (`GetProtocolInfo` lists containers and legacy
    `DLNA.ORG_PN` profiles, Matroska as a bare `*`): an undecodable picture is
    left to fail visibly, but a soundtrack no set decodes (`noReceiverAudio`,
    `castSoundKind`: DTS, DCA, TrueHD, MLP) fails silently, so it is converted
    first by `remuxSound`, gated on `soundFixable`, **not** `remuxable` (which
    refuses these files); a set listing DTS among its sinks is exempt.
  - MIME types come from our table (`mimeFor`, registered in `server.go`'s
    `init`): `.webm` is `video/webm`, as Go's built-in `audio/webm` overrules
    even `/usr/share/mime/globs2` and `/etc/mime.types`; a set judges by that
    name alone.
  - Offered is the file, or a rewrap with ranges where the set does not list
    our container; never the live conversion (no ranges or length). A set that
    will not say what it accepts is assumed to accept everything (its refusal
    on screen beats ours).
  - Music carries full DIDL: tagged title, artist under both spellings, album,
    genre, year, track, size, bitrate, and a `castArtWidth` thumbnail as cover
    (hence `thumb` is a signed path; it reveals nothing else). Artwork only for
    music. UPnP `bitrate` is **bytes** per second.
  - DLNA cannot pick a soundtrack (no `-map`), so a choice is a copy with that
    track alone: `Remuxer.File` takes it, in the **key and file name**
    (`-a<n>`) so one language is never served as another; `Adopt` deletes
    names that do not parse (their soundtrack is unrecorded). A track is named
    only where it differs from the file's default (`castAudioChoice`, tested),
    a copy being the cost — not on a non-zero index, which drops track 0 chosen
    over another default and copies for nothing. `castSource` settles the file
    before naming it: a `switch` whose rename branch `break`s never reaches the
    soundtrack. A dubbed Matroska download (VP9, an Opus track per language;
    not `remuxable`) gets `remuxTrack`: streams copied, **container kept**, one
    soundtrack; `trackContainer` allows only Matroska (`.webm` → `webm`,
    `.mkv` → `matroska`; an MP4 is already `remuxable`, others a set is
    unlikely to list), the name carries it (`remuxExt`), and
    `remuxKeyFromName` cuts any extension, not only `.mp4`.
  - The copy is made **before** the URL is handed over (a set must not wait on
    ffmpeg), its progress (`/api/convert/{id}`) shown in the player's label by
    `countCastPreparation` until the set has the URL, a late tick dropped; it
    is bytes over the source's size, so it under-reports. Where no copy fits
    (usually the scratch budget), `CastStatus.Note` warns the viewer, on queued
    tracks too, since the set will play the leading soundtrack; an
    unrewrappable file goes as it is. No address on the set's network is 503,
    not 422.
  - Subtitles: `?sub=` carries the choice (`off` for none, else the first),
    indexed like the listing (sidecars, then embedded), so embedded captions
    reach a set like sidecars; both cast handlers run `EnsureCodecs` first or
    the index counts a shorter list. Sent in three places, as sets differ
    (`sec:CaptionInfoEx`, `sec:CaptionInfo`, a `text/srt` `res`; Samsung's
    namespace, read by LG too), as SubRip (`ToSRT` over `ToVTT`, encodings
    handled once): sets read no WebVTT and have no subtitle menu.
  - **A subtitle inside the file is read out before the set is handed
    anything** (`castCaption`, `readCaption`): a set fetches the subtitle as
    it opens the film and holds `Play` until it has it, and the read is the
    whole film (a minute or more for a large one). The page counts the wait
    through `/api/convert/{id}?for=cast` (kind `subtitles` while
    `casting.captioning` marks the item), not by whatever the film is being
    converted for in the browser. One that cannot be read is left out and the
    viewer told (`CastStatus.Note`). `handleCastNext` (the music queue) names
    a subtitle without waiting: no `Play` there for it to hold up.
  - A set's fetch carries `tv=1` (minted by `remuxQuery`, honoured by
    `handleRemux`), lifting the rewrap's 404 for a picture reordering beyond
    its declaration — right for a browser, wrong for a set decoding like VLC
    (else `716 Resource not found`). It authorises nothing else.
  - Refusals reach the viewer as sentences naming the set (`castFault`), the
    SOAP fault going to the log.
  - **A cast claims the set before preparing the film** (`castClaim`; the set
    itself cannot be owned): the newest request takes the claim and cancels
    the previous, every SOAP call runs on that context, and the loser issues
    nothing more, so no viewer's play or resume point lands on another's film.
    Nothing queues (a slow set's 45 s is worse); a generation guards the
    release so an old `done()` cannot clear a newer claim. Claimed at the first
    SOAP call, an abandoned film's long copy could finish later and take the
    set; the copy itself outlives the request. Every step re-checks
    (`castTaken`): in `showing`, in the seek, after preparation, before
    answering (any step can succeed and still be overtaken). A superseded
    request gets **409 naming the set**, never a 5xx (not the set's fault).
  - Silence is not failure: a set may not answer `SetAVTransportURI` while
    already showing the film, so `showing` asks what it holds and the seek goes
    ahead; a `Play` left unanswered is asked the stricter question, whether the
    set is playing the URI it was handed (`started`; holding it while still
    opening is not enough). A refusal (`dlna.Fault`) is an answer, reported at
    once.
  - UPnP 701 (a state the set cannot leave that way, e.g. `LG_TRANSITIONING`,
    whose `GetCurrentTransportActions` offers only Stop) gets Stop and retry
    (`dlna.SetURIFromAnyState`; `endCast` also sends Stop on clean-up): at
    once, then `stopRetries` more times `stopSettle` apart, as a stopping set
    can refuse once more. Nothing else stops a set: 714 (a type it will not
    play) leaves it playing; still refusing after the stop is reported
    **busy**. Reactive, never a Stop before every cast (the cost is one quick
    refusal).
  - `Item.Unreadable` files are never handed to a set (422 at `/play` and
    `/next`; a set can stick on one). Automatic roll-on skips them (`damaged`
    in `query.ts`, tested, applied by `findKind` wherever the player advances
    *by itself*); a swipe still lands on one.
  - Music queue: the bar hands the set the next track in advance
    (`SetNextAVTransportURI`, optional) and sees it move on when the reported
    URI changes; failing that it sends the next on an ending, polling more
    often than the player (`CAST_POLL` 3 against 4) and every second near the
    end (`CAST_END_WINDOW`), as gaps are heard. A set about to advance says
    STOPPED briefly, so with something queued a stop waits `CAST_HANDOVER`
    polls before counting. A stop short of a track's end is the remote, so the
    bar stops driving, unless the set gives no duration (then the next is
    sent). STOPPED before playing is no ending (`seen`; a start can take 7 s).
    `load` splits into `loadDecks` and `castLoad`. The bar's spectrum is
    dimmed, not removed, while a set plays (routing into Web Audio cannot be
    undone and spends the AirPlay route).
  - **One transport** (`CastTransport`, `casting.ts`) under two adapters
    (`audio.ts`, `video.ts`): tick, carried clock corrected by answers,
    cadence, `castStep`'s seen/ended/stopped reading, handover wait, and a
    generation guard dropping answers after a stop, kept once (two copies drift
    in what a stop means). No DOM, injected timers, tested by `casting.test.ts`
    against a fake set. Only playing and paused write the carried state, not
    opening. The player's hooks persist position per poll and roll a season
    on; the bar's clear its error streak, follow the set into the queued track
    and send the next. A poll has one request in flight, and an answer
    predating a local control or a change of item is discarded; queue replies
    follow the latest request, and a playing set's position of zero means
    zero.
  - While a set holds the film nothing reaches `startSource` (the one door for
    sound fix, track change, escalation and rewrap; a source here is a second,
    audible showing); source-bearing polls stop when the cast begins; media
    keys drive the set; the element's `error` is ignored; a chosen subtitle is
    re-sent like a soundtrack; the set's own stop hides the poster and resets
    the clock.
  - Casting forks the player's transport: `curT`/`totT`/`seekTo`/`togglePlay`
    answer for the set, so the seek bar, clock, resume point and media keys
    work unchanged, the poster covering the paused, empty element. The clock is
    corrected every fourth tick (`CAST_POLL`), counting ticks, never the
    fractional position (a modulo would misfire). `castStep` (`playback.ts`,
    tested) interprets a set's poll and the transport's tick acts only on it;
    a set's volume is sent 150 ms after the slider settles; speed is disabled
    while casting.
  - A season rolls on while a set plays it: `goTo` passes the next file to
    `castItem` after the shared per-file reset (`beginFile`), so remembered
    languages apply; `endedOnSet` (`playback.ts`, tested) tells an ending from
    a remote stop by the clock (ended rolls on, stopped ends casting); the poll
    stands down during a handover, or the old film's STOPPED reads as a second
    ending. `playSeason` plays a season card as an ordered listing. Closing the
    player stops the set.
  - Deliberately no authentication: whoever reaches the port can play to a
    television in the house.

- **Media URLs carry their own permission** (`sign.go`): receivers (AirPlay)
  fetch URLs without credentials, which a password proxy refuses. The page
  prefixes a token from `/api/info` to every media path
  (`/api/signed/<token>/stream/<id>`; also `hls`, `remux`, `transcode`,
  `subs`) via one route (`{rest...}`), so relative HLS segments stay signed.
  `signedPaths` is a media-only allowlist; a token is no login. It encodes
  only its expiry, so the page builds URLs itself (per-file tokens would
  defeat thumbnail caching); a leaked URL streams until `signTTL` (12 h). The
  key, `blob.SignKey`, survives restarts; deleting the db revokes every link
  (`-db off`: per run). The proxy needs a `/api/signed/` location with auth
  off carrying the face's `X-Media-Content`, or a music face serves films
  (README).
