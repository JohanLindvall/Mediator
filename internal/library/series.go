package library

import (
	"cmp"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Television, from what the files are called.
//
// There is no metadata to read here: an episode carries no tags worth the
// name, and what a release actually tells you is in its path. So the season
// and the episode are parsed out of the names, and — the part that matters —
// **the series name is taken from the best place it appears, which is very
// often not the file**.
//
// Measured over this library: of 85,609 videos, 3,275 carry an episode or
// season marker at all, in two shapes of roughly equal size. Half have it in
// the file name; half have the file in a "Season 5" directory whose parent is
// the only thing that names the show. And within the first half sit releases
// whose file is named for the *group* — "grp.hl.s01.e01.1080.mkv" — and
// the series is only in the directories above it. A parser that read the file
// alone would file three seasons of one show under three release groups.
//
// Hence the order below: the shallowest directory that both marks a season
// and has something in front of the marker is the most trustworthy name,
// because that is the level a season pack is named at, and its name has not
// been through a group's abbreviation. The file is asked last.

// Markers, in the order they are tried. Each captures a season and, where it
// can, an episode.
var (
	// Every one of these wants the marker to be **a whole token**: a
	// separator or the end of the name on both sides of it. That is not
	// fussiness. Half this library is downloads whose names end in a random
	// identifier — "clip-S74tb48v.mp4", "tumblr_s1tuqlsKPg1a8fz7r.mp4" — and
	// a season pattern that will match in the middle of a word reads every
	// one of those as season 74 of a series named after the rest of the
	// file. Measured before the rule was tightened: twenty of them, against
	// a hundred and twenty real shows.

	// S01E02, s01.e02, S01 E02, and the same with a three-digit episode.
	seasonEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[ ._-]*e(\d{1,3})(?:[ ._-]|$)`)
	// 1x02, the older convention, still common in older rips.
	seasonXEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})x(\d{2})(?:[ ._-]|$)`)
	// "Season 5" — a directory, almost always, and one that names no
	// episode. "Series 2" means the same thing in British usage but only on
	// a directory: in a file name the word turns up in the middle of
	// descriptions far more often than it names a season.
	seasonWord     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:season|series)[ ._-]*(\d{1,2})(?:[ ._-]|$)`)
	seasonWordFile = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])season[ ._-]*(\d{1,2})(?:[ ._-]|$)`)
	// "Episode 9", which the parenthesised form writes out in full and the
	// patterns above never see, since they want the number against the S.
	// Looser after the number than the others, because this form is usually
	// inside brackets — "(Season 3 Episode 9)" — and a closing one is not a
	// separator. It can afford to be: the word itself has to be there, and
	// it is only ever asked to fill in a number beside a season already
	// found.
	episodeWord = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:episode|ep)[ ._-]*(\d{1,3})(?:[^0-9]|$)`)
	// S01 on its own: a season pack's *directory*, and two things keep it
	// from being a menace. It is asked of directories only — a file that
	// names a season but no episode identifies nothing anyway — and it
	// wants **two digits**, because a season pack writes S01 while a
	// variant tag writes s2. Without either guard a clip ending
	// "-s2_hd_x265_aac.mp4" is season 2 of a series named after the
	// sentence in front of it, which is exactly what it was.
	seasonOnly = regexp.MustCompile(`(?i)(?:^|[ ._-])s(\d{2})(?:[ ._-]|$)`)
	// A year at the end of a series name: a disambiguator, not part of what
	// anybody calls the show.
	trailingYear = regexp.MustCompile(`[ ._-](19|20)\d{2}$`)
)

// howFarUp is how many directories above a file are read for a series name.
// Three covers a season pack inside a show folder inside a category; beyond
// that the directory is not naming this show any more.
const howFarUp = 3

// mark is what one name says: where the marker began, and what it held.
type mark struct {
	at      int // index the marker starts at, so the name before it can be taken
	season  int
	episode int // 0 when the marker names no episode
	ok      bool
}

// aspectRatios are the pairs that look exactly like a season and an episode
// in the "1x02" form and are nothing of the sort. A vertical clip called
// "clip-9x16.mp4" is not season nine.
var aspectRatios = map[[2]int]bool{
	{9, 16}: true, {16, 9}: true, {4, 3}: true, {3, 4}: true,
	{21, 9}: true, {1, 1}: true, {2, 3}: true, {3, 2}: true,
}

// markerIn reads the first season/episode marker in a name.
//
// dir says whether this is a directory, and it decides two things a file is
// not allowed to claim: the bare season-pack form ("S02"), and the word
// "series" as a synonym for a season. Both are how directories are named and
// both are, in a file, far more often part of something else.
func markerIn(s string, dir bool) mark {
	if m := seasonEpisode.FindStringSubmatchIndex(s); m != nil {
		season, _ := strconv.Atoi(s[m[2]:m[3]])
		episode, _ := strconv.Atoi(s[m[4]:m[5]])
		return mark{at: m[0], season: season, episode: episode, ok: true}
	}
	if m := seasonXEpisode.FindStringSubmatchIndex(s); m != nil {
		season, _ := strconv.Atoi(s[m[2]:m[3]])
		episode, _ := strconv.Atoi(s[m[4]:m[5]])
		// "03x00" is a time and "9x16" is an aspect ratio; a clip called
		// "flash at 03x00" is not season three, and a vertical one is not
		// season nine. This form is the loosest of the lot, so it insists
		// on both numbers being real and on the pair not being a shape.
		if season >= 1 && episode >= 1 && !aspectRatios[[2]int{season, episode}] {
			return mark{at: m[0], season: season, episode: episode, ok: true}
		}
	}
	word := seasonWord
	if !dir {
		word = seasonWordFile
	}
	if m := word.FindStringSubmatchIndex(s); m != nil {
		season, _ := strconv.Atoi(s[m[2]:m[3]])
		out := mark{at: m[0], season: season, ok: true}
		// "(Season 3 Episode 9)" spells both out, and the number is worth
		// having: it is what orders a listing.
		if e := episodeWord.FindStringSubmatch(s); e != nil {
			out.episode, _ = strconv.Atoi(e[1])
		}
		return out
	}
	if dir {
		if m := seasonOnly.FindStringSubmatchIndex(s); m != nil {
			season, _ := strconv.Atoi(s[m[2]:m[3]])
			return mark{at: m[0], season: season, ok: true}
		}
	}
	return mark{}
}

// seriesName cleans what stands in front of a marker into something readable:
// separators to spaces, the release's trailing year dropped, and nothing else
// — a show is called what it is called, and inventing capitalisation would
// only make two spellings of one series.
func seriesName(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, " -_.")
	if trimmed := trailingYear.ReplaceAllString(s, ""); trimmed != "" {
		s = strings.TrimSpace(trimmed)
	}
	return s
}

// Episode is what a path says about one episode of television.
type Episode struct {
	Series  string
	Season  int
	Episode int // 0 when nothing names it, which a "Season 5" folder does not
}

// parseEpisode reads a path. ok is false for anything that names no season at
// all, which is most of a library.
func parseEpisode(path string) (Episode, bool) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	dir := filepath.Dir(path)

	// The ancestors, nearest first, bounded.
	ancestors := make([]string, 0, howFarUp)
	for i := 0; i < howFarUp && dir != "" && dir != "." && dir != string(filepath.Separator); i++ {
		ancestors = append(ancestors, filepath.Base(dir))
		dir = filepath.Dir(dir)
	}

	fileMark := markerIn(base, false)
	// A file may only claim to be an episode if its own name says which
	// episode it is. A season on its own identifies nothing — that is a
	// season pack's directory speaking, and a file that merely mentions a
	// season is a description with a number in it.
	if fileMark.ok && fileMark.episode == 0 {
		fileMark = mark{}
	}
	var out Episode
	found := false

	// The numbers: the file first, since it is the only thing that names an
	// episode in the common case, then the nearest ancestor that does.
	if fileMark.ok {
		out.Season, out.Episode, found = fileMark.season, fileMark.episode, true
	}
	for _, seg := range ancestors {
		m := markerIn(seg, true)
		if !m.ok {
			continue
		}
		if !found {
			out.Season, out.Episode, found = m.season, m.episode, true
		} else if out.Episode == 0 && m.episode != 0 && m.season == out.Season {
			out.Episode = m.episode
		}
	}
	if !found {
		return Episode{}, false
	}

	// The name: the shallowest ancestor that marks a season and has
	// something in front of it. That is the level a season pack is named
	// at — "Show.Name.S01.1080p.BluRay.x264-GRP" — and it has not
	// been through a release group's abbreviation the way a file name has.
	for i := len(ancestors) - 1; i >= 0; i-- {
		m := markerIn(ancestors[i], true)
		if !m.ok {
			continue
		}
		if name := seriesName(ancestors[i][:m.at]); name != "" {
			out.Series = name
			break
		}
		// A directory called only "Season 5" names the show one level up,
		// which is the other half of this library.
		if i+1 < len(ancestors) {
			if name := seriesName(ancestors[i+1]); name != "" {
				out.Series = name
				break
			}
		}
	}
	// Only then the file itself, which in the worst case is all there is.
	if out.Series == "" && fileMark.ok {
		out.Series = seriesName(base[:fileMark.at])
	}
	if out.Series == "" {
		return Episode{}, false
	}
	return out, true
}

// SeriesKey is how two spellings of one show are recognised as one.
func SeriesKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// setEpisode stamps what the path says about a video onto the item. Only
// video: a track called "s01e02" is a track, and a picture is a picture.
func setEpisode(it *Item) {
	if it.Kind != KindVideo {
		return
	}
	if ep, ok := parseEpisode(it.Path); ok {
		it.Series, it.Season, it.EpisodeNo = ep.Series, ep.Season, ep.Episode
	}
}

// Series is one show, grouped from the episodes that name it.
//
// Grouped from **items** rather than from a collection above them, unlike
// artists and genres, because there is no album-shaped thing in between: an
// episode belongs to a season and a season is a number, not a directory that
// can be relied on. The seasons come with the series rather than from an
// endpoint of their own — a show has a handful, they are wanted the moment
// one is opened, and a second round trip to learn "Season 1, Season 2" would
// be a round trip to learn almost nothing.
type Series struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Seasons  []Season `json:"seasons"`
	Episodes int      `json:"episodes"`
	CoverID  string   `json:"coverId,omitempty"`
	Size     int64    `json:"size"`
	Duration int64    `json:"duration,omitempty"`
	Plays    int      `json:"plays,omitempty"`
	Likes    int      `json:"likes,omitempty"` // the net verdict on its episodes
	ModTime  int64    `json:"mtime"`

	lower    string
	sortName string
}

// Season is one season of one show.
type Season struct {
	Season   int    `json:"season"`
	Episodes int    `json:"episodes"`
	CoverID  string `json:"coverId,omitempty"`
	Size     int64  `json:"size"`
	Duration int64  `json:"duration,omitempty"`
	Plays    int    `json:"plays,omitempty"`
	Likes    int    `json:"likes,omitempty"` // the net verdict on its episodes
	ModTime  int64  `json:"mtime"`
}

// Series returns the grouped shows, rebuilt when the library has changed.
func (l *Library) Series() []*Series {
	return l.series.get(l.Version(), func() []*Series { return l.buildSeries(nil) })
}

// AllowedSeries groups the shows a caller restricted to part of the library
// can see.
//
// It is a **regrouping** rather than a filter of the cached list, and it has
// to be. A show is a show only where more than one of its episodes is in
// front of the viewer, and its episode count, its running time and the
// seasons it offers are the ones the caller can actually reach — filtering
// whole shows out of the whole library's grouping would put a shelf of one
// episode on screen under numbers from a library this caller cannot see.
// It would also disagree with the chip above it, which counts this way
// already (`CountsFor`): the chip read 32 while the grid drew 42.
//
// Paid only when the header is set, exactly as the albums are.
func (l *Library) AllowedSeries(f PathFilter) []*Series {
	if !f.Restricted() {
		return l.Series()
	}
	return l.buildSeries(f.allower())
}

// buildSeries groups the episodes into shows. allowed, when not nil, is what
// the caller may see.
func (l *Library) buildSeries(allowed func(string) bool) []*Series {
	plays := l.playsSnapshot()
	likes := l.likesSnapshot()
	type acc struct {
		s       *Series
		seasons map[int]*Season
		// Which totals an unmeasured episode has spoiled: as with a release
		// (fillAlbum), a running time is shown only when every part of it
		// was measured, or a half-read show would show half its length.
		unmeasured        bool
		unmeasuredSeasons map[int]bool
		cover             episodeKey
	}
	byKey := map[string]*acc{}

	l.mu.RLock()
	for _, it := range l.items {
		if it.Series == "" {
			continue
		}
		if allowed != nil && !allowed(it.Path) {
			continue
		}
		key := SeriesKey(it.Series)
		a := byKey[key]
		if a == nil {
			a = &acc{
				s:                 &Series{ID: "t" + PathID(key), Name: it.Series},
				seasons:           map[int]*Season{},
				unmeasuredSeasons: map[int]bool{},
			}
			byKey[key] = a
		}
		se := a.seasons[it.Season]
		if se == nil {
			se = &Season{Season: it.Season}
			a.seasons[it.Season] = se
		}
		n, k := plays[it.ID], likes[it.ID]
		a.s.Episodes++
		a.s.Size += it.Size
		a.s.Plays += n
		a.s.Likes += k
		se.Episodes++
		se.Size += it.Size
		se.Plays += n
		se.Likes += k
		if it.Duration > 0 {
			a.s.Duration += it.Duration
			se.Duration += it.Duration
		} else {
			a.unmeasured = true
			a.unmeasuredSeasons[it.Season] = true
		}
		if it.ModTime > se.ModTime || (it.ModTime == se.ModTime && it.ID > se.CoverID) {
			se.ModTime = it.ModTime
			se.CoverID = it.ID
		}
		if it.ModTime > a.s.ModTime {
			a.s.ModTime = it.ModTime
		}
		// The first episode of the earliest season is the face of a show:
		// its thumbnail is a frame from it, and the most recently added
		// episode is a spoiler as often as not.
		if this := (episodeKey{it.Season, it.EpisodeNo, it.ID}); a.s.CoverID == "" || this.before(a.cover) {
			a.s.CoverID = it.ID
			a.cover = this
		}
	}
	l.mu.RUnlock()

	out := make([]*Series, 0, len(byKey))
	for _, a := range byKey {
		// **A series needs more than one episode.** One is not a series; it
		// is an episode, and this is where the mistakes collect — a clip
		// whose name happens to carry a marker makes a show of one, and
		// every false positive found in practice was of exactly that shape.
		// Nothing is hidden by the rule: the file is still in the listings,
		// still searchable, still a video. It simply does not get a shelf of
		// its own for being alone on it.
		if a.s.Episodes < 2 {
			continue
		}
		if a.unmeasured {
			a.s.Duration = 0
		}
		for n, se := range a.seasons {
			if a.unmeasuredSeasons[n] {
				se.Duration = 0
			}
			a.s.Seasons = append(a.s.Seasons, *se)
		}
		slices.SortFunc(a.s.Seasons, func(x, y Season) int { return cmp.Compare(x.Season, y.Season) })
		a.s.lower = searchText(a.s.Name)
		a.s.sortName = strings.ToLower(a.s.Name)
		out = append(out, a.s)
	}
	byID(out, func(s *Series) string { return s.ID })
	return out
}

// episodeKey is where an episode stands in its show: the season, the
// number, and the id.
//
// **The id is what makes the order total**, and that is the whole point of
// it. The grouping walks the index's own map, so it arrives in a different
// order every time; with a season and a number to compare that does not
// matter, but episodes that share both are ordered by nothing at all and the
// first one seen wins. A show of twelve files that all parsed as season one
// episode zero — a folder of unnumbered episodes, which is common — therefore
// chose a different face on every rebuild. The list is rebuilt on every
// change to the library, which is every few seconds while anything is being
// written, so its tiles visibly cycled through episodes, blanking whenever
// the new choice had no thumbnail made yet.
type episodeKey struct {
	season, episode int
	id              string
}

// before says whether this episode comes earlier in its show than the other.
func (k episodeKey) before(o episodeKey) bool {
	if k.season != o.season {
		return k.season < o.season
	}
	if k.episode != o.episode {
		return k.episode < o.episode
	}
	return k.id < o.id
}

// SearchSeries filters and sorts the shows this caller may see.
func (l *Library) SearchSeries(search, sortKey string, desc bool, paths PathFilter) []*Series {
	all := l.AllowedSeries(paths)
	words := searchWords(search)
	out := make([]*Series, 0, len(all))
	for _, s := range all {
		if matchWords(s.lower, words) {
			out = append(out, s)
		}
	}
	orderBy(out, desc,
		func(s *Series) bool { return knownLength(sortKey, s.Duration) },
		func(a, b *Series) int { return compareSeries(a, b, sortKey) },
		func(s *Series) string { return s.sortName },
		func(s *Series) string { return s.ID })
	return out
}

// compareSeries orders two shows by one key: their own, then the ones every
// collection has (a show's "tracks" being its episodes).
func compareSeries(a, b *Series, sortKey string) int {
	if sortKey == "seasons" {
		return cmp.Compare(len(a.Seasons), len(b.Seasons))
	}
	return compareCommon(a, b, sortKey)
}
