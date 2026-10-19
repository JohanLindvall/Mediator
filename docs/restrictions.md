# Faces and path restrictions

**One library, several faces** (`internal/server/content.go`):
`X-Media-Content: music` (or `videos`, `images`, comma-separated) shows that
and nothing else, in listings, counts and by id. One scan feeds every face.

The proxy sets the header, **never the page** (`<img>`/`<video>` requests
carry none), so filtering is in the backend. Nothing depending on
`/api/info` is drawn before it arrives (`contentKnown`), or the first live
event draws every chip; hiding empty views (`content.ts`, tested) is a
courtesy. Enforcement is
`Server.item` for every by-id handler — the album sheet and zip (`albumFor`:
permitted tracks only, else 404) and positions (one proves a film exists)
included — plus `content.kinds()` on listings and `content.mask()` on
counts; every request under an HLS session resolves its item afresh
([hls](hls.md)). Albums and artists are music's; music includes playlists.

**A restricted face counts; it does not mask**: masking cannot handle the
cross-kind totals (started, finished, played) and lets stream and listing
disagree, so restricted callers go through `CountsFor` with the face's
`Kinds`, unrestricted ones keep the O(1) running totals (`q.Kinds == 0`),
and `mask` stays as a belt. The debug access log records each request's
**face**, as a proxy block missing the header fails invisibly.

**A caller can also be restricted to part of the library**
(`X-Allowed-Paths`, `internal/library/paths.go`: directories, CSV-quoted or
one per repeated header field), everywhere; absent or empty is everything.
**The two compose**: `Server.item` asks both, listing queries carry both, and
the result is the intersection. Three things about the path restriction are
load-bearing:

**It matches the absolute path**, not the display path (`Rel` starts at the
root's base name, so two roots can collide); an archived member's path is
the archive's plus a NUL and the member, so needs no special case (tested).

**The prefix test is component-aware**: `/srv/mediax` is not under
`/srv/media`.

**It is a value type in every cache key** — listing, counts and ETag — or one
caller's restricted answer reaches the next; `versionTag` hashes the
restrictions in and sends `Vary: X-Media-Content, X-Allowed-Paths`, as
`protect` does on every response, media included.

**The headers are parsed once per request** (`http.go`): `contentOf` and
`pathsOf` reuse that value, signed routes included, rather than parsing CSV
for every item of a batch. An invalid nonempty restriction is refused with
403 before routing, and `PathFilter` used directly on invalid input denies
every path. `NoKinds` is an empty allowed set, unlike zero, which is
unrestricted. Either restriction refuses the directory preferences
(`fullLibrary`), and `/api/info`'s `confined` reflects both, so the page
hides the control ([scanning](scanning.md)).

Collections (albums, artists, genres, shows) are cached over everything; a
restricted caller gets the albums with any permitted **track**, and artists
and genres regrouped from those, never filtered afterwards (which counts
unseen ones), paid only when the header is set. **Every grouped endpoint
passes the restriction on** via `groupedView`/`listOrNear` (`near` or
ordinary by one rule; a list, never `null`), pinned by
`TestPathsRestrictEveryCollection`: one that skips it serves the whole
library under restricted chips, invisibly.

`library.KindSet` carries the face into a query as `Query.Kinds` (not
`Query.Kind`, the viewer's chosen view), a value type to key the cache.
