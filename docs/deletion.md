# Deleting from disk

**Deleting from disk is two requests; the split is the safety**
(`library/delete.go`, `library/delete_exec.go`, `server/delete.go`;
`deleting.ts`, `deletedialog.ts`, tested). `POST /api/delete/plan` changes
nothing and lists exactly what would go (whole folders, lone files, counts,
size, container-mates); `POST /api/delete` with its token removes exactly
that. Plans live 10 min and are single-use.

- Execution re-checks: a file whose identity, mode, size or full mtime
  changed stays; folder plans snapshot every entry (sidecars and empty
  directories too) and `checkDeleteTree` rejects any addition, removal or
  replacement; directory handles are pinned with `os.Root` and confirmed
  entries removed one by one with `Remove`, never `RemoveAll`, so concurrent
  arrivals survive; roots are re-checked; a partial deletion is reported and
  reconciled with the index (`delete_snapshot_test.go`).
- **What goes is what the thing is made of**: a file and its *own*
  subtitles (`Subtitles`; each goes with the video whose name it carries
  most of, `subtitleOfAnother`: `Film.Part2.en.srt` goes with
  `Film.Part2.mkv`, not `Film.mkv`); for content inside another file, the
  whole container (`rarVolumes`, `zipPartsOf`, the disc image, a DVD
  folder's files) and its other members, listed (`Others`); a release, show
  or season's tracks or episodes; a playlist release's tracks plus the list,
  or only the list if a track lives outside the playlist's folder (a
  mixtape). A release is taken only by its own id (`AlbumByID` also answers
  a track's).
- **A folder goes whole only if what remains is furniture** (`folderGoes`,
  `leftover`): .nfo, checksums, cue sheets, logs, playlists, subtitles, OS
  litter, a release's sample (by name or Sample folder), and pictures
  **named or filed as artwork** (cover, folder, poster, scan, Covers/Scans
  folders). Anything else keeps it: another item, a document, an unindexed
  archive, a symlink, and above all **photographs** (`IMG_0001.jpg` is not
  artwork). Never a root, nothing outside the roots, no folder containing a
  root, nothing past `folderScanLimit` entries. A folder's parent is
  considered **only if the folder was part of a release** (`partOfARelease`:
  disc, season, `VIDEO_TS`, Sample, Subs), carrying a DVD up to its release
  and seasons up to the show, never a release into the folder it is filed
  in (an emptied `TV` folder stays). Files inside a folder going whole are
  still **in the plan** (`PlannedFile.InFolder`), or the second look takes
  them (a rar set's volumes) for strangers and keeps the folder.
- **Only the whole library deletes** (`mayDelete`): no content-restricted
  face, path-confined caller or `-lock` server, since with no
  authentication a view given to someone must not delete the owner's files.
  `/api/info` reports `deletable`; the handlers enforce it. Both requests
  need a JSON body (a cross-origin preflight is refused), and only the
  plan's answer carries the token.
- The index is updated at once (`Library.Remove` per path, covering
  containers and sidecars, notifying). Every deletion is logged at Info
  regardless of `-debug`, with what was kept and why (it cannot be undone).
- Offered from a card's menu (right click, or a hand-timed long press since
  phone Safari sends no `contextmenu`; the lift's tap is swallowed until the
  next gesture), the player's delete button (next file, or close) and a
  release sheet's menu (closes it). The dialog captures keys at the window
  ahead of the player's document listener, so Escape answers it; Cancel has
  focus.
