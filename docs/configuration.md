# Configuration

[Documentation](README.md) · [All command-line flags](../README.md#configuration)

Pass flags before the directories to index. For a persistent Docker setup,
start with [Getting started](getting-started.md).

## Indexed directories

Positional arguments are the directories to scan and watch (recursive) — the
first run's, at least: once directories are chosen in the preferences those
are what is indexed, and the command line is only the seed. `-lock` keeps the
command line in charge.

## Exclude paths

`-exclude` matches the file or directory name when the pattern has no slash
in it, and the whole path when it does. Quote globs so your shell does not
expand them, and repeat the flag for more than one pattern:

```sh
./mediator -open -lock -exclude '*.part' -exclude '/srv/media/private/*' /srv/media
```

Exclusions apply when restoring the saved index as well as when scanning: a
new `-exclude` rule takes effect before the server starts serving cached items.
Empty paths in directory preferences are rejected.

## Media and request limits

Only regular files are indexed. Named pipes, devices and links to directories
are skipped, including subtitle and archive names, so they cannot stall a scan
or appear as playable items. Rescans remove entries replaced by such files.

Archive readers validate record boundaries and required ZIP64 fields. ZIP
directories are read incrementally, with the entry limit checked against actual
records as well as the declared count. Unpacking never writes more bytes than
the member declares; excess output or a bad checksum rejects the cached copy.
Text subtitle input and extracted output are limited to 16 MiB per track.
Subtitle ETags follow the converted captions, so editing a sidecar takes effect
without changing the video's modification time.

Browser requests that change state must come from the same origin. Other sites
receive 403; command-line clients without browser origin headers still work.
Keep the original `Host` header when proxying, including its port when using a
nonstandard port (`proxy_set_header Host $http_host` in nginx). JSON writes
require one non-null object within the endpoint's size limit; trailing values
and oversized bodies are rejected before applying changes. Incoming requests
have a 30-second read limit and idle connections close after two minutes;
streaming responses have no fixed write deadline.

Media opened as a document, including SVG images, is sandboxed so it cannot run
scripts with the application's permissions. The UI cannot be embedded in a
frame, and responses suppress referrers and MIME sniffing. These protections
do not add authentication: use a trusted network or an authenticating proxy.

## Conversion storage

Converted files (rewraps and HLS segments) and films unpacked from archives
live under `-tmp` in a fixed place rather than a fresh one per run, so a
restart finds what was already converted or unpacked instead of doing it
again. `-tmp-max` is a budget shared by all three, and it prunes: least
recently wanted first, never taking something that is being watched — being
over a limit you chose is a smaller wrong than deleting a film out from under
a viewer. A file too large for the whole budget is left to the segmented
converter, which only holds what it has produced; an archived film too large
for it is played as it is packed, which seeks slowly rather than not at all.

Files being written count towards the budget alongside finished copies.
The segmented converter updates its share as conversions finish, fail or
are evicted. A completed copy remains available after shutdown so the next
run can reuse it. See [rewrap and scratch storage](rewrap.md) for the
implementation details.

In Docker, the default temporary directory lives in the container filesystem.
To keep conversion caches across container **recreation**, mount a separate
volume and pass its path with `-tmp`. The `/data` volume preserves the
database; it does not include the default scratch directory.

## Open a local session

```sh
./mediator -open -lock /path/to/your/media
```

`-open` launches your desktop's URL handler (`xdg-open`, with `gio`,
`gnome-open`, `kde-open`, `wslview` and `x-www-browser` as fallbacks; `open`
on macOS, `rundll32` on Windows). On its own it also switches the listen
address to `127.0.0.1:0` — a free port on loopback — so an ad-hoc session
never collides with a running instance and is not reachable from the
network. Pass `-listen` explicitly to override that; the server still opens
whatever address it ends up bound to. The chosen URL is logged either way,
and if no URL handler is found the server logs a warning and keeps serving.
