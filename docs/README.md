# Mediator documentation

[Project overview and quick start](../README.md) · [Support](../SUPPORT.md) · [Contributing](../CONTRIBUTING.md)

## Use and run Mediator

| Guide | Covers |
| --- | --- |
| [Getting started](getting-started.md) | Platforms, Compose, updates, backups, media mounts, network access and GPUs |
| [Feature guide](features.md) | Players, controls, formats, search, collections and analysis |
| [Configuration](configuration.md) | Directory selection, exclusions, scratch storage and request behavior |
| [Command-line flags](../README.md#configuration) | Every flag and its default |
| [Network access, proxies and casting](deployment.md) | Signed media URLs, receivers and restricted views |
| [Troubleshooting](troubleshooting.md) | Setup checks, logs and playback problems |
| [API reference](../README.md#api) | Endpoints, request headers and access rules |

## Develop and contribute

- [Contributing](../CONTRIBUTING.md): first contribution, bug reports and pull requests.
- [Development](development.md): Docker builds, the live frontend, native builds and tests.
- [AGENTS.md](../AGENTS.md): rules for every change and the code covered by each chapter.
- [Project maintenance](project.md): documentation, GitHub configuration and releases.

## Architecture

Read the chapter for the code you are changing and update it with that change.
These explain implementation and constraints; the guides above explain usage.

### Library and storage

| Chapter | Covers |
| --- | --- |
| [Persistence](persistence.md) | Index cache, database, state store and shutdown flushes |
| [Scanning](scanning.md) | Media detection, watches, reconciliation and directories |
| [Listings](listings.md) | Cached listings, versions, counts and events |
| [Names](names.md) | Encodings, natural sorting and search ranking |
| [Enrichment](enrichment.md) | Tags, duration, codecs and probes |
| [Collections](collections.md) | Albums, discs, performers and genres |
| [Television](television.md) | Shows, seasons, intros and credits |
| [Music](music.md) | Plays, preferences, audio analysis, similarity and radio |
| [Archives](archives.md) | RAR/ZIP, unpacking, DVDs and loopback input |

### Server and media delivery

| Chapter | Covers |
| --- | --- |
| [Serving](serving.md) | HTTP, build information, limits, logs and shutdown |
| [Restrictions](restrictions.md) | Content and path restrictions |
| [Conversion](conversion.md) | Playback routes, codecs, GPU conversion and seeking |
| [Rewrap](rewrap.md) | Container copies, audio conversion and scratch space |
| [HLS](hls.md) | Segmented conversion and Safari playback |
| [Subtitles](subtitles.md) | Sidecars, embedded captions and WebVTT |
| [Thumbnails](thumbnails.md) | Tiles, hover previews and seek frames |
| [Receivers](receivers.md) | AirPlay, DLNA and signed media URLs |
| [Deletion](deletion.md) | Plans, confirmation and disk deletion |

### Frontend

| Chapter | Covers |
| --- | --- |
| [Grid](grid.md) | Virtual grid, cards, sources and thumbnail loading |
| [Views](views.md) | Navigation, sorting, shortlinks and preferences |
| [Player](player.md) | Video, picture viewing, gestures and shape |
| [Music bar](music-bar.md) | Queue, decks, fades and media keys |
| [Spectrum](spectrum.md) | Audio visualization |
