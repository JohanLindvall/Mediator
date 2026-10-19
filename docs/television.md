# Television: shows, seasons, intros and credits

- **Television is parsed from names** (`series.go`); half the episodes are
  named only by a "Season N" folder.
  - Series name: the **shallowest directory marking a season with text before
    the marker** (a bare "Season 5" defers to its parent); the file comes
    last, often naming the release group. **A file is an episode only if its
    name says which episode**; the bare pack "S02" (two digits) and "Series 2"
    are directory-only forms. **Markers are whole tokens** (else "-S74tb48v"
    is season 74); "1x02" needs real numbers and no screen shape ("03x00",
    "9x16"). **A series needs more than one episode** (lone clips are the
    false positives; the file stays listed).
  - Parsed at all four index doors, warm start included (the mirrored record
    has no series field). **Video's own**: no video, no shows; a restricted
    face counts shows itself, by the same rule (more than one visible
    episode).
  - **Search finds a show by name or by any one episode's own text**
    (`answers`, `answering`, shared by `SearchSeries` and `CountsFor`;
    `Item.lower` kept as `eps`), as the file listing does, after grouping over
    what the caller may see (hidden episodes reveal nothing). Matched only
    through some seasons, a show is a copy naming them (`Series.Matched`) for
    the seasons view (`seasonsOffered` in `query.ts`, tested), whose listing
    searches episodes too; the card stays whole. The seasons view refetches
    shows when the search changes (`CollectionSource.searched`).
    Hidden/favourite flags do not affect show counts.
  - **A restriction regroups** (`AllowedSeries`), never filters the cached
    list, so shows, counts, running times and seasons are the reachable ones
    (paid only with the header set). **A show's face is a total order**
    (`before`: season, number, id; season covers' mtime ties likewise; albums:
    `ID` after `lower`), or tiles cycle as the list rebuilds.
  - **Seasons ride inside the show**; episodes are an ordinary listing sorted
    by episode, not a sheet, so the player steps through them. Running times
    only when every episode is measured.
- **Intros and credits are detected from audio** (`fingerprint.go`,
  `skipdetect.go`, `skips.ts`, `GET /api/skip/{id}`), never asked for (no
  dialogs): a season's episodes share opening and closing audio.
  - The first `skipHeadWindow` (10 min, ≤40% of the episode; cold opens run 5+
    min) and last `skipTailWindow` (4 min, ≤30%) are fingerprinted
    Haitsma–Kalker style (32 bands 300 Hz–2 kHz, 16 frames/s, 31
    band-difference sign bits: level-invariant, nearly EQ-invariant;
    `fpMaxBits` 9), with **one bit for silence**, or silent openings match.
    The intro is the longest opening stretch another episode shares; credits
    likewise.
  - A match is a run at one alignment (`commonRun`, walking every alignment in
    tens of ms), riding up to 1 s of misses with ≥3/5 matching; `trimRun` cuts
    its ends to dense matches (chance matches, 1 in 60, would pad them and win
    the vote). **Neighbours must agree**: two answers within 4 s from the
    nearest episodes (until three answer or six are asked; one may share a
    recap); a lone answer stands only if all others were asked. Over
    `skipMaxIntro` (3 min) is repeated footage; an intro running into the
    window's end takes the season's typical length from its start
    (`typicalIntro`, median of whole ones). Marks to 0.25 s; credits as
    seconds **before the end**.
  - **Lowest tier**: `SkipDetectLoop` on the music analysis's gate, one
    episode at a time, a minute between passes. A season is rejudged only when
    its `signature` (ids, identities, lengths) changes; fingerprints are
    stored by file identity (`prints`, pruned with the item; `printsVersion`
    for the recipe), so decodes (days for a large library) are paid once.
    `GET /api/skip/{id}` calls `WantSkips`, which puts the season first and
    wakes the loop; it is read past the busy gate, **outwards from that
    episode** (`nearestFirst`), and judged once its neighbours are in and
    again at the end. Results (`SkipFor`, `LoadSkips`, `skips` bucket) go
    through the face.
  - **Buttons only**: *Skip intro* above the controls, outside their fade;
    *Next episode* in the credits opens the next **past its intro**
    (`startAfterIntro`, marks prefetched) and records this one **finished**
    (`persistDone`), so it does not resume at its credits. A cold open plays
    first; via *Next episode* the intro skips itself once
    (`skipIntroOnArrival`), so seeking back offers the button. Nothing else
    skips unpressed (a wrong mark costs a button, not a scene); none in the
    intro's last second (`SKIP_MARGIN_S`).
