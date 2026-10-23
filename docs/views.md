# Views, sorting, addresses and chrome

- **A music-only face opens on the artists** (`defaultMode`), its file listing
  being tracks in disk order; a link naming a view still opens that view.
- **File listings default to Flat**, with a **Folders** option beside sort
  (`supportsFolders`): All, Videos, Images, Music, In progress, Watched and
  Popular. Folder cards open the ordinary item grid with an exact leaf-name
  filter; a folder chip returns to the groups. Search survives both steps.
  The hash carries `group=folders` and `folder=<name>`, so Back, reload and
  shortlinks restore the view. Existing collection views hide this control.
  Folder cards offer aggregate sort keys and hover details: matching item
  count, size, known total playing time and latest modified and added dates.
  Their tiles use the same responsive width and height as individual files;
  their contents offer file keys.
  The toolbar wraps when its controls cannot fit on one line.
- **Resolution and bitrate are separate orders** (`pixels`, `bitrate`; videos
  only). Resolution sorts by **area** (one dimension misranks portrait clips; a
  turned picture ties its twin); bitrate is file size over playing time (no
  stream read), deliberately not size. An unread shape sorts to the unsorted
  end.
- Every view change refills the sort select from `sortOptions(mode)`, drilling
  into an artist included (releases take album keys). A key that does not
  survive falls back to the view's opening key (`openingSort`, in `sorts.ts`
  beside the option table, both pure and tested) rather than reach a server
  that ignores it: the table's first row, except the popularity listing
  (popularity key) and a performer's releases (by year: a discography reads in
  order); a genre's releases stay by name. The drill-down sets the key
  outright, or the artists view's name key carries over. `openingDesc`
  (tested): newest first, except a **season**, which opens from its first
  episode whatever list led there. An address naming a sort or direction keeps
  it; after that the direction is the viewer's. Every way into a view (chip,
  drill-down, brand, address) goes through `enterView`. One `bindCollection`
  wires the five grouped sources and one `collectionOnScreen` says which is
  showing, for both `reloadGroupedView` and the guard keeping a listing's stale
  counts out of a grouped view. The six collection cards are one
  `renderCollectionCell`.
- UI state (mode/search/sort/artist) is the URL hash (`readHash`/`writeHash`,
  `main.ts`): a view change is **pushed**, a keystroke or sort **replaces**
  (Back leaves a drill-down, not the app; a search leaves no trail); arriving
  by address or shortlink replaces. Grid cells take focus and open on Enter or
  Space. A tile's play count, verdict and spoken-word marks are one `.marks`
  row beside the watched tick, styled like the duration pill. The audiobook
  shelf and like-this listings say *why* they are empty while unanalysed.
- **A shortlink is the hash under a short name** (`links.go`, `blob.PutLink`,
  `web/src/links.ts`).
  - The server stores the fragment and **never parses it**, so no view, present
    or future, needs backend work; validation is of shape only (printable
    ASCII, no `#`, bounded length), never meaning.
  - `/s/{code}` redirects (a fragment never reaches a server): 302 to
    `/#<target>`, an unknown code to the library rather than an error.
  - Both directions are stored (`c`/`t` prefixes, one bucket, one transaction)
    so a view keeps one code. A mint is one operation (`links.mintMu`), or
    simultaneous presses both miss and orphan a code; it is taken outside the
    index's lock, which the memory path takes inside.
  - The item is arrival state (`i=`, `al=` for a release): `readHash` reads it,
    `writeHash` never writes it (an overlay is not a view), `openLinked` clears
    it on reading (or it comes back). A linked item opens on a listing of one
    (`justThis`) on top of the view; `viewParams` drops any existing `i=`
    before adding one (or the address names two).
  - A link belongs to its hostname (one server, several faces): the host is the
    first component of both keys (`linkKey`, NUL-separated, NUL being in
    neither part), so under another name a code reads as unminted. It is a key
    component, not part of the code (a second mechanism; short markers could
    collide). `linkHost` reads `X-Forwarded-Host`, then `Host`, as do the m3u
    export's absolute URLs (`requestBase`); without nginx's
    `proxy_set_header Host $host` every face is one host and links cross
    silently, so the mint logs its host. Lower-cased; port kept (two servers,
    two libraries).
  - No authentication, and no face filter on the redirect: enforcement is on
    the request for the thing itself.
  - The alphabet omits `i`, `l`, `o`, `0`, `1` (codes are read aloud).
- The listing's scroll position belongs to the app (`scrollhold.ts`): overlays
  save it on open and restore it on close, `#scroller` being
  `overflow: hidden` meanwhile. On leaving iOS's native fullscreen (the only
  one a `<video>` gets on an iPhone), WebKit scrolls the grid to the top
  *after* the close event, so the restore re-asserts next frame and shortly
  after, only if the listing sits at the very top (sparing a reader who
  scrolled).
- Touch-keyboard text fields are 16px (`@media (pointer: coarse)`): iOS Safari
  zooms in on smaller ones and never zooms out. `#scroller` refuses horizontal
  overflow.
- The preferences list truncates long directories from the head with a
  right-to-left box, so each path is in a `<bdi>` (a leading `/` is
  bidi-neutral), its `title` on that element (tooltips inherit direction).
  The sheet allows no edit before its first read and disables its controls
  while saving; once closed, it ignores late answers.
- **The grid takes the whole window from the top left**: nothing (grid, top
  bar, transport) is capped or centred; a capped grid looks displaced and
  wastes columns.
- **The chips are reconciled, not rebuilt**: counts change constantly, and a
  click fires only when press and release hit one element, so rebuilding
  swallows clicks. Structure is redrawn only when it changes (chip set,
  drill-down); numbers and lit state are written into existing elements.
- Filter chips wrap onto as many rows as needed at **every** width, never
  scrolling sideways (later filters would hide behind an edge); the top bar has
  no fixed height.
  - At ≤560px the chips container dissolves (`display: contents`) so the sort
    pill ends the last chip line when it fits, else wraps right-aligned;
    `space-between` goes (it would spread loose lines), chips packing left and
    the toolbar's margin pushing it right; the controls share one height.
  - On **coarse pointers only** (a desktop window has a wheel; a bar ducking
    every tick is a twitch): counts compact (`chipCount`, tested; five digits
    and up round to thousands, "210k"), and the bar hides while browsing,
    returning on an upward flick or at the top (`barhide.ts`, a pure tested
    reducer; `main.ts` only toggles a body class). The collapse is
    `grid-template-rows: 1fr → 0fr` with the padding on an inner `.bar-inner`
    (outer padding would leave a strip). Reducer rules: downward travel
    accumulates, so slow scrolls hide; a jump larger than a finger makes in a
    frame is an overlay restoring scroll and decides nothing; the top outranks
    that guard (a view switch resets scroll at once, and a bar hidden at the
    top could never return).

## Server-complete, UI-pending

Complete and tested on the server, deliberately not yet in the UI (the grid
has no selection model): the hidden/favourite flags (`PUT /api/flags`, whose
batch form is faced like the single one, else it bypasses the restriction and
is an existence oracle; the `hidden=`/`fav=` listing filters), the seeded
shuffle (`sort=random` + `seed=`) and the m3u export helper (`playlistUrl` in
`api.ts`). The endpoint itself is in use: the player writes rotation, kept
borders and the picture's shape through it.
