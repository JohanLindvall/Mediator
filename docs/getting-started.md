# Getting started

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Support](../SUPPORT.md)

## Platforms and requirements

| Route | Requirements | What you get |
| --- | --- | --- |
| Published container | Docker on amd64 / x86-64; Compose for the persistent setup below | Mediator, its web app, ffmpeg and the Intel media driver |
| `make build` | Git, Make and Docker with BuildKit | A Linux binary for the build platform |
| Native source build | Go, Node 24+, npm; see [development](development.md#native-build) | A binary for your host with the web app embedded |

The published container is Linux/amd64 only; the Intel driver in its runtime
is x86-specific. ARM hosts need a native source build; an ARM container is
not published. Go code is vetted for Linux, macOS and Windows, with automated
runtime tests on Linux. Native installs need `ffmpeg` and `ffprobe` on `PATH`
for video thumbnails, conversion and audio analysis.

Use [the quick start](../README.md#quick-start) for a single session. The
commands in these guides use a POSIX shell; Windows users can use WSL for the
same command syntax.

## Keep it running with Compose

From a checkout:

```sh
cp .env.example .env
```

Edit `.env` and set `MEDIA_DIR` to an existing absolute directory. Keep quotes
around paths with spaces or `#`. Then start:

```sh
docker compose config --quiet
docker compose up -d
docker compose ps
```

Open [localhost:8080](http://localhost:8080). The page responds before the
initial scan completes. Files appear during the scan, then tags, thumbnails
and analysis fill in. Playback takes priority over background processing.
Docker's health check confirms the HTTP server is responding, not that the
scan or enrichment has finished.

The example [compose.yaml](../compose.yaml) uses:

- A read-only media mount. A missing source directory is an error, so a typo
  cannot silently create an empty library.
- `-lock`, which fixes indexed directories to the command line and disables
  deletion. Playback state and favourites still work.
- A named volume at `/data`, holding the database and your playback state.
- A loopback port, reachable from the Docker host.
- `restart: unless-stopped` and a 30-second shutdown allowance.

Compose prefixes the data volume with the project name, normally `mediator`.
Keep using the same checkout directory or a consistent `docker compose -p`
name so later commands find the same volume. The one-line Docker quick start
uses a separate volume named `mediator-data`.

## Update, stop and back up

`latest` tracks the tested `main` branch. For a fixed version, set
`MEDIATOR_IMAGE` in `.env` to a published
[release tag](https://github.com/JohanLindvall/Mediator/releases) or image
digest. Release tags are available as `vX.Y.Z`, `X.Y.Z` and `X.Y`;
commit builds use `sha-<short-commit>`. Tag builds do not move `latest`.

```sh
docker compose pull
docker compose up -d
docker compose logs --tail=100 mediator
```

`docker compose down` stops the service and keeps the data volume.
`docker compose down --volumes` also deletes that volume and its saved state.
Stopping or updating ends active streams.

For a consistent backup, stop the service before copying the database:

```sh
docker compose stop
docker compose cp mediator:/data/media.db ./mediator-backup.db
docker compose start
```

Keep the backup private: it includes library state and the media signing key.
It does not contain your media files. To restore into an existing, stopped
container:

```sh
docker compose stop
docker compose cp ./mediator-backup.db mediator:/data/media.db
docker compose start
```

If the container was removed, run `docker compose create` before copying the
backup. Back up any separate `-db` path you configure instead of `/data/media.db`.
Keep media mounted at the same container paths so indexed IDs remain stable.

## Change the library

Edit `MEDIA_DIR` and run `docker compose up -d` to recreate the container with
the new mount. The included `-lock` makes those command-line roots authoritative
even if the data volume has directory preferences from an earlier run.

For multiple roots, add a bind mount for each and list their **container**
paths in `command`. This replaces the single `/library` mount in the example:

```yaml
command: ["-lock", "/videos", "/music"]
volumes:
  - type: bind
    source: /path/to/videos
    target: /videos
    read_only: true
    bind:
      create_host_path: false
  - type: bind
    source: /path/to/music
    target: /music
    read_only: true
    bind:
      create_host_path: false
  - mediator-data:/data
```

To manage directories through Preferences, remove `-lock` from `command`.
Preferences can then select paths the container can see. To permit deletion
as well, the relevant media mount must be writable. Anyone who reaches the
server can use those controls; see the [security policy](../SECURITY.md).

Keep personal Compose changes in an ignored `compose.override.yaml` where
possible. Review the merged result with `docker compose config` before running.
The [configuration guide](configuration.md) explains exclusions and cache limits.

## Access from other devices

On a trusted LAN, set `BIND_ADDRESS` in `.env` to the Docker host's LAN address
and run `docker compose up -d`. Open `http://<server-address>:8080` on the other
device. `0.0.0.0` binds all host IPv4 interfaces; use it only where those
interfaces are intended to serve the library.

For access outside a trusted network, use an authenticating reverse proxy and
restrict direct access to the backend port. Mediator has no account system.
The [proxy guide](deployment.md) explains signed media URLs and restricted views.

## Play on a television

DLNA discovery needs IPv4 multicast between the server and the television.
The default Docker bridge may prevent discovery or give the receiver an
unreachable server address. On **native Linux**, host networking lets the
server use the host's LAN interfaces:

```sh
docker run --rm --network host \
  --mount "type=bind,src=/path/to/your/media,dst=/library,readonly" \
  -v mediator-data:/data \
  ghcr.io/johanlindvall/mediator:latest -lock /library
```

This listens on the host's port 8080, including its LAN interfaces. Stop any
other server using that port first. Docker Desktop networking differs; a
native build on the receiver's network is another option. See the
[receiver guide](deployment.md#playing-to-a-television) for codec and subtitle
behavior.

## Hardware conversion

On a Linux host with supported graphics hardware, pass `/dev/dri` to the
container. For Compose, add this to the `mediator` service in an ignored
`compose.override.yaml`:

```yaml
services:
  mediator:
    devices:
      - /dev/dri:/dev/dri
```

Only add it when the device exists. Mediator probes available acceleration
and uses software when hardware is unavailable; Preferences shows what it
found. See [conversion](conversion.md) for the supported paths.
