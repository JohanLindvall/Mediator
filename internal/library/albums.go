package library

import (
	"bufio"
	"cmp"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Album groups audio tracks: either every audio file in one directory, or the
// entries of an m3u playlist.
type Album struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Artist   string   `json:"artist,omitempty"`
	Source   string   `json:"source"` // "dir" | "m3u"
	TrackIDs []string `json:"-"`      // track lists ship via AlbumDetailResponse only
	Tracks   int      `json:"tracks"`
	CoverID  string   `json:"coverId,omitempty"` // item whose artwork represents the album
	Genre    string   `json:"genre,omitempty"`   // when the tracks agree: the first one named
	// Genres is every genre the tag names — a field reading "Death Metal |
	// Viking Metal" puts the release in both, which is what it says. Genre
	// is the first of these, for sorting and for the one-line caption.
	Genres   []string `json:"genres,omitempty"`
	Year     int      `json:"year,omitempty"` // when the tracks agree
	Size     int64    `json:"size"`
	Duration int64    `json:"duration,omitempty"` // total playing time, ms (0 if unknown)
	// Plays is what its tracks have been played, summed. A release is
	// listened to a track at a time, so this is the only number that says
	// anything about the release as a whole.
	Plays   int   `json:"plays,omitempty"`
	Likes   int   `json:"likes,omitempty"` // the net verdict on its tracks: likes less dislikes
	ModTime int64 `json:"mtime"`
	// Spoken says the release is somebody reading — an audiobook — by the
	// analysis of its tracks (spoken.go): most of those read say so.
	Spoken bool `json:"spoken,omitempty"`
	// Similarity is set only on the copies a "similar releases" answer hands
	// out: how much this sounds like the release asked about, 0..1.
	Similarity float32 `json:"similarity,omitempty"`

	lower    string // tokenized search text (name, artist, genre, year, path)
	sortName string // lowercased name, for ordering
}

// Albums returns all albums, cached per library version.
func (l *Library) Albums() []*Album {
	return l.albums.get(l.Version(), l.buildAlbums)
}

// AlbumByID resolves one album plus its tracks in playback order.
func (l *Library) AlbumByID(id string) (*Album, []Item, bool) {
	for _, a := range l.Albums() {
		if a.ID == id {
			tracks := make([]Item, 0, len(a.TrackIDs))
			for _, tid := range a.TrackIDs {
				if it, ok := l.Get(tid); ok {
					tracks = append(tracks, it)
				}
			}
			return a, tracks, true
		}
	}
	return nil, nil, false
}

// AlbumQuery selects and orders releases. Artist and Genre are the two
// drill-downs — one performer, or one genre — and they are a value type
// rather than four positional arguments because a third narrowing would have
// made the call unreadable and the next one after that unwritable.
type AlbumQuery struct {
	Search string
	Artist string
	Genre  string
	Sort   string
	Desc   bool
	// Audiobooks asks for the releases that are somebody reading, and
	// nothing else; unset, those are left out. The two never mix: a chapter
	// among the records is what the analysis exists to prevent.
	Audiobooks bool
	// Paths restricts the caller to part of the library (paths.go).
	Paths PathFilter
}

// AllowedAlbums keeps the releases with at least one track the caller may
// see.
//
// Any track rather than all of them, and rather than the release's own
// directory: a playlist album is a file in one place naming tracks in
// several, and a directory album whose folder sits outside an allowed path
// cannot have tracks inside one. Asking about the tracks answers for both
// kinds without a special case, and it is the tracks that are served.
func (l *Library) AllowedAlbums(all []*Album, f PathFilter) []*Album {
	if !f.Restricted() {
		return all
	}
	allowed := f.allower()
	out := make([]*Album, 0, len(all))
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, a := range all {
		for _, id := range a.TrackIDs {
			if it, ok := l.items[id]; ok && allowed(it.Path) {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// SearchAlbums filters and sorts the album list. A non-empty artist or genre
// limits the result to that one, which is how both views drill down.
func (l *Library) SearchAlbums(q AlbumQuery) []*Album {
	all := l.AllowedAlbums(l.Albums(), q.Paths)
	words := searchWords(q.Search)
	sortKey, desc := q.Sort, q.Desc
	out := make([]*Album, 0, len(all))
	for _, a := range all {
		if a.Spoken != q.Audiobooks {
			continue
		}
		if q.Artist != "" && !strings.EqualFold(a.Artist, q.Artist) {
			continue
		}
		if q.Genre != "" && !albumInGenre(a, q.Genre) {
			continue
		}
		if matchWords(a.lower, words) {
			out = append(out, a)
		}
	}
	orderBy(out, desc,
		func(a *Album) bool { return albumHasKey(a, sortKey) },
		func(a, b *Album) int { return compareAlbums(a, b, sortKey) },
		func(a *Album) string { return a.sortName },
		func(a *Album) string { return a.ID })
	return out
}

// collection is what every grouped thing carries — a release, a performer,
// a genre, a show — and so what their sort keys share. compareCommon orders
// by those, and each type's own comparator adds only its own keys, where
// four switches used to spell the shared cases out four times.
type collection interface {
	modTime() int64
	size() int64
	tracks() int
	popularity() int64
	duration() int64
	orderName() string
}

func (a *Album) modTime() int64     { return a.ModTime }
func (a *Album) size() int64        { return a.Size }
func (a *Album) tracks() int        { return a.Tracks }
func (a *Album) popularity() int64  { return popularity(a.Likes, a.Plays) }
func (a *Album) duration() int64    { return a.Duration }
func (a *Album) orderName() string  { return a.sortName }
func (a *Artist) modTime() int64    { return a.ModTime }
func (a *Artist) size() int64       { return a.Size }
func (a *Artist) tracks() int       { return a.Tracks }
func (a *Artist) popularity() int64 { return popularity(a.Likes, a.Plays) }
func (a *Artist) duration() int64   { return a.Duration }
func (a *Artist) orderName() string { return a.sortName }
func (g *Genre) modTime() int64     { return g.ModTime }
func (g *Genre) size() int64        { return g.Size }
func (g *Genre) tracks() int        { return g.Tracks }
func (g *Genre) popularity() int64  { return popularity(g.Likes, g.Plays) }
func (g *Genre) duration() int64    { return g.Duration }
func (g *Genre) orderName() string  { return g.sortName }
func (s *Series) modTime() int64    { return s.ModTime }
func (s *Series) size() int64       { return s.Size }
func (s *Series) tracks() int       { return s.Episodes }
func (s *Series) popularity() int64 { return popularity(s.Likes, s.Plays) }
func (s *Series) duration() int64   { return s.Duration }
func (s *Series) orderName() string { return s.sortName }

// compareCommon orders two collections by a key every kind has; the name
// is the order for a key nobody knows.
func compareCommon(a, b collection, sortKey string) int {
	switch sortKey {
	case "mtime":
		return cmp.Compare(a.modTime(), b.modTime())
	case "size":
		return cmp.Compare(a.size(), b.size())
	case "tracks", "episodes":
		return cmp.Compare(a.tracks(), b.tracks())
	case "plays", "popular":
		return cmp.Compare(a.popularity(), b.popularity())
	case "duration":
		return cmp.Compare(a.duration(), b.duration())
	default:
		return strings.Compare(a.orderName(), b.orderName())
	}
}

// compareAlbums orders two releases by one key: the tag keys here, the rest
// in compareCommon.
func compareAlbums(a, b *Album, sortKey string) int {
	switch sortKey {
	case "artist":
		return strings.Compare(strings.ToLower(a.Artist), strings.ToLower(b.Artist))
	case "genre":
		return strings.Compare(strings.ToLower(a.Genre), strings.ToLower(b.Genre))
	case "year":
		return cmp.Compare(a.Year, b.Year)
	default:
		return compareCommon(a, b, sortKey)
	}
}

// albumHasKey reports whether the release actually carries the value being
// sorted on. A name is always there, so the keys that do not come from tags
// answer true for everything and the rule above does nothing.
func albumHasKey(a *Album, sortKey string) bool {
	switch sortKey {
	case "artist":
		return a.Artist != ""
	case "genre":
		return a.Genre != ""
	case "year":
		return a.Year != 0
	case "duration":
		return a.Duration > 0
	default:
		return true
	}
}

func (l *Library) buildAlbums() []*Album {
	// One snapshot for the whole build: the counts are wanted per track, and
	// taking the lock 21,000 times to read a map would cost more than the
	// grouping does.
	plays := l.playsSnapshot()
	likes := l.likesSnapshot()
	spoken := l.scaledVectors()
	l.mu.RLock()
	byDir := make(map[string][]*Item)
	var playlists []*Item
	// Every performer the tags name, keyed by the lowercase form and counted
	// by spelling. It rides along with the grouping walk: a release with no
	// tags at all can then be filed under the performer its directory names,
	// where that is somebody this library already knows (artistFromParent).
	spellings := map[string]map[string]int{}
	for _, it := range l.items {
		switch it.Kind {
		case KindAudio:
			dir := filepath.Dir(it.Path)
			byDir[dir] = append(byDir[dir], it)
			if it.Artist != "" {
				key := strings.ToLower(it.Artist)
				if spellings[key] == nil {
					spellings[key] = map[string]int{}
				}
				spellings[key][it.Artist]++
			}
		case KindPlaylist:
			playlists = append(playlists, it)
		}
	}
	l.mu.RUnlock()
	// The commonest spelling wins, since it is the one the rest of the
	// library is already grouped under.
	known := make(map[string]string, len(spellings))
	for key, byName := range spellings {
		if name, n := mostCommon(byName); n > 0 {
			known[key] = name
		}
	}

	// A release spread over CD1, CD2, CD3 is one release: fold those
	// directories into the one above them so it comes out as the album it
	// is. Only where there are at least two of them — a lone folder named
	// like a disc, sitting among unrelated albums, would otherwise drag
	// everything in that directory into one heap.
	byDir = foldDiscs(byDir)

	albums := make([]*Album, 0, len(byDir)+len(playlists))
	// What every release gets once its tracks are in order, whichever kind
	// it is: the tags, the verdicts, the reading.
	finish := func(a *Album, path string, tracks []*Item) {
		fillAlbum(a, path, tracks, plays, known)
		a.Likes = sumLikes(tracks, likes)
		markSpoken(a, tracks, spoken)
		albums = append(albums, a)
	}

	for dir, tracks := range byDir {
		// The disc a track is on is read from its directory's name — a
		// regexp — and a comparator asks for it n log n times; once each.
		discs := make(map[*Item]int, len(tracks))
		for _, t := range tracks {
			discs[t] = discOf(t.Path)
		}
		// Tag track numbers order the album when present; name breaks ties
		// and orders untagged files (which sort after numbered ones).
		slices.SortFunc(tracks, func(a, b *Item) int {
			// Discs first, or the second disc's track one lands between the
			// first disc's one and two: every disc numbers from the start.
			if c := cmp.Compare(discs[a], discs[b]); c != 0 {
				return c
			}
			if (a.Track > 0) != (b.Track > 0) {
				if a.Track > 0 {
					return -1
				}
				return 1
			}
			if c := cmp.Compare(a.Track, b.Track); c != 0 {
				return c
			}
			return strings.Compare(a.Name, b.Name)
		})
		finish(&Album{
			ID:     "d" + PathID(dir),
			Name:   displayText(filepath.Base(dir)),
			Source: "dir",
		}, dir, tracks)
	}

	for _, pl := range playlists {
		ids := l.parseM3U(pl.Path)
		if len(ids) == 0 {
			continue
		}
		tracks := make([]*Item, 0, len(ids))
		l.mu.RLock()
		for _, id := range ids {
			if it, ok := l.items[id]; ok {
				tracks = append(tracks, it)
			}
		}
		l.mu.RUnlock()
		if len(tracks) == 0 {
			continue
		}
		a := &Album{
			ID:     "p" + pl.ID,
			Name:   strings.TrimSuffix(pl.Name, filepath.Ext(pl.Name)),
			Source: "m3u",
		}
		finish(a, pl.Path, tracks)
		a.ModTime = pl.ModTime
	}

	albums = dropDuplicateAlbums(albums)

	slices.SortFunc(albums, func(a, b *Album) int {
		if c := strings.Compare(a.lower, b.lower); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	// The audiobooks among them, for the chip: counted by the build the
	// album total comes from, so the two always describe one list. And the
	// release's word for each of its tracks — an intro on a record is music
	// whatever it sounds like alone, a chapter is a reading — kept where
	// spokenOf can read it without a build of its own (see similar.go).
	books := 0
	byRelease := make(map[string]bool)
	for _, a := range albums {
		if a.Spoken {
			books++
		}
		for _, id := range a.TrackIDs {
			byRelease[id] = a.Spoken
		}
	}
	l.spokenAlbums.Store(int32(books))
	l.featMu.Lock()
	l.byRelease = byRelease
	l.featMu.Unlock()
	return albums
}

// dropDuplicateAlbums removes a directory album when a playlist holds
// exactly the same tracks — a release shipped with its own .m3u produces
// both, and they are the same record listed twice. The playlist wins: it
// carries the intended running order.
func dropDuplicateAlbums(albums []*Album) []*Album {
	byTracks := make(map[string]*Album, len(albums))
	for _, a := range albums {
		if a.Source != "m3u" {
			continue
		}
		byTracks[trackSignature(a)] = a
	}
	if len(byTracks) == 0 {
		return albums
	}
	out := albums[:0]
	for _, a := range albums {
		// Only an identical track set counts: a playlist covering part of
		// a directory, or spanning several, is a collection of its own.
		if a.Source == "dir" {
			if _, dup := byTracks[trackSignature(a)]; dup {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// trackSignature identifies an album by its members, order-independent.
func trackSignature(a *Album) string {
	ids := slices.Clone(a.TrackIDs)
	slices.Sort(ids)
	return strings.Join(ids, "\x00")
}

// discWords are the numbers a disc is sometimes counted in rather than
// written with. Ten is as far as it goes: a release in double figures is
// counted in digits by everybody, and every word added is another word that
// could begin a title.
var discWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
}

// discPattern matches a directory that is one disc of a release: "CD2",
// "cd 2", "Disc.3", "disk4", the common form that names the disc as well —
// "CD 1-Firstlight", naming the disc as well — and the ones that spell
// the number out, "DISC ONE".
// The number has to be a number on its own, which is what keeps "CD 1990s
// Hits" and "Discography" out of it; the same whole-word rule is what keeps
// "Disco Lantern" out, "disc" there running straight into a letter.
var discPattern = regexp.MustCompile(
	`^(?i)(cd|disc|disk)[ ._-]*([0-9]{1,2}|one|two|three|four|five|six|seven|eight|nine|ten)\b`)

// discNumber reads the disc a directory name stands for, or 0.
func discNumber(name string) int {
	m := discPattern.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	if n, err := strconv.Atoi(m[2]); err == nil {
		return n
	}
	return discWords[strings.ToLower(m[2])]
}

// foldDiscs merges the disc directories of one release into the directory
// above them. One level only — a release is discs inside a release, never
// discs inside discs — and only where the release actually has several,
// since a single directory named like a disc is just a directory.
func foldDiscs(byDir map[string][]*Item) map[string][]*Item {
	discs := map[string]int{}
	for dir := range byDir {
		if discNumber(filepath.Base(dir)) > 0 {
			discs[filepath.Dir(dir)]++
		}
	}
	out := make(map[string][]*Item, len(byDir))
	for dir, tracks := range byDir {
		key := dir
		if parent := filepath.Dir(dir); discNumber(filepath.Base(dir)) > 0 && discs[parent] > 1 {
			key = parent
		}
		out[key] = append(out[key], tracks...)
	}
	return out
}

// discOf is which disc a track belongs to: 0 for a release that has only
// one, which sorts before everything and so changes nothing.
func discOf(path string) int {
	return discNumber(filepath.Base(filepath.Dir(path)))
}

// discSuffix matches what a tagger appends to an album name to say which
// disc this is — "Best Of 40 Years (CD2)", "Album - Disc 3".
// It takes the spelled-out numbers for the same reason the directories do:
// where the folder folds and the tag does not, the discs disagree about the
// album's name and the fold comes out under the directory's name instead of
// the release's.
var discSuffix = regexp.MustCompile(
	`(?i)[ ._-]*[(\[]?(cd|disc|disk)[ ._-]*([0-9]{1,2}|one|two|three|four|five|six|seven|eight|nine|ten)\b[)\]]?([ ._-].*)?$`)

// albumTitle is an album tag with any disc marker taken off the end, which
// is what lets three discs agree on one name — and, for a release that
// really is a single disc labelled that way, reads better anyway.
func albumTitle(s string) string {
	if t := strings.TrimSpace(discSuffix.ReplaceAllString(s, "")); t != "" {
		return t
	}
	return s
}

// fillAlbum derives aggregate fields from an ordered track list. If a majority
// of tracks agree on an Album/Artist tag, those override the fallback name.
// path is where the release lives — the directory for a directory album, the
// playlist file for one built from an m3u — and is indexed for search so the
// same query finds a release and its tracks.
// artistFromParent names the performer of an untagged release from the
// directory holding it — but only where that name is one the library already
// knows from tags somewhere else.
//
// Music is filed as performer, then release, then tracks, so the answer is
// usually sitting one level up; a release ripped or downloaded without tags
// has nothing else to go on and is filed under nobody. But a parent directory
// is not always a performer, and taking it on trust is how the artists view
// fills with rubbish. Measured over this library's untagged releases, the
// folder above them was as often a download client's status folder, a
// bootleg folder, a category ("EP, Single, Demo") or an interview archive as
// it was somebody's name — more often, in fact.
//
// So the name has to be **corroborated**: it counts only when some other
// release, tagged properly, already established that performer. The rule can
// then never invent one, which is the whole of its safety — the worst it can
// do is what happens today, which is nothing. Measured on the same set: five
// releases correctly filed, and every container directory refused, including
// two that were really disc folders whose fold had failed.
//
// The match is case-insensitive because a directory shouts where a tag does
// not, and the spelling handed back is the tagged one: it is the one the rest
// of the library is already grouped under, and a second spelling would be a
// second performer.
func artistFromParent(path string, known map[string]string) string {
	parent := displayText(filepath.Base(filepath.Dir(path)))
	if parent == "" {
		return ""
	}
	return known[strings.ToLower(parent)]
}

func fillAlbum(a *Album, path string, tracks []*Item, plays map[string]int, known map[string]string) {
	tagCount := map[string]int{}
	artistCount := map[string]int{}
	genreCount := map[string]int{}
	yearCount := map[string]int{}
	knownDurations := 0
	for _, t := range tracks {
		a.TrackIDs = append(a.TrackIDs, t.ID)
		a.Size += t.Size
		if t.Duration > 0 {
			a.Duration += t.Duration
			knownDurations++
		}
		if t.ModTime > a.ModTime {
			a.ModTime = t.ModTime
		}
		if t.Album != "" {
			tagCount[albumTitle(t.Album)]++
		}
		if t.Artist != "" {
			artistCount[t.Artist]++
		}
		if t.Genre != "" {
			genreCount[t.Genre]++
		}
		if t.Year > 0 {
			yearCount[strconv.Itoa(t.Year)]++
		}
	}
	// A partial total would be misleading while enrichment is still running.
	if knownDurations != len(tracks) {
		a.Duration = 0
	}
	a.Tracks = len(tracks)
	// Prefer the album the tags name over the directory or playlist file
	// name. A playlist has to be unanimous: a directory that mostly holds
	// one album still is that album, but a mixtape must keep its own name.
	if name, n := mostCommon(tagCount); n > 0 &&
		((a.Source == "dir" && n*2 >= len(tracks)) || n == len(tracks)) {
		a.Name = name
	}
	switch artist, n := mostCommon(artistCount); {
	case n > 0 && n*2 >= len(tracks):
		a.Artist = artist
	case len(artistCount) == 1:
		// One name and nothing against it. The majority test above is there
		// to settle a *disagreement*, and where there is none there is
		// nothing to settle: a release with one track tagged and eleven
		// blank is not an anonymous release, it is that performer's with the
		// tagging left half done. One voice is better than silence.
		a.Artist = artist
	case len(artistCount) > 1:
		a.Artist = "Various Artists"
	case a.Source == "dir":
		// Nothing tagged this release at all — not a disagreement between
		// tracks, which is a different thing and says something of its own.
		// Only then is the directory worth asking.
		a.Artist = artistFromParent(path, known)
	}
	if genre, n := mostCommon(genreCount); n*2 >= len(tracks) {
		a.Genres = splitGenres(genre)
		if len(a.Genres) > 0 {
			a.Genre = a.Genres[0]
		}
	}
	if year, n := mostCommon(yearCount); n*2 >= len(tracks) {
		a.Year, _ = strconv.Atoi(year)
	}
	// After the name is settled and the year is known from the tags, since
	// this both changes the one and may supply the other.
	liftYear(a)
	// The plays are summed here rather than kept up to date afterwards: the
	// list is cached per version, and a play bumps the version, so a rebuild
	// is exactly when this can go stale and exactly when it is redone.
	for _, t := range tracks {
		a.Plays += plays[t.ID]
	}
	if len(tracks) > 0 {
		a.CoverID = tracks[0].ID
	}
	// Albums are searched by the same word rules as items, over the tag
	// metadata the UI shows — name, artist, genre and year — plus where the
	// release is kept, so that a query answering in the file listing does not
	// come back empty one view across.
	year := ""
	if a.Year > 0 {
		year = strconv.Itoa(a.Year)
	}
	a.lower = searchText(a.Name, a.Artist, a.Genre, year, displayText(path))
	a.sortName = strings.ToLower(a.Name)
}

// mostCommon returns the most frequent key and its count, ties broken by
// name so the answer does not change between two builds of the same library.
func mostCommon(m map[string]int) (string, int) {
	best, n := "", 0
	for k, v := range m {
		if v > n || (v == n && k < best) {
			best, n = k, v
		}
	}
	return best, n
}

// parseM3U reads a playlist and resolves its entries to indexed item IDs.
// Only files already present in the index are included, so playlists cannot
// expose paths outside the configured roots.
func (l *Library) parseM3U(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	dir := filepath.Dir(path)
	var paths []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "://") {
			continue // remote URLs are not indexable
		}
		line = filepath.FromSlash(strings.ReplaceAll(line, "\\", "/"))
		p := line
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		paths = append(paths, filepath.Clean(p))
	}
	// Resolved under one lock rather than one per line: a playlist of a
	// thousand entries was a thousand lock rounds per build.
	ids := make([]string, 0, len(paths))
	l.mu.RLock()
	for _, p := range paths {
		if it, ok := l.byPath[p]; ok && (it.Kind == KindAudio || it.Kind == KindVideo) {
			ids = append(ids, it.ID)
		}
	}
	l.mu.RUnlock()
	return ids
}

// A year in a release's name is where a lot of libraries keep it: the
// directory is called "2018 - Some Release (Single)" because a file listing has
// nowhere else to put it. A card does have somewhere else — it shows the
// year beside the genre — so the name says it twice, and the second time is
// the one taking up room.
//
// So it is lifted out: dropped from the name and kept as the release's year
// where the tags did not already give one. Nothing is lost either way, since
// the directory is indexed for search exactly as it is spelt on disk, and a
// query for the year still finds the release.
const (
	albumYearFirst = 1900
	albumYearLast  = 2099
)

// yearSeparators are what stands between a year and the title it precedes.
const yearSeparators = " \t-–—._"

// splitYear takes a leading year off a release name, returning the year and
// what is left. It answers 0 and "" for a name that does not start with one
// — including a release *called* a year, where there would be nothing left.
func splitYear(name string) (int, string) {
	s := strings.TrimSpace(name)
	var closer byte
	switch {
	case strings.HasPrefix(s, "("):
		closer = ')'
	case strings.HasPrefix(s, "["):
		closer = ']'
	}
	if closer != 0 {
		s = s[1:]
	}
	if len(s) < 4 {
		return 0, ""
	}
	year, err := strconv.Atoi(s[:4])
	if err != nil || year < albumYearFirst || year > albumYearLast {
		return 0, ""
	}
	rest := s[4:]
	if closer != 0 {
		if rest == "" || rest[0] != closer {
			return 0, ""
		}
		rest = rest[1:]
	}
	trimmed := strings.TrimLeft(rest, yearSeparators)
	// Something has to divide the year from the title, or "2018Something" is a
	// word beginning with digits and not a dated release. A bracketed year
	// has already divided itself.
	if trimmed == rest && closer == 0 {
		return 0, ""
	}
	if trimmed == "" {
		return 0, ""
	}
	return year, trimmed
}

// splitTrailingYear does the same for a year at the end, which has to be
// bracketed: a name ending in a bare number is far more likely to mean it —
// a sequel, a catalogue number, a title that is a number.
func splitTrailingYear(name string) (int, string) {
	s := strings.TrimSpace(name)
	if len(s) < 6 {
		return 0, ""
	}
	var opener byte
	switch s[len(s)-1] {
	case ')':
		opener = '('
	case ']':
		opener = '['
	default:
		return 0, ""
	}
	if s[len(s)-6] != opener {
		return 0, ""
	}
	year, err := strconv.Atoi(s[len(s)-5 : len(s)-1])
	if err != nil || year < albumYearFirst || year > albumYearLast {
		return 0, ""
	}
	rest := strings.TrimRight(s[:len(s)-6], yearSeparators)
	if rest == "" {
		return 0, ""
	}
	return year, rest
}

// liftYear moves a year out of a release's name and into its own field,
// which is where a card reads it from.
func liftYear(a *Album) {
	year, rest := splitYear(a.Name)
	if rest == "" {
		year, rest = splitTrailingYear(a.Name)
	}
	if rest == "" {
		return
	}
	a.Name = rest
	// The tags outrank the directory: someone typed the folder name, and a
	// year in a tag came from the release itself.
	if a.Year == 0 {
		a.Year = year
	}
}

// markSpoken says whether a release is an audiobook. A genre tag that says
// so settles it. Otherwise the tracks the analysis has judged vote, by
// playing time rather than by count: a grindcore release is sixty
// four-second tracks and a dozen long songs, and by count the short ones
// would carry it — they are not judged at all (spokenMinSound), and even
// judged the long songs outweigh them.
//
// And they vote only once at least half the release's playing time has
// been judged. The analysis reads a library over hours, in whatever order
// the index comes, and a black metal EP was shelved as an audiobook while
// only its first track had been read — an ambient intro of quiet gaps and
// no tempo, which by these cues is what a reading is. Its four songs, read
// later, outweighed it four to one; until they were, the release had no
// business on the shelf. A release nothing has judged is music until told
// otherwise. Marked ones are found by the word.
func markSpoken(a *Album, tracks []*Item, sv *scaled) {
	tagged := spokenGenre(a.Genre)
	for _, g := range a.Genres {
		tagged = tagged || spokenGenre(g)
	}
	// By playing time where every track has one; by count where any lacks
	// it. One measured track weighed against unmeasured ones at a second
	// each is not a vote, it is that track deciding.
	byTime := true
	for _, t := range tracks {
		if t.Duration <= 0 {
			byTime = false
			break
		}
	}
	var speech, music, total float64
	for _, t := range tracks {
		w := 1.0
		if byTime {
			w = float64(t.Duration)
		}
		total += w
		if !sv.judged[t.ID] {
			continue
		}
		if sv.spoke[t.ID] {
			speech += w
		} else {
			music += w
		}
	}
	if tagged || (speech > music && (speech+music)*2 >= total) {
		a.Spoken = true
		a.lower += " audiobook"
	}
}
