# Troubleshooting

[Documentation](README.md) · [Get help](../SUPPORT.md)

## First checks

| Symptom | Check |
| --- | --- |
| The page will not open | Run `docker compose ps` and `docker compose logs --tail=100 mediator`. Check the published port. |
| The library is empty | Check `MEDIA_DIR` in `.env`, the media mount and read permissions. A first scan takes time. |
| Compose says `MEDIA_DIR` is missing | Copy `.env.example` to `.env` and set an existing absolute directory. |
| No image matches the host architecture | The published image is for amd64. See [platform support](getting-started.md#platforms-and-requirements). |
| Directory preferences are read-only | The quick start uses `-lock`. See [changing directories](getting-started.md#change-the-library). |
| Thumbnails or conversions are missing | Check capabilities in Preferences. Native installs need `ffmpeg` and `ffprobe`; the image includes them. |
| A television is missing | Check that it is on, on the same network, and reachable over IPv4 multicast. Docker bridge networking can prevent discovery. See [casting](getting-started.md#play-on-a-television). |

When asking for help, include the build from Preferences or `/api/info`, your
installation method, browser and redacted logs. Never share signed media URLs
or real library names. The [support guide](../SUPPORT.md) links the right form.

## A download or a stream stops part way.

Run with `-debug`: every request
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

## Stopping takes five seconds, and with a page open it takes all five.

`shut down with connections still open` in the log at info level is that, and
not a fault: the drain waits for the requests in flight without cancelling
them, and one browser sitting on the library holds the event stream open
until it is cut. The order is what matters for what survives. The server
stops accepting, the requests in flight get their five seconds — a player
watching a segmented conversion goes on being served out of it for the whole
of them — and only then do the playback positions and the mirrored index
write their last and the database close. So a position saved by a request
answered during the drain is kept, where it used to be acknowledged to the
browser and then dropped: both stores flushed and returned the instant the
signal arrived, before the server had so much as been asked to stop.
Before the final store commits, the analysis gets two seconds to notice it
has been cancelled — `the analysis was still reading at shutdown` says it did not, which costs
one track its vector and nothing else, since it is simply read again next
run. It is a bound rather than a wait because the pass sits behind the
first walk, which cannot be interrupted: waited for outright, a signal
arriving during a cold start would hold the process open for the whole of
that walk.

## A release inside a rar set or a zip archive is missing.

If the whole
archive is missing, look for `archive left out: it holds more media than
-archive-max allows` in the log, which names the archive and how many members
it holds; raise `-archive-max` (or set it to `0`) to have it. Otherwise, with
`-debug` the log says why, once per archive and not per member or per rescan:
`rar set holds members it cannot serve` (or `zip archive …`), with how many
and the reason — solid (compressed against the members before it, which
cannot be unpacked one by one), encrypted, a zip method other than deflate,
or incomplete, with how many bytes of how many have arrived or which part of
a split zip is not there. A split zip with its directory part missing (the
`.zip` of a `.z01` set, the last piece of a `.zip.001` set) cannot be read
at all and says `zip parse failed`. A film that is listed and will not play,
the player saying *the archive it is in is damaged*, is a member whose bytes
do not unpack; `could not unpack an archive member` in the log has the
decoder's own words.

## A film stutters in the browser and plays smoothly in VLC.

Usually the
stream lies about how far it reorders its frames: it declares a depth the
encoder did not keep to, ffmpeg and VLC buffer past it, and a browser that
takes the declaration at its word shows frames early and drops the late ones.
The server reads the opening of every film when it is opened and, where it
finds this, has the player convert the film instead of playing the file as it
is — the log says `picture must be re-encoded rather than copied`. That costs
a re-encode for exactly those files and nothing for the rest.

## Music stutters or stops on a phone.

The page reports to the server's
log what a listener hears and nothing else would record: `audio-stalls`, one
line per track that ran out of data or whose own clock fell behind (how
many times, for how long in all, how many with the page out of sight, and
how many seconds its clock lost), and `audio-refused`, a track the browser
would not start without a tap. A track that played cleanly says nothing.
Stalls or a lagging clock show that playback could not keep up; these
measurements alone cannot distinguish the network, decoding, or audio
output. Stutter without either can still be a browser audio-session or
Bluetooth problem. The server's own request log cannot tell them apart
behind a reverse proxy, which takes each file whole in a moment and feeds
the phone from its own buffer.

## A film never starts, and the player says the file cannot be read.

That
is the disk, not the server: the log carries `stream open failed … err="…:
input/output error"` (or `permission denied`, or `no such file`), and the
same error from the conversions that tried afterwards. A filesystem that has
shut itself down after an error — XFS does this — keeps its mount and answers
every access with an I/O error until it is unmounted and repaired; the rescan
logs `scan error path=/mnt/… input/output error` every ten minutes while it
lasts. The files on it stay in the listings on purpose: a root that fails to
read is protected from reconciliation, so nothing is forgotten while the disk
is away, and it all plays again the moment the mount answers. The tiles come
back with it: a thumbnail that failed is remembered for ten minutes and no
longer. Nothing in its key — the id, the modification time, the size, the
width — changes when a disk returns, so a failure remembered for the life of
the process left the files playing again under grey tiles until the server
was restarted; ten minutes is long enough to damp the retry storm the memory
is there for, since the grid asks again for every failed tile on every change
to the library.
