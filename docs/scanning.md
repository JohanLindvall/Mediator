# Scanning, watching and the indexed directories

## What is media

- `extKind` (`scan.go`) decides what is media, every entry measured. `.ts` is
  in (captures, opening with the TS sync byte; `remuxable` copies their
  H.264/AAC); deliberately out of the table: `.dat` (anything at all by name;
  sniffing admits the one whose bytes are a VCD's MPEG) and `.tif` (a decoder
  dependency for two files).
- **A file whose name says nothing has its first bytes read** (`sniff.go`,
  `SniffContent`): no extension, or one this library does not know
  (`nameSaysNothing`: a dotted release name, a suffix a tool cut short, a type
  it has no use for), from `sniffMinSize` (1 MiB) up. Left alone: a known
  name, which has answered; an unfinished download (`unfinished`: `.part`,
  `.crdownload` and kin), renamed to its real name when whole and indexed
  then, where read now it would show twice; a part of a rar or zip set, which
  its set's first part answers for; and anything smaller, likelier a
  repository object or a lock file (measured: 462 of 138,579 files over the
  floor, 301 of them unfinished). `kindOfMagic` and `archiveOfMagic` are pure,
  tested tables of fixed-offset signatures — no scanning, guessing or
  statistics, as a wrong answer hands a program to the player: ISO base media
  (an unknown brand reads as video; `EnsureCodecs` settles it), EBML, RIFF AVI,
  WAVE, WebP and CDXA (a VCD), ASF, Ogg, FLAC, ID3, an MPEG program stream's
  pack header, a transport stream's sync byte three packets running, the
  pictures, and **ZIP and RAR archives, which are read as any other archive
  is** (`indexZip`, `indexRarSet` — a single file whatever its name), by the
  walk and the watcher alike. `Name` never changes; `opensDirectly` lets
  unknown containers try, `mimeFor` falls back on the kind, the mirrored record
  keeps it.
- Only regular files are media: walk and watcher check before dispatching to
  the subtitle or container readers, so a FIFO named as media neither enters
  the index nor blocks a reader during discovery. Reconciliation drops an
  ordinary file replaced by a special one, while keeping DVD folders, the
  containers that are directories.

## The index, the walk and the watcher

- `internal/library` holds the index (`map[id]*Item`,
  ID = `sha1(abs path)[:16]`), fed via `upsert`/`removePath` by `Scan` (which
  reconciles deletions), the watcher (`watch.go`, every directory) and
  `-rescan` (default 10m).
  - New directories are re-walked (`settleWalks`: 5 s, 30 s, 2 min) after
    `walkNew`: files created before the watch exists are never reported, and
    archive sets need their last volume. `maxSettles` bounds outstanding
    re-walks, not timers (a moved-in tree is one Create per directory);
    overflow is left to the rescan. Settle timers live on the `Watcher`,
    numbered (`armSettle`: a timer cannot name itself to its callback),
    dropped by `Reset` and `Run`'s return; `forget` decides whether the
    callback or `stopSettles` returns the slot, and a callback finding none
    does not walk.
  - Watches are refused outside the roots (`AddDir`, `rewalk`, `AddFile`):
    timers and walks begun before a change outlive `Reset`, leaving watches
    nothing removes.
  - A changed file forgets what was read (`forgetContent`: duration, codecs,
    shape, track/caption lists, looked-for marks) in both upserts; a DVD title
    then restores its declared length (`declaresDuration`). `Chmod` re-reads
    the file: copy tools stamp times last, and the mtime keys the metadata
    cache, grid cell and thumbnail URL.
  - Closes are events, writes are not (`fswatch.go`, `fswatch_linux.go`):
    `IN_MODIFY` fires per `write()` (a download writing its `.part` file in
    tiny pieces raises hundreds of thousands a second, overflowing the
    16,384-event queue) and the kernel merges only identical tail events.
    fsnotify cannot set the mask (`WithOps` unexported through v1.10.1), so
    Linux speaks inotify (`IN_CLOSE_WRITE`, `IN_ATTRIB`,
    `IN_CREATE`/`IN_MOVED_TO`, `IN_DELETE`/`IN_MOVED_FROM`/`*_SELF`,
    `IN_ONLYDIR`); elsewhere fsnotify sits behind `fsBackend`, a write
    counting as a close. The non-blocking fd is a pollable `os.File` (`Close`
    wakes the reader); keep the raw fd for watch calls, as `File.Fd()` would
    make it blocking. A growing file's size updates on close or rescan.
  - A `Q_OVERFLOW` burst is one loss, answered by one walk after it (mid-burst
    it would be stale): `overflowQuiet` (10 s) after the last, `Run` logs the
    count and signals `Watcher.Lost()`; `main` walks via `rescanNow` even with
    `-rescan 0` (that disables timed walks, not correctness).
  - Reconciliation stats unseen paths lock-free (a slow disk must not stall
    readers), deleting under the write lock after a recheck; containers
    reconcile against their own `members`. Member paths hold a NUL (`os.Stat`:
    EINVAL), so the container is asked, once; a failed parse is no verdict.
  - An item whose `FirstSeen` follows the walk's `startedAt` (set after
    `scanMu.Lock()`) survives an empty stat for one cycle (its path may have
    been recreated mid-stat); where the disk answered, or it became a
    duplicate, the rules still decide.
  - Sidecars reconcile like items, dropped only when gone or not `UnderRoots`
    (a removed root's sidecars go with its films); a change sets both counters
    so the closing notify fires.
  - Scans serialize on `scanMu` (rescans and prefs changes overlap cold
    walks): concurrent walks would clear `byInode` and reconcile different
    `seen` sets, misreading hard links and dropping each other's files.
  - Watcher enrichment is debounced (`enrichAfterQuiet`, `enrichQuiet` 2 s, a
    var only for tests): one read after a file's last close or stamp, then a
    notify (the watcher paths' publish contract). Every event arms a fresh
    timer, never resetting the mapped one: `Reset` cannot tell a pending timer
    from one fired and blocked on the mutex, and a stray one deletes a live
    entry, letting reads land mid-write. The reads take a process-wide slot
    (`enrichSlots`, `enrichWorkers` = 4; one disk) and wait out playback
    (`standDownForPlayback`, `Library.Streaming`) at most `enrichBusyWait`
    (30 s), since the waiter holds a scarce slot.
  - `Run` only receives events, keeping the kernel queue (which drops overflow)
    drained; one worker works them in arrival order (Create before close) via
    `eventQueue` (4096, blocks when full). Shutdown: close the queue, wait for
    the worker, stop pending container reads, settles, then the watcher, so
    nothing arms a read after the worker; queued events are abandoned
    (draining them would overrun shutdown).
  - A container's parse and its `indexStored` reconcile are one
    read-modify-write, held whole per container (`containers`,
    `enterContainer` in `scan.go`): watcher events and timers skip `scanMu`,
    and an older parse finishing last would drop new members or install stale
    ranges. Keyed by path alone (two libraries over one file must exclude each
    other), refcounted; order container → `l.mu`, never `enterContainer` under
    the index lock. Walk and watcher name a container identically (rar: first
    volume; zip: `.zip` or first piece; disc image: itself; DVD folder: its
    VOB directory), or the hold does nothing; tested, divergence being silent.
  - Members are read once the writer is quiet (`readMembersSoon`/
    `readMembers`, `containerQuiet` 2 s), as a growing set moves every
    member's mtime: one read pending and one running per container at most.
    Clear the running flag in a defer, or the container is stranded forever;
    `enrichOne` defers its marks too (`tag.ReadFrom` panics on corrupt files).
    `stopMemberReads` drops pending reads on `Watcher.Reset` and `Run`'s
    return, per library (it is in the key), so none reads outside the roots
    or a closing database; running reads finish, and `Run` drops them only
    after draining the worker.

## What is skipped, and duplicates

- `-exclude` globs keep paths out of the index (`excluded`, installed by
  `SetExcludes` before anything indexes; a bad pattern fails the command line
  in `ValidateExcludes`): one without a separator matches a name, one with a
  separator the whole path, and an excluded directory covers everything under
  it. Every door asks — walk, watcher, reconciliation and the restore
  ([persistence](persistence.md)).
- A `Sample`/`Samples` subdirectory is skipped when its release is present
  (`isRedundantSampleDir` → `releaseNear`), else kept; the release is sought
  to `sampleReleaseDepth` (2) below the parent (multi-disc releases hold only
  disc folders, and a disc may be a folder of parts), and another sample
  folder never counts. A sample file beside the film (`isRedundantSampleFile`)
  is skipped only if it is a video ("sample" means something else among
  music) and the directory holds a release ≥ `sampleRatio` (5×) its size or
  an archive set — never by name alone. `namedLikeASample` matches the word
  wherever punctuation sets it off, never splitting on spaces ("Sample Text"
  is a title). Reconciliation asks `stillIndexable` before keeping a path
  still on disk (which also drops new duplicates).
- Duplicates collapse by `(device, inode)` (`dedup.go`; inode alone collides
  across volumes): the first claiming path is indexed, but a real file beats
  a symlink to it in either order. `Scan` clears `byInode` before each walk:
  claims reconcile only afterwards, so a deleted path's claim would drop its
  surviving hard link. `upsert` returns `(changed, dup)`; a duplicate stays
  out of `seen` so a demoted path is removed. `statEntry` follows symlinks
  (`DirEntry.Info` is an lstat).

## The directories to index

The directories to index can change at runtime (`internal/server/prefs.go`,
`/api/prefs`); one callback in `main` moves three things together: the list,
the watches (`Watcher.Reset`, or a watch on a removed directory restores what
the scan removes) and the index, where `stillIndexable` asks `UnderRoots` (a
removed directory's files are still on disk). Stored roots outrank the command
line, which seeds a first run, except under `-lock`, where the command line's
roots stand whatever the database holds; with `-db off` a change lasts the run
and `/api/prefs` reports `persisted: false`. Directories nested in a listed
one are dropped (it walks them already).

**One change of the directories at a time**: with no authentication two PUTs
can interleave, leaving the database naming one set of roots while the index
walks another. `Server.rootsMu` spans the callback (both stores and the scan
launch) and the read of the answer, so no request reports the set another
applied; it is brief and released before the answer is written.
`watcher.Reset()` runs inside the queued scan under `scanGate`, or a walk
already running installs watches under the removed root that nothing removes;
`AddDir` refuses paths outside the roots (for settle timers). **A confined
caller is refused the preferences outright**, reading too (the list names
roots it must not learn), checked before `-lock` so it cannot learn what
`-lock` would say; `/api/info`'s `confined` hides the button (like `content`),
the handler is the guarantee.

**There is no authentication in front of this**: whoever reaches the port can
point the library at any directory the process can read — deliberate for a
personal server on a trusted network.
