# Archives, discs and the loopback input

- **A member a set holds but cannot serve is reported**: `parseRarSet` and
  `parseZip` return `[]rarSkip`, and `reportSkips` logs why — solid (with
  method), encrypted, a non-deflate zip method, a name held twice, a missing
  split-zip part, incomplete (bytes of total; normal mid-download). **One line
  per set, once per process, naming no members** (`skipReasons` groups by
  kind); `Library.once` keys it by set and reason kinds, so rescans repeat it
  only when what is wrong changes; a set's parse failure likewise.
  - **Compressed members are unpacked** (`pack.go`, `unpack.go`). The
    process's own near-start reads (headers, tags, thumbnails) use
    `packedReader`: unpacked as read, never written; a forward seek discards,
    a backward one restarts. Playback seeks everywhere, so a streamed member
    of `unpackFrom` (32 MiB) or more is unpacked once into the shared scratch
    budget and served as a file (`OpenForPlayback`; the owner's choice over
    unpacking in pieces); one bigger than the whole budget streams as packed.
    An unpacker caps the bytes it writes before it checks for excess output,
    never after filling the disk. The copy follows the rewrap's rules
    ([rewrap](rewrap.md)): one per (path, time, size), made by the first
    asker, renamed into place, checksum-verified, room made first counting
    copies in progress, adopted across restarts, owned by no request,
    `unpackAtOnce` (2) at a time; least recently wanted is evicted but **never
    within `unpackKeepFor`** (5 min, as `remuxKeepFor`: a player reopens it
    per range); `CloseScratch` stops it before the drain, a late copy kept for
    the next run and not offered. `serveStream` marks playback **before** the
    open, which is the unpacking.
  - **Native Go only, by the owner's rule** (no unrar or 7z): klauspost/compress
    flate, rardecode. rardecode v2.4.1 truncates RAR 2.9 members whose LZ match
    passes the window's end (`copyBytes` clamps); `go.mod` pins it plus the two
    commits of nwaples/rardecode#69 — drop the `replace` once a release has
    them.
  - **Solid archives' members are reported and left out** (each read would
    unpack every earlier member). A compressed member is complete only if its
    last part says nothing follows and no volume is short (`packing.ended`,
    `short`); it is never served in part. It is matched in rardecode's listing
    by name, else by sizes (rar keeps two name encodings; this parser reads
    the first); `rarListings` caches the last four sets' listings.
  - **A member that does not unpack is damaged** (`ErrDamagedMember`: broken
    deflate, checksum mismatch, short output, or rardecode's equivalents): a
    verdict, unlike a failed read — refused as "the archive it is in is
    damaged" (`openFault`) and remembered per run by identity.
- **Zip** (`archive.go`, `zipset.go`) uses a rar set's doors (members after a
  NUL, `indexStored`, not persisted, `OpenItem`, `enterContainer`). **The
  directory is parsed here, not by the zip package**, which drops the part a
  member starts in; one reader serves every shape, the single file included.
  Spanned (`name.z01 … name.zip`) and byte-split (`name.zip.001, .002 …`) sets
  map members to `storedEntry` segments (`zipSet.span`). The container (the
  `.zip`, or the first piece) is named from any part by `zipContainerOf`
  exactly as the walk names it (test-pinned); a part arriving or leaving
  re-reads the set; `name.z01` is a zip part only where `name.zip` exists.
  Pieces a file host renamed apart group by name, else by name less a trailing
  `-identifier` (`hostID`); a number two files hold joins nothing.
  - **A member's own header must name it before it is served** (`dataStart`),
    catching any wrong offset. A missing part costs only its members
    (`incomplete: part 1 of 4 is not here`); an archive read cleanly but not
    whole (no end record, unparsable directory, byte-split set short of its
    last piece) is a verdict (`zipShape`) and its members go, where a failed
    read keeps them. A self-extractor's prefix shifts offsets, as in the zip
    package.
  - The directory is streamed through a bounded buffer, counting the records
    actually read; a ZIP64 value a record needs must be present and fit
    signed bounds, and a member's local method and flags must agree with the
    directory. The synthetic fuzz target (`FuzzZipDirectory`) stays beside the
    parser's regression tests.
- **An archive with more media than `-archive-max` is left out whole**
  (`archiveMax`, default 1,000, 0 for none; `mediaMembers`), never cut to its
  first thousand: a verdict, logged once at Info (`archiveOverCap`). A zip is
  judged from its end record (`zipEntriesPerMember`: over ten entries of any
  kind per allowed member) before its directory is read. Its cost is in
  `DefaultArchiveMax`'s comment.
- Rar (`rar.go`): RAR4/RAR5 members, stored or compressed, are virtual items
  (`Path` = "<rar>\x00<member>", `Archived()` true) read via `OpenItem`. A
  `storedEntry` (name, size, byte ranges in files we do not own) also carries
  DVD titles and zip members, so `Archived()` means "bytes inside something
  else"; its segment index lives on the reader (`newStoredReader`, searched by
  `readAt`), so no producer can forget it.
  - Open item content via `library.OpenItem`, never `os.Open`. ffmpeg reads
    archived bytes over the loopback stream URL (thumbnail, probe, all three
    converters); the pipe is only the no-loopback fallback and cannot seek.
  - A second member under a held name is refused and reported. Legacy
    numbering runs past `.r99` into `.s00…` up to `rarMaxVolumes`. The
    stitched reader drops zero-length segments, keeps at most
    `rarMaxOpenFiles` (4) volume fds (LRU; never 100 per viewer) and is
    mutex-guarded for parallel `ReadAt`, as `io.ReaderAt` requires. A failed
    seek keeps the old position, and one that would overflow is refused.
- **DVD-Video** (`disc.go`) is a parser, not a reader: VOBs (MPEG-2 program
  streams, stored plainly, split at 1 GB) are a `storedEntry` in an image or a
  `VIDEO_TS` folder alike, so reading, loopback, unpersisted offsets,
  thumbnails and sprites come free.
  - A **title** is indexed: `VTS_01_1.VOB` … `VTS_01_4.VOB` are stitched in
    the disc's numbering order, never directory or offset order (a film
    written back to front would start half way). Menus (`VTS_nn_0.VOB`,
    `VIDEO_TS.VOB`) and titles under `discTitleFraction` (1/20) of the largest
    are left out. The ISO9660 parse decides, not the extension (`.iso`/`.img`
    prefilter only); UDF-only images (Blu-ray) answer nothing, deliberately; a
    partial image's ranges are clipped to what is there.
  - A title of an image inside a rar set is two mappings deep (`placeInStored`
    the second, `discsInside` the replacing) and is named after the set's
    **directory**, or the member's stem where the set holds several images;
    an unpacked disc's after the first directory at or above its VOBs that is
    not `VIDEO_TS`, `AUDIO_TS` or `dvd`; a title number only where there are
    several. A DVD folder is folded into its titles
    (`isDVDStructure`, once per folder per walk) and `stillIndexable` agrees,
    or reconciliation restores its VOBs; any other `.vob` is a plain video.
  - **Only the disc knows a title's length** (ffprobe's estimate can be half):
    `ifoDuration` sums the distinct **cells** of `VTS_nn_0.IFO`, not programme
    chains. It rides the `storedEntry` (`upsertStored`, `nativeDuration`);
    `setMeta` and `setProbe` must honour it (`declaresDuration`). Only a
    probe's duration is outranked, not the rest of it.
  - **A DVD is seeked by byte** (`library.SeekByte`), ffmpeg refusing to seek
    past its wrong estimate: cell sectors map time → offset, passed as
    `-seekable 0 -offset <n>` instead of `-ss`, loopback only. **Both flags
    are required**: with a seekable input the demuxer rewinds and silently
    undoes the offset. The map is kept only if the cells cover the whole
    title; within a cell it interpolates. `convertInput` decides input and
    seek together for every converter ([conversion](conversion.md)).
  - No episode boundaries are recorded, so a multi-episode title is one item,
    never a guessed split.
  - **A downloaded title's clock is straightened** (`ps.go`): each authored
    piece restarts near zero, so timestamps jump back at cell joins (not the
    stitching: a plain `cat` of VOBs does it too). The rewriter fixes
    SCR/PTS/DTS in place from the cell table: 2048-byte packs and fixed-width
    fields keep the length (so `Content-Length` and Range stay honest), and
    joins are known, not guessed.
    **Download only**: playback seeks by byte and must not depend on it.
    Navigation packs (stream 0xBF) are untouched. ffmpeg `-c copy` into MKV is
    rejected (minutes and gigabytes of scratch per download, no resuming, no
    longer a `.vob`). The timestamp readers share the archive seek validator:
    a negative offset or a read past the end is refused before rounding to
    sectors or allocating, and the sector end is clamped without overflowing
    the file offset.
  - Nothing decodes MPEG-2: `opensDirectly` and `decodesVideo` map `vob`,
    `mpeg2video` and `mpeg1video` to `video/mpeg`, so conversion starts at
    once and the browser is never handed the disc.

- **The loopback input** (`loopback.go`): ffmpeg/ffprobe read archived content
  over `/api/stream/{id}` (Range over `OpenItem`, a member's only seekable
  view; a pipe reaches only the first minute) at the address `run` gives
  `library.SetLoopback` (`LoopbackAddr`: unspecified/loopback IP → base URL;
  one LAN interface → `""`, pipe fallback). `X-Media-Internal: <per-process
  random token>` (`library.InternalHeader`/`InternalToken`) authorises
  nothing: `handleStream` only skips `StartStream` for it (else thumbnailing
  throttles against itself and pauses enrichment) and applies
  `internalStreamCap` (64 MiB per response); a runaway reader is bounded by
  the tile's budget and ffmpeg's `-rw_timeout`.
