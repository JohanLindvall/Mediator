# Plays, verdicts and how music sounds

- **Plays are counted by the client** (`POST /api/plays/{id}`, `plays.go`),
  since a byte response cannot tell a play from a seek or a range request:
  after `PLAY_AFTER` seconds of playing time on the element's clock, counted
  from where the file was loaded — deliberately the "started" floor (5 s), as
  two floors for one idea drift. Handing to a television counts at once.
  - The count shares the position's state record; `Set` reads before writing,
    or position saves would reset it. Clearing a resume point keeps the plays
    and the verdict (`ClearPosition`). A restricted caller's state answer
    leaves out ids the index lacks as well as items it may not see.
  - A play bumps the library `version` (a position, saved every few seconds,
    bumps `watchVersion`), refreshing the per-version collections (which sum
    plays), their ETags and the chips. Counts are pushed into the library
    (listings must not reach another store under the index lock) and stamped
    on handed-out copies, never on indexed items, which walks rebuild.
  - Popular is a filter (played or judged), not only a sort, lest the
    untouched majority bury the rest.
  - Verdicts (`likes.go`, `POST /api/like/{id}`) travel like plays: same
    record (`Position.Like`), pushed into the library, stamped on copies,
    bumping the version;
    in the library both are `ownerCounts` (own lock, a generation the
    affinity cache checks). Popular orders sort on `popularity(like, plays)`,
    the verdict 40 bits above the count; a collection's verdict is net. The
    key is `popular` (`plays` is read as its alias, which saved addresses
    carry). The lit thumb
    withdraws; empty records are dropped; tiles show the verdict beside the
    count.
  - The state store and the library copy are written in one turn per id
    (`Server.owning`, `server.go`): the values are absolute (`Play` returns
    the new total), so unordered writes land out of order. 64 stripes
    (`ownerStripes`, FNV-1a over the hex id; cheaper splits cluster, a per-id
    mutex map needs pruning), taken by `handleStatePut`, `handlePlay`,
    `handleStateDelete`, `handleLike`, and released as every lock is
    (AGENTS.md, "Background work").
  - Owner flags have their own lock (`flagStore`, `annotate.go`), not the
    index's (bolt commits can take seconds). Concurrent first callers share
    one load (else each request of the first burst re-reads the bucket and
    bumps the version); `LoadFlags` takes it too. `SetFlags` holds it from the
    decision (under the index lock) through `SaveFlags`, so bolt sees writes
    in decision order. A load finding the work done discards its read whole,
    never merging per key: a withdrawal is a deletion, which a merge would
    undo.
- **How music sounds is read once in the background** (`analyze.go`,
  `features.go`), the lowest tier: one track at a time, only while `busy()`
  reports no stream or thumbnail and no tag pass runs (`enriching`), resting a
  minute between passes. ffmpeg decodes three 20 s windows at ¼, ½, ¾
  (`analysisOffsets`, skipping front matter) by path or loopback;
  `extractFeatures` yields 56 numbers (pure-Go FFT; MFCCs, spectral shape,
  chroma, loudness, dynamics, tempo with a prior toward 120, three speech
  cues).
  - Vectors live in `features`, stamped with mtime, size and
    `featuresVersion` (like `shapeVersion`); restored at startup
    (`LoadFeatures`), pruned with the item, never re-read for an unchanged
    file (~1 s per track). Pauses count within a window, never across seams.
    Raising `featuresVersion` re-reads every track, a core-day, so it waits
    for a change stored vectors cannot live with: a vector that counted
    pauses across seams is kept, its higher pause shares leaning toward the
    tuned verdict. `-analyze=false` disables it; without
    ffmpeg it does nothing. A failed decode is remembered for the run; a
    silent track's empty vector restores as "nothing to say"; a failed db
    write keeps the vector in memory (not a decode failure); a pass publishes
    only if it read something.
  - Windows are described separately and merged (`extractFeaturesFrom`), as
    concatenation fakes an onset at each seam. Flatness takes one log per
    eight bins. An unmeasured track is sampled at fixed marks, else from the
    start. Synthetic-signal tests pin the arithmetic.
  - A timeout is not a failure: `analyzeOne` bounds a track by
    `analysisTimeout` (90 s, a var for tests). The pass checks the parent's
    `Err` first (a shutdown writes nothing), then maps
    `DeadlineExceeded`/`Canceled` (`errors.Is`) to `markInterrupted`, which
    records only a retry time (`analysisRetryAfter`, 1 h, rather than every
    pass) that `needsAnalysis` honours. `markFailed` would drop
    the track for the run from vectors, resemblances, radio, its release's
    sound and the audiobook vote.
  - `readyForAnalysis` (a measured length, or the tag pass done) gates both
    `analysisTodo` and the pass itself (the file may have changed;
    `forgetContent` clears its length): windows come from the duration, a
    wrong-seconds vector is stamped final, and an unready queued track makes
    the loop spin empty passes. It stays out of `needsAnalysis` ("still to be
    read", for other callers).
  - The write is skipped once the caller's context is gone — a narrowing
    (bolt refuses writes after `Close`); shutdown waits via `analysisDone` in
    `main`.
  - Memos never go backwards: `putScaled`, `putAffinity`, `putSounds` install
    only over something not newer (lock-free builders overlap; an older
    install makes every reader rebuild). An affinity build whose release
    verdicts (replaced wholesale per album build, compared by content) no
    longer match the library's is not installed.
  - One reading of release verdicts per answer: `Similar` filters and stamps
    from the stamper's set; `affinities` hands its verdicts to `spokenWith`
    instead of `spokenSet` re-reading. Two readings can straddle an album
    build.
  - **Shown tempo** (`tempo.go`, `Item.BPM`, `sort=tempo`): column 50 is never
    shown (5–10 BPM whole-lag steps) nor changed (vectors compare only under
    one recipe). `bpmOf` reads the decode's onset envelopes
    (`describeWindows`): candidates a quarter beat apart, scored at their
    period and three multiples under the prior toward 120, the correlation
    read between frames (`correlationAt`). Tags are ignored (2% carry one,
    mostly 0 or a bitrate). Not done: period from multiple peaks (no gain),
    envelope smoothing (hides real tempos), a drawn beat (phase unknown).
    - Floors, each with a test showing the tempo it prevents
      (`TestAnEncodersFlickerIsNoTempo`): onsets floor each band `tempoTopDB`
      (80 dB) under the loudest (MP3 encoders' empty bands flicker like
      drums; the vector's onsets stay unfloored), and the strongest must reach
      `bpmOnsetFloor` (residue peaks 0.4–3.4, the weakest real track 15).
      Clarity under `bpmClarity` (0.3) shows nothing; spoken tracks have none
      (`stamp`). A strong vibrato can still read as a tempo.
    - Stored apart (`blob.PutTempo`, `tempos`, `tempoVersion`, pruned,
      `LoadTempos`). A current non-empty vector lacking a tempo is re-read for
      it alone (`needsAnalysis`) without rewriting the vector (`analyzeOne`
      reports freshness); tempo batches publish via `publishRead` without
      moving the features generation, which would rebuild resemblances for
      nothing. Stamper and order read one map (`publishTempos`); none sorts
      last.
    - Offered where a listing is only tracks (`listsTracks`, `content.ts`):
      the music chip, plus All and Popular on a music-only face; `sortOptions`
      takes the face at both key checks, and boot reads the address only after
      `/api/info`. Shown whole (`tempoLabel`) on tile, sheet (not under
      480px), bar and hover.
  - Similarity is cosine over per-column z-scored unit vectors (`similar.go`),
    brute force deliberately (56 × 100k is milliseconds; an index is one more
    thing to keep right). `Similar` keeps the n best in a sorted slice,
    building a candidate's recording key only once it beats the last (keys
    dominate a radio top-up); releases and performers rank via `nearest`.
  - One recording once (`RecordingKey`, tagged performer + title): only the
    nearest copy is kept, copies of the seed dropped. Untagged files and
    titles naming nothing (`namesNothing`, as `PLACEHOLDER` in `queue.ts`) get
    no key (distinct songs). `FoldRecordings` folds every `of=` answer in
    `handleTracks`, keeping the first (arrival order is what was asked); a
    performance titled as one ("… [Live]") survives — the key is the title,
    not the sound.
  - `Similar` backs "more like this" and radio (`of=similar` on
    `/api/tracks`). A release's or performer's sound is its tracks' mean vector
    (`sounds`, per version and features generation); `near=` on album and
    artist listings orders by it, stamps `Similarity`, counts via
    `countsOfAlbums`/`countsOfArtists`, offers only "Similarity" (direction
    the viewer's) and opens most alike first (`showNear`).
  - Affinity: greatest resemblance to a liked track minus to a disliked one,
    graded −2..2 (`affinityBucket`, 0.35/0.6), stamped with `Akin`; it ranks,
    stays on the wire, and is never drawn (the owner wants no mark).
    `trackPopularity` is verdict, affinity, plays; collections use
    `popularity`. Rebuilt only on `likesGen`/`featuresGen` or changed release
    verdicts, compared by content (`sameMap`; each album build makes a fresh
    map).
  - Pages, similar tracks, the queue and `Get` stamp from one `stamper`
    snapshot. The owner flags' lazy load belongs to `stamper()`, which every
    producer (album sheet included) builds before taking the index lock, or a
    cold request reports nothing hidden, favourite or turned.
  - Speech (`spoken.go`) is scored on the stored raw vector (scaled columns
    lose their mean), tunable without re-analysis; weights live only in that
    file's comment, the threshold mid-gap. A song taken for a book leaves the
    music while a book taken for music stays filed, so: unread is music;
    pauses count only between first and last sound (column 52); a track needs
    a full window of sound (column 55, `spokenMinSound` 19 s, as a 20 s
    window measures 19.99; `spokenVerdict`) or is music; a release votes by
    playing time and needs `spokenMargin` (2:1, not a bare majority; both
    tracks of a two-track release); no analysed verdict before half its
    playing time is judged (`markSpoken`; quiet intros read like pauses); a
    genre naming an audiobook (`spokenGenre`) shelves outright; a track's
    mark follows its release (`byRelease`, from the album build, read by
    `spokenOf` without forcing a build, which would wait on itself under the
    index lock).
  - Audiobooks (releases so judged or tagged) get their own chip
    (`audiobooks=1`, `Counts.Audiobooks`, kept out of `Counts.Albums` so the
    two add up), leave the Albums, Artists and Genres views and queue-all
    groupings (a narrator is not a performer), match "audiobook", and
    `Similar` answers music with music and readings with readings only.
  - Analysis publishes every `analysisReport` tracks and at a pass's end
    (`publishAnalysis`), never per track: the version (collections) and the
    features generation (scaled vectors, resemblances, affinity — rebuilt on
    the request path, so per-track moves make every listing re-z-score the
    library). The pass writes with `putFeatures`, which publishes nothing;
    `SetFeatures` (loader, tests) publishes.
  - **Radio** (`audio.ts`): the remembered toggle tops up with similar tracks
    when fewer than `RADIO_AHEAD` follow, on each track change and at the
    queue's end (moving the parked player on); `append` is enqueue without
    toasts.
    - It draws `RADIO_BATCH` from a `RADIO_POOL` (50) with position-linear
      weights (`pickRadio`, tested), never the head (which would play one
      neighbourhood in one order for ever).
    - A performer's weight falls by `ARTIST_DAMP` (a third) per track drawn
      or among the last `RADIO_MEMORY` queued — multiplicative, not a quota,
      so a one-band neighbourhood still fills a batch.
    - Nothing queued returns (`freshForRadio`), file or copy. Memory is by
      song (`songKey`): `songTitle` strips version tails (words from the
      library's titles; "(Part II)", "(Reprise)", "(Intro)" kept; prefix match
      only for four unambiguous compounds); accents, punctuation and "&" are
      spelling; apostrophes are dropped, not split at (`folded`); a guest in
      the artist tag is still the performer's. An untitled file keys by its
      name's title (`trackTitle`; a wrong fold costs less than a repeat); a
      letterless name has no key.
  - **Artist radio** (`station.go`, `of=station`; `topUpStation`,
    `startStation`) keeps the queue to one performer: one setting with radio
    (`RadioMode`, `media.radio`, "1" = plain radio); bar buttons fold into "⋯"
    on a phone; a performer page's chip starts a new queue.
    - A station is the whole catalogue, nearest the seed first (a 50-pool of
      one band runs dry), unanalysed tracks after by popularity so it never
      falls silent; no seed means popularity alone. The bar draws
      `RADIO_POOL` from the unqueued head, undamped (`pickRadio` `damp` 1;
      damping favours tracks naming a guest).
    - Whose a track is (`performerOf`): its tag decides whenever it names
      anyone, after `withoutGuests` ("feat.", "ft.", "featuring", or a bracket
      opening on one; a bare "feat" outside brackets stays). The release
      credit applies only where the tag names nobody or leads with the
      release's performer (`leadsWith`: "A, B", "A/B", "A;B", "AB", wanting a
      separator or capital after). Never fall back to it because the tag's
      name has no release (that admits split partners and tribute bands).
      Stations are asked for by seed, so every track a station answers seeds
      it again (tested).
    - It ends rather than repeats (`stationSpent`) until the queue, setting or
      manual queueing changes.
  - `radioAsk` generation-guards radio requests: a new setting or queue
    discards answers in flight and frees the busy flag.
