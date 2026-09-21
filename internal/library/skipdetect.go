package library

// Finding a show's intro and credits, so the player can offer to skip them.
//
// Every episode of a season opens and closes with the same sound, and
// nothing else in the season repeats: so the intro of an episode is the
// stretch of its opening minutes that another episode's opening minutes
// also hold, and the credits are the same at the end (fingerprint.go). Each
// episode is compared with a few of its neighbours, and a stretch counts
// when two of them agree on it — one neighbour alone can share more than
// the intro with an episode (a two-part story, a recap of the same scene),
// and two rarely share the same accident.
//
// What it costs is a decode of the first and last minutes of every episode
// of every show, once: a few seconds apiece, and for a container that
// interleaves its sound with its picture nearly the whole of those minutes
// read from the disk. It runs in the lowest tier of background work, beside
// the music analysis, standing down for playback and for anything a viewer
// is waiting on — except for the season an episode being opened belongs to,
// which is asked for by name (WantSkips) and read ahead of everything else,
// as the tags of the page on screen are. The fingerprints are kept, keyed
// by the file's identity, so a season is only ever decoded once and a new
// episode costs its own minutes and a comparison.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

const (
	// skipHeadWindow and skipTailWindow are how much of each episode is
	// fingerprinted: the opening, where an intro sits after a cold open,
	// and the closing, where the credits are. Each is also capped as a
	// share of the episode, for a short one. Ten minutes at the head
	// because cold opens run long — measured on a real season, six minutes
	// cut the intro off in two episodes of ten and missed it in two more,
	// their cold opens being five minutes and longer.
	skipHeadWindow = 10 * time.Minute
	skipTailWindow = 4 * time.Minute
	skipHeadShare  = 0.4
	skipTailShare  = 0.3
	// skipMinRun is the shortest shared stretch worth calling an intro or a
	// set of credits: a sting shorter than this is not worth a button, and
	// two episodes sharing this much by accident is rare. skipMaxIntro is
	// the longest an intro is allowed to be — past it what two episodes
	// share is not an intro but the same footage.
	skipMinRun   = 12.0
	skipMaxIntro = 180.0
	// skipAgree is how far apart two neighbours' answers may be and still
	// be one answer, in seconds of start and of length.
	skipAgree = 4.0
	// skipPartners is how many neighbours' answers an episode wants, and
	// skipMaxPartners how many it will ask for them: a neighbour that
	// shares nothing — a recap episode, a file with another soundtrack —
	// is passed over for the next nearest.
	skipPartners    = 3
	skipMaxPartners = 6
	// skipRest is the pause between passes, and skipWantFor how long an
	// asked-for season stays at the front of the queue.
	skipRest    = time.Minute
	skipWantFor = 15 * time.Minute
	// skipDecodeTimeout bounds one episode's two decodes.
	skipDecodeTimeout = 4 * time.Minute
)

// skipState is what the library holds about the credits.
type skipState struct {
	once     sync.Once
	mu       sync.Mutex
	marks    map[string]blob.Skip   // by item id: where the credits are
	judged   map[string]string      // by season: the membership last judged
	wanted   map[string]time.Time   // by season: asked for, and when
	wantedEp map[string]string      // by season: the episode that was asked for
	prints   map[string]blob.Prints // by identity, only without a database
	wake     chan struct{}
}

func (s *skipState) init() {
	s.once.Do(func() {
		s.marks = map[string]blob.Skip{}
		s.judged = map[string]string{}
		s.wanted = map[string]time.Time{}
		s.wantedEp = map[string]string{}
		s.prints = map[string]blob.Prints{}
		s.wake = make(chan struct{}, 1)
	})
}

// SkipFor is where an episode's credits are, if they have been found.
func (l *Library) SkipFor(id string) (blob.Skip, bool) {
	l.skips.init()
	l.skips.mu.Lock()
	defer l.skips.mu.Unlock()
	m, ok := l.skips.marks[id]
	return m, ok
}

// SetSkip records where an episode's credits are; a zero mark forgets them.
func (l *Library) SetSkip(id string, m blob.Skip) {
	l.skips.init()
	l.skips.mu.Lock()
	defer l.skips.mu.Unlock()
	if m == (blob.Skip{}) {
		delete(l.skips.marks, id)
	} else {
		l.skips.marks[id] = m
	}
}

// LoadSkips restores what an earlier run found.
func (l *Library) LoadSkips(db *blob.DB) int {
	if db == nil {
		return 0
	}
	all, err := db.AllSkips()
	if err != nil {
		l.log.Warn("credits: could not read the marks", "err", err)
		return 0
	}
	l.skips.init()
	l.skips.mu.Lock()
	defer l.skips.mu.Unlock()
	for id, m := range all {
		l.skips.marks[id] = m
	}
	return len(all)
}

// WantSkips asks for an episode's season to be looked at ahead of the rest:
// somebody is opening it. Nothing is promised — the season is read as soon
// as the loop reaches it, which is at once if it is idle.
func (l *Library) WantSkips(it Item) {
	if it.Series == "" {
		return
	}
	l.skips.init()
	l.skips.mu.Lock()
	l.skips.wanted[seasonKey(it)] = time.Now()
	l.skips.wantedEp[seasonKey(it)] = it.ID
	l.skips.mu.Unlock()
	select {
	case l.skips.wake <- struct{}{}:
	default:
	}
}

// seasonKey names one season of one show, the way the grouping does.
func seasonKey(it Item) string {
	return SeriesKey(it.Series) + "|" + strconv.Itoa(it.Season)
}

// SkipDetectLoop finds the credits of every show, season by season, for as
// long as the process runs: once through everything, and then whatever a
// scan or a viewer brings.
func (l *Library) SkipDetectLoop(ctx context.Context, db *blob.DB, busy func() bool) {
	if FFmpegPath() == "" {
		l.log.Info("credits detection off: ffmpeg not found")
		return
	}
	if busy == nil {
		busy = func() bool { return false }
	}
	l.skips.init()
	for {
		l.skipPass(ctx, db, busy)
		select {
		case <-ctx.Done():
			return
		case <-time.After(skipRest):
		case <-l.skips.wake:
		}
	}
}

// skipSeason is one season's episodes, as the pass works on them.
type skipSeason struct {
	key    string
	series string
	season int
	eps    []Item
}

// skipSeasons groups the episodes by show and season. Copies, taken under
// the read lock and worked on outside it.
func (l *Library) skipSeasons() []skipSeason {
	l.mu.RLock()
	byKey := map[string]*skipSeason{}
	for _, it := range l.items {
		if it.Kind != KindVideo || it.Series == "" {
			continue
		}
		k := seasonKey(*it)
		s := byKey[k]
		if s == nil {
			s = &skipSeason{key: k, series: it.Series, season: it.Season}
			byKey[k] = s
		}
		s.eps = append(s.eps, *it)
	}
	l.mu.RUnlock()
	out := make([]skipSeason, 0, len(byKey))
	for _, s := range byKey {
		if len(s.eps) < 2 {
			continue // one episode has nothing to be compared with
		}
		sort.Slice(s.eps, func(i, j int) bool {
			if s.eps[i].EpisodeNo != s.eps[j].EpisodeNo {
				return s.eps[i].EpisodeNo < s.eps[j].EpisodeNo
			}
			return s.eps[i].ID < s.eps[j].ID
		})
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// signature says which files a season is made of, so a season already
// judged for exactly these is not judged again.
func (s *skipSeason) signature() string {
	parts := make([]string, len(s.eps))
	for i, it := range s.eps {
		parts[i] = fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, it.Duration)
	}
	return strings.Join(parts, ";")
}

// skipPass looks at every season that has changed since it was last looked
// at, the asked-for ones first.
func (l *Library) skipPass(ctx context.Context, db *blob.DB, busy func() bool) {
	seasons := l.skipSeasons()
	l.skips.mu.Lock()
	now := time.Now()
	for k, at := range l.skips.wanted {
		if now.Sub(at) > skipWantFor {
			delete(l.skips.wanted, k)
			delete(l.skips.wantedEp, k)
		}
	}
	wanted := map[string]time.Time{}
	wantedEp := map[string]string{}
	for k, at := range l.skips.wanted {
		wanted[k] = at
		wantedEp[k] = l.skips.wantedEp[k]
	}
	judged := map[string]string{}
	for k, sig := range l.skips.judged {
		judged[k] = sig
	}
	l.skips.mu.Unlock()

	// Asked-for seasons first, oldest ask first; then the rest in order.
	sort.SliceStable(seasons, func(i, j int) bool {
		wi, oki := wanted[seasons[i].key]
		wj, okj := wanted[seasons[j].key]
		if oki != okj {
			return oki
		}
		return oki && wi.Before(wj)
	})
	for _, s := range seasons {
		if ctx.Err() != nil {
			return
		}
		sig := s.signature()
		if judged[s.key] == sig {
			continue
		}
		_, asked := wanted[s.key]
		if !l.judgeSeason(ctx, db, s, busy, asked, wantedEp[s.key]) {
			return // the context ended
		}
		l.skips.mu.Lock()
		l.skips.judged[s.key] = sig
		delete(l.skips.wanted, s.key)
		delete(l.skips.wantedEp, s.key)
		l.skips.mu.Unlock()
	}
}

// judgeSeason fingerprints what the season lacks, compares the episodes,
// and records what it found. False only where the context ended.
//
// An asked-for season is read from the asked-for episode outwards, and
// judged once that episode and its nearest neighbours are in hand — a
// viewer is waiting, and the whole of a long season is minutes of decoding
// they should not wait through for the episode in front of them. The rest
// follows, and the season is judged again whole.
func (l *Library) judgeSeason(ctx context.Context, db *blob.DB, s skipSeason, busy func() bool, asked bool, askedEp string) bool {
	order := s.eps
	if askedEp != "" {
		order = nearestFirst(s.eps, askedEp)
	}
	var eps []episodePrints
	early := false
	for _, it := range order {
		if askedEp != "" && !early && len(eps) > skipPartners {
			l.recordSeason(db, s, eps)
			early = true
		}
		if it.Duration <= 0 {
			continue // nowhere to place the credits from until the length is read
		}
		p, ok := l.printsOf(db, it)
		if !ok {
			// The sweep yields to everything above it; a season somebody is
			// opening does not, since they are waiting.
			if !asked {
				for busy() || l.enriching.Load() > 0 {
					select {
					case <-ctx.Done():
						return false
					case <-time.After(2 * time.Second):
					}
				}
			}
			if ctx.Err() != nil {
				return false
			}
			var err error
			p, err = l.fingerprintEpisode(ctx, it)
			if err != nil {
				if ctx.Err() != nil {
					return false
				}
				l.log.Debug("credits: could not read an episode", "path", it.Rel, "err", err)
				continue
			}
			l.keepPrints(db, it, p)
		}
		eps = append(eps, episodePrints{
			id: it.ID, episode: it.EpisodeNo, duration: float64(it.Duration) / 1000,
			head: p.Head, tailFrom: p.TailFrom, tail: p.Tail,
		})
	}
	l.recordSeason(db, s, eps)
	return true
}

// nearestFirst orders a season's episodes by distance from one of them.
func nearestFirst(eps []Item, id string) []Item {
	at := -1
	for i, it := range eps {
		if it.ID == id {
			at = i
		}
	}
	if at < 0 {
		return eps
	}
	out := []Item{eps[at]}
	for d := 1; at-d >= 0 || at+d < len(eps); d++ {
		if at+d < len(eps) {
			out = append(out, eps[at+d])
		}
		if at-d >= 0 {
			out = append(out, eps[at-d])
		}
	}
	return out
}

// recordSeason compares the episodes in hand and writes down what they
// agree on. Order matters to the comparison — neighbours are neighbours by
// position — so the episodes are put back in the season's order first.
func (l *Library) recordSeason(db *blob.DB, s skipSeason, eps []episodePrints) {
	if len(eps) < 2 {
		return
	}
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].episode != eps[j].episode {
			return eps[i].episode < eps[j].episode
		}
		return eps[i].id < eps[j].id
	})
	found := detectSeason(eps)
	intros, credits := 0, 0
	for _, e := range eps {
		m := found[e.id]
		if m.IntroEnd > 0 {
			intros++
		}
		if m.Outro > 0 {
			credits++
		}
		old, _ := l.SkipFor(e.id)
		if old == m {
			continue
		}
		l.SetSkip(e.id, m)
		if db != nil {
			if err := db.PutSkip(e.id, m); err != nil {
				l.log.Warn("credits: could not write the marks", "path", e.id, "err", err)
			}
		}
	}
	l.log.Info("credits looked for", "series", s.series, "season", s.season,
		"episodes", len(eps), "intros", intros, "credits", credits)
}

// printsOf is an episode's stored fingerprints, where they match the file
// as it is now.
func (l *Library) printsOf(db *blob.DB, it Item) (blob.Prints, bool) {
	if db != nil {
		return db.GetPrints(it.ID, it.ModTime, it.Size)
	}
	l.skips.mu.Lock()
	defer l.skips.mu.Unlock()
	p, ok := l.skips.prints[printsKey(it)]
	return p, ok
}

func (l *Library) keepPrints(db *blob.DB, it Item, p blob.Prints) {
	if db != nil {
		if err := db.PutPrints(it.ID, it.ModTime, it.Size, p); err != nil {
			l.log.Warn("credits: could not write a fingerprint", "path", it.Rel, "err", err)
		}
		return
	}
	l.skips.mu.Lock()
	l.skips.prints[printsKey(it)] = p
	l.skips.mu.Unlock()
}

func printsKey(it Item) string {
	return it.ID + "|" + strconv.FormatInt(it.ModTime, 10) + "|" + strconv.FormatInt(it.Size, 10)
}

// fingerprintEpisode reads an episode's opening and closing minutes.
func (l *Library) fingerprintEpisode(parent context.Context, it Item) (blob.Prints, error) {
	ctx, cancel := context.WithTimeout(parent, skipDecodeTimeout)
	defer cancel()
	dur := float64(it.Duration) / 1000
	head := math.Min(skipHeadWindow.Seconds(), dur*skipHeadShare)
	tailLen := math.Min(skipTailWindow.Seconds(), dur*skipTailShare)
	tailFrom := math.Max(0, dur-tailLen)
	input, extra, err := analysisInput(it)
	if err != nil {
		return blob.Prints{}, err
	}
	h, err := decodeSpan(ctx, input, extra, 0, head, fpRate)
	if err != nil {
		return blob.Prints{}, err
	}
	t, err := decodeSpan(ctx, input, extra, tailFrom, tailLen, fpRate)
	if err != nil {
		return blob.Prints{}, err
	}
	return blob.Prints{Head: fingerprintPCM(h), TailFrom: tailFrom, Tail: fingerprintPCM(t)}, nil
}

// episodePrints is one episode as the comparison sees it.
type episodePrints struct {
	id       string
	episode  int
	duration float64
	head     []uint32
	tailFrom float64
	tail     []uint32
}

// skipCandidate is one neighbour's answer: a stretch of this episode, in
// seconds, and how many frames of it matched.
type skipCandidate struct {
	start, end float64
	run        int
}

// detectSeason compares each episode with its nearest neighbours and
// returns the marks for those where the neighbours agree. Pure, so it can
// be tested on fingerprints made from sound that never existed.
func detectSeason(eps []episodePrints) map[string]blob.Skip {
	out := map[string]blob.Skip{}
	minRun := fpFrames(skipMinRun)
	for i, e := range eps {
		var intros, credits []skipCandidate
		partners := partnersOf(len(eps), i)
		tried := 0
		for _, j := range partners {
			if len(intros) >= skipPartners && len(credits) >= skipPartners {
				break
			}
			tried++
			p := eps[j]
			if ai, _, n := commonRun(e.head, p.head, minRun); n > 0 {
				s, length := fpSeconds(ai), fpSeconds(n)
				if length <= skipMaxIntro {
					intros = append(intros, skipCandidate{start: s, end: s + length, run: n})
				}
			}
			if ai, _, n := commonRun(e.tail, p.tail, minRun); n > 0 {
				s := e.tailFrom + fpSeconds(ai)
				credits = append(credits, skipCandidate{start: s, end: s + fpSeconds(n), run: n})
			}
		}
		// A single answer stands only where there was nobody else to ask:
		// every other episode of the season was compared and shared
		// nothing, which is a two-episode season, or a small one with a
		// recap in it.
		lone := tried >= len(eps)-1
		var m blob.Skip
		if c, ok := agreed(intros, lone); ok {
			m.IntroStart, m.IntroEnd = roundMark(c.start), roundMark(c.end)
		}
		if c, ok := agreed(credits, lone); ok && e.duration > c.start {
			m.Outro = roundMark(e.duration - c.start)
		}
		if m != (blob.Skip{}) {
			out[e.id] = m
		}
	}
	// An intro that runs into the end of the window was cut off by it,
	// not by the episode: the comparison cannot see past what was
	// fingerprinted. Where the rest of the season says how long the intro
	// is, the cut one is given that length from its own start — measured
	// on a season with five-minute cold opens, two of ten were cut this
	// way, and a skip that lands a third of the way into the intro is a
	// skip that has to be pressed twice.
	typical := typicalIntro(eps, out)
	if typical > 0 {
		for _, e := range eps {
			m, ok := out[e.id]
			if !ok || m.IntroEnd == 0 || !cutByWindow(e, m) {
				continue
			}
			m.IntroEnd = roundMark(m.IntroStart + typical)
			out[e.id] = m
		}
	}
	return out
}

// cutByWindow says an intro ends where the fingerprint does, give or take
// a second, which is the window's end and not the intro's.
func cutByWindow(e episodePrints, m blob.Skip) bool {
	return m.IntroEnd >= fpSeconds(len(e.head))-1
}

// typicalIntro is the median length of the intros that were seen whole.
func typicalIntro(eps []episodePrints, marks map[string]blob.Skip) float64 {
	var lengths []float64
	for _, e := range eps {
		if m, ok := marks[e.id]; ok && m.IntroEnd > 0 && !cutByWindow(e, m) {
			lengths = append(lengths, m.IntroEnd-m.IntroStart)
		}
	}
	if len(lengths) == 0 {
		return 0
	}
	sort.Float64s(lengths)
	return lengths[len(lengths)/2]
}

// partnersOf orders the other episodes by distance from this one — the
// next, the previous, the one after that — up to skipMaxPartners of them.
func partnersOf(n, i int) []int {
	var out []int
	for d := 1; len(out) < skipMaxPartners && (i-d >= 0 || i+d < n); d++ {
		if i+d < n {
			out = append(out, i+d)
		}
		if i-d >= 0 && len(out) < skipMaxPartners {
			out = append(out, i-d)
		}
	}
	return out
}

// agreed picks the answer to trust: the longest that another agrees with,
// or the only one where a single answer may stand.
func agreed(cs []skipCandidate, lone bool) (skipCandidate, bool) {
	if len(cs) == 0 {
		return skipCandidate{}, false
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].run > cs[j].run })
	if len(cs) == 1 {
		return cs[0], lone
	}
	for i, c := range cs {
		for j, d := range cs {
			if i == j {
				continue
			}
			cl, dl := c.end-c.start, d.end-d.start
			if math.Abs(c.start-d.start) <= skipAgree && math.Abs(cl-dl) <= math.Max(skipAgree, 0.25*cl) {
				return c, true
			}
		}
	}
	return skipCandidate{}, false
}

// roundMark keeps a mark to a quarter of a second, which is as fine as a
// sixteenth-of-a-second fingerprint can honestly place one.
func roundMark(sec float64) float64 { return math.Round(sec*4) / 4 }
