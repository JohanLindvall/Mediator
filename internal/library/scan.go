package library

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	gopath "path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// extKind maps lowercase file extensions to media kinds.
//
// Everything here was measured against real disks rather than guessed at.
// The transport streams are the ones worth knowing about: `.ts` is what a
// stream capture is written as, and it was the single largest thing the
// library could not see.
var extKind = map[string]Kind{
	// video
	".mp4": KindVideo, ".mkv": KindVideo, ".webm": KindVideo, ".mov": KindVideo,
	".avi": KindVideo, ".m4v": KindVideo, ".mpg": KindVideo, ".mpeg": KindVideo,
	".wmv": KindVideo, ".flv": KindVideo, ".3gp": KindVideo, ".vob": KindVideo,
	// video: transport streams, which is what a capture and a camcorder write
	".ts": KindVideo, ".mts": KindVideo, ".m2ts": KindVideo,
	// video: the older wrappers that still turn up
	".divx": KindVideo, ".f4v": KindVideo, ".ogv": KindVideo,
	".rm": KindVideo, ".rmvb": KindVideo,
	// image
	".jpg": KindImage, ".jpeg": KindImage, ".png": KindImage, ".gif": KindImage,
	".webp": KindImage, ".bmp": KindImage, ".svg": KindImage, ".avif": KindImage,
	// jfif is a JPEG under an older name, and decodes as one
	".jfif": KindImage,
	// audio
	".mp3": KindAudio, ".m4a": KindAudio, ".m4b": KindAudio, ".flac": KindAudio, ".ogg": KindAudio,
	".oga": KindAudio, ".wav": KindAudio, ".aac": KindAudio, ".opus": KindAudio,
	".wma": KindAudio,
	// playlists
	".m3u": KindPlaylist, ".m3u8": KindPlaylist,
}

// Classify returns the media kind for a path, or "" if it is not media.
func Classify(path string) Kind {
	return extKind[strings.ToLower(filepath.Ext(path))]
}

// Scan walks all roots, reconciling the index with what is on disk: new and
// changed files are upserted, files that no longer exist are dropped. If
// addWatch is non-nil it is invoked for every directory encountered (used to
// install fsnotify watches during the walk). Safe to call repeatedly;
// concurrent calls serialize — the periodic rescan can fire while a cold
// initial walk is still running, and two walks at once each clear byInode
// and reconcile against their own idea of what they saw, mis-detecting
// duplicates and dropping each other's files.
func (l *Library) Scan(addWatch func(dir string)) {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	seen := make(map[string]struct{}, 1024)
	seenSubs := make(map[string]struct{}, 64)
	// DVD folders already folded into titles: every VOB in one names the
	// same folder, and the folder is read once.
	folded := make(map[string]struct{}, 8)
	changed := false
	pending := 0
	dupes := 0
	// Directories the walk could not read. Their entries are left alone
	// below: a directory that fails to list lists nothing, and reconciling
	// against that would drop and re-add its whole subtree — resetting
	// every FirstSeen under it, and every cache keyed by the item. Only
	// what actually failed goes in here, not the root it sits under.
	failed := make(map[string]struct{})

	// Rebuild the duplicate map from scratch: claims are only reconciled
	// with the disk at the end of the walk, so a claim left by a path that
	// has since been deleted would otherwise block the surviving link —
	// and both would be dropped.
	l.mu.Lock()
	clear(l.byInode)
	l.mu.Unlock()

	for _, root := range l.Roots() {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				l.log.Warn("scan error", "path", path, "err", err)
				// Protect only what could not be read. Marking the whole
				// root — which is what this used to do — switches off the
				// reconciliation below for everything beneath it, so one
				// unreadable file anywhere in a library means nothing
				// deleted ever leaves the index and no duplicate is ever
				// dropped. A directory protects its own subtree; a file
				// protects the directory it is in, since that is the
				// smallest thing whose listing is now in doubt.
				switch {
				case path == root || (d != nil && d.IsDir()):
					failed[path] = struct{}{}
				default:
					failed[filepath.Dir(path)] = struct{}{}
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != root {
					return filepath.SkipDir // skip hidden directories
				}
				if isRedundantSampleDir(path) {
					return filepath.SkipDir
				}
				if l.excluded(path) {
					return filepath.SkipDir
				}
				if addWatch != nil {
					addWatch(path)
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			if l.excluded(path) {
				return nil
			}
			if IsSubtitle(path) {
				// Attachments to videos, not library items of their own.
				seenSubs[path] = struct{}{}
				l.mu.Lock()
				l.addSub(path)
				l.mu.Unlock()
				return nil
			}
			if isRarRelated(path) {
				if isRarFirstVolume(path) {
					paths, ch := l.indexRarSet(path)
					for _, p := range paths {
						seen[p] = struct{}{}
					}
					if ch {
						changed = true
						pending++
					}
				}
				return nil
			}
			// A disc image is a container like a rar set, and a DVD's own
			// VOBs are the titles they belong to rather than a tile apiece.
			if dir := isDVDStructure(path); dir || isDiscImage(path) {
				container := path
				if dir {
					container = filepath.Dir(path)
					if _, done := folded[container]; done {
						return nil
					}
					folded[container] = struct{}{}
				}
				paths, ch := l.indexDisc(container, dir)
				for _, p := range paths {
					seen[p] = struct{}{}
				}
				if ch {
					changed = true
					pending++
				}
				return nil
			}
			kind := Classify(path)
			info, symlink, err := statEntry(path, d)
			if err != nil {
				return nil
			}
			if kind == "" {
				// A name that said nothing may still be media (sniff.go).
				// Guarded by the size floor there, which is what keeps this
				// to a few dozen reads across a whole library.
				if kind = ClassifyContent(path, info.Size()); kind == "" {
					return nil
				}
			}
			if isRedundantSampleFile(path, kind, info.Size()) {
				return nil
			}
			key, _ := fileID(info)
			ch, dup := l.upsert(path, kind, info.Size(), info.ModTime(), key, symlink)
			if dup {
				// Another path already represents this file; leaving it out
				// of seen also drops it if it used to be the indexed one.
				dupes++
				return nil
			}
			seen[path] = struct{}{}
			if ch {
				changed = true
				pending++
				// Publish progress during large initial scans so the UI
				// fills in as files are discovered.
				if pending >= 500 {
					pending = 0
					l.notify()
				}
			}
			return nil
		})
		if err != nil {
			l.log.Warn("scan failed", "root", root, "err", err)
			failed[root] = struct{}{}
		}
	}

	// Drop subtitle files that disappeared while we were not looking.
	l.mu.Lock()
	for dir, paths := range l.subsByDir {
		kept := paths[:0]
		for _, p := range paths {
			if _, ok := seenSubs[p]; ok || underAny(failed, p) {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			delete(l.subsByDir, dir)
		} else {
			l.subsByDir[dir] = kept
		}
	}
	l.mu.Unlock()

	// Drop index entries whose files disappeared while we were not looking.
	// The candidates are gathered under the read lock and the disk is asked
	// about them under no lock at all: a mass disappearance on a slow or
	// unmounted disk used to hold the write lock for as long as the stats
	// took, and every listing with it.
	l.mu.RLock()
	var gone []string
	for p := range l.byPath {
		if _, ok := seen[p]; !ok && !underAny(failed, p) {
			gone = append(gone, p)
		}
	}
	l.mu.RUnlock()
	kept := make(map[string]fs.FileInfo, len(gone))
	for _, p := range gone {
		// Still on disk, but the walk passed it over deliberately — a
		// sample beside its release, or a second path to a file already
		// indexed — so it does not belong in the index any more.
		if info, err := os.Stat(p); err == nil && l.stillIndexable(p, info) {
			kept[p] = info
		}
	}
	l.mu.Lock()
	for _, p := range gone {
		it, ok := l.byPath[p]
		if !ok {
			continue // gone already, by the watcher's hand
		}
		if info, ok := kept[p]; ok && !l.heldElsewhere(p, info) {
			continue // still exists (e.g. race with watcher), keep it
		}
		l.dropItem(it)
		changed = true
	}
	l.mu.Unlock()

	if dupes > 0 {
		l.log.Info("skipped duplicate files", "paths", dupes)
	}
	if changed || pending > 0 {
		l.notify()
	}
}

// underAny reports whether path is one of the given directories or lies
// inside one. Archived items key off their volume's path, so the prefix
// test covers them too.
func underAny(dirs map[string]struct{}, path string) bool {
	for dir := range dirs {
		if pathUnder(path, dir) {
			return true
		}
	}
	return false
}

// excluded reports whether path is covered by one of the -exclude globs.
// A pattern containing no separator is matched against the base name, so
// "*.iso" behaves the way a shell glob would; one that contains a separator
// is matched against the whole slash-normalised path. Directories above the
// path are tested as well: the walk skips an excluded directory wholesale,
// so nothing beneath it may slip back in through another entry point.
func (l *Library) excluded(path string) bool {
	if len(l.excludes) == 0 {
		return false
	}
	// One lock, no copy: this is asked about every file of every walk, and
	// the parent loop below asks about every directory above each of them.
	roots := l.rootsNow()
	under := func(p string) bool {
		for _, root := range roots {
			if pathUnder(p, root) {
				return true
			}
		}
		return false
	}
	for p := path; under(p) && !slices.Contains(roots, p); p = filepath.Dir(p) {
		slash := filepath.ToSlash(p)
		for _, pat := range l.excludes {
			subject := slash
			if !strings.Contains(pat, "/") {
				subject = filepath.Base(p)
			}
			// A malformed pattern matches nothing; it is reported once, at
			// startup, rather than on every path considered.
			if ok, err := gopath.Match(pat, subject); ok && err == nil {
				return true
			}
		}
	}
	return false
}

// ValidateExcludes reports the first exclude pattern that is not a valid
// glob, so a typo fails the command line instead of silently matching
// nothing for the lifetime of the process.
func ValidateExcludes(patterns []string) error {
	for _, pat := range patterns {
		if _, err := gopath.Match(pat, ""); err != nil {
			return fmt.Errorf("exclude %q: %w", pat, err)
		}
	}
	return nil
}

// stillIndexable reports whether a path the walk did not report should
// nevertheless keep its place — the usual reason being a race with the
// watcher rather than a deliberate skip. Caller must hold l.mu.
func (l *Library) stillIndexable(path string, info fs.FileInfo) bool {
	// A directory that has been taken out of the preferences leaves its files
	// exactly where they were, so "it is still on disk" cannot be the whole
	// test: without this, removing a directory would leave everything under
	// it in the index for the life of the process, and the reconciliation
	// below — which exists to drop what the walk no longer covers — would
	// keep putting it back.
	if !l.UnderRoots(path) {
		return false
	}
	if isDVDStructure(path) {
		return false // one of a DVD's own VOBs, indexed as the title it is in
	}
	if isRedundantSampleDir(filepath.Dir(path)) {
		return false
	}
	if isRedundantSampleFile(path, Classify(path), info.Size()) {
		return false
	}
	if l.excluded(path) {
		return false // matches an exclude now, so it is not ours any more
	}
	return true
}

// heldElsewhere says whether another path now represents this file — a
// hard link or a symlink that claimed the inode. The one question about a
// vanished path that reads the index rather than the disk, so it is asked
// under the lock while the rest (stillIndexable) is not. Caller holds l.mu.
func (l *Library) heldElsewhere(path string, info fs.FileInfo) bool {
	if key, ok := fileID(info); ok {
		if held, exists := l.byInode[key]; exists && held != path {
			return true
		}
	}
	return false
}

// isRedundantSampleDir reports whether a directory is a release's sample
// folder — a short excerpt of something that sits, whole, in the parent
// directory. Those are noise in the library, so they are skipped; a sample
// folder whose parent holds no playable release is kept, since then the
// excerpt is all there is.
func isRedundantSampleDir(path string) bool {
	if !sampleDirName(filepath.Base(path)) {
		return false
	}
	// Read the parent rather than relying on walk order: directories and
	// files are visited in name order, so the release itself may not have
	// been seen yet when the sample folder comes up.
	return releaseNear(filepath.Dir(path), sampleReleaseDepth)
}

func sampleDirName(name string) bool {
	switch strings.ToLower(name) {
	case "sample", "samples":
		return true
	}
	return false
}

// sampleReleaseDepth is how far below the sample's own parent to look for the
// release it is an excerpt of.
//
// One level is the common case and the one that was missing: a film split
// over discs keeps nothing in the release directory itself — CD1, CD2, an
// nfo, and the Sample folder — so a rule that only read the parent's files
// found no release, concluded the excerpt was all there was, and kept it.
// Two levels covers a disc that is itself a folder of parts, which a DVD rip
// often is. Deeper than that stops being "the release this sample belongs
// to" and starts being "some video somewhere below".
const sampleReleaseDepth = 2

// releaseNear reports whether the release a sample belongs to is present:
// playable media, or an archive set, in this directory or within depth
// levels below it.
//
// Bounded on purpose. This is asked once per sample directory during a scan,
// the answer is nearly always in the first place it looks, and an unbounded
// search under a release directory that happened to sit near the top of a
// library would walk the library.
func releaseNear(dir string, depth int) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	subdirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			// Not into another sample folder: one excerpt does not make
			// another one redundant.
			if !sampleDirName(e.Name()) {
				subdirs = append(subdirs, filepath.Join(dir, e.Name()))
			}
			continue
		}
		if k := Classify(e.Name()); k == KindVideo || k == KindAudio {
			return true
		}
		if isRarFirstVolume(e.Name()) {
			return true // the release is inside an archive set
		}
	}
	if depth <= 0 {
		return false
	}
	for _, sub := range subdirs {
		if releaseNear(sub, depth-1) {
			return true
		}
	}
	return false
}

// sampleRatio is how much bigger the release has to be before a file named
// like a sample is taken for one. A scene sample is a minute or two of a
// feature — a percent or two of its size — so this is generous by an order
// of magnitude, and exists so that nothing is dropped on the strength of its
// name alone.
const sampleRatio = 5

// isRedundantSampleFile reports whether a file is a release's sample lying
// beside the release itself, rather than in a folder of its own.
//
// The same excerpt as isRedundantSampleDir covers, differently arranged:
// scene releases put it either way, and this is the way that leaves a
// hundred-megabyte "Sample.mkv" in the listing next to the film it is an
// excerpt of. Videos only, because "sample" means something else entirely in
// a folder of music, and never on the name alone — the directory has to hold
// a release that dwarfs it, or an archive set with the release inside.
func isRedundantSampleFile(path string, kind Kind, size int64) bool {
	if kind != KindVideo || !namedLikeASample(path) {
		return false
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	self := filepath.Base(path)
	for _, e := range entries {
		if e.IsDir() || e.Name() == self {
			continue
		}
		if isRarFirstVolume(e.Name()) {
			return true // the release is inside an archive set
		}
		if Classify(e.Name()) != KindVideo {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() >= size*sampleRatio {
			return true
		}
	}
	return false
}

// namedLikeASample reports whether a file name is one of the ways a release
// says "this is only a taste of it". Deliberately narrow: "sample" has to be
// the whole name or a piece of it set off by punctuation, so that a film
// actually called something like "Sample Text" is left alone.
func namedLikeASample(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	name = strings.TrimSuffix(name, filepath.Ext(name))
	// A whole word, wherever it falls: scene samples are named
	// "grp-sample.film68" as readily as "film-sample", and a rule that
	// only looked at the two ends missed the middle.
	//
	// Split on punctuation only, never on a space. That is what keeps a film
	// called "Sample Text" out of this: its name is one word by this
	// reckoning, and a title that merely begins with the word is a title.
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		if part == "sample" {
			return true
		}
	}
	return name == "sample"
}

// statEntry describes the file a directory entry leads to. Symlinks are
// resolved, so a link is indexed with its target's size and mtime rather
// than the handful of bytes the link itself occupies.
func statEntry(path string, d fs.DirEntry) (info fs.FileInfo, symlink bool, err error) {
	info, err = d.Info()
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return info, false, nil
	}
	info, err = os.Stat(path) // follow to the target
	if err != nil {
		return nil, true, err // dangling link
	}
	return info, true, nil
}

// AddFile stats path and indexes it if it is a media file. Used by the watcher.
func (l *Library) AddFile(path string) {
	if strings.HasPrefix(filepath.Base(path), ".") {
		return
	}
	// A settle timer armed before the directories changed can report a
	// file that is no longer ours; indexed, it would sit there until the
	// next scan's reconciliation dropped it.
	if !l.UnderRoots(path) {
		return
	}
	if l.excluded(path) {
		return
	}
	if IsSubtitle(path) {
		l.mu.Lock()
		l.addSub(path)
		l.mu.Unlock()
		l.notify() // an open player refetches its subtitle list
		return
	}
	if isRarRelated(path) {
		l.reindexRarSet(rarFirstVolumeOf(path))
		return
	}
	if isDiscImage(path) {
		l.reindexDisc(path, false)
		return
	}
	if isDVDStructure(path) {
		l.reindexDisc(filepath.Dir(path), true)
		return
	}
	if isRedundantSampleDir(filepath.Dir(path)) {
		return
	}
	kind := Classify(path)
	// os.Stat follows symlinks, so a linked file is measured by its target.
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	if kind == "" {
		// As in the walk: a nameless file gets its opening read (sniff.go).
		if kind = ClassifyContent(path, info.Size()); kind == "" {
			return
		}
	}
	if isRedundantSampleFile(path, kind, info.Size()) {
		return
	}
	symlink := false
	if li, err := os.Lstat(path); err == nil {
		symlink = li.Mode()&fs.ModeSymlink != 0
	}
	key, _ := fileID(info)
	changed, dup := l.upsert(path, kind, info.Size(), info.ModTime(), key, symlink)
	if dup {
		return // already in the library under another path
	}
	if changed {
		if kind == KindAudio || kind == KindVideo {
			// Once the writer goes quiet, not per event: a file still being
			// written changes on every flush, and a tag read per Write event
			// is an unbounded number of readers on a file that is about to
			// change again. The debounced read still notifies when it lands,
			// so the metadata reaches connected clients (see
			// enrichAfterQuiet).
			l.enrichAfterQuiet(PathID(path))
		}
		l.notify()
	}
}

// Remove drops path (file or directory subtree) from the index.
func (l *Library) Remove(path string) {
	if IsSubtitle(path) {
		if l.removeSub(path) {
			l.notify()
		}
		return
	}
	if isRarRelated(path) {
		if isRarFirstVolume(path) {
			// First volume gone: drop every member (virtual paths hang off it).
			if l.removePath(path) > 0 {
				l.notify()
			}
			return
		}
		// A later volume changed the set: reparse; incomplete members drop out.
		l.reindexRarSet(rarFirstVolumeOf(path))
		return
	}
	if isDVDStructure(path) {
		// One of a DVD's VOBs went: the titles it was part of change or go.
		l.reindexDisc(filepath.Dir(path), true)
		return
	}
	// removeSub also sweeps a removed directory's subtitles — both are
	// asked, since a directory holding films and their subtitles used to
	// keep the subtitles listed until the next rescan: the removal of the
	// films short-circuited past the sweep.
	items := l.removePath(path) > 0
	subs := l.removeSub(path)
	if items || subs {
		l.notify()
	}
}

// reindexRarSet re-parses the volume set (if its first volume exists) and
// enriches any new members. Used for watcher events on rar volumes.
func (l *Library) reindexRarSet(first string) {
	l.reindexContainer(first, func() ([]string, bool) { return l.indexRarSet(first) })
}

// reindexDisc re-parses a DVD and enriches any new titles. Used for watcher
// events on a disc image or on one of a DVD folder's VOBs.
func (l *Library) reindexDisc(container string, dir bool) {
	l.reindexContainer(container, func() ([]string, bool) { return l.indexDisc(container, dir) })
}

// reindexContainer is what a watcher event on any container does: a
// container that has gone takes its members with it, and one that changed
// is parsed again and its new members enriched — published twice, once for
// the file list and once when the metadata lands, which is the watcher
// paths' contract.
func (l *Library) reindexContainer(container string, index func() ([]string, bool)) {
	if _, err := os.Stat(container); err != nil {
		if l.removePath(container) > 0 {
			l.notify()
		}
		return
	}
	paths, changed := index()
	if !changed {
		return
	}
	go func() {
		for _, p := range paths {
			l.enrichOne(context.Background(), PathID(p))
		}
		l.notify() // publish the metadata, not just the file list
	}()
	l.notify()
}

// indexRarSet parses the volume set starting at first and indexes its stored
// media members, dropping members that vanished from the set. Returns the
// members' virtual paths (for scan reconciliation) and whether anything
// changed.
func (l *Library) indexRarSet(first string) (paths []string, changed bool) {
	entries, skipped, err := parseRarSet(first)
	// What the set holds but cannot be served, and why. A compressed member
	// is the common one and it is invisible from outside: the set parses,
	// yields nothing, and looks exactly like a release nobody walked. This
	// line is the only place that question is answered — one line per set,
	// once per process, naming no members: how many cannot be served and
	// the reasons, grouped. It used to be a line per member per rescan,
	// which for a set of three hundred compressed pictures was three hundred
	// lines every ten minutes for the life of the process.
	if len(skipped) > 0 {
		why, kinds := skipReasons(skipped)
		if l.once("rar skip\x00" + first + "\x00" + kinds) {
			l.log.Debug("rar set holds members it cannot serve",
				"path", first, "skipped", len(skipped), "served", len(entries), "why", why)
		}
	}
	if err != nil {
		if l.once("rar parse\x00" + first + "\x00" + err.Error()) {
			l.log.Debug("rar parse failed", "path", first, "err", err)
		}
		return nil, false
	}
	var mt time.Time
	for _, vol := range rarVolumes(first) {
		if info, err := os.Stat(vol); err == nil && info.ModTime().After(mt) {
			mt = info.ModTime()
		}
	}
	return l.indexStored(first, discsInside(first, entries), mt)
}

// skipReasons folds a set's unservable members into one reason line —
// each kind of reason once, with how many members it covers and the first
// member's own wording, which for a compressed set names the method — and
// the kinds alone, in a fixed order, as the key that says whether there is
// anything new to report: an incomplete member is not news again for every
// byte that arrives, while a set that turns out compressed is.
func skipReasons(skipped []rarSkip) (why, kinds string) {
	counts := map[string]int{}
	firsts := map[string]string{}
	var order []string
	for _, sk := range skipped {
		kind, _, _ := strings.Cut(sk.why, " ")
		kind = strings.TrimSuffix(kind, ":")
		if counts[kind] == 0 {
			order = append(order, kind)
			firsts[kind] = sk.why
		}
		counts[kind]++
	}
	slices.Sort(order)
	parts := make([]string, 0, len(order))
	for _, kind := range order {
		if n := counts[kind]; n > 1 {
			parts = append(parts, fmt.Sprintf("%s (x%d)", firsts[kind], n))
		} else {
			parts = append(parts, firsts[kind])
		}
	}
	return strings.Join(parts, "; "), strings.Join(order, ",")
}

// discsInside replaces any disc image in a container's contents with the
// titles on it. A DVD release ships as one image split across seventy rar
// volumes, and the image is not what anybody wants to open — the film is.
//
// The release is named after the directory the set is in rather than after
// the member, which is a name the packer chose and usually says nothing
// ("GIS_7.img"); where a set holds more than one image, the member's own
// stem comes back, since one name for two discs would be one item.
func discsInside(container string, entries []*storedEntry) []*storedEntry {
	images := 0
	for _, e := range entries {
		if isDiscImage(e.name) {
			images++
		}
	}
	if images == 0 {
		return entries
	}
	out := make([]*storedEntry, 0, len(entries))
	for _, e := range entries {
		if !isDiscImage(e.name) {
			out = append(out, e)
			continue
		}
		release := filepath.Base(filepath.Dir(container))
		if images > 1 {
			release = strings.TrimSuffix(e.name, filepath.Ext(e.name))
		}
		titles, err := discEntriesIn(e, release)
		if err != nil {
			continue // not a DVD, or not one we can read: the image is not an item
		}
		out = append(out, titles...)
	}
	return out
}

// indexDisc parses a DVD — a disc image, or an unpacked VIDEO_TS folder —
// and indexes the titles on it. Like a rar set's members, the titles are byte
// ranges derived from the container rather than anything stored, so every
// scan re-derives them.
func (l *Library) indexDisc(container string, dir bool) (paths []string, changed bool) {
	entries, err := discEntries(container, dir)
	if err != nil {
		l.log.Debug("disc parse failed", "path", container, "err", err)
		return nil, false
	}
	// A title is as old as the newest byte in it. An image is one file and an
	// unpacked folder is several, and a part rewritten in place would not
	// touch the folder's own mtime.
	var mt time.Time
	stated := make(map[string]struct{}, 8)
	for _, e := range entries {
		for _, seg := range e.segs {
			if _, done := stated[seg.path]; done {
				continue
			}
			stated[seg.path] = struct{}{}
			if info, err := os.Stat(seg.path); err == nil && info.ModTime().After(mt) {
				mt = info.ModTime()
			}
		}
	}
	return l.indexStored(container, entries, mt)
}

// indexStored indexes the media inside one container — a rar volume set, a
// disc image, a DVD folder — and drops what is no longer in it (a deleted or
// truncated volume, a title that has gone). Every item's virtual path is the
// container's path, a NUL and the member's name, which is what makes a
// container's items a subtree of it for reconciliation and removal.
func (l *Library) indexStored(container string, entries []*storedEntry, mt time.Time) (paths []string, changed bool) {
	valid := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if Classify(e.name) == "" {
			continue
		}
		p := container + "\x00" + e.name
		valid[p] = struct{}{}
		paths = append(paths, p)
		if l.upsertStored(container, e, mt) {
			changed = true
		}
	}

	// What the container held last time, so a set of three hundred members
	// is reconciled against its own three hundred and not against the whole
	// index — once per set, per scan, that walk was most of a rescan.
	l.mu.Lock()
	for p := range l.members[container] {
		if _, ok := valid[p]; ok {
			continue
		}
		if it, ok := l.byPath[p]; ok {
			l.dropItem(it)
			changed = true
		}
	}
	if l.members == nil {
		l.members = map[string]map[string]struct{}{}
	}
	l.members[container] = valid
	l.mu.Unlock()
	return paths, changed
}

// Size returns the number of indexed items.
func (l *Library) Size() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.items)
}
