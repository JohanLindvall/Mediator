// SPDX-License-Identifier: MIT

package library

import (
	"cmp"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Folder groups matching files by their immediate folder's displayed name,
// across roots and parents. It is a view, not a directory on disk.
type Folder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Items     int    `json:"items"`
	Size      int64  `json:"size"`
	ModTime   int64  `json:"mtime"`
	FirstSeen int64  `json:"added,omitempty"`
	Duration  int64  `json:"duration,omitempty"`
	CoverID   string `json:"coverId"`
	CoverTime int64  `json:"coverTime"`
	CoverSVG  bool   `json:"coverSvg,omitempty"`

	nameKey string
	lower   string
	partial int // best member's search match, when the folder name misses
}

// leafFolder uses the real containing directory, including a root's own
// name. An archived member's inner directory wins where it has one; a
// top-level member uses the container's parent, not the archive filename.
// Display decoding happens before comparison so names merge as shown.
func leafFolder(it *Item) string {
	p := filepath.ToSlash(it.Path)
	if container, member, ok := strings.Cut(p, "\x00"); ok {
		if dir := path.Dir(member); dir != "." {
			return displayText(path.Base(dir))
		}
		p = container
	}
	return displayText(path.Base(path.Dir(p)))
}

// Folder results depend on the selection as well as the group version:
// search, faces, paths and owner filters are applied before aggregation.
// Keeping this cache separate from the paged listing avoids confusing the
// group version with the listing's faster-moving byte version.
type folderCache struct {
	mu       sync.Mutex
	query    Query
	version  int64
	watchVer int64
	items    []*Folder
}

// SearchFolders returns immutable cards for the same files List would show.
// Search stays in force when a card is opened with Query.Folder, so its count
// and thumbnail describe the files the viewer will actually see there.
func (l *Library) SearchFolders(q Query) []*Folder {
	q.Offset, q.Limit, q.Seed = 0, 0, 0
	q.Folder = ""
	if q.ShowHidden != "include" && q.ShowHidden != "only" {
		q.ShowHidden = ""
	}
	l.ensureFlags()
	version, watchVer := l.GroupVersion(), l.watchVersion()
	c := &l.folders
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items != nil && c.query == q && c.version >= version && (q.Watch == "" || c.watchVer >= watchVer) {
		return c.items
	}
	// Reuse the listing's selection rules, including hidden/unreadable files,
	// watch and play filters. Only a cache miss walks the index.
	selected := l.buildQuery(q, version)
	words := searchWords(q.Search)
	hits := newHits(words)
	groups := make(map[string]*Folder)
	unknownDuration := make(map[string]bool)
	now := nowMillis()
	func() {
		l.mu.RLock()
		defer l.mu.RUnlock()
		for _, it := range selected.items {
			name := leafFolder(it)
			f := groups[name]
			if f == nil {
				f = &Folder{ID: "f" + PathID(name), Name: name, nameKey: naturalKey(name),
					lower: searchText(name), partial: len(words)}
				groups[name] = f
			}
			f.Items++
			f.Size += it.Size
			f.FirstSeen = max(f.FirstSeen, it.FirstSeen)
			if knownTime(it.ModTime, now) {
				f.ModTime = max(f.ModTime, it.ModTime)
			}
			f.Duration += it.Duration
			unknownDuration[name] = unknownDuration[name] || it.Duration <= 0
			f.partial = min(f.partial, hits.partial(it.lower))
			// A deterministic representative from the permitted, matching items.
			// Its own mtime, rather than the group's, versions the thumbnail.
			if f.CoverID == "" || it.ID < f.CoverID {
				f.CoverID, f.CoverTime = it.ID, it.ModTime
				f.CoverSVG = strings.HasSuffix(strings.ToLower(it.Name), ".svg")
			}
		}
	}()
	out := make([]*Folder, 0, len(groups))
	for name, f := range groups {
		if unknownDuration[name] {
			f.Duration = 0
		}
		out = append(out, f)
	}
	orderBy(out, q.Desc, func(f *Folder) bool { return knownKey(q.Sort, f.Duration, f.ModTime) },
		func(a, b *Folder) int {
			switch q.Sort {
			case "mtime":
				return cmp.Compare(a.ModTime, b.ModTime)
			case "added":
				return cmp.Compare(a.FirstSeen, b.FirstSeen)
			case "size":
				return cmp.Compare(a.Size, b.Size)
			case "duration":
				return cmp.Compare(a.Duration, b.Duration)
			case "items":
				return cmp.Compare(a.Items, b.Items)
			}
			return 0
		}, func(f *Folder) string { return f.nameKey }, func(f *Folder) string { return f.ID })
	rankByHit(out, words,
		func(f *Folder) (string, int32, int32) { return f.lower, int32(len(f.lower)), int32(len(f.lower)) },
		func(f *Folder) (int, bool) { return f.partial, !matchWords(f.lower, words) })
	c.query, c.version, c.watchVer, c.items = q, selected.version, selected.watchVer, out
	return out
}
