# Network access, proxies and casting

[Documentation](README.md) · [Docker setup](getting-started.md)

- [Signed media URLs](#signed-media-urls)
- [Playing to a television](#playing-to-a-television)
- [Restricted views](#one-library-several-faces)

## Signed media URLs

Anything that fetches media without a browser behind it — an AirPlay
receiver, a casting device, a player handed an exported m3u — is given a URL
and goes and gets it with no credentials at all. Behind a proxy asking for
HTTP Basic authentication it is refused before the request reaches this
server, and the television sits on a spinner while the access log stays
empty.

The page and exported playlists use signed media URLs. The page refreshes
its credentials before expiry and when it becomes visible again; exported
playlists retain the token’s expiry and must be exported again after it passes.
The URLs take this form:

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
    proxy_set_header Host $http_host;              # shortlinks are scoped by it
    proxy_set_header X-Media-Content music;   # if this face is restricted
  }

  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $http_host;
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

New database files are created with owner-only permissions (`0600` on Unix).
Existing files retain their permissions; when upgrading, use
`chmod 600 data/media.db` (or your `-db` path) if other local users should not
read the library state and signing key. Protect database backups the same way.

The service's debug access log redacts tokens from request and redirect paths.
Configure proxy access logs with the same care: a signed URL is a credential.

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
are found by the same probe that lists the soundtracks, extracted and
cached, and served through the same conversions a sidecar gets. The
extraction reads the whole container for a hundred kilobytes of text, which
settles three things about it. One read takes every text track the file
carries, so changing language afterwards reads nothing again. It outlives
the request that asked for it, since the player re-points its subtitle track
at a new clock offset on every seek. And at most two run at once, this being
the read here that goes through gigabytes, each given time in proportion to
the file.

Subtitles go with a film, converted to SubRip on the way out, since that is
the format sets read and WebVTT is not — sidecars and the tracks inside the
file alike, under the same numbering the player's menu uses. The set draws
one or none — it has no menu to choose from — so the one the player is
showing is the one that is sent, and turning them off in the player sends
none. A set fetches the subtitle as it opens the film and does not start
until it has it, so one carried inside the file is read out before the set is
handed anything — for a large film a minute or more, which the player counts
out as *Reading the subtitles* — and one that cannot be read is left out, with
a note saying so. The soundtrack choice travels the same way: the one the player is
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
is what answered, and nothing answered. A search a client gives up on is
given up with it — the sockets are wound up there and then instead of running
out their window against the next client — and what it had heard by that
point is not written down as the answer, so the next client searches rather
than being told the house has no televisions in it.

**A search that misses a set has not lost it.** A reply is a datagram and a
description is a fetch, either of which can go missing while the television
carries on playing the film, and the picker's list is only ever what the last
search actually found. So an id a client is already holding is resolved
against every set this server has been told about: a television one round
failed to see can still be paused, seeked and handed the next track. One that
really is off fails the command instead, which is what the viewer is told
about anyway.

**A television busy where it is is stopped first.** A set's player can be
left somewhere it takes no new file from — measured, one handed a file it
could not open sat for minutes in a state whose only available action is
Stop — and there a cast is refused ("Transition not available") and used to
work only on the second press, after the page had stopped the set. The
server does what the set asks now: on that refusal it stops the set and
hands it the file again. A set still refusing after that is reported as busy
straight away, since its own remote is what reaches it. A file this server
has already found damaged or incomplete is never handed to a set at all, and
the player going on to the next video by itself — a film ending, "Next
episode", playing a season — passes over one; a swipe still lands on it.

**Two people can reach for the same television**, and nothing here stops
them: the set answers whoever spoke to it last, and somebody with the remote
in their hand is assumed throughout. What this server does about it is make
the newest press the one that drives. A second play request cancels the
first, which stops talking to the set wherever it had got to and answers
`409` rather than reporting a fault of a television that refused nothing. The
claim is taken **before** the preparation rather than at the first command to
the set, and that ordering is the whole of what it is worth: nearly all the
waiting is above that first command — the probe, and, where the chosen
soundtrack means a copy of the whole film, minutes of copying that the page
does not cancel when it changes its mind. Claimed at the first command, the
order the sets were taken in was the order the copies happened to finish in,
so a film abandoned in favour of another could put itself on the set minutes
later, at its own resume point. The copy is not thrown away; it goes on for
whoever asks for it next.

Note the posture, which is the same as the rest of this server: **there is no
authentication**. Whoever can reach the port can start something playing on a
television in the house.

## One library, several faces

A face can also be restricted to **part** of the library with
`X-Allowed-Paths`, naming absolute directories separated by commas. Use CSV
quoting for a literal comma, such as `"/srv/music, live",/srv/archive`, or
repeat the header with one literal path in each field. Embedded newlines
are not valid HTTP header values. The request then sees only what
lives under them — in listings, in counts, in the albums, artists and genres,
and when asking for anything by id — and such a caller is refused the
preferences outright, since the list of scanned directories names the roots
of a tree they have been given one branch of. Unset means the whole library, and the two
headers compose: a request carrying both is shown the intersection.

An invalid nonempty restriction fails closed with 403: a content header must
name at least one supported class, and a path header must be valid CSV (or
repeated literal paths) containing at least one absolute path. Relative paths
are ignored when valid absolute paths are also present. Both content-restricted
and path-restricted views are refused directory preferences, including reads.

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

Media responses also advertise both restriction headers in `Vary`. Each HLS
segment, child playlist and subtitle request rechecks the current item and
request scope, including signed links and sessions restored after a restart.
Collection ETags change across server restarts to prevent old version counters
from validating a different listing.

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
    proxy_set_header Host $http_host;
    proxy_set_header X-Media-Content music;
    proxy_pass http://127.0.0.1:8080;
  }
}
```

`proxy_set_header Host $http_host` is worth setting on every face: without it
nginx sends the **upstream** address instead, so every face looks to the
server like one host and shortlinks stop telling them apart. `$http_host`
also preserves the port for same-origin writes on nonstandard ports.
`X-Forwarded-Host` is honoured too, for a proxy that prefers to say it that
way.

The UI adapts: a music face offers the library, albums and artists, a video
face just the library, and a link to a view that is not there opens the
listing instead of an empty screen.
