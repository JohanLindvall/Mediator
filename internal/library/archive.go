// SPDX-License-Identifier: MIT

package library

// Media inside zip archives, and how much of it one archive may bring in.
//
// A zip is a container like a rar set, and it goes through the same doors: a
// member is an item whose path is the archive's with the member's name after
// a NUL, indexed by indexStored, reconciled against what the archive held
// last time, never persisted (its offsets are re-derived by every walk), and
// read through OpenItem. A member stored as it is (method 0) is a run of
// bytes in the file — or across the files of a split set (zipset.go) — which
// is exactly a storedEntry; a deflated one is a packed entry, unpacked as it
// is read (pack.go) or into the scratch space for playback (unpack.go).
// Encrypted members and the methods that are not deflate are reported once
// and left out, as a rar set's are.
//
// Measured across the library's roots when this was written: 126 zip
// archives, none encrypted, 108 of them holding media — 5,605 deflated
// pictures, 348 deflated films and 20 deflated songs, two thousand members
// stored as they are — and one archive of 220,930 stored map tiles, a
// gigapixel panorama cut into pieces of ten kilobytes each. That one is why
// there is a cap. Besides those, one spanned set short of two of its four
// parts and three byte-split sets: two damaged part way (see
// ErrDamagedMember), and a first piece whose others are not here.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultArchiveMax is how many media members one archive may hold and still
// be indexed, unless -archive-max says otherwise.
//
// An archive is a collection somebody made or a dataset somebody shipped, and
// the second kind is the one that floods a library: the panorama above would
// have put more pictures in the grid than the rest of the library held, every
// one of them a tile nobody would look at. The owner asked for a thousand, for
// rar sets as much as for zips. What that costs, measured on the same roots:
// thirteen rar sets of pictures hold more than a thousand media members each
// — nine of them past it on their stored pictures alone, which were in the
// library before compressed members could be read, and four only once their
// compressed members count — about 62,000 pictures that were in the library
// and are not now; and one photo export zipped whole, of 2,337. The flag is
// how to have them back.
const DefaultArchiveMax = 1000

// archiveMax is the cap in force; zero or less is no cap. See SetArchiveMax.
var archiveMax atomic.Int64

func init() { archiveMax.Store(DefaultArchiveMax) }

// SetArchiveMax sets how many media members one archive may hold and still be
// indexed (-archive-max); zero or less removes the cap. An archive over it is
// left out whole — never its first thousand members, which for a tile set
// would be a thousand tiles — and the walk that finds it says so, once.
func SetArchiveMax(n int) { archiveMax.Store(int64(max(n, 0))) }

// tooManyMembers is an archive left out for holding more media than the cap.
type tooManyMembers struct{ n, max int64 }

func (e tooManyMembers) Error() string {
	return fmt.Sprintf("holds %d media members, more than the %d one archive may bring in (-archive-max)", e.n, e.max)
}

// zipEntriesPerMember bounds what is read of an archive's directory before
// the cap is applied: past this many entries of any kind per member allowed,
// the archive is over the cap however few of them are media, and reading a
// directory of a quarter of a million names every ten minutes to find that
// out again is the cost the bound exists to avoid.
const zipEntriesPerMember = 10

// mediaMembers counts what of a container's contents would be items.
func mediaMembers(entries []*storedEntry) int64 {
	var n int64
	for _, e := range entries {
		if Classify(e.name) != "" || isDiscImage(e.name) {
			n++
		}
	}
	return n
}

// isZip says whether a file is a zip archive by its name, which is how the
// library decides everything it holds.
func isZip(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".zip")
}

// parseZip reads a zip set's directory and returns the members that can be
// served — stored as they are, or deflated — with the ones that cannot and the
// reason, and the files the set is made of. Members that are not media are
// passed over without a word.
func parseZip(container string) ([]*storedEntry, []rarSkip, []string, error) {
	z, end, err := openZipSet(container)
	if err != nil {
		return nil, nil, []string{container}, err
	}
	defer z.close()
	var parts []string
	for _, p := range z.parts {
		if p != "" {
			parts = append(parts, p)
		}
	}
	limit := archiveMax.Load()
	if limit > 0 && end.entries > 0 && (end.entries-1)/zipEntriesPerMember >= limit {
		return nil, nil, parts, tooManyMembers{n: end.entries, max: limit}
	}
	dir, err := z.directory(end)
	if err != nil {
		return nil, nil, parts, err
	}
	var out []*storedEntry
	var skips []rarSkip
	seen := map[string]bool{}
	for _, ze := range dir {
		name := strings.TrimLeft(strings.ReplaceAll(ze.raw, "\\", "/"), "/")
		if name == "" || ze.dir {
			continue
		}
		if Classify(name) == "" && !isDiscImage(name) {
			continue
		}
		if seen[name] {
			skips = append(skips, rarSkip{name, "duplicate: a second member of this name in the archive"})
			continue
		}
		seen[name] = true
		switch {
		case ze.flags&0x1 != 0:
			skips = append(skips, rarSkip{name, "encrypted"})
			continue
		case ze.method != zipStore && ze.method != zipDeflate:
			skips = append(skips, rarSkip{name, fmt.Sprintf("compressed (method %d), which this server cannot unpack", ze.method)})
			continue
		case ze.size <= 0 || ze.packed < 0:
			skips = append(skips, rarSkip{name, "empty"})
			continue
		case ze.method == zipStore && ze.packed != ze.size:
			skips = append(skips, rarSkip{name, fmt.Sprintf("stored, yet %d bytes for a member of %d", ze.packed, ze.size)})
			continue
		}
		d, off, err := z.dataStart(ze)
		var segs []storedSeg
		if err == nil {
			segs, err = z.span(d, off, ze.packed)
		}
		if err != nil {
			// A part that is not here, or bytes past the end, are a set still
			// arriving or never finished; anything else is the archive
			// saying something that is not so.
			why := "unreadable: "
			var missing zipPartMissing
			if errors.As(err, &missing) || errors.Is(err, errPastTheEnd) || errors.Is(err, io.EOF) {
				why = "incomplete: "
			}
			skips = append(skips, rarSkip{name, why + err.Error()})
			continue
		}
		e := &storedEntry{name: name, size: ze.size, segs: segs}
		if ze.method == zipDeflate {
			e.pack = &packing{format: packZip, crc: ze.crc}
		}
		out = append(out, e)
	}
	if limit > 0 {
		if n := mediaMembers(out); n > limit {
			return nil, skips, parts, tooManyMembers{n: n, max: limit}
		}
	}
	slices.SortFunc(out, func(a, b *storedEntry) int { return strings.Compare(a.name, b.name) })
	return out, skips, parts, nil
}

// The two methods a zip member can be served in: kept as it is, or deflated.
const (
	zipStore   = 0
	zipDeflate = 8
)

// indexZip parses an archive and indexes its media members, dropping members
// that are no longer in it. Like indexRarSet, it returns the members' virtual
// paths for the walk's reconciliation and whether anything changed.
func (l *Library) indexZip(path string) (paths []string, changed bool) {
	defer enterContainer(path)()
	entries, skipped, parts, err := parseZip(path)
	l.reportSkips("zip archive", path, len(entries), skipped)
	// The set's time is its newest part's, as a rar set's is its newest
	// volume's: a part still arriving moves it.
	var mt time.Time
	for _, p := range parts {
		if info, statErr := os.Stat(p); statErr == nil && info.ModTime().After(mt) {
			mt = info.ModTime()
		}
	}
	if err != nil {
		if l.archiveOverCap(path, err) {
			return l.indexStored(path, nil, mt)
		}
		if l.once("zip parse\x00" + path + "\x00" + err.Error()) {
			l.log.Debug("zip parse failed", "path", path, "err", err)
		}
		// Read, and not a whole archive — a piece of a set gone, an end
		// record never written — is a verdict, and what it held goes. A read
		// that failed is not one: the disk may come back.
		var shape zipShape
		if errors.As(err, &shape) {
			return l.indexStored(path, nil, mt)
		}
		return nil, false
	}
	return l.indexStored(path, discsInside(path, entries), mt)
}

// reindexZip re-parses a set the watcher saw a part of change, by its
// container.
func (l *Library) reindexZip(path string) {
	l.reindexContainer(path, func() ([]string, bool) { return l.indexZip(path) })
}

// archiveOverCap says whether a parse refused an archive for holding more
// media than the cap allows, and says so in the log the first time — at Info,
// since an archive left out whole is a thing an owner may go looking for. A
// verdict, unlike a parse that failed: what the archive held before is dropped.
func (l *Library) archiveOverCap(path string, err error) bool {
	var over tooManyMembers
	if !errors.As(err, &over) {
		return false
	}
	if l.once("archive cap\x00" + path + "\x00" + strconv.FormatInt(over.max, 10)) {
		l.log.Info("archive left out: it holds more media than -archive-max allows",
			"path", path, "members", over.n, "max", over.max)
	}
	return true
}

// reportSkips says, once per container and kind of reason, what a container
// holds and cannot serve — one line naming no members. See indexRarSet.
func (l *Library) reportSkips(what, container string, served int, skipped []rarSkip) {
	if len(skipped) == 0 {
		return
	}
	why, kinds := skipReasons(skipped)
	if l.once(what + " skip\x00" + container + "\x00" + kinds) {
		l.log.Debug(what+" holds members it cannot serve",
			"path", container, "skipped", len(skipped), "served", served, "why", why)
	}
}
