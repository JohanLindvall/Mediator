package library

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Deleting something from the disk, for good.
//
// It is done in two steps, and the split is the safety of it. The first works
// out exactly what would go — every file, and every folder that would go
// whole — and changes nothing; the owner is shown that, and confirms it. The
// second removes exactly what was shown and nothing else, and looks at each
// file again as it does: a file that changed since it was shown is left
// alone, and a folder that gained something is kept.
//
// What goes is what the thing is made of:
//
//   - A file is itself and the subtitle files beside it that are its own.
//   - Content inside another file — a film in a rar set, a title on a DVD —
//     cannot be taken out of it, so the container goes: every volume of the
//     set, the disc image, or the disc's folder. Whatever else that
//     container held goes with it, and the plan names it.
//   - A release, a show or a season is its tracks or its episodes, each as
//     above; a playlist release is its tracks and the playlist, unless its
//     tracks live elsewhere — a mixtape naming other releases' songs — when
//     it is the playlist alone.
//
// And the folder it lived in goes too, but only when what would be left in it
// is a release's furniture: an .nfo, checksums, subtitles, cover art, a
// sample. A folder holding anything else — another film, a track of another
// release, a photograph, a document — keeps it all and loses only what was
// asked for. A root of the library is never removed, nor anything outside the
// roots, nor a folder with a root inside it, nor one too large to have been
// looked through.

// What a deletion is of.
const (
	DeleteItem   = "item"
	DeleteAlbum  = "album"
	DeleteSeries = "series"
	DeleteSeason = "season"
)

// DeleteRequest names what is to go: an item or a release by its id, a show
// by its name, and a season by its show's name and its number.
type DeleteRequest struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Season int    `json:"season,omitempty"`
}

// DeletePlan is exactly what a deletion removes, worked out and not yet done.
type DeletePlan struct {
	// Title is what the owner asked to delete, in words.
	Title string
	// Files are removed first, each only while it is still the file planned.
	Files []PlannedFile
	// Folders are removed whole afterwards, deepest first, each only while
	// what is left in it is still nothing but furniture.
	Folders []string
	// Items are the indexed things that go.
	Items []string
	// Others are the names of those not asked for that go with a container
	// they share with something that was.
	Others []string
	// Bytes is what the files and folders hold together.
	Bytes int64
}

// PlannedFile is one file a plan removes, with the identity it had when the
// plan was made. InFolder says it goes with a folder that goes whole.
type PlannedFile struct {
	Path     string
	Size     int64
	MTime    int64
	InFolder bool
}

// DeleteOutcome is what a deletion did.
type DeleteOutcome struct {
	Files   int
	Folders int
	Bytes   int64
	// Kept are the planned paths left in place, and why.
	Kept []KeptPath
}

// KeptPath is a planned file or folder a deletion left where it was.
type KeptPath struct {
	Path string
	Why  string
}

// ErrNothingToDelete is a request naming nothing the library holds.
var ErrNothingToDelete = errors.New("nothing to delete")

// folderScanLimit is how many entries a folder may hold and still be looked
// through for whether it can go whole. A folder bigger than that is not a
// release's, and is kept.
const folderScanLimit = 5000

// PlanDelete works out what deleting the thing named would remove.
func (l *Library) PlanDelete(req DeleteRequest) (DeletePlan, error) {
	members, title, place := l.deleteMembers(req)
	if len(members) == 0 {
		return DeletePlan{}, ErrNothingToDelete
	}
	p := &planner{l: l, plan: DeletePlan{Title: title}, files: map[string]bool{}, items: map[string]bool{}}
	for _, it := range members {
		p.items[it.ID] = true
	}
	for _, it := range members {
		p.addItem(it)
	}
	// The folders: every one a planned file was in and the release's own,
	// deepest first so a release's disc folders are looked at before the
	// release — and a folder that goes puts the one above it in question,
	// since a disc's folder leaves its release folder holding an .nfo and an
	// empty AUDIO_TS, and a performer's last release leaves a folder with
	// their picture in it. The climb ends at the first folder holding
	// anything else, and never reaches a root.
	candidates := map[string]bool{}
	for path := range p.files {
		candidates[filepath.Dir(path)] = true
	}
	if place != "" {
		candidates[place] = true
	}
	gone := map[string]bool{}
	decided := map[string]bool{}
	for {
		var next string
		for dir := range candidates {
			if !decided[dir] && (next == "" || len(dir) > len(next) || len(dir) == len(next) && dir < next) {
				next = dir
			}
		}
		if next == "" {
			break
		}
		decided[next] = true
		if insideAny(next, gone) || !l.folderGoes(next, p.files, p.items, gone) {
			continue
		}
		gone[next] = true
		// Up from a folder that is part of a release — a disc's, a season's,
		// a sample's — to the release; never up from a release to the folder
		// it is filed in, however empty it leaves it.
		if parent := filepath.Dir(next); parent != next && partOfARelease(filepath.Base(next)) {
			candidates[parent] = true
		}
	}
	folders := make([]string, 0, len(gone))
	for dir := range gone {
		if !insideAny(dir, withoutKey(gone, dir)) {
			folders = append(folders, dir)
		}
	}
	slices.SortFunc(folders, func(a, b string) int { return len(b) - len(a) })
	p.plan.Folders = folders
	// Every planned file, with the identity it has now: the ones inside a
	// folder that goes whole go with it and are counted with it, and they are
	// still listed, since whether the folder may go is asked again when it
	// is removed and the answer depends on knowing them.
	for path := range p.files {
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		in := insideAny(path, gone)
		p.plan.Files = append(p.plan.Files, PlannedFile{Path: path, Size: fi.Size(), MTime: fi.ModTime().UnixMilli(), InFolder: in})
		if !in {
			p.plan.Bytes += fi.Size()
		}
	}
	slices.SortFunc(p.plan.Files, func(a, b PlannedFile) int { return strings.Compare(a.Path, b.Path) })
	for _, dir := range folders {
		p.plan.Bytes += treeSize(dir)
	}
	for id := range p.items {
		p.plan.Items = append(p.plan.Items, id)
	}
	slices.Sort(p.plan.Items)
	slices.Sort(p.plan.Others)
	if len(p.plan.Files) == 0 && len(p.plan.Folders) == 0 {
		return DeletePlan{}, ErrNothingToDelete
	}
	return p.plan, nil
}

// seasonFolder is how a season's folder is named.
var seasonFolder = regexp.MustCompile(`(?i)^(season|series|staffel|saison|s)[ ._-]*\d{1,3}$|^specials$`)

// partOfARelease says a folder of this name is a piece of the release above
// it — a disc, a season, the disc's own structure, its sample or its
// subtitles — rather than the release, or a folder releases are filed in.
func partOfARelease(name string) bool {
	switch strings.ToLower(name) {
	case "video_ts", "audio_ts", "sample", "samples", "subs", "subtitles":
		return true
	}
	return discNumber(name) > 0 || seasonFolder.MatchString(name)
}

// planner gathers a plan's files, containers and items.
type planner struct {
	l     *Library
	plan  DeletePlan
	files map[string]bool // files to remove
	seen  map[string]bool // containers already planned
	items map[string]bool
}

func (p *planner) addItem(it Item) {
	if container, _, inside := strings.Cut(it.Path, "\x00"); inside {
		p.addContainer(container)
	} else {
		p.files[it.Path] = true
	}
	for _, s := range p.l.Subtitles(it) {
		if s.path != "" {
			p.files[s.path] = true
		}
	}
}

// addContainer plans the whole of what an item inside another file lives in,
// and whatever else it held.
func (p *planner) addContainer(container string) {
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	if p.seen[container] {
		return
	}
	p.seen[container] = true
	fi, err := os.Stat(container)
	switch {
	case err != nil:
		return
	case fi.IsDir():
		// A DVD's folder of VOBs: its files are the disc, and the folder
		// goes with them when nothing else is in it.
		entries, _ := os.ReadDir(container)
		for _, e := range entries {
			if e.Type().IsRegular() {
				p.files[filepath.Join(container, e.Name())] = true
			}
		}
	case isRarFirstVolume(container):
		for _, v := range rarVolumes(container) {
			p.files[v] = true
		}
	default:
		p.files[container] = true // a disc image
	}
	p.l.mu.RLock()
	defer p.l.mu.RUnlock()
	prefix := container + "\x00"
	for path, it := range p.l.byPath {
		if strings.HasPrefix(path, prefix) && !p.items[it.ID] {
			p.items[it.ID] = true
			p.plan.Others = append(p.plan.Others, it.Name)
		}
	}
}

// deleteMembers resolves a request to the items it names, what they are
// called together, and the folder that is the thing's own, if it has one.
func (l *Library) deleteMembers(req DeleteRequest) (members []Item, title, place string) {
	switch req.Kind {
	case DeleteItem:
		l.mu.RLock()
		it, ok := l.items[req.ID]
		var cp Item
		if ok {
			cp = *it
		}
		l.mu.RUnlock()
		if !ok {
			return nil, "", ""
		}
		return []Item{cp}, cp.Name, ""
	case DeleteAlbum:
		a, tracks, ok := l.AlbumByID(req.ID)
		if !ok || a.ID != req.ID {
			// Only a release by its own id: AlbumByID also answers to a
			// track's, and deleting the release a track is on because a
			// track's id was sent would be deleting more than was named.
			return nil, "", ""
		}
		if a.Source != "m3u" {
			return tracks, a.Name, a.where
		}
		dir := filepath.Dir(a.where)
		l.mu.RLock()
		pl, ok := l.byPath[a.where]
		var cp Item
		if ok {
			cp = *pl
		}
		l.mu.RUnlock()
		for _, t := range tracks {
			if !pathUnder(t.Path, dir) {
				// A playlist naming other releases' tracks is a list, and
				// deleting it deletes the list.
				if ok {
					return []Item{cp}, a.Name, ""
				}
				return nil, "", ""
			}
		}
		if ok {
			tracks = append(tracks, cp)
		}
		return tracks, a.Name, dir
	case DeleteSeries, DeleteSeason:
		key := SeriesKey(req.ID)
		if key == "" {
			return nil, "", ""
		}
		l.mu.RLock()
		for _, it := range l.items {
			if it.Series == "" || SeriesKey(it.Series) != key {
				continue
			}
			if req.Kind == DeleteSeason && it.Season != req.Season {
				continue
			}
			members = append(members, *it)
			title = it.Series
		}
		l.mu.RUnlock()
		slices.SortFunc(members, func(a, b Item) int { return strings.Compare(a.Path, b.Path) })
		return members, title, ""
	}
	return nil, "", ""
}

// folderGoes says whether a folder can be removed whole once the planned
// files and items are gone: it is inside the roots and is not one, holds
// none, and everything left in it is a release's furniture.
func (l *Library) folderGoes(dir string, files, items, gone map[string]bool) bool {
	if !l.UnderRoots(dir) {
		return false
	}
	for _, root := range l.rootsNow() {
		if pathUnder(root, dir) {
			return false // a root, or a folder with one inside it
		}
	}
	seen := 0
	ok := true
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		if seen++; seen > folderScanLimit {
			ok = false
			return filepath.SkipAll
		}
		if path == dir {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			ok = false // a link could point anywhere at all
			return filepath.SkipAll
		}
		if d.IsDir() {
			if gone[path] {
				return filepath.SkipDir
			}
			return nil
		}
		if files[path] {
			return nil
		}
		if !l.leftover(path, items) {
			ok = false
			return filepath.SkipAll
		}
		return nil
	})
	return err == nil && ok
}

// leftover says whether a file left in a folder is the furniture of what is
// being deleted rather than something of its own.
func (l *Library) leftover(path string, items map[string]bool) bool {
	l.mu.RLock()
	it, indexed := l.byPath[path]
	var kind Kind
	var id string
	if indexed {
		kind, id = it.Kind, it.ID
	}
	l.mu.RUnlock()
	if indexed && items[id] {
		return true
	}
	name := strings.ToLower(filepath.Base(path))
	ext := filepath.Ext(name)
	switch {
	case furnitureNames[name], furnitureExts[ext], IsSubtitle(path):
		return !indexed || kind == KindPlaylist && ext != ""
	case imageExts[ext]:
		// Cover art, not photographs: a picture called a cover, a poster or
		// a scan, or one kept in a folder for them. A folder of pictures
		// with one clip in it keeps every picture.
		return artworkName.MatchString(name) || artworkDir(filepath.Dir(path))
	case indexed:
		return false
	case videoExts[ext]:
		// A release's sample, which the scan skips when the release is
		// there, and so which nothing indexed.
		return namedLikeASample(path) || sampleDir(filepath.Dir(path))
	}
	return false
}

// furnitureExts are what a release ships beside its media and nothing else
// ever needs: descriptions, checksums, cue sheets, playlists, rip logs.
var furnitureExts = map[string]bool{
	".nfo": true, ".sfv": true, ".md5": true, ".sha1": true, ".sha256": true, ".par2": true,
	".txt": true, ".url": true, ".log": true, ".cue": true, ".accurip": true,
	".m3u": true, ".m3u8": true, ".pls": true, ".diz": true,
}

// furnitureNames are files an operating system leaves in any folder.
var furnitureNames = map[string]bool{
	".ds_store": true, "thumbs.db": true, "desktop.ini": true,
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true,
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".mov": true, ".wmv": true, ".ts": true, ".mpg": true, ".vob": true,
}

// artworkName is how a release names its pictures.
var artworkName = regexp.MustCompile(`(?i)(^|[^a-z])(cover|covers|folder|front|back|inlay|inside|booklet|cd\d*|disc\d*|poster|fanart|banner|clearart|logo|thumb|landscape|scan|proof|art|artwork|screen|screens|screenshot)([^a-z]|$)`)

// artworkDir says a folder is where a release keeps its pictures.
func artworkDir(dir string) bool {
	switch strings.ToLower(filepath.Base(dir)) {
	case "covers", "cover", "scans", "artwork", "art", "images", "screens", "screenshots", "proof", "booklet":
		return true
	}
	return false
}

// sampleDir says a folder holds a release's sample.
func sampleDir(dir string) bool {
	switch strings.ToLower(filepath.Base(dir)) {
	case "sample", "samples":
		return true
	}
	return false
}

// DeleteNow carries out a plan: the files that are still the files planned,
// then the folders that still hold nothing but furniture, and the index
// told of each, which tells the clients.
func (l *Library) DeleteNow(p DeletePlan) DeleteOutcome {
	var out DeleteOutcome
	files := map[string]bool{}
	for _, f := range p.Files {
		files[f.Path] = true
	}
	items := map[string]bool{}
	for _, id := range p.Items {
		items[id] = true
	}
	folders := map[string]bool{}
	for _, dir := range p.Folders {
		folders[dir] = true
	}
	// Every planned file looked at again first: one that changed since it
	// was shown stays, and a folder it is in stays with it.
	kept := map[string]bool{}
	for _, f := range p.Files {
		fi, err := os.Lstat(f.Path)
		if err != nil {
			continue // gone already: what was asked for is done
		}
		if !fi.Mode().IsRegular() || fi.Size() != f.Size || fi.ModTime().UnixMilli() != f.MTime {
			out.Kept = append(out.Kept, KeptPath{f.Path, "changed since it was shown"})
			for dir := range folders {
				if pathUnder(f.Path, dir) {
					kept[dir] = true
				}
			}
			delete(files, f.Path)
			continue
		}
	}
	var removed []string
	for _, f := range p.Files {
		if f.InFolder || !files[f.Path] {
			continue
		}
		if err := os.Remove(f.Path); err != nil {
			if !os.IsNotExist(err) {
				out.Kept = append(out.Kept, KeptPath{f.Path, err.Error()})
			}
			continue
		}
		out.Files++
		out.Bytes += f.Size
		removed = append(removed, f.Path)
	}
	gone := map[string]bool{}
	for _, dir := range p.Folders {
		// Looked at again: a folder that gained something since it was
		// shown keeps it, and itself.
		if kept[dir] || !l.folderGoes(dir, files, items, withoutKey(gone, dir)) {
			out.Kept = append(out.Kept, KeptPath{dir, "holds something that was not shown"})
			continue
		}
		size := treeSize(dir)
		if err := os.RemoveAll(dir); err != nil {
			out.Kept = append(out.Kept, KeptPath{dir, err.Error()})
			continue
		}
		gone[dir] = true
		out.Folders++
		out.Bytes += size
		removed = append(removed, dir)
	}
	for _, path := range removed {
		l.Remove(path)
	}
	return out
}

// treeSize is what the files under a folder hold.
func treeSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// insideAny says whether path lies inside one of the folders, not being one.
func insideAny(path string, dirs map[string]bool) bool {
	for dir := range dirs {
		if path != dir && pathUnder(path, dir) {
			return true
		}
	}
	return false
}

func withoutKey(m map[string]bool, k string) map[string]bool {
	out := make(map[string]bool, len(m))
	for key, v := range m {
		if key != k {
			out[key] = v
		}
	}
	return out
}

// DisplayPath is a place on the disk as the listing names one.
func (l *Library) DisplayPath(path string) string { return l.displayPath(path) }
