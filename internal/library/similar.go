package library

// What sounds like what.
//
// Every track's vector (features.go) is scaled column by column to the
// library — each column's mean taken out and its spread divided away, so a
// tempo in beats per minute and a cepstral coefficient count alike — and
// then made unit length, after which similarity is the dot product: cosine
// similarity, 1 for the same sound, 0 for unrelated, below for opposite.
// Brute force throughout, deliberately: fifty-five numbers by a hundred
// thousand tracks is a few milliseconds, and an index would be a second
// thing to keep right for no gain.
//
// Three things are read off it. Nearest tracks, for "more like this" and for
// radio. Affinity: how much a track sounds like what the owner liked, less
// how much like what they disliked, which is what the popular orders put
// between the owner's own verdict and the play count. And the sound of a
// release or a performer, which is the mean of their tracks', for "similar
// releases" and "similar performers". Speech is kept apart from music
// everywhere (spoken.go): a chapter after a song is not what anybody meant.

import (
	"cmp"
	"math"
	"reflect"
	"slices"
	"strings"
)

// scaled is every track's vector scaled and normalised, built once per
// change of the features and read many times.
type scaled struct {
	gen    int64
	vecs   map[string][]float32
	spoke  map[string]bool // reads as speech (spoken.go), from the raw vector
	judged map[string]bool // there was enough sound to say either way
}

// scaledVectors answers the current scaling, rebuilding it when a vector
// has arrived since.
func (l *Library) scaledVectors() *scaled {
	l.featMu.RLock()
	gen := l.featuresGen
	cur := l.scaledCache
	l.featMu.RUnlock()
	if cur != nil && cur.gen == gen {
		return cur
	}
	l.featMu.RLock()
	raw := make(map[string][]float32, len(l.features))
	for id, rec := range l.features {
		if len(rec.vec) == featureDims {
			raw[id] = rec.vec
		}
	}
	l.featMu.RUnlock()

	mean := make([]float64, featureDims)
	std := make([]float64, featureDims)
	for _, v := range raw {
		for i, x := range v {
			mean[i] += float64(x)
		}
	}
	n := float64(len(raw))
	if n > 0 {
		for i := range mean {
			mean[i] /= n
		}
		for _, v := range raw {
			for i, x := range v {
				d := float64(x) - mean[i]
				std[i] += d * d
			}
		}
		for i := range std {
			std[i] = math.Sqrt(std[i] / n)
			if std[i] < 1e-6 {
				std[i] = 1 // a column that never varies says nothing; leave it be
			}
		}
	}
	out := &scaled{gen: gen, vecs: make(map[string][]float32, len(raw)),
		spoke: make(map[string]bool, len(raw)), judged: make(map[string]bool, len(raw))}
	for id, v := range raw {
		u := make([]float32, featureDims)
		var norm float64
		for i, x := range v {
			z := (float64(x) - mean[i]) / std[i]
			u[i] = float32(z)
			norm += z * z
		}
		if norm > 0 {
			norm = math.Sqrt(norm)
			for i := range u {
				u[i] = float32(float64(u[i]) / norm)
			}
		}
		out.vecs[id] = u
		// Judged on the raw vector: the scaled one has had the library's
		// mean taken out of every column, the seconds of sound included.
		out.spoke[id], out.judged[id] = spokenVerdict(v)
	}
	l.featMu.Lock()
	l.scaledCache = out
	l.featMu.Unlock()
	return out
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// spokenOf says whether a track is speech: its release's word where it has
// one — an intro on a record is music however it sounds alone, a chapter
// is a reading — and its own where it stands alone. The release's word is
// the last album build's, read without forcing one: this is asked under
// the index's lock, where a build would wait on itself.
func (l *Library) spokenOf(id string) bool {
	l.featMu.RLock()
	spoken, ok := l.byRelease[id]
	l.featMu.RUnlock()
	if ok {
		return spoken
	}
	return l.scaledVectors().spoke[id]
}

// spokenSet is spokenOf for a whole pass: the release verdicts as a map,
// with the track's own verdict as the fallback.
func (l *Library) spokenSet(sv *scaled) func(id string) bool {
	l.featMu.RLock()
	byRelease := l.byRelease
	l.featMu.RUnlock()
	return func(id string) bool {
		if spoken, ok := byRelease[id]; ok {
			return spoken
		}
		return sv.spoke[id]
	}
}

// Similar answers the n tracks that sound most like the given one, nearest
// first: analysed tracks the caller may see, of the seed's own kind — music
// for music, speech for speech — and never the seed itself.
func (l *Library) Similar(id string, n int, kinds KindSet, f PathFilter) []Item {
	sv := l.scaledVectors()
	seed, ok := sv.vecs[id]
	if !ok || n <= 0 {
		return nil
	}
	l.Albums() // so the releases' word is current before it is read
	isSpoken := l.spokenSet(sv)
	spoken := isSpoken(id)
	type hit struct {
		id    string
		score float32
	}
	// Nearest first, ties by id so two answers cannot disagree.
	before := func(a, b hit) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	}
	// The n best of the whole library, kept as a short sorted slice that a
	// candidate is slotted into only when it beats the last: a few dozen
	// insertions against twenty thousand candidates, where sorting them all
	// to keep twenty was the expensive way to say the same thing.
	best := make([]hit, 0, n+1)
	consider := func(h hit) {
		if len(best) == n && before(h, best[n-1]) >= 0 {
			return
		}
		i, _ := slices.BinarySearchFunc(best, h, before)
		best = slices.Insert(best, i, h)
		if len(best) > n {
			best = best[:n]
		}
	}
	allowed := f.allower()
	st := l.stamper()
	l.ensureFlags()
	l.mu.RLock()
	for other, v := range sv.vecs {
		if other == id || isSpoken(other) != spoken {
			continue
		}
		it, ok := l.items[other]
		if !ok || !kinds.Has(it.Kind) || !allowed(it.Path) {
			continue
		}
		consider(hit{other, dot(seed, v)})
	}
	out := make([]Item, 0, len(best))
	for _, h := range best {
		out = append(out, st.stamp(*l.items[h.id]))
	}
	l.mu.RUnlock()
	return out
}

// affinity is what the owner's verdicts say about every analysed track:
// how much it sounds like something they liked, less how much like
// something they disliked. Built once per change of verdicts or features.
type affinity struct {
	likesGen, featGen int64
	// The releases' word it kept speech apart by (byRelease): an album build
	// replaces that map wholesale, and an affinity built against the old
	// one would go on separating music from speech by a verdict the shelves
	// no longer hold. Compared by identity, since it is never edited in
	// place.
	release map[string]bool
	bucket  map[string]int    // -2..2, see affinityBucket
	akin    map[string]string // the verdict's track it most resembles
}

// sameMap says whether two maps are the same map, not merely equal.
func sameMap(a, b map[string]bool) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// affinities answers the current affinity, rebuilding it when a verdict or
// a vector has changed since.
func (l *Library) affinities() *affinity {
	// The cache is asked first, on the generations alone: this is called
	// once per item a listing hands out, and copying the verdicts before
	// looking was a map per item for an answer that was already there.
	sv := l.scaledVectors()
	l.featMu.RLock()
	cur, release := l.affinityCache, l.byRelease
	l.featMu.RUnlock()
	if cur != nil && cur.likesGen == l.likes.generation() && cur.featGen == sv.gen && sameMap(cur.release, release) {
		return cur
	}
	likes, likesGen := l.likes.snapshot()
	out := &affinity{likesGen: likesGen, featGen: sv.gen, release: release, bucket: map[string]int{}, akin: map[string]string{}}
	isSpoken := l.spokenSet(sv)
	var liked, disliked []string
	for id, v := range likes {
		if _, ok := sv.vecs[id]; !ok {
			continue
		}
		if v > 0 {
			liked = append(liked, id)
		} else {
			disliked = append(disliked, id)
		}
	}
	if len(liked)+len(disliked) > 0 {
		for id, v := range sv.vecs {
			if likes[id] != 0 {
				continue // its own verdict speaks
			}
			var pos, neg float32
			akin := ""
			for _, lid := range liked {
				if s := dot(v, sv.vecs[lid]); s > pos && isSpoken(lid) == isSpoken(id) {
					pos, akin = s, lid
				}
			}
			for _, did := range disliked {
				if s := dot(v, sv.vecs[did]); s > neg && isSpoken(did) == isSpoken(id) {
					neg = s
					if s > pos {
						akin = did
					}
				}
			}
			if b := affinityBucket(pos - neg); b != 0 {
				out.bucket[id] = b
				out.akin[id] = akin
			}
		}
	}
	l.featMu.Lock()
	l.affinityCache = out
	l.featMu.Unlock()
	return out
}

// affinityBucket grades a similarity difference: unrelated tracks sit near
// zero, and a resemblance has to be clear before it says anything, so that
// what the tile marks and what the popular order lifts is a track that
// really does sound like what was liked.
func affinityBucket(d float32) int {
	switch {
	case d >= 0.6:
		return 2
	case d >= 0.35:
		return 1
	case d <= -0.6:
		return -2
	case d <= -0.35:
		return -1
	}
	return 0
}

// akinName is the title of the track a resemblance was measured against.
// Caller must hold l.mu.
func (l *Library) akinName(id string) string {
	if it, ok := l.items[id]; ok {
		if it.Title != "" {
			return it.Title
		}
		return it.Name
	}
	return ""
}

// trackPopularity is what the popular orders sort tracks on: the owner's
// verdict first, then how much the track sounds like what they liked, then
// the count. See popularity for the collections, which have no affinity.
func trackPopularity(like, bucket, plays int) int64 {
	return int64(like)<<40 + int64(bucket+2)<<24 + int64(plays)
}

// sound is the mean of a set of scaled vectors, unit length: what a release
// or a performer sounds like, and how many analysed tracks said so.
type sound struct {
	vec   []float32
	count int
}

func soundOf(sv *scaled, isSpoken func(string) bool, ids []string) sound {
	acc := make([]float32, featureDims)
	n := 0
	for _, id := range ids {
		v, ok := sv.vecs[id]
		if !ok || isSpoken(id) {
			continue
		}
		for i := range acc {
			acc[i] += v[i]
		}
		n++
	}
	if n == 0 {
		return sound{}
	}
	var norm float64
	for _, x := range acc {
		norm += float64(x) * float64(x)
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for i := range acc {
			acc[i] = float32(float64(acc[i]) / norm)
		}
	}
	return sound{vec: acc, count: n}
}

// sounds is what every release and every performer sounds like, cached per
// library version and features generation.
type sounds struct {
	version, featGen int64
	albums           map[string]sound
	artists          map[string]sound // by the artists view's lowercase key
}

func (l *Library) sounds() *sounds {
	version := l.Version()
	sv := l.scaledVectors()
	l.featMu.RLock()
	cur := l.soundsCache
	l.featMu.RUnlock()
	if cur != nil && cur.version == version && cur.featGen == sv.gen {
		return cur
	}
	out := &sounds{version: version, featGen: sv.gen, albums: map[string]sound{}, artists: map[string]sound{}}
	byArtist := map[string][]string{}
	albums := l.Albums()
	isSpoken := l.spokenSet(sv)
	for _, a := range albums {
		s := soundOf(sv, isSpoken, a.TrackIDs)
		if s.count == 0 {
			continue
		}
		out.albums[a.ID] = s
		if a.Artist != "" {
			key := strings.ToLower(a.Artist)
			byArtist[key] = append(byArtist[key], a.TrackIDs...)
		}
	}
	for key, ids := range byArtist {
		if s := soundOf(sv, isSpoken, ids); s.count > 0 {
			out.artists[key] = s
		}
	}
	l.featMu.Lock()
	l.soundsCache = out
	l.featMu.Unlock()
	return out
}

// SimilarAlbums answers the releases that sound like the given one, among
// those the query allows — the search words and the caller's paths apply;
// the sort key does not, similarity being the order, but the direction
// does: nearest first, or turned round, the least alike first. The seed
// itself is left out. Copies, each carrying its Similarity.
func (l *Library) SimilarAlbums(id string, q AlbumQuery) []*Album {
	snd := l.sounds()
	seed, ok := snd.albums[id]
	if !ok {
		return nil
	}
	words := searchWords(q.Search)
	var out []*Album
	for _, a := range l.AllowedAlbums(l.Albums(), q.Paths) {
		s, ok := snd.albums[a.ID]
		if !ok || a.ID == id || !matchWords(a.lower, words) {
			continue
		}
		c := *a
		c.Similarity = dot(seed.vec, s.vec)
		out = append(out, &c)
	}
	nearest(out, func(a *Album) float32 { return a.Similarity }, func(a *Album) string { return a.ID }, q.Desc)
	return out
}

// SimilarArtists answers the performers that sound like the named one,
// among those the caller may see and the search matches: nearest first, or
// turned round, the least alike first. Copies, each carrying its Similarity.
func (l *Library) SimilarArtists(name, search string, desc bool, f PathFilter) []*Artist {
	snd := l.sounds()
	seed, ok := snd.artists[strings.ToLower(name)]
	if !ok {
		return nil
	}
	words := searchWords(search)
	var out []*Artist
	// Every performer the caller may see, in no order: the resemblance is
	// the order, and sorting them by name first was work thrown away.
	for _, ar := range l.artistsFor(f) {
		key := strings.ToLower(ar.Name)
		s, ok := snd.artists[key]
		if !ok || key == strings.ToLower(name) || !matchWords(ar.lower, words) {
			continue
		}
		c := *ar
		c.Similarity = dot(seed.vec, s.vec)
		out = append(out, &c)
	}
	nearest(out, func(a *Artist) float32 { return a.Similarity }, func(a *Artist) string { return a.ID }, desc)
	return out
}

// nearest orders the things that sound like a seed by their resemblance —
// nearest first, or the other way — with ties broken by id so two answers
// cannot disagree. The releases and the performers sort by the one rule.
func nearest[T any](out []*T, score func(*T) float32, id func(*T) string, desc bool) {
	slices.SortFunc(out, func(a, b *T) int {
		if c := cmp.Compare(score(a), score(b)); c != 0 {
			if desc {
				return -c
			}
			return c
		}
		return strings.Compare(id(a), id(b))
	})
}
