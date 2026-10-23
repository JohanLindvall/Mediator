# Releases, performers, genres and folders

## Leaf folder groups

`folders.go` groups the ordinary file selection by the displayed name of
its **immediate** containing directory. Equal names merge across roots and
parents; comparison is exact after the existing filename decoding, including
case. There is no recursive roll-up and no music disc folding. Root-level
files use the root's own name. Archived members use their innermost member
directory, or the container's parent when the member has no directory.

`SearchFolders` applies the listing's kind, face, path, search, watch, play
and owner filters **before** grouping, using `buildQuery`. The cards count
matching files, sum their sizes, take the newest valid modification and
arrival times, and show total duration only when every member has one. A
stable member ID selects the representative thumbnail from that same
permitted selection; its own mtime versions the image, and SVGs use their
native stream route. Folder-name search hits rank before member hits using
the existing word-ranking rule.

The result is cached by the full selection and group version, with the watch
version for watch filters, under its own lock. It never shares the item
listing's cache, whose version also moves with byte growth. A cache miss
copies the aggregate fields under the index read lock; cached cards are
immutable. As with albums, size and mtime aggregates can lag byte growth
until a group change. `Query.Folder` opens a group through the ordinary paged
listing and is part of that cache's key, so viewers and music queues retain
the folder selection. Groups are views across paths and offer no directory
deletion action.

## Grouping

- **A release spread over discs is one release**: disc-named directories
  (`CD2`, `disc 3`, `disk-4`, `CD 1-…`) fold into their parent. `discPattern`
  wants a standalone number and whole words, and reads numbers spelled out up
  to ten (past that everyone writes digits; each word could begin a title);
  `discSuffix` takes spelled forms too, or folder and tag disagree on the
  name. `foldDiscs` folds only where a release has more than one disc (a lone
  one would merge unrelated neighbours), one level deep. Tracks sort by disc,
  then number; the album-name vote strips disc markers (`albumTitle`).
- `dropDuplicateAlbums` drops a directory album when a playlist lists exactly
  its tracks (the playlist has the running order); partial or cross-directory
  playlists stay separate. It runs before artists are grouped, so they count
  each album once.
- `fillAlbum` derives a release's `Genre`, `Year` (majority of tagged tracks)
  and `Duration` (sum; 0 unless *every* track is measured, never
  half-counted), beside its search text ([names](names.md)).
- **Genre tags** are cleaned through `cleanGenreTag` at **both** metadata
  doors, `setMeta` and `LoadFromDB`, or a restart would not tidy what a
  re-read does. `cleanGenre` folds whitespace (else a lookalike second card)
  and collapses an exact doubling ("X X", "X/X") left by ID3 numeric
  references (a reader expands "(138)Black Metal" and keeps the text too), for
  genres only: a title may repeat itself. `splitGenres` splits on pipe,
  semicolon or comma, **never on a slash**, which mostly joins one compound
  genre ("Black/Death Metal"; 48 slash tags vs 19 lists). Albums carry
  `Genres` beside `Genre` (the first: sort key, caption); grouping walks all,
  the drill-down matches any.
- **Genres** (`genres.go`) group from albums, like artists, so the views
  agree; each counts **its performers**, deduplicated on `buildArtists`'s
  lowercase key so the artists chip agrees. Untagged releases join none
  (deliberately no "Unknown"); search text is name plus performers, not
  release titles. **Drill-downs clear each other**: `state.artist` and
  `state.genre` are never both set (else an intersection under a chip naming
  one); leaving either uses `setModeForce`, as returning to the same chip is
  no mode change and would be ignored. In a narrowing, `CountsFor` gathers the
  matching releases' performers and genres as it counts.
- Artists (`artists.go`) group from **albums**, not tracks, so the views agree
  and `SearchAlbums`'s artist filter shows exactly those counted. Playlists
  are excluded (double counting). Cached per version, searched and sorted like
  albums.

## Releases and their cards

- **Names on cards are links** (performer, genre, **the release a song is
  on**) wherever shown; the album sheet closes onto the result (a drill-down
  behind it is invisible). Release ids hash a directory unknown to the client,
  so the album tag labels the link and the **track's id** opens the sheet:
  `AlbumByID` takes track ids in a second pass (release ids never pay for it;
  their letter prefix prevents collisions). No album tag, no link. Links look
  like caption text until pointed at. `viaLink` matches the link class too
  (`.link-artist[data-artist]`): `data-artist` also marks the music bar's
  non-link artist line.
- **A collection's sleeve must exist** (`betterCover`, `Album.hasArt`):
  performers, genres and shelves show **the most recent release with
  artwork**, else the most recent. Artwork is judged from the **index** (a
  picture in the release's first track's directory), free in the album build
  and deliberately weaker than the thumbnailer's search (certainty suffices
  for a preference).
- A release card's artwork badge is **its track count, playlist or not** (the
  caption has a count only without a performer); a playlist's marker takes the
  other corner (`CollectionCard.tag`).
- **A release card's hover is path, then technical line**, as on tiles
  (`Album.Path`, `Album.Formats`, `releaseShape`, `hoverLines` in `format.ts`,
  tested): the folder (above folded discs) or playlist file via `displayPath`;
  formats commonest first (`formatOf`: probed codec, else extension; track
  hovers spell extensions via `codecName` too); a rate only with a running
  time. Set on the cell root and cleared every render (a key change redraws in
  place; `scrubCell` runs only on recycling); the name element keeps its own
  tooltip; the sheet reuses the text. The lines are never joined and re-split
  on a separator (folder names contain them). **A confined caller is never
  told where an out-of-view playlist lives** (`shownTo`, in `AllowedAlbums`
  and via `ShownUnder` in the sheet's `albumFor`): it gets a path-less copy of
  the shared cached release, tested on the absolute location (`where`).
- **Cards say what is known** (release and track: performer, year, genre),
  mostly true over empty, the full line in `title`; a performer's adds tracks,
  running time, the span of dated releases and the majority genre
  (`Artist.Genre`, `FromYear`, `ToYear` from `buildArtists`, ties broken by
  name so builds agree). For music, **tags are part of the cell key**,
  enrichment adding them later.
- **A year in a release's name moves to its year** (`liftYear`): "2018 - Some
  Release (Single)" shows without it and is dated by it unless the tags give a
  year (tags outrank a typed folder); a bare-year name stays. `splitYear`
  wants a separator after or brackets around ("2018Something" is a word),
  `splitTrailingYear` brackets. Search still matches the directory as spelt;
  chronology is the year sort's job.
  - **Else the location may date it** (`yearOfPlace`, `yearOfName`, tested):
    the release's own directory (above folded discs), never its parent; a
    playlist's file name, then directory. In order: leading and spaced
    ("2007 - Title (Reissue 2020)"; unspaced, a leading year is a scene
    performer's name), alone in brackets ("Title (2019) [V0]"), last between
    separators ("Performer-Title-WEB-2026-GROUP"; last, as a title may be a
    year). Not years: digits run into letters, spans ("1993-1997"), bare
    years. **The .nfo is deliberately not read**: its year is already in the
    directory name.
- **An untagged release takes its parent directory as performer only where a
  tagged release has established that name** (`artistFromParent`, gathered on
  the grouping walk), parents being as often containers ("complete", "EP,
  Single, Demo") as performers; it never invents one. Only where **no track is
  tagged**, for **directory albums only** (a playlist's tracks live anywhere),
  returning the **tagged** spelling, matched case-insensitively (a second
  spelling is a second performer). Separately, **one voice suffices without
  contradiction**: one tagged track among blanks names the release; two names
  with no majority read "Various Artists". Half the tracks is a majority only
  for a name that leads alone (`sharedLead`): a split of one track each is a
  disagreement, and the name that sorts first is no answer to one.
- **A track naming nobody is credited to its release's performer**
  (`Item.Performer`), so its row says whose it is and the client can strip
  that name from the file name. **Never written into `Artist`**:
  `RecordingKey` derives from the tag's `Artist`, and the client builds the
  same key over the queue to keep radio off what is queued (`freshForRadio`);
  a derived `Artist` gives a file two identities by endpoint. Answered **from
  the album build**, beside the spoken verdict (`performers`, under `featMu`,
  read by `stamper`), so every route a track takes agrees, without forcing a
  build (`spokenOf`'s reason, [music](music.md)). On the copy only, never the
  indexed item (the build votes on indexed tags and would feed on itself); a
  track that names a performer is not corrected; a compilation credits nobody
  (its marker means "more than one"; compared case-insensitively).
