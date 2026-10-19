# Names, text encodings and search

## Names and text encodings

- **A file name is bytes** (`name.go`): `encoding/json` turns invalid UTF-8
  into U+FFFD, so `displayText` decodes **only display name, display path and
  search text** (invalid bytes as Windows-1252, byte by byte). `Path`, its
  hashed id and the database record stay raw, or they name no file and change
  every id: `Subtitles` matches `filepath.Base(it.Path)`, never `it.Name`;
  `blob.Item` keeps such paths in `PathBytes` (base64, only when needed; JSON
  strings lose them); `upsert` repairs an item whose stored path differs from
  the walked one (a lossy record), or reconciliation drops it.
- **Thai arrives as TIS-620**, which bytes cannot tell from Windows-1252, so
  `looksTIS620` judges shape: **four or more Thai-range bytes with no ASCII
  between** (European accents come one or two at a time). Any byte in
  0x80–0xA0 means Windows-1252 (unassigned in TIS-620).
- **CP1251 mistagging** (Cyrillic where Latin-1 has accented vowels: 0xF6 ö/ц,
  0xE4 ä/д, 0xE5 å/е, 0xF8 ø/ш) is valid text (even in UTF-16 tags),
  undetectable from bytes. `reinterpretCyrillic` judges by company: a Cyrillic
  letter beside a Latin one is a misread byte, a Cyrillic word alone a word
  (real Russian far outnumbers the damage); letters only, never punctuation.
  In `setMeta`, so cached values are fixed without a rescan. Double-encoded,
  accent-folded names ("Ã¶" → "A¶") are ambiguous: never repaired by rule;
  rename them on disk.
- **Misread-and-rewritten text is put back** (`reinterpret`,
  `reinterpretParts`, `westernBytes` in `name.go`, tested): UTF-8 made by
  reading bytes as Windows-1252 becomes those bytes again, read as UTF-8 if
  valid (first: its Thai lies in TIS-620's range), else as TIS-620 if
  Thai-looking, up to `misreadRounds` passes. Safe: accidental UTF-8 is rare
  in Western text, and `westernBytes` refuses other alphabets at their first
  letter. A two-byte-script letter (Greek to Arabic) beside a Latin one is
  refused (`strandedLetter`), or a chance Ukrainian letter in English gets
  "repaired"; three-byte scripts go unchecked (CJK sits beside Latin in
  titles). Paths go a component at a time (`reinterpretParts`); `mayBeMisread`
  returns early, unallocated, without a character in U+00A1..U+00FF. In
  `displayText`, it reaches all shown and stored text without a rescan.
- `cleanTag` decodes ID3v1 frames (never UTF-8) the same way. `setMeta` trims
  tag whitespace (a leading space sorts first and splits an artist) and
  repairs misreads via `displayText`, rebuilding bytes from the reader's
  Latin-1 string (`westernBytes`); one door cleans cached values too.

## Order and search

- **Names sort as a reader counts** (`naturalKey`, `natural.go`, tested): a
  digit run is its number (length byte, then digits without leading zeros),
  case is ignored, a run of spaces, underscores, dots and hyphens is one
  separator, nothing at either end counts; otherwise code points, keeping
  ASCII's classes (separator, punctuation, number, letter). Built once per
  name — `Item.nameKey` in `indexText` (every door already calls it),
  collections' `sortName` — and compared as plain strings: comparing names
  in place measured ten times slower over 150k. Every name order reads it:
  the name sort, numeric sorts' and episode ties (`byName`), `orderBy`,
  discographies and a release's untagged tracks.
- Search (`search.go`): items index lowercase word runs of name + absolute
  path + tags; every query word must be a substring. Rebuild `lower` via
  `indexText` whenever its inputs change, at every door (`upsert`,
  `upsertStored`, a repaired path, `setMeta`, `LoadFromDB`). Absolute, not `Rel`, which starts at
  the root's base name and hides where the root is. Albums index
  name/artist/genre/year plus their directory or playlist file (`fillAlbum`),
  so a file-listing hit answers one view across, and sort by a separate
  `sortName`; artists index no place.
  - Since the search persists across views, a release also matches through
    any one visible track holding every word, and a performer through any
    matched release (`albumsAnswering`, `performersAnswering`; shows via
    `answering`); tracks are checked only for releases their own text missed.
    Album and artist listings, like-this, chips and queue-all share this;
    genres do not.
  - **Results rank by how directly they answer** (`rankByHit`, after every
    view's sort — items, releases, performers, genres, shows — and so for
    queue-all too): first what the search names, word for word as `tokenize`
    reads words (`hitName`); then what shows every word on its card in this
    view (`hitCard`: a track's title, performer, release, genre and year; a
    release's title, performer, genres and year; a performer's name, genre and
    years; a genre's or a show's name); then what was found further away
    (`hitBeyond`: a folder, a file name behind a title, a release's tracks, a
    performer's releases, a genre's bands, a show's episodes).
  - **Within a tier a whole word beats part of one** (`hits.rank`): each
    tier runs by how many words were found only inside longer words, none
    first — judged on the card in `hitCard`, on the whole text in
    `hitBeyond`. A whole word never lifts a thing out of its tier: every file
    under a root of that name holds it whole in its path, and would bury
    every card showing it. A thing its own text does not find but a member
    does ranks as found further away, by the member's answer (where its own
    text finds it, that text decides): a release by its best track
    (`albumsAnswering` fills `byTrack`, then asking every track until one is
    whole, where a filter stops at the first), a performer by their best
    release (`performersAnswering`), a show by its best episode
    (`byEpisode`).
  - Each rank keeps the view's sort, either direction (`rankBy`, a stable
    counting sort). The text is built in three segments with where the first
    two end (`segmentedText`, `nameEnd`, `cardEnd` on each item and
    collection), and a whole word is a token between spaces (`" w "`), so a
    rank is a slice compare and a few substring checks, never a tokenize per
    search.
