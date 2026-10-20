# Feature guide

[Documentation](README.md) · [Quick start](../README.md#quick-start)

Detailed behavior, supported formats and playback controls. Use the page
outline to jump to a feature, or start with the [overview](../README.md#features).

## Play on a TV, the way that actually reaches one

the button in the
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
See [Playing to a television](deployment.md#playing-to-a-television).

## AirPlay and Cast

the same button also carries the browser's own
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
see [Signed media URLs](deployment.md#signed-media-urls), which is the one piece of
configuration this needs. **A file recorded with its index at the end** —
which is how a phone writes one — is rewrapped before it plays, a copy at
disk speed that moves the index to the front and is kept for next time:
such a file cannot start until its last bytes have been fetched, and
Safari on a phone hunted through one eleven times over before it played.
A film in a codec the television is unlikely to
have — AV1, most often — is converted the moment playback moves there,
since otherwise it arrives as sound with a black screen behind it. **A
soundtrack no television decodes** — DTS and the other cinema formats,
which set makers have been dropping rather than pay a licence for — is
converted before the set is given the film, because that failure is the
silent one: the picture plays, nothing says why there is no sound, and a
television has no menu to put it right. A set that says it decodes DTS is
handed the film untouched. **And a set the search happens to miss is still
offered**: discovery is multicast and can lose a reply — about one search
in seventy against a television that was plainly on — and the button for
it used to vanish for the next minute. Where a device answers the search
and then cannot be described, the log says so rather than leaving the set
to go missing quietly. Older sets that cannot agree with a modern
certificate will still sit on a spinner; that part is theirs.

## A bitrate to suit the connection

a pill at the lower left of the
player, reading *Original*, opens a short ladder: 6, 3 and 1.5 Mbit/s,
showing only the rungs that would cost fewer bits than the film itself.
A film plays at its own rate by default, whatever the link, and a phone
recording at 10 Mbit/s over a link carrying eight stalls no matter what
the player does; pick a rung and the film is re-encoded under that
ceiling from where it has got to, fitted into a smaller picture so the
bits go further, and the choice stays for the session. On browsers other
than Safari the converted stream is fed to the player by the page itself,
which is what keeps a lower bitrate from costing more link than the
original — a browser fetching such a stream on its own re-reads it from
the start every time its buffer fills. While a rung is playing the
browser's own AirPlay and cast buttons are away, since those hand a set a
URL and this stream has none; a television over DLNA is still offered.

## Hover a film and it moves

the pointer resting on a tile plays ten
frames from across the film in place of the still, a five-second tour of
what is in it. It is one small image and no stream at all; the play badge
stays on top of it, the still comes back when the pointer leaves, and a
touch screen — which has no hover — is left alone.

## Delete from the disk, after seeing what goes

right-click a card (or
long-press it on a touch screen) for *Delete file…*, *Delete release…*,
*Delete show…* or *Delete season…*; the player has a delete button beside
Download and a release's sheet has *Delete release…* in its menu. Nothing
is removed until the confirmation has shown exactly what would be: every
file, every folder that goes whole, how many and how much, and anything
else sharing an archive or a disc with it. A film sharing its folder with
other things takes only its own file and the subtitles named for it —
never another film's, even one whose name begins the same way — and the
folder and everything else in it stay. A film's release folder goes
with it only when what is left there is the release's furniture — an
.nfo, checksums, subtitles, cover art, a sample — and a folder holding
anything else keeps it; a root of the library is never removed. A film
inside a rar set or on a disc goes as the set or the disc. What is
confirmed is exactly what goes: a file that changed since it was shown is
left alone. Folder confirmations include sidecars and empty directories;
a replacement file or a new arrival invalidates that folder’s plan.
Only the whole library can delete — not a face restricted to some media,
not a caller confined to part of the disk, and not a server started with `-lock`.

## The frame under the pointer on the seek bar

hovering the player's
seek bar, or dragging along it with a finger, shows the frame at that exact
moment, to the millisecond, with the time under it. The server takes that
one frame then and there (about half a second for a 1080p film, a few for
4K on a busy machine) and nothing waits for it: the box and the time follow
the pointer at once, the last picture stays up dimmed until the new one
arrives, and a request the pointer has left behind is dropped. The box is
sized to the player and the picture — a fifth of a wide player, a portrait
clip bounded by its height, turned with the film — and asked for at the
screen's own density. A DVD title shows the frame where its reading
enters rather than at the exact moment, its clock not being continuous.

## Video player

custom controls: play/pause, seek with buffer display,
±10s skip, volume/mute, playback speed, rotation for sideways footage
(`r`/`R`, remembered per file and shared across devices, since a clip shot
sideways is sideways everywhere), a picture-shape menu for a file made at
the wrong shape (a widescreen film squeezed into a 4:3 frame shows at
16:9 again, remembered per file the same way), trimming of black borders that are baked
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

## Image lightbox

full-resolution viewing, fullscreen (`f`), arrow-key
navigation, neighbour preloading, double-click zoom, rotation (`r`/`R`),
download. Drag sideways to move between pictures — both are on screen
during the drag — and flick down to dismiss.

## Music

mp3/flac/m4a/ogg files are tagged (ID3 & friends, read in the
background) and grouped into **albums** by directory; `.m3u`/`.m3u8`
playlists become albums too. A persistent bottom player provides
queue/shuffle/repeat, a queue panel, and OS media-key integration, a thumb up and a thumb down for the playing track, radio and artist radio, the spectrum
and a link to the track — the last four under a "⋯" menu on a phone, so
the title keeps its room —
play/pause, stop (which pauses where it is, and resumes when pressed
again), previous and next, and the lock screen's scrubber. A queue goes
on from track to track on a locked iPhone using the same audio element
started by the listener's tap. The bar loops at the boundary and replaces
its source immediately, keeping the next track on that element instead of
trying to start a second player in the background. It never starts and
stops a spare player to obtain permission, which could disturb playback
over Bluetooth. A pause from the lock screen takes effect at
once. On a phone the AirPlay button is always offered, its picker listing
the phone's own outputs too, and the page never searches for receivers
itself. An album
or playlist opens as a sheet listing its tracks, numbered once rather than
twice — the "01." a file carries in its name is dropped, unless the digits
are the title; because the sheet covers
the bottom bar, it carries its own transport: the button pauses and resumes
the queue that sheet started, and otherwise plays the album from the top.
Whichever track is loaded is marked in the list, wherever it was started
from, and tapping a track starts the album at that track. The play badge
on an album or playlist card starts it without opening it. On desktop,
the next track buffers in a second element while the current plays, so
the boundary is a change of deck rather than a fresh
request — and the preloading stands aside whenever the track being
listened to still has fetching left to do. On iPhone and iPad the same
player loads each track in turn, so a boundary can include a short buffer
wait. The sleeve is fetched ahead, so it changes with the track; and
another release's is never left standing under a new title while the next
one loads. A spectrum view draws what is
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

## Metadata

tags (title/artist/album/genre/track/year) and playing
time are read in the background (pure-Go header parsing for mp4/m4a/mov,
mp3, flac, ogg/opus and wav; `ffprobe` for the rest when available) and
persisted in the blob database, so restarts don't re-read the files.
What you open is read first — the page of the grid you are looking at,
the album you just opened, the file you started playing — so metadata
appears where your attention is instead of in scan order.
A reading is only ever written down against the bytes it was read from.
Reading a file takes time — a probe's own ceiling is half a minute — and a
download finishing inside that window leaves the reading describing the
part that had arrived, under an identity that never changes again: a
ninety-minute film carrying twelve minutes on its tile, in the length
sort, in the resume rule and in the offset its thumbnail is taken at, for
as long as the file sits there. Where the file moved under the reading,
the reading is discarded and the file read again. And a read that ran out of
time is not an answer either, so it is not written down as one — a film
whose probe was cut short on a busy or network-mounted disk would otherwise
carry no duration and no codecs in every listing from then on. It is left
alone for the rest of the run instead, one attempt per file, and looked at
afresh the next time the server starts: without that, every listing page
holding it asks again, at half a minute of probing apiece.
**Hovering a tile says what the file is**: the codecs, the picture's size
and frame rate for a film, the dimensions of a photograph, the format and
average bitrate of a track. Those are read from the file's own header — a
still's from the few hundred bytes that describe it, a film's from its box
tree — rather than by running a probe per file across the whole library,
and they are written to the database so they are read once. The containers
with no reader of their own here (Matroska, AVI, the transport streams) do
cost an `ffprobe`, but once each rather than per page, and the result is
kept with the rest. **A release's card does the same for the whole
release**: where it is kept — its folder, the one above its disc folders
for a release split over several, or the playlist file — and what it is
made of: the format its tracks are in (every one, where a release mixes
them), what it averages, its size on disk and its running time. The open
release says the same over its title. The rate is the whole release over
its whole length, so it appears once every track has been measured and
not before. A caller confined to part of the library (`X-Allowed-Paths`)
is not told where a playlist is kept when that is outside what it may see.
Album and track views prefer tags over the filesystem: ID3 titles instead
of filenames, tag track numbers, per-track and total playing time instead
of byte sizes, and the album's year and genre in its header. Anything not
tagged falls back to the filename and size. A release that carries **no
tags at all** is filed under the directory above it — music is kept as
performer, then release, then tracks, so the answer is usually sitting one
level up — but only where that name is a performer the library already
knows from tags elsewhere. That keeps folders called "complete" or
"EP, Single, Demo" from becoming artists, which is what a folder above an
untagged release turned out to be more often than not. **Its tracks are
then credited to it**, so the queue names the performer the release is by
rather than leaving the column blank, and **a name that had to come from
the file is tidied on the way to the screen**: the extension, the leading
track number the list is already numbering, a leading performer prefix
where it matches the performer the library knows, and a trailing bitrate
marker. So a row that read "PERFORMER - 01.Some Title_320.mp3" reads "Some
Title". Only where the file carries no title tag — a tag is what somebody
wrote and is shown as written — and only the rates that exist are taken
for bitrates, so a track called "1979" or "Summer of 96" keeps its number.
Nothing is written back to the files: this is how the library shows them,
not a retagger. Panels stay live — metadata
appears as background enrichment reads it, without reopening. Tags are
normalised where they enter the index: trimmed of whitespace and of the
byte-order mark a UTF-8 file leaves at the front of a value, and a year
frame holding a whole date is cut back to the year, so sorting by year
does not put a release from the year twenty million at the top.

## Shortlinks

a button on the top bar, in the player, in the picture
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

## Files are recognised by content where the name says nothing

some
downloads arrive with no extension, with a dotted name that only looks like
one, or with a suffix cut short, and an archive is one whatever it is
called. Such a file has its first bytes read and is indexed as what they
say: a film, a song or a picture, or a ZIP or RAR archive whose contents
join the library as any archive's do. Only files from a megabyte up are
opened, and never a download still being written (`.part` and the like),
so it costs a few hundred header reads across a whole library rather than
one per file.

## Plays anything

a file the browser will not open is converted, and
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
A picture that decodes is left alone on that path: iOS decodes a segmented
stream outside the page and reports no frames for it, and reading that as
"nothing decoded" used to escalate a soundtrack conversion into a
re-encode of every frame, which on a busy machine is slower than playback.
**On Safari a conversion is served as HLS** — short segments and a
playlist, which is the only shape that browser will play, since it opens a
media URL with a byte-range request and a conversion of unknown length
cannot answer one. Playback starts after the first segment rather than
after the whole file: a two-hour film whose audio needs converting is
playing in under a second. **The playlist is the whole film from the
first request**: every segment is decided before it is made — on the
file's own keyframes for a copied picture, read off the container's index,
or on a four-second grid the encoder is held to for a re-encoded one — so
the player shows the film's real length and position, iOS's own fullscreen
player included, and seeks anywhere in it natively; the segments are made
as they are asked for, from wherever a seek lands — and a file whose seeks
land after the moment asked for (some Windows Media files' index does this)
is seeked earlier and trimmed to the moment, so it seeks as well as any
other; a film whose picture begins seconds after its sound is held to the
same grid from its first segment. Where the container
keeps no keyframe index this can read (anything but Matroska and MP4) a
copied conversion falls back to a playlist that grows with it, and the
player is told which it got (`X-Media-Timeline`). Everything else is
streamed straight from the converter as before, and seeking reopens it at
the new position — on the keyframe at or before it, where a copied picture
has to begin, with the sound and the subtitles starting there too: the
server reads where that keyframe is and what it is, and cuts both streams
at it, so a seek no longer leaves the sound and the subtitles running
seconds ahead of the picture. That holds for films inside archives as
well, which are measured through the server's own stream. Where the
film has subtitles the playlist is a master carrying them as renditions —
which is what puts them on an **AirPlay receiver**, that route handing
over a URL and nothing else, and gives Safari one native subtitle menu
inline, in fullscreen and on the receiver alike. One conversion serves
every choice; picking a subtitle only changes which rendition the
playlist marks default.
Converted files are kept — until the space is needed, and across a restart
— so a film converted once is not converted again. A conversion that
*failed* is not kept: it is forgotten, so asking for the same film at the
same point again is a fresh attempt rather than the same error played back
for the life of the process. What a failed attempt did manage to write is
kept wherever it is playable, since starting over begins by clearing the
directory — worth doing only when nothing came of the last try.

## Soundtracks

a film carrying several (a Nordic release with four
languages, a disc with a commentary) gets a menu in the player: pick from
it, or cycle with `a`. The choice
is remembered for that film, and the language is remembered for every
other: pick Swedish once and the next release carrying Swedish starts in
it. Left alone it takes the file's own default, and never a commentary
track unless that is all there is. Because a browser cannot be told which
stream to decode, choosing one costs nothing where the browser can switch
its own tracks (Safari), a lossless rewrap where a copy will do, and the
converter otherwise.

## Subtitles

external `.srt`, `.vtt`, `.ass` and `.ssa` files sitting
next to a video are offered in the player: pick one from the CC menu, or
cycle with `c`. A language
or flag appended to the video's name — before the subtitle extension —
becomes a labelled, language-tagged track. Everything is converted to
WebVTT on the fly, and non-UTF-8 files (Latin-1 is still common) are
transcoded so accents survive. The chosen language is remembered. Cue
times follow a converted stream, which starts at a keyframe rather than
where you seeked, so subtitles stay in step after skipping around.

## Archives: rar sets and zip files

the media inside them is indexed
and played without extracting anything by hand. RAR4 and RAR5, plain and
split across volumes, and zip — in one file, or split into parts either
way an archive is split: `name.z01 … name.zip` as an archiver writes them,
or `name.zip.001, .002 …` cut from one archive, including pieces a file
host has renamed apart with an identifier of its own. A part that is
missing costs only the members whose bytes were in it, and every member's
own header is checked against the archive's directory before it is
served, so a wrong offset is a member left out rather than another
member's bytes under its name. A member *stored* as it is — the classic
multi-part set holding one huge video — is streamed straight out of the
volumes with full seeking, and a viewer keeps only a handful of volume
files open however many parts the set has. A *compressed* member (rar's
own compression, or zip's deflate) is unpacked as it is read for
everything the server reads for itself — tags, picture sizes,
thumbnails — and, when a film of 32 MB or more is played, unpacked once
into the scratch space the conversions use (`-tmp`, `-tmp-max`) and
served from there with full seeking; the copy is checked against the
archive's checksum and kept across restarts until the budget needs the
room. A member whose bytes do not unpack — a download that went wrong part
way — is said to be damaged, in those words, and not unpacked again for
the rest of the run. Decompression is native Go (klauspost/compress for
zip, rardecode for rar). Members of a *solid* rar archive (compressed against the members
before them), encrypted members and zip compression methods other than
deflate are skipped, and `-debug` says which and why, so a release that
does not appear can be told from one that was never scanned. **An archive
holding more than `-archive-max` media members (1,000 by default) is left
out whole** — a dataset rather than a collection, like a gigapixel
panorama shipped as two hundred thousand tiles — and the log says so once.

## Formats

video: mp4, mkv, webm, mov, avi, m4v, mpg/mpeg, wmv, flv,
3gp, vob, the transport streams a capture or a camcorder writes (ts, mts,
m2ts), and the older wrappers (divx, f4v, ogv, rm, rmvb); images: jpg,
jpeg, jfif, png, gif, webp, bmp, svg, avif; audio: mp3, m4a, flac, ogg,
oga, wav, aac, opus, wma; playlists: m3u, m3u8. Whatever the browser
cannot open is converted (below), so the list is about what is *found*,
not about what plays.

## Spectrum analyser

a quarter-octave analyser for the music bar and
for the video player alike, shown above the player's controls and fading
with them. On a browser that cannot copy a playing element's sound
(Safari), opening it on a film ends AirPlay for that film until it is
reopened, which the button says before you press it; where the sound can
be copied, and for casting to a television, it is unaffected.

## Downloadable DVD titles

a disc is authored from pieces that each
start their own clock, so a title downloaded as one file reports minutes
instead of hours and cannot be seeked. (That is the disc, not the export:
a plain `cat` of the VOBs does the same.) The timestamps are corrected as
the file goes out, using the disc's own cell table — same bytes, same
length, ranges and resuming intact.

## Hardware conversion where it is needed

a phone's 4K video is more
than a processor can convert while you watch it (measured: 0.41 of real
time, which stalls for ever), so those go to the graphics hardware
instead — 2.3 times real time, and at half the bitrate. Everything a
processor handles comfortably stays with it, because fixed-function
encoders spend far more bytes for the same picture: on a DVD, five times
as many. VAAPI, Quick Sync, NVENC and VideoToolbox are all tried, each
proved by a real conversion before it is used, and a machine with none of
them works exactly as before.

## Thumbnails from the film, not its titles

a video's tile is a frame
from a tenth of the way in, wherever the video lives, because the opening
is the distributor's logo and the title card. Tiles already made are kept
as they are; new and changed files get the better frame.

## Deinterlacing

a PAL DVD is 576i, and a browser shows those combed
fields as horizontal teeth on anything that moves. The software converter
and every extracted thumbnail deinterlace; progressive files are
untouched, since only the frames the file itself flags are processed. The
hardware converter does not deinterlace, deliberately — nothing large
enough to reach it is interlaced.

## HDR to ordinary colour

a 4K release is often graded in wide colour
with a perceptual curve (HDR10, or Dolby Vision over it), and an H.264
stream that keeps that description is refused by some players. When such a
film is converted its picture is tone-mapped back to ordinary BT.709 —
on the processor, since the graphics engine's own tone-mapper produces no
picture on the hardware tested.

## DVD images and folders

a disc image (`.iso`, `.img`) holding
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

## One library, several faces

a request header (`X-Media-Content:
music`, `videos`, `images`) restricts everything a caller is shown to that,
so one server and one scan can be a music library on one hostname and a
video library on another. Enforced in the backend, down to a direct link;
see [One library, several faces](deployment.md#one-library-several-faces).

## What is running

the foot of the preferences shows the build (version,
commit, when it was made) and what this machine turned out to be able to
do: whether video is converted on the graphics hardware or the processor,
and anything missing that would explain odd behaviour — no ffmpeg, no
database, no loopback address.

## Preferences

the directories being indexed can be changed from the
UI without restarting: add one and it is walked and watched, remove one
and everything under it leaves the index. The choice is remembered, and
outranks the command line, which seeds the first run. `-lock` makes the
dialog read-only for a server that should not be reconfigured over the
network — there is no authentication in front of any of this.

## Download a release

an album's sheet offers the whole thing as one
zip: the audio, the sleeve art, the notes — a release is a directory, not
a track list. A playlist has no directory of its own, so for one of those
it is the entries it names. Multi-disc releases include the parent folder’s
artwork and preserve the disc folders. A view restricted by content or
path receives only its permitted tracks. An interrupted download is left
incomplete, so it cannot silently appear to be a complete release. Hidden
files and directories and symlinks are omitted. Releases exceeding 5,000
files or three levels of files are refused before the download starts.

## Add to queue

the sheet can also put a release after everything
already queued: in order, or shuffled among themselves when the bar is
shuffling, and never mixed in among what was already waiting. With
nothing playing it simply plays; a queue that had run out moves on to it
at once.

## Queue all

a button in the toolbar of every music view puts the whole
view after what is already queued: every release listed, every release of
every performer listed (each played through from their first), every
release in every genre listed (each once, performer by performer), or
every track the music listing shows. One request — the server flattens the
view into its tracks — and there is no limit worth the name: the whole
library goes into the queue and is shuffled there, the queue panel drawing
a window of it rather than every row, its columns lined up whatever the
tracks' lengths. **Resting the pointer on a row says
what the row has no space for**: the sleeve, the release, the year and the
genre, with the format and bitrate under them. It is the rule every hover
here follows — what you are shown is what the row is not already showing,
so a grid tile answers with its path and its codecs instead, and a track
inside an open release, whose header already names the record, answers
with the codecs alone. The server gathers it a page at a
time and every page has to come from one version of the library: an offset
names a row in a particular sorted result, and this library is written to
constantly by design — a download moves the version dozens of times a
second, and under the default order a file that merely grew moves in it.
Measured before that was checked, one collection came back with 3047
tracks in it of which 3004 were distinct, and rows at the far end were
never fetched at all. A collection that spanned a change is taken again
from the top, once; a library being written to continuously will move
under a third pass exactly as it moved under the second, and a page's
worth of drift in a million-row queue is the cheaper wrong.

## How the music sounds

in the background, below everything else the
server does and below you: it stands down for a few seconds whenever
anybody asks this server for anything, so a page you are looking at is
never waiting behind it. Each track is decoded once (three twenty-second
windows from the middle of it) and described by fifty-six numbers: timbre, brightness,
harmony, loudness and dynamics, tempo, pauses. Written down beside the
thumbnails, read again only when the file changes, about a second of one
core per track. A track is left alone until something is known
about it — its length, the windows being fractions of it, or simply that
the tag pass has already been through it, which is what lets a file whose
length nobody could measure be read from fixed marks instead; and a read
the clock ran out on is offered
again an hour later rather than written off for the run, which would have
left that track out of radio, out of the resemblances and out of its own
release's sound. Four things are read off it:

- **How fast it goes.** Each track's tempo, in beats a minute: on its tile
  beside the length, in a release's sheet beside each track's length, in
  the music bar beside the performer, and on hover — and a listing of
  tracks can be ordered by it (*Tempo*): the Music chip's, and on a face
  that shows only music, All and Popular. A track with none comes last
  whichever way the order runs. It is read from the sound rather than the tags:
  across the library measured, 587 of 28,767 files carried a BPM tag at
  all, 447 of those said nought and 46 said 320, a bitrate in the wrong
  field. Read against the tags that were plausible it agreed within one
  beat a minute on most, and like every tempo estimator it sometimes reads
  half or double what a drummer would count. Nothing is shown where
  nothing steady is struck — a sustained chord, a drone, a pulse too faint
  to trust — nor for a reading. A library analysed before this reads each
  track once more for its tempo, in the same background pass and at the
  same second or so a track; its vectors and resemblances are left exactly
  as they were meanwhile.

- **Radio, and similar tracks.** The bar's radio button keeps the queue
  going with the tracks that sound most like the one playing, fetching a
  batch whenever fewer than a handful remain. It draws from a wider field
  than it takes, nearest likeliest, so an evening does not repeat itself;
  and it never brings back a song already in the queue, in that file or in
  any other copy of the same recording. It also spreads itself across
  performers: what sounds most like a song is usually the same band, so
  each of their tracks makes the next one less likely, and a batch of ten
  comes out with half a dozen names in it. Every queue is folded that way, not
  only radio's: queueing a search or a discography holds one place for each
  recording, keeping the first, while a performance tagged as its own
  ("… [Live]") stays. Radio goes one step further and holds one place for
  each *song*: a live take, a demo, a remaster, an instrumental or a cover
  of a song already queued is that song again, and so is the same title
  spelt with or without its accents, apostrophes and full stops. A track called
  "Untitled" or "Track 3" names no song and is folded with nothing — a
  performer's untitled pieces are as many songs as there are of them.

- **Artist radio.** The button beside radio, and *Artist radio* on a
  performer's page, keep the queue going with one performer's songs and
  nobody else's: the next ones are drawn, nearest likeliest, from those
  of theirs that sound most like the one playing, and nothing the analysis
  has not reached is left out. A performer's catalogue is where the same
  song turns up most — measured on this library, 28,158 tagged tracks are
  14,938 songs — so each song comes once, however many copies and takes of
  it there are, and when every one of them has been queued the station
  says so and stops rather than play one again. Started from the bar it is
  the playing track's performer — the name in its own tag, with any
  "feat." credit taken off, or for a file with no artist tag the
  performer its release is credited to; a split partner's track is the
  partner's, not the band the split is credited to — and it follows
  whatever is queued next; started from a performer's page it
  opens on one of their songs, leaning to what you liked and played.
  Radio and artist radio are one setting: turning one on turns the other
  off.

- **Similar releases and performers.** A release's sheet offers *Similar*,
  the releases that sound most like it, each saying how alike; a performer,
  once drilled into, offers *Similar artists*. Nearest first, a chip back.

- **A song says which release it is on.** The album under a track's name is
  a link to that release's sheet, beside the performer and the genre that
  already were.

- **A verdict reaches what sounds like it.** A track that sounds like one
  you liked is lifted in the popular orders, above anything merely played,
  and one that sounds like a dislike is sunk. Your own verdicts still
  outrank it, and the tiles show only those: the resemblance ranks, it is
  not drawn.

## Audiobooks

a reading is told from music by the sound of it: a reader
pauses between sentences where a band plays through, alternates voiced and
unvoiced sounds, and keeps no tempo. The rule was set against real files
and the two sit far apart; a track needs a full window of sound (nineteen
seconds) to be judged at all, a release votes by playing time once half of it has been
read **and has to win two to one rather than by a hair** — a two-track
record whose speech-scoring song ran forty seconds longer than its other
one is not a reading, and being strict is the safe direction here, since a
book left in the music is merely filed where it always was while a record
taken for a book leaves the releases, the performers, the genres and radio
— an intro on a record is music by its release's word, and a genre
tag that says audiobook (in the usual spellings) shelves it outright.
Audiobooks get a
chip of their own, leave the Albums, Artists and Genres views (a narrator
is not a performer), are never offered as "similar" to a song, and are
found by the word *audiobook*.

## Pause and resume fade

a tenth of a second or so either way, because
cutting an element mid-waveform is a click, not a silence.

## What you actually play, and what you think of it

every item counts
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

## Play a season

the badge on a season card plays it from the first
episode, and a finished episode rolls on to the next by itself; on a
television driven over DLNA the set is handed each episode in turn. The
soundtrack and subtitle language chosen once hold for the whole season,
here and on the set alike, matched by language in each file.

## Skip the intro and the credits

nothing to mark and nothing to
configure: the server finds where a show's opening and closing are from
the sound itself. Every episode of a season opens and closes with the same
audio and nothing else in the season repeats, so each episode's first and
last minutes are fingerprinted and the stretch that recurs across the
season's episodes is the intro, or the credits. Found, a *Skip intro*
button appears while the opening plays and a *Next episode* button while
the credits roll — and that one goes on to the next episode with its
intro skipped as well, since that is what skipping a season's credits
means. `S` takes whichever is on offer. The looking happens in the
background at the lowest priority, once per episode (the fingerprints are
kept), and the season of an episode being opened is looked at first.
`-credits=false` turns it off.

## Series

television grouped by show, with its seasons folded under it:
the chip lists the programmes, one opens its seasons, and a season opens
its episodes in order. There is nothing to tag and nothing to configure —
the show, the season and the episode are read out of the names, taken from
whichever part of the path actually says them, which is usually not the
file (a release names the group as often as the programme). A search finds
a show by its name or by any of its episodes — a word from one episode's
title finds the show, and opening it offers just the seasons holding what
the search found, and in them just those episodes — and the Series chip
counts by the same rule, so it never promises a show the grid then cannot
find.

## A phone-sized top bar

on touch screens the chip counts compact
("210k" rather than "209,857"), the sort controls share the chips' last
row, and the whole bar slides away while you browse, returning on the
first upward flick. Nothing is ever hidden behind an edge; it is one
gesture away.

## Grouped views

the same library seen as **Albums**, **Artists** or
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

## Cards say what is known

a release card carries its performer, year
and genre, and a year written into the directory name ("2018 - Release")
is lifted out of the title and shown as the year instead of twice. A
release whose tags carry no year is dated by where it is kept — its
directory, or a playlist's own name — whether that is spelt "2018 -
Release", "Release (2018)" or the scene's "Performer-Release-WEB-2018-GROUP";
a performer carries their tracks, running time, the years their
releases span and what most of them are filed under; a track carries its
artist, year and genre. Whatever is untagged is simply left out rather than
leaving a gap or a stray separator.

## Names as they were meant

file names and tags that are not UTF-8
(Latin-1 and Windows-1252 are common on disks that have travelled between
systems; Thai in TIS-620 is common in music tagged on Windows) are decoded
for display and for search, so a release with an umlaut in it reads
properly and can be typed into the search box instead of showing black
diamonds. A tag that was written under the wrong alphabet entirely is put
back too: CP1251 keeps Russian letters where Latin-1 keeps its accented
ones, so a Nordic title tagged by a program guessing Russian arrives with a
Cyrillic letter inside a Latin word. That, and only that, is repaired — a
Cyrillic *word* is left exactly as it is. And a name or a tag that some
program read in the wrong encoding and then saved that way — valid text,
wrong letters: "KÃ¶ln" for "Köln", "â€™" for an apostrophe, a Thai name
as a run of "à¸" — is read the way it was meant, in any alphabet, a folder
at a time. The file itself is still opened by the exact bytes on disk;
nothing is renamed.

## Less clutter

a release's sample is skipped when the full version
sits beside it, whether it is a folder or a lone `Sample.mkv` next to the
film, so short excerpts stay out of the library. Never on the name alone:
the real release has to be there and has to dwarf it, or the sample is
kept — sometimes the excerpt is all there is.

## No duplicates

a file reachable by several paths (hard links, or a
symlink beside its target) is indexed once. Identity is the file's
device + inode, not its contents, so genuinely separate copies still both
appear. Symlinks are measured by their target, and a real file always
wins over a link to it. On a library that hardlinks torrents into place,
this removed 355 duplicate entries.

## A file that is not media is said so

a download that stopped half way,
or any container nothing can parse, is recognised when it is first read and
the player says "this file is damaged or incomplete" instead of trying a
copy, a conversion and a pipe in turn and then blaming the browser. A song
like that is left out altogether — no listing, count, release or queue has
it — and so is a file of no bytes, or a placeholder a download reserved and
never wrote, which is the right size and nought throughout. The mark goes
when the file changes, so finishing the download is all it takes.

## Live library

directories are scanned recursively and watched with
inotify; new/changed/deleted files stream to the UI over server-sent events.
A periodic rescan (default 10 min) acts as a safety net. **A file is taken
up when its writer closes it**, not on every write: a watch that reports
writes reports one per write call, at whatever size the writer chooses —
measured here, a downloader writing 46 bytes a call made 464,000 events a
second, overflowed the kernel's queue eight times a second and cost a
whole core for hours, for files that were not even media. So a file still
being written shows the size it had when it appeared until its writer
closes it or the next rescan measures it. When the kernel does overflow,
the loss is logged once per burst and answered with a walk once the burst
ends, `-rescan 0` or not. What the kernel
reports is taken off it immediately and acted on behind it, because the
work behind a single event is not small — a recursive walk of a directory
moved in whole, or a re-read of every volume of an eighty-nine part set —
and for as long as that takes nothing is draining the queue. What the
kernel cannot queue it drops, and a dropped create is a file nobody will
ever be told about again. Subtitle files are reconciled like the media
they belong to, so one dropped beside a film after its directory had been
walked is kept and announced rather than swept away at the end of that
walk — which with `-rescan 0` meant for the rest of the run. The rescan
also *reads* what it finds: a file the watcher never saw used to reach the
listing with no playing time, no codecs and no preview on it, and stay that
way until the server was restarted. With `-rescan 0` there is no safety net
for that either — the watcher is then the only thing that reads a new
file.

## One entry per record

a release spread over `CD1`, `CD2` and `CD3`
is one album, with its tracks running disc by disc rather than interleaved
by track number, and named without the disc marker its tags carry. And a
release that ships its own playlist used to
appear twice, once as a directory and once as the playlist. When both
hold exactly the same tracks only the playlist is kept, since it carries
the intended running order; a playlist covering part of a directory, or
spanning several, remains a collection of its own.

## Artists

a view grouping music by performer, derived from the same
tags the albums view uses, so the two always agree. Each card shows their
album and track counts and total playing time; opening one narrows the
albums view to that performer, with a chip to step back out. The name
under a release is a way in as well — click it and you have the rest of
their work. The view is part of the URL, so a performer's albums can be
linked to directly.

## Pick up where you left off

videos you have started appear under an
**In progress** chip and ones you have watched through under **Watched**,
both counted like the rest and filtered server-side, so they page and sort
like any other listing. The same rule draws the progress bar and the
checkmark on the tiles: five seconds in is a start, and past 96% is
watched, which is also where the player stops offering to resume. Marking
an item unwatched clears its resume point while preserving likes and play history.

## Search / filter / sort

the filter chips count what you are looking
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
as on their own name. A release is found by any track on it too, and a
performer by any release of theirs that is found, so typing a song's
title counts and lists the album it is on and who made it — and the
search stays in force as you move between views and drill into a
performer, until you clear it. What a search names comes first — the
performer called that, the release, the track — then whatever shows the
words on its card, then what was found a page further in, by a track, a
release or the folder something is kept in. Within each group a whole word
beats part of one: "gorse" lists a card reading "Gorse" before one reading
"Gorseland", and a release found by a track titled with the word before
one whose track only holds it. Beyond that, each group keeps the order the
view is sorted by. Results arrive in place: the listing stays up while
the next one is fetched and only what differs is redrawn, so typing narrows
the grid instead of flashing it, and anything the two searches have in
common keeps its tile and its loaded thumbnail. Type filter chips (all / videos / images / music /
albums / artists). What can be sorted on follows the view: files by
modified date (a file stamped with a date in the future, which some copies
leave behind, sorts after every real date rather than leading the list),
name, popularity, size, date added or playing time; **videos also by
resolution and by bitrate**, which are two different questions — the
biggest picture and the heaviest file are rarely the same release; albums
also by artist, year, genre, track count and length; artists by how many
albums, how many tracks and how long. Both also by **date added** — when
the newest thing in them turned up here, which is not when the files were
written: a record from 1994 downloaded this morning is old by one and new
by the other, and the second is what answers "what is new in my
library". A release that carries the value sorts ahead
of one that does not, whichever way the order runs. **Names sort the way
you count** — part 2 before part 10, capitals or not, and a name spelt
with underscores, dots or hyphens beside the same name spelt with spaces —
wherever a name orders anything: files, releases, performers, genres,
shows, and the untagged tracks of a release. Opening a performer
lists their releases by year, newest first unless the order has been
turned round. UI state lives in the URL hash. A change of view goes onto the history, so Back returns from a
drill-down. The grid can be worked from a keyboard: Tab to a tile, Enter or
Space to open it. Dialogs keep focus inside them and return it to their
opener. Nested dialogs keep the listing’s scroll locked until the last one
closes. Sizes are shown in thousands, as a file manager shows them.

## Thumbnails

generated on demand and cached in a single-file blob
database ([bbolt](https://go.etcd.io/bbolt), `<data>/media.db` by default;
relocate with `-db PATH` or skip caching with `-db off`): images
are downscaled in pure Go (EXIF orientation respected), videos use `ffmpeg`
when available, audio uses embedded cover art, a folder image next to the
tracks, or the artwork a release ships under its own name or in an
artwork subfolder. The UI fetches thumbnails a few at a time — visible ones first,
off-screen requests canceled — so streaming and browsing stay snappy while
a cold cache fills.

## Fast at scale

a windowed virtual grid plus server-side paging, a
per-query result cache and O(1) counts keep six-figure libraries smooth:
at 150k files, page requests answer in under a millisecond at any offset
and sort order (the one-off build for a new query costs ~150 ms), and
metadata for a cold 135k-track library is read in ~20 seconds. The album
and artist lists revalidate with ETags, so an unchanged list costs a
304 and no body. The index is mirrored into the database, so a restart
serves the whole library in under a second instead of waiting for the
walk; the scan still reconciles in the background, and anything that
disappeared is dropped along with its cached thumbnails and metadata.

## Streaming

all media is served with HTTP Range support
(`http.ServeContent`), so seeking is instant and nothing is buffered
whole-file.
