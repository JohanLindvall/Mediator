# The HTTP server

**What is running is on the screen** (`about.go`, via `/api/info`): the build
and the hardware. The linker stamps the version (images build from copied
sources); a checkout's `debug.ReadBuildInfo` wins, never being stale, and
`modified` is shown. Capabilities list only what is established; the box
names only what is missing. `FindHardware` runs at startup in a goroutine (a
broken driver can spend the whole probe budget); conversions before it
finishes use software. Read via `chosen()` under a lock: `/api/info` can ask
mid-search (`sync.Once` covers only later readers).

`run` binds the listener itself (not `ListenAndServe`) so `-listen` port 0
resolves to the real port for `browse.URL` and `library.SetLoopback`, and bind
errors surface before anything claims to listen. `-open` without `-listen` uses
`127.0.0.1:0`: an ad-hoc session must not collide with a running instance or
expose the library to the LAN.

- **`http.go` wraps every request** (`protect`): cross-origin write
  protection (Go's `CrossOriginProtection`, which lets media GETs and
  non-browser clients through and refuses cross-origin mutations), the
  response security headers, and the request's parsing of the proxy's
  restrictions ([restrictions](restrictions.md)). A proxy in front must pass
  on the original Host and port.
- JSON mutations go through `decodeJSON`: a bounded, single, non-null object
  is read completely before the decoded value is published. Reading a
  response stays separate from it.
- `GET /api/folders` serves leaf-name groups through the ordinary collection
  ETag policy. Opening one uses `folder=` on `/api/library`, `/api/tracks`
  or the m3u export; `listQuery` preserves this exact displayed name through
  each route ([collections](collections.md)).
- Reading a request is bounded to 30 s and an idle connection to two minutes;
  a response has no write timeout, deliberately, as a stream lasts a film.
- Stream responses carry a CSP sandbox: an SVG or a misnamed HTML file is an
  untrusted document, even where an `<img>` elsewhere shows it safely.
- Media is streamed only by indexed ID (`http.ServeContent`, Range support);
  m3u entries resolve against the index, so playlists cannot expose paths
  outside the configured roots. Keep it that way.
- Downloads: an m3u export carries signed URLs. A release's ZIP
  (`albumzip.go`) takes a multi-disc release's real directory and, for a
  restricted caller, permitted tracks only; entry names stay unique even
  where an existing name resembles a generated suffix, and hidden
  directories are skipped. A walk error or an entry or depth limit fails the
  request before anything streams; a copy error aborts it rather than
  closing a partial ZIP.
- **A transfer stopping half way is usually not this server.** A stream that
  stops logs why: `ServeContent` swallows the copy error, so
  `recordingReader` keeps it; our failed read and the far end leaving look
  alike to a proxy ("upstream prematurely closed connection") and need
  different fixes. Cancelled requests are not reported. `recordingReader`
  reports our failed reads, the proxy logs `upstream prematurely closed
  connection` when we closed first, and silence from both means the far end.
  Serving and ranges run at full rate; a browser stopping a few hundred MiB
  in (then resuming with a `206`) while another pulls the file whole is a
  client fault.
- **Shutdown cuts transfers in flight** (`httpSrv.Shutdown`, 5 s; check
  restart times before calling a truncated transfer a bug). Spending all 5 s
  is normal, logged at Info: `Shutdown` cancels no request contexts and
  `handleEvents` waits on its own. Do not cancel request contexts from `main`:
  films would be cut at once. Converters read archived content via this
  server's `/api/stream`, which the drain waits for, so the rewrapper is closed
  before the drain (the defer stays as idempotent backstop; `Close` removes
  the unplayable `.part`) and `library.CloseScratch` stops the unpacker there
  too. The segmented converter deliberately is not: `HLS.Close` deletes
  directories players are still fetching from, so until a session's ffmpeg
  can be stopped with its files kept (`hlsSession.stopConverting`,
  unexported) a converting archived film spends the budget.
- With `-debug`, `access.go` logs each finished request (method, path,
  status, bytes sent, duration, requested range): the only record of how much
  left this process. Signed tokens are redacted from request and redirect
  paths: a signed URL is a credential.
- **Browser faults go to the server log** (`clientlog.go`, `report.ts`,
  `POST /api/log`): element errors by code (`mediaErrorText`), give-ups with the
  sentence shown, feed stops and resumes, failed API requests, uncaught errors,
  the music bar's stalls and refused plays — each with film, position and route
  (`routeName`). **Bounded**, as the one route where clients write the log: a
  large body is refused unread; fields are trimmed and stripped of control
  characters (a newline forges a record); a process-wide rate limit reports its
  drops; the page won't repeat a fault within a quiet window (`shouldReport`).
  Sent with `keepalive` and forgotten; a failed report is never reported. A
  film the caller may not see is logged **without the film** (no existence
  oracle). Always 204.
- Static: hashed `/assets/*` are immutable; **everything else is
  `no-store`** (embedded files have no modtime to revalidate; `no-cache` keeps
  stale shells naming dead hashes). An `/assets/*` path not in the bundle is
  from a replaced build (UI state lives in the URL hash) and gets 404, not the
  SPA fallback (HTML for a `<script>`); so does an unmatched `/api` path, even
  one the mux cleans to nothing (ending in `..`): never the shell to a JSON
  caller.
