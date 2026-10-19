# The music bar, its queue and media keys

- `audio.ts` is a singleton bar with a queue/shuffle model (`order[]` of queue
  indices).
  - Toggles set class and `aria-pressed` together (`press`); a doubled verdict
    press keeps the latest answer (`rateGen`). Pure queue rules live in
    `queue.ts`, tested (`nextPosition`, `placeFirst`, `windowRows`,
    `resumable`). Below 720 px spectrum, radio and link fold into "⋯", as do
    the sheet's Download and Link.
  - The queue panel keeps the listener's scroll unless the current row leaves
    view and repaints on a frame; its length column never yields, is absent
    until measured, and has one right-aligned width over the whole queue
    ("59:59" or "9:59:59", `runsHours`, tested, updated on append).
  - When a set stops, `endCast` re-points the decks (whose sources `startCast`
    cleared) to the current track at the set's position, autoplaying only if
    the listener chose to play here; resuming a parked cast queue restarts the
    transport tick. A track the set advanced to by itself counts as played;
    `advanceWithSet` uses `nextPosition`. `queuedAhead` is tri-state: a `null`
    URI also means "refused", not re-asked each top-up.
  - Radio keeps queued ids and songs (`songKey`) in two Sets extended with the
    queue (`freshFrom`), never walking it per top-up; recent performers are
    read backwards from the position through the order (`recentArtists`,
    tested), not off the tail; `ahead` (tested) counts what follows.
  - Queue and spectrum stack in one anchoring container (`.ab-panels`) so they
    cannot overlap: spectrum lowest, against the bar (the queue never moves
    it); it grows upward within the window, the queue yielding, and is
    pointer-transparent (or the gap swallows presses).
  - Add to queue (`enqueue`): `appendToOrder` (`queue.ts`, tested) shuffles
    new entries among themselves and appends them, never into the existing
    order. Nothing loaded → play the release; played out (`exhausted`) → jump
    to the appended segment's start (a bar shuffle may have rebuilt the
    order); playing → only the order changes, and a set with nothing queued
    ahead gets the first new track (`queueAhead`). The owner is kept; cap
    `QUEUE_CAP`, as `playItems`.
  - The sleeve is never another release's (`sameRelease`, `cover.ts`, tested;
    an `<img>` keeps its picture until the next loads): `prefetchArt` fetches
    the next sleeve with the next track (deck preload, TV handover); one not
    yet loaded hides behind the placeholder unless the release continues (else
    a blink per boundary). A release is one directory, or one album tag under
    one performer.
  - Queue-all (`queueSource`, `content.ts`, tested; `handleTracks`,
    `library/tracks.go`): `/api/tracks?of=` returns a view's tracks at once —
    listed releases in order; each listed performer's releases from their
    first (`ReleasesOf`, matched as the artists view groups) or genre's, once
    each, performer by performer (`ReleasesIn`, uncredited last); or the music
    listing forced to audio. `TracksOf` flattens under one read lock, giving a
    confined caller only tracks it may see. `maxQueue` must equal `QUEUE_CAP`;
    the answer says when cut. Offered on the grouped views, the music chip,
    and mixed listings only on a music-only face. `listFilters` (`query.ts`)
    is the one query wording for grid, m3u export and this; the bar words
    every enqueue outcome.
  - A paged collection needs one version (`collectPages`, `server.go`,
    tested): offsets index one sorted result and the library changes
    constantly, so mixed versions duplicate, miss and stop short of `Total`.
    Spanning versions, it is retaken from the top **once**, then accepted —
    more passes move too, each hundreds of listings under the one query lock.
    The cure, a listing held open across pages, belongs to the library (`List`
    clamps limit to 500).
  - The queue is effectively unbounded (`QUEUE_CAP`, `maxQueue`: a million):
    extended by loop (spreading 100k arguments overflows the stack), painted
    as a window over one tall spacer (`paintQueue`, `Q_ROW` pinned in the
    stylesheet); "play from here" uses queue-all's single answer.
- **Pausing fades** (`FADE_MS`, `audio.ts`; an abrupt stop clicks): 150 ms
  down, then the pause; resuming climbs to the live volume setting. It ramps
  the element's volume, not a Web Audio gain (graph routing is
  irreversible). Frames smooth it and a timeout ensures it ends (hidden tabs
  run no frames); the first applies the endpoint once, under a generation
  counter. A track boundary or close cancels it and restores both decks to
  full (boundaries are gapless swaps; a part-faded deck would play the next
  track quietly). Where inaudible (`fadeHeard`: iOS ignores element volume,
  `volumeSettable`; hidden pages throttle timers) a pause is immediate, or a
  late pause overrides a play pressed meanwhile. A play during a fade-out
  reverses it (`fadingOut`).
- **iOS queues keep the element the listener started** (`AudioDecks` in
  `audiodecks.ts`, chosen by `singleAudioElement` in `playback.ts`):
  permission and the audio session belong to an element. iPhone, iPod and
  iPad WebKit (incl. an iPad's desktop UA, CriOS, FxiOS) swap the source on
  that element and call `play()` in the same callback — no `pause`, `load` or
  async wait; the idle element never plays or preloads (cost: a possible
  buffer wait at boundaries). Never "unlock" a spare by an empty-source play
  counting any error but `NotAllowedError` as success: it proves nothing, and
  a second element can disturb the route. The active element loops
  (`wrappedAround`, `checkWrap`), never ending naturally before the swap
  (WebKit may drop the session there); each seek updates the seek-vs-loop
  clock (`seekDeck`, also the lock-screen scrubber), reset by a source change
  before queued events; the queue's end pauses. Desktop alternates
  prebuffered decks. `AudioDecks` owns source changes, play requests and the
  cancellation generation; stale rejections are dropped, a current one
  reaches `audio-refused`. Verify iOS's audio session and car Bluetooth on a
  device after deployment; the per-track `audio-stalls`/`audio-refused`
  reports name no cause, and request logs are blind behind a buffering proxy.
- The tab title (`nowplaying.ts`) is the player's file, else the bar's track,
  else the app's name.
- Media keys are **claimed, not registered** (`mediakeys.ts`): the video
  player takes the page's one set while open and returns it on close;
  unclaimed actions are cleared rather than left to a hidden bar, and the
  player leaves previous/next alone. Actions do what they name, never toggle
  (the two sides' states can disagree); `stop` pauses in place;
  `playbackState` is published on every play and pause; mute is the system
  mixer's. A callback keeps its owner as `this`. The player's keys are one
  table, `PLAYER_KEYS` (`playback.ts`, tested), which `?` draws, so the card
  cannot drift from the handler.
- The queue panel renders only while visible: unhide it before filling it.
