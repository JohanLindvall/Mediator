# Persistence: the index, the state store and the database

## The index and its persistence

- The index is mirrored into the blob db (`persist.go`, `items` bucket) so
  restarts serve it before the walk ends (36k files: 0.7 s vs 101 s):
  `SetMetaDB` tracks changes, `LoadFromDB` restores, `PersistLoop` batches
  every 2 s (failed writes stay dirty). `PruneDB` (deleting vanished files'
  records and everything derived from them: metadata, thumbnails, borders,
  vectors, tempos, fingerprints, skips) and then the state-store prune on the
  same `LiveIDs` (nil when empty) run after **every** *completed* scan via
  `pruneAll` in `main` — never on an empty index, or an unmounted root would
  wipe the cache.
- `LoadFromDB` restores only what the roots and the `-exclude` globs still
  admit (either may have changed since the record was written), before the
  first scan.
- A prune restores what it deleted from under the watcher, prune and repair
  being one function (`pruneTo`, called by `PruneDB`) so the repair always
  runs: items indexed after the live-set snapshot are re-marked dirty
  (`remarkAfterPrune`; cheaper than freezing the index), and their metadata is
  rebuilt from the item, since the restored "examined" mark keeps
  `needsEnrich` from re-reading — only where examined at the current shape
  recipe, so no withheld verdict is written. Thumbnails and feature vectors
  are deliberately not restored (made on demand; re-analysed next pass).
- A walk and its prune are one operation (`scanGate` in `main`, all three walk
  sites), or a concurrent walk's new records are pruned against an older live
  set. The watcher's microseconds-wide snapshot-to-delete window remains;
  closing it needs the library's scan lock, unreachable from `main`.
- An id is never both dirty and removed (one flush transaction would write then
  delete it; a recreated path keeps its id): `markDirty` and `upsert`'s
  creation branch clear removals, as `markRemoved` clears dirty; `flush` never
  deletes an indexed id; `SaveItems`/`PutPositions` apply removals before puts.
- Stores stop after the HTTP drain (on the `signal.NotifyContext` context they
  would exit before handlers in `ServeHTTP` record writes): flush loops use
  `storeCtx`, stopped by one deferred wait registered after the deferred
  `db.Close` (so LIFO runs it first, error paths included). The analysis
  goroutine (`PutFeatures`) is awaited there (`analysisDone`) for at most
  `analysisDrain` (2 s): it runs after the first walk and `Scan` takes no
  context, so an outright wait could last a whole cold walk; its context check
  and bolt's closed-db refusal cover the rest.
- Final flushes serialize snapshot-and-commit and re-check at most three rounds
  (`finalFlushRounds`, `finalPersistFlushRounds`), catching mid-commit writes
  without hanging on a failing database; a failed metadata write is restored
  only if nothing newer is pending. Order: cancel producers, bounded analysis
  drain, final commits.
- Rar members and DVD titles are never persisted: stale byte offsets would
  serve garbage, so the scan re-derives them.

## The state store and the database

- `state.Store` (positions) lives in the shared blob database (one file to
  back up or delete), in memory (`Get`, `All`), flushed debounced: changed
  ids, one transaction. With `-db off` positions last the run — never a second
  store. Owner's data: no `(mtime, size)` stamp; `Prune` drops only ids the
  index lacks.
  - `Flush` clears the dirty set before its transaction (a save during the
    commit is the next flush's), so `Run` ends with `flushFinal`, bounded by
    `finalFlushRounds` (3) so a failing database cannot hold shutdown open.
  - `Flush` drops its lock before committing, so a failed flush's `restore`
    discards a removal whose id was marked dirty meanwhile (else one commit
    writes and deletes it); a put whose record was since deleted is skipped by
    `Flush`'s existence check.
  - Writes go through a one-method `writer` interface so tests can fail them;
    `Load`'s nil check stays outside the struct literal (a nil pointer in an
    interface field is non-nil).
- A new database file is created `0600`: it holds the owner's state and the
  signing key. An existing one keeps its permissions (the README says how to
  tighten one).
- The database **epoch** (`blob.initEpoch`, minted when the file is created,
  served by `GET /api/info`) is in every thumbnail and sprite URL: they are
  `immutable` and otherwise versioned only by source mtime, so a new database
  must change them and the same one keeps the cache. `main.ts` awaits
  `/api/info` before the first listing; the live stream opens earlier, so its
  change handler refetches nothing before the first draw (`booted`), or the
  default query would fetch the whole library under a link naming a search.
