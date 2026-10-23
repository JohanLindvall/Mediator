# Mediator

**Your video, music and pictures, in one self-hosted web app.**

[![CI](https://github.com/JohanLindvall/Mediator/actions/workflows/docker.yml/badge.svg?branch=main)](https://github.com/JohanLindvall/Mediator/actions/workflows/docker.yml)
[![Release](https://img.shields.io/github/v/release/JohanLindvall/Mediator)](https://github.com/JohanLindvall/Mediator/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Point Mediator at your folders and browse, search and play from a browser.
It watches for new files, remembers playback positions, plays media inside
archives and DVD images, and finds music that sounds like what you enjoy.
The web app is embedded in one Go binary. No account or database server to set up.

[Get started](#quick-start) · [Documentation](docs/README.md) ·
[Get help](SUPPORT.md) · [Contribute](CONTRIBUTING.md) ·
[Releases](https://github.com/JohanLindvall/Mediator/releases)

## Quick start

Install Docker, then run this on an **amd64 / x86-64** host. Replace
`/path/to/your/media` with an existing absolute directory:

```sh
docker run --rm --name mediator -p 127.0.0.1:8080:8080 \
  --mount "type=bind,src=/path/to/your/media,dst=/library,readonly" \
  -v mediator-data:/data \
  ghcr.io/johanlindvall/mediator:latest -lock /library
```

Open **[localhost:8080](http://localhost:8080)**. Files appear as the first scan
progresses; thumbnails, tags and music analysis fill in afterwards. Click a
card to play, use the search box to filter, and open Preferences for build
and playback capabilities. Stop with `Ctrl+C`; the data volume keeps your state.

This example binds to your own computer, mounts media read-only, and uses
`-lock` to disable deletion and directory changes. **Mediator has no built-in
authentication.** For phones, TVs or other computers, follow the
[network setup](docs/getting-started.md#access-from-other-devices).

Want a persistent service? Use [Docker Compose](#docker). Building from source?
See [Development](#development).

## Features

| What you want to do | What Mediator provides |
| --- | --- |
| Watch video | Resume across devices, subtitles, soundtrack selection, playback speed, picture-in-picture and a bitrate selector. |
| Play on a television | DLNA from the server, plus AirPlay and Remote Playback where the browser and receiver support them. |
| Listen to music | Albums, artists, genres, playlists, a queue, shuffle, media keys, similar tracks and radio from audio analysis. |
| Browse pictures | Full-resolution viewing, zoom, rotation, fullscreen and touch navigation. |
| Open archives and discs | Browse supported RAR/ZIP sets and DVD images without manually extracting them. |
| Organize a large library | Live directory watching, search, optional grouping by leaf folder name with compact thumbnails and details on hover, filters, favourites, watched state and a virtual grid. |
| Keep deployment simple | One binary, an embedded web app and a local database; Docker includes ffmpeg for conversion and analysis. |

[Explore the feature guide](docs/features.md) for formats, controls and details.
Browser and receiver codec support varies; Mediator copies compatible streams
and converts when needed. The published Docker image is amd64 only. Native
builds can run without ffmpeg, with fewer playback and analysis capabilities.

## Docker

For a service that survives restarts, use the included [compose.yaml](compose.yaml)
and [example environment](.env.example):

```sh
git clone https://github.com/JohanLindvall/Mediator.git
cd Mediator
cp .env.example .env
# Edit .env: set MEDIA_DIR to an existing absolute media directory.
docker compose up -d
```

The default URL is [localhost:8080](http://localhost:8080). Compose keeps media
read-only and stores state in a named volume. `.env` is ignored by Git.

```sh
docker compose logs --tail=100 -f mediator   # follow the server log
docker compose pull && docker compose up -d # update to the configured image
docker compose down                        # stop; keep the data volume
```

See [Getting started](docs/getting-started.md) for image tags, backups, multiple
folders, GPU access, platforms and local builds.

## Documentation

| Task | Guide |
| --- | --- |
| Install, update or back up | [Getting started](docs/getting-started.md) |
| Learn the players and supported formats | [Feature guide](docs/features.md) |
| Set scan limits, exclusions or cache storage | [Configuration](docs/configuration.md) and [flag reference](#configuration) |
| Set up a proxy, restricted view or receiver | [Network access, proxies and casting](docs/deployment.md) |
| Diagnose a problem | [Troubleshooting](docs/troubleshooting.md) and [support](SUPPORT.md) |
| Work on the code | [Development guide](docs/development.md) and [architecture chapters](docs/README.md#architecture) |
| Integrate another client | [API reference](#api) |

### Playing to a television

DLNA needs the server and receiver on the same IPv4 network with multicast
between them. See [Docker networking for TVs](docs/getting-started.md#play-on-a-television)
and the [receiver guide](docs/deployment.md#playing-to-a-television).

### Signed media URLs

Receivers fetch media themselves. Behind an authenticating proxy they need the
[signed media route](docs/deployment.md#signed-media-urls), which grants media
access until its token expires. Treat these URLs as credentials.

### One library, several faces

A reverse proxy can restrict a hostname to music, videos, images or selected
paths. See [restricted views](docs/deployment.md#one-library-several-faces).

## Troubleshooting

Start with the [common setup checks](docs/troubleshooting.md#first-checks).
If the problem persists, [ask for help](SUPPORT.md) with your version,
installation method and redacted logs.

## Development

The build and test pipeline runs in Docker. Install **Git, Make and Docker
with BuildKit**; local Go and Node installations are optional.

```sh
git clone https://github.com/JohanLindvall/Mediator.git
cd Mediator
make help       # list commands and their requirements
make test       # frontend tests/build, three-platform go vet, Go race tests
make docker     # build mediator:latest with ffmpeg included
make build      # extract ./mediator (a Linux binary)
```

On Linux, `./mediator -open -lock /path/to/your/media` starts a local session
and opens the browser. On other hosts use Docker, or follow the
[native build and live frontend workflow](docs/development.md).

[CONTRIBUTING.md](CONTRIBUTING.md) covers your first pull request.
[AGENTS.md](AGENTS.md) describes the repository rules and links each code area
to its architecture chapter. API types are generated from Go with `make generate`.

### Layout

| Path | Purpose |
| --- | --- |
| `main.go` | Flags, embedded frontend and server lifecycle |
| `internal/library/` | Scanning, watching, collections and media analysis |
| `internal/server/`, `internal/dlna/` | HTTP API, playback, conversions and receivers |
| `internal/blob/`, `internal/state/` | Database, index cache and playback state |
| `web/src/` | TypeScript frontend, without runtime dependencies |
| `cmd/gen-ts/` | Go-to-TypeScript API model generator |
| `docs/` | User guides, development workflow and architecture |
| `.github/` | CI, issue forms, dependency updates and release notes |

## Contributing

Bug reports, documentation fixes and code contributions are welcome. Start
with the [contribution guide](CONTRIBUTING.md), browse
[good first issues](https://github.com/JohanLindvall/Mediator/labels/good%20first%20issue)
or [propose an improvement](https://github.com/JohanLindvall/Mediator/issues/new?template=feature_request.yml).
You can also help by testing a browser or receiver and reporting what works.
Use invented media names in public reports and follow the [code of conduct](CODE_OF_CONDUCT.md).

Maintained by [Johan Lindvall](https://github.com/JohanLindvall) and
[contributors](https://github.com/JohanLindvall/Mediator/graphs/contributors).
If Mediator is useful to you, consider starring the repository or sharing it
with someone who could use it. Watch releases for update notifications.

## Security

Use a trusted network or an authenticating reverse proxy. Anyone who reaches
the server can browse and stream; without `-lock`, they can also change indexed
directories and delete writable media. [SECURITY.md](SECURITY.md) explains the
trust model and how to report vulnerabilities privately.

## License

[MIT](LICENSE). To cite the project, use [CITATION.cff](CITATION.cff).

## Configuration

Flags go **before** directory arguments. See the
[configuration guide](docs/configuration.md) for directory selection,
exclusions, storage budgets and proxy behavior.

<details>
<summary>All command-line flags</summary>

| Flag       | Default            | Description                                                    |
| ---------- | ------------------ | -------------------------------------------------------------- |
| `-listen`  | `:8080`            | HTTP listen address; port `0` picks a free one                 |
| `-open`    | `false`            | Open the UI in your browser once listening; [local session behavior](docs/configuration.md#open-a-local-session) |
| `-data`    | `data`             | Directory for playback state + the default blob database       |
| `-db`      | `<data>/media.db`  | Blob database (thumbnails + metadata); `off` disables caching  |
| `-rescan`  | `10m`              | Full reconciliation rescan interval (`0` disables)             |
| `-analyze` | `true` | Read how the music sounds in the background (ffmpeg needed), for similar tracks, radio, similar releases and performers, and audiobook detection; `-analyze=false` turns it off |
| `-credits` | `true` | Find each show's intro and credits from the sound, so the player can skip them (needs ffmpeg) |
| `-version` | | Print the build (version, commit, time, toolchain) and exit |
| `-tmp`     | system temp        | Where converted files and unpacked archive members are kept; [storage behavior](docs/configuration.md#conversion-storage) |
| `-tmp-max` | `8G`               | How much converted and unpacked material may be held at once; `off` (or `0`) for no limit |
| `-archive-max` | `1000`         | How many media members one zip archive or rar set may hold and still be indexed; one over it is left out whole. `0` for no limit |
| `-lock`    | `false`            | Refuse changes to the scanned directories and deleting from the disk: the preferences become read-only and nothing can be deleted |
| `-exclude` | —                  | Glob of paths to keep out of the index; repeatable             |
| `-debug`   | `false`            | Log every request — method, range, status, bytes, duration     |

</details>

## API

The server uses JSON for metadata and HTTP Range requests for media. The
[generated TypeScript model](web/src/types.gen.ts) describes response types;
[Go API types](internal/server/apitypes.go) are their source of truth.

<details>
<summary>Endpoints, request headers and access rules</summary>

| Endpoint                                  | Description                              |
| ----------------------------------------- | ---------------------------------------- |
| `GET /api/library?kind&q&folder&sort&order&offset&limit` | Paged, filtered, sorted listing; `folder` selects the exact displayed leaf folder name across all parents and roots. `sort=tempo` orders music by the tempo read from the sound (each track's `bpm`), those with none last |
| `GET /api/folders?kind&q&sort&order` | File groups by leaf folder name, merging equal names; each carries an item count and representative thumbnail ID. Search, `watch`, `played`, `hidden` and `fav` filter members before grouping; sorts: `name`, `mtime`, `added`, `items`, `size`, `duration` |
| `GET /api/albums?q&artist&genre&sort&order` | Albums (directory + m3u), narrowed to one performer or one genre; `audiobooks=1` lists the audiobook shelf instead. Each carries `path` (where the release is kept, as the listing names a file) and `formats` (what its tracks are, commonest first) |
| `GET /api/artists?q&sort&order`           | Artists, grouped from album tags         |
| `GET /api/genres?q&sort&order`            | Genres, grouped from album tags          |
| `GET /api/tracks?of&…`                    | The tracks behind a view (`of` = albums, artists, genres or items, with that view's own parameters), in the order a queue plays them; `of=similar&id=…&n=` the tracks that sound most like one (`n` at most 200); `of=station&id=…` or `of=station&artist=…` one performer's tracks for artist radio — the seed's performer or the one named, nearest the seed first (by popularity without one), the unanalysed after, `artist` saying whose |
| `GET /api/albums?near={id}`               | The releases that sound like one, nearest first (`order=asc` turns it round), each with `similarity`; `audiobooks=1` lists the audiobook shelf instead of the records |
| `GET /api/artists?near={name}`            | The performers that sound like one, nearest first (`order=asc` turns it round) |
| `GET /api/series?q&sort&order`            | Television, read out of the file and directory names; each show carries its seasons. `q` finds a show by its name or by any episode in it; one found through some of its seasons carries `matched`, those seasons |
| `POST /api/plays/{id}`                    | Count one play; returns the new total    |
| `POST /api/like/{id}`                     | Record a verdict (`{"like": 1 / -1 / 0}`); returns what is stored |
| `GET /api/albums/{id}`                    | Album detail with tracks                 |
| `GET /api/stream/{id}`                    | Media bytes (Range supported, `?dl=1`)   |
| `GET /api/signed/{token}/…`               | The media paths again, reachable with a signed token instead of the proxy's credentials — see [Signed media URLs](#signed-media-urls) |
| `GET /api/thumb/{id}?w=360`               | Cached JPEG thumbnail                    |
| `GET /api/item/{id}`                      | One item, with its metadata read first if it has not been |
| `GET /api/remux/{id}?a=&mode=`            | The same streams in a container the browser opens, served as an ordinary seekable file, keeping soundtrack `a`; `mode=audio` copies the picture and converts the soundtrack instead; 404 when copying would not help |
| `GET /api/transcode/{id}?t=0[&mode=audio][&q=]` | Live fMP4 conversion from t seconds (`mode=audio` copies the video, and where t is a keyframe both streams begin on it; `q=` a rung of the bitrate ladder in kbit/s, which re-encodes under that ceiling whatever the mode); a conversion that produces nothing answers 503 where the file cannot be read, 500 where it can |
| `GET /api/hls/{id}/index.m3u8?t=&mode=[&q=]` | The same conversion as HLS — what Safari plays: the whole film as a VOD playlist beginning at `t`, its segments made as they are asked for, one session per film, mode, soundtrack and rung; `X-Media-Timeline: film` says so, `session` that the older growing playlist from the seek is being served |
| `GET /api/convert/{id}?for=`              | How far a conversion has reached, while something is waiting on one; `for=cast` asks for a cast being prepared, which answers kind `subtitles` while a subtitle is read out of the file for it |
| `GET /api/skip/{id}`                      | Where a video's intro and credits are, as found from the sound (`introStart`, `introEnd`, `outro` in seconds, the last counted back from the end); all nought where nothing has been found. Asking puts the episode's season at the front of the search |
| `POST /api/delete/plan`                   | What deleting something would remove — `{kind: item\|album\|series\|season, id, season}` — worked out and nothing removed: the folders and files, counts, size, and a token. JSON body only; refused (403) for a face, a confined caller or `-lock` |
| `POST /api/delete`                        | Delete exactly what a plan listed, named by its token (`{token}`); once, within ten minutes, and each file only while it is still the one shown. 410 for a token that is used, unknown or too old |
| `POST /api/log`                           | One fault the page reports — a conversion whose connection came apart, a request that never arrived, an error the page raised — so it lands in the server's log beside the requests that explain it. Bounded per process; answers 204 whether it recorded it or not |
| `GET /api/keyframe/{id}?t=` | Where a copied conversion seeking to t really begins (measured; a disc title is answered with t) |
| `GET /api/crop/{id}`                      | Where the picture sits inside the file's own black borders, measured once and remembered. The samples are fractions of the running time, so a film the library has not measured yet is probed first rather than answered without a look — and an answer nothing looked at is not stored as "no borders here" |
| `GET /api/albums/{id}/zip`                | The release as one download                |
| `GET /api/sprite/{id}`                    | Scrub sheet: ten frames across a video, taken by ten seeks (3.5 s for an 87-minute film) |
| `GET /api/frame/{id}?t=MS&w=PX`           | The frame at one moment of a video, to the millisecond, `w` pixels wide (96–960): the seek bar's preview. Taken on request, not stored; served immutable with `v=<mtime>` |
| `GET /api/playlist.m3u?…`                 | The current query as an m3u                |
| `GET /api/info`                           | What the client needs before anything else: the thumbnail epoch, which classes of media this face may show, and the bitrate ladder (`qualities`) |
| `POST /api/links`                         | Mint a shortlink to a view or to one item; asking twice for the same place returns the same code |
| `GET /s/{code}`                           | Follow one, redirecting to the address it names; an unknown code opens the library |
| `GET /api/prefs`, `PUT /api/prefs`        | The directories being indexed              |
| `PUT /api/flags/{id}`, `PUT /api/flags`   | Hidden / favourite, rotation, borders kept and picture shape (`aspect`, e.g. `"16:9"`; empty for the file's own), one item or a batch |
| `GET /api/subs/{id}`, `GET /api/subs/{id}/{n}?shift=&format=` | External subtitles: list, and one as WebVTT — or as SubRip (`format=srt`), which is what a television reads |
| `GET/PUT/DELETE /api/state/{id}`, `GET /api/state` | Playback positions, filtered by face and paths like everything else |
| `GET /api/renderers`                      | The DLNA renderers on the network (`?fresh=1` searches again) |
| `GET /api/renderers/{rid}`                | Where that set has got to: transport state, position, duration |
| `POST /api/renderers/{rid}/play/{id}?t=&sub=&audio=` | Play an item on it, from t seconds, with one subtitle (`sub=off` for none; one inside the file is read out before the set is handed the film) and one soundtrack; 409 where another request has taken that set meanwhile — the newest press drives it and the older one stops talking to it; 422 for a file already found damaged or incomplete. A set that refuses a new file from where it is (UPnP 701) is stopped and asked again |
| `POST /api/renderers/{rid}/next/{id}?audio=` | Queue what follows on the set itself, so a track boundary costs no silence; 501 where the renderer will not, 422 for a damaged file |
| `POST /api/renderers/{rid}/control`       | `{action: play\|pause\|stop\|seek\|volume, seconds, volume}` |
| `GET /api/events`                         | Server-sent library change events        |

#### Request headers

| Header             | Values                          | Effect                                                       |
| ------------------ | ------------------------------- | ------------------------------------------------------------ |
| `X-Media-Content`  | `music`, `videos`, `images` — comma-separated for more than one | Restricts listings, counts, search, collections and by-id requests (404 for excluded items). Absent or empty means the whole library; a nonempty header with no supported class is refused with 403. Set it in a reverse proxy — see [One library, several faces](#one-library-several-faces). |
| `X-Allowed-Paths` | absolute directories, CSV quoted or one per repeated header | Restricts listings, counts, collections and by-id requests to those paths. Absent or empty means the whole library; malformed CSV or a nonempty header with no absolute path is refused with 403. Composes with `X-Media-Content` by intersection. Set by the proxy. |
| `X-Media-Internal` | a token the server mints for itself | Marks the server's own loopback reads — ffmpeg fetching an archived file's bytes through `/api/stream` to make a thumbnail or read its codecs. It keeps those from registering as playback, which would make the thumbnailer throttle against its own reading. Not something a caller sets: it grants nothing, and a request carrying someone else's guess at it is treated as any other request. |

Notes: media is only ever served by indexed ID, so playlists cannot reach
outside the configured roots. This server implements **no authentication** of
its own — a deployment that needs it puts a proxy in front. Anything
fetching a URL without a browser (an AirPlay receiver, a casting device, an
external player handed an m3u) cannot answer a password challenge, which is
what [signed media URLs](#signed-media-urls) are for. As shipped: whoever can reach the port can browse, stream and — unless the server
was started with `-lock` — point the library at any directory the process can
read, and delete what the library holds from the disk. That is the posture
for a personal server on a network its owner trusts; a face restricted by the
proxy to some media or to part of the disk cannot delete, and `-lock` turns
deleting off altogether. Thumbnails for `.avif` fall back to an icon
(no pure-Go decoder); the full-size view still renders in the browser.
</details>
