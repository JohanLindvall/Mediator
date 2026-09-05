package library

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// A vector shaped like music with a given timbre, or like a voice reading.
func shapedVector(timbre float32, spoken bool) []float32 {
	v := make([]float32, featureDims)
	for i := range 13 {
		v[i] = timbre * float32(i+1)
	}
	v[55] = 60 // a minute of sound: enough to be judged
	if spoken {
		for i := 34; i < 46; i++ {
			v[i] = 1.0 / 12
		}
		v[51], v[52], v[53], v[54] = 0.1, 0.5, 0.6, 0.1
	} else {
		v[34] = 0.5
		for i := 35; i < 46; i++ {
			v[i] = 0.05
		}
		v[51], v[52], v[53], v[54] = 0.6, 0.02, 0.15, 0.01
	}
	return v
}

// The catalogue of tracks_test.go with every track read: one performer's
// tracks sound alike, the other's differently, and the uncredited release
// is somebody reading.
func libWithSounds(t *testing.T) *Library {
	t.Helper()
	l := libForQueue(t)
	l.mu.RLock()
	items := make([]*Item, 0, len(l.items))
	for _, it := range l.items {
		items = append(items, it)
	}
	l.mu.RUnlock()
	for _, it := range items {
		var v []float32
		switch {
		case it.Album == "Sampler":
			v = shapedVector(1, true)
		case it.Artist == "Gorse Beacon":
			v = shapedVector(1, false)
			v[0] += float32(len(it.Name)) * 0.01 // alike, not identical
		default:
			v = shapedVector(-1, false)
		}
		l.SetFeatures(it.ID, it.ModTime, it.Size, v)
	}
	return l
}

func TestSimilarTracksAreTheNearestOfTheirOwnKind(t *testing.T) {
	l := libWithSounds(t)
	seed := PathID("/library/Gorse Beacon/Signal Fires/01 track.mp3")
	got := l.Similar(seed, 10, 0, PathFilter{})
	if len(got) == 0 {
		t.Fatal("nothing similar was found")
	}
	for i, it := range got {
		if it.ID == seed {
			t.Error("the seed was offered as similar to itself")
		}
		if it.Album == "Sampler" {
			t.Error("a reading was offered after a song")
		}
		// The performer's own tracks come before the other performer's.
		if i < 3 && it.Artist != "Gorse Beacon" {
			t.Errorf("position %d is %s's, want the performer's own first", i, it.Artist)
		}
	}
	// And a reading is answered with readings only.
	sampler := PathID("/library/comp/Sampler/01 track.mp3")
	if got := l.Similar(sampler, 10, 0, PathFilter{}); len(got) != 0 {
		t.Errorf("a lone reading found %d similar tracks among the music", len(got))
	}
	// A confined caller is offered only what it may see.
	f := ParsePaths("/library/Tern Signal")
	for _, it := range l.Similar(seed, 10, 0, f) {
		if it.Artist != "Tern Signal" {
			t.Errorf("%s reached a caller confined elsewhere", it.Rel)
		}
	}
}

// Liking one track lifts the tracks that sound like it, each saying which
// one it resembles; disliking sinks them; the verdict's own track is not
// graded against itself; and the other performer is untouched.
func TestAffinityFollowsTheVerdict(t *testing.T) {
	l := libWithSounds(t)
	liked := PathID("/library/Gorse Beacon/Signal Fires/01 track.mp3")
	l.SetLike(liked, 1)
	for _, it := range l.List(Query{Limit: 20}).Items {
		switch {
		case it.ID == liked:
			if it.Affinity != 0 || it.Akin != "" {
				t.Errorf("the liked track graded itself: %+v", it)
			}
		case it.Artist == "Gorse Beacon":
			if it.Affinity < 1 || it.Akin != "01 track.mp3" {
				t.Errorf("%s: affinity %d akin %q, want lifted by the liked track", it.Rel, it.Affinity, it.Akin)
			}
		default:
			if it.Affinity != 0 {
				t.Errorf("%s: affinity %d, want none", it.Rel, it.Affinity)
			}
		}
	}
	l.SetLike(liked, -1)
	for _, it := range l.List(Query{Limit: 20}).Items {
		if it.Artist == "Gorse Beacon" && it.ID != liked && it.Affinity > -1 {
			t.Errorf("%s: affinity %d after the dislike, want sunk", it.Rel, it.Affinity)
		}
	}
}

// The popular order: the verdict, then the resemblance, then the count.
func TestPopularOrderPutsResemblanceBetweenVerdictAndPlays(t *testing.T) {
	l := libWithSounds(t)
	liked := PathID("/library/Gorse Beacon/Signal Fires/01 track.mp3")
	often := PathID("/library/Tern Signal/Harbour Lights/01 track.mp3")
	l.SetPlaysAll(map[string]int{often: 40, PathID("/library/Gorse Beacon/Demos/01 track.mp3"): 1})
	l.SetLike(liked, 1)
	got := l.List(Query{Sort: "popular", Desc: true, Limit: 20}).Items
	if got[0].ID != liked {
		t.Errorf("first = %s, want the liked track", got[0].Rel)
	}
	oftenAt, akinAt := -1, -1
	for i, it := range got {
		if it.ID == often {
			oftenAt = i
		}
		if akinAt < 0 && it.Artist == "Gorse Beacon" && it.ID != liked {
			akinAt = i
		}
	}
	if akinAt < 0 || oftenAt < 0 || akinAt > oftenAt {
		t.Errorf("a track resembling the liked one sits at %d, the one played forty times at %d; want the resemblance first", akinAt, oftenAt)
	}
	if trackPopularity(0, 1, 0) <= trackPopularity(0, 0, 1_000_000) {
		t.Error("a resemblance must outrank a million plays")
	}
	if trackPopularity(1, -2, 0) <= trackPopularity(0, 2, 0) {
		t.Error("the verdict must outrank any resemblance")
	}
}

// A release sounds like the mean of its tracks, a performer like theirs,
// and the readings are neither offered nor counted.
func TestSimilarReleasesAndPerformers(t *testing.T) {
	l := libWithSounds(t)
	var signalFires *Album
	for _, a := range l.Albums() {
		if a.Name == "Signal Fires" {
			signalFires = a
		}
	}
	if signalFires == nil {
		t.Fatal("release not built")
	}
	got := albumNames(l.SimilarAlbums(signalFires.ID, AlbumQuery{Desc: true}))
	if len(got) < 3 || got[0] != "Later Work" && got[0] != "Demos" || got[2] != "Harbour Lights" {
		t.Errorf("similar releases = %v, want the performer's own two first, the other performer's last", got)
	}
	// Turned round, the least alike comes first: the direction is the one
	// thing the viewer can still choose on this listing.
	if up := albumNames(l.SimilarAlbums(signalFires.ID, AlbumQuery{Desc: false})); up[0] != "Harbour Lights" {
		t.Errorf("ascending similar releases = %v, want the other performer's first", up)
	}
	if slices.Contains(got, "Sampler") || slices.Contains(got, "Signal Fires") {
		t.Errorf("similar releases = %v: the reading, or the seed itself, was offered", got)
	}
	// The performer's other releases really are nearer than the other
	// performer's, and each answer says by how much.
	for _, a := range l.SimilarAlbums(signalFires.ID, AlbumQuery{Desc: true}) {
		if a.Similarity == 0 {
			t.Errorf("%s carries no similarity", a.Name)
		}
	}
	artists := l.SimilarArtists("gorse beacon", "", true, PathFilter{})
	if len(artists) != 1 || artists[0].Name != "Tern Signal" {
		t.Errorf("similar performers = %+v, want the one other performer", artists)
	}
	if len(l.SimilarArtists("Nobody", "", true, PathFilter{})) != 0 {
		t.Error("a performer nobody has heard of has similar performers")
	}
}

// A release whose tracks are a reading is an audiobook: marked, and found
// by the word.
func TestAReadingIsAnAudiobook(t *testing.T) {
	l := libWithSounds(t)
	l.notify() // the releases were built before the vectors were seeded
	var found *Album
	for _, a := range l.Albums() {
		if a.Name == "Sampler" {
			found = a
		} else if a.Spoken {
			t.Errorf("%s was taken for an audiobook", a.Name)
		}
	}
	if found == nil || !found.Spoken {
		t.Fatalf("the reading was not marked: %+v", found)
	}
	// It has a shelf of its own: found there by the word, absent from the
	// records however it is searched for, counted apart from them, and its
	// narrator is no performer.
	if got := albumNames(l.SearchAlbums(AlbumQuery{Search: "audiobook", Audiobooks: true})); !slices.Equal(got, []string{"Sampler"}) {
		t.Errorf("the audiobook shelf searched for the word holds %v", got)
	}
	if got := albumNames(l.SearchAlbums(AlbumQuery{Search: "sampler"})); len(got) != 0 {
		t.Errorf("the records hold the audiobook: %v", got)
	}
	if c := l.Counts(); c.Audiobooks != 1 || c.Albums != 4 {
		t.Errorf("counts = %d audiobooks and %d albums, want 1 and 4", c.Audiobooks, c.Albums)
	}
	if c := l.CountsFor(CountQuery{Search: "sampler"}); c.Audiobooks != 1 || c.Albums != 0 || c.Artists != 0 {
		t.Errorf("counts for the audiobook's name = %+v, want one audiobook and nothing else", c)
	}
	if got := l.SearchArtists("", "name", false, PathFilter{}); len(got) != 2 {
		t.Errorf("performers = %d, want the two musicians and no narrator", len(got))
	}
	for _, it := range l.List(Query{Search: "sampler", Limit: 5}).Items {
		if !it.Spoken {
			t.Errorf("%s is not marked as speech", it.Rel)
		}
	}
	l.upsert("/library/x.mp3", KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
	if l.spokenOf(PathID("/library/x.mp3")) {
		t.Error("a track nobody has read is speech")
	}
}

func TestAnalysisOffsets(t *testing.T) {
	if got := analysisOffsets(0); !slices.Equal(got, []float64{30, 90, 150}) {
		t.Errorf("unknown length: %v", got)
	}
	if got := analysisOffsets(20_000); !slices.Equal(got, []float64{0}) {
		t.Errorf("a short track: %v", got)
	}
	if got := analysisOffsets(60_000); !slices.Equal(got, []float64{20}) {
		t.Errorf("a minute: %v", got)
	}
	if got := analysisOffsets(400_000); !slices.Equal(got, []float64{100, 200, 300}) {
		t.Errorf("a long track: %v", got)
	}
}

// A release of many short shouted tracks is not an audiobook: three
// seconds of sound is not judged at all, and the long songs outweigh the
// rest by playing time. And a release whose tag says audiobook is one
// before anything has been read.
func TestShortShoutsAreNotAReading(t *testing.T) {
	l := quietLib("/m")
	add := func(dir, name string, ms int64, genre string) string {
		path := "/m/" + dir + "/" + name
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{artist: "Gorse Beacon", album: dir, genre: genre}, 1000)
		l.mu.Lock()
		l.items[PathID(path)].Duration = ms
		l.mu.Unlock()
		return PathID(path)
	}
	// Sixty four-second tracks that would read as speech if judged, and
	// three long songs that read as music.
	for i := range 60 {
		v := shapedVector(1, true)
		v[55] = 3
		l.SetFeatures(add("Shouts", fmt.Sprintf("%02d short.mp3", i), 4_000, "Grindcore"), 1, 1000, v)
	}
	for i := range 3 {
		l.SetFeatures(add("Shouts", fmt.Sprintf("long %d.mp3", i), 120_000, "Grindcore"), 1, 1000, shapedVector(1, false))
	}
	// A book of long chapters, tagged as one, none of them read yet.
	for i := range 5 {
		add("A Reading", fmt.Sprintf("chapter %d.mp3", i), 1_800_000, "Ljudbok")
	}
	l.notify()
	for _, a := range l.Albums() {
		switch a.Name {
		case "Shouts":
			if a.Spoken {
				t.Error("a release of short shouts was shelved as an audiobook")
			}
		case "A Reading":
			if !a.Spoken {
				t.Error("a release tagged as an audiobook was not shelved as one")
			}
		}
	}
	// And a judged chapter outvotes short tracks by time, however many.
	v := shapedVector(1, true)
	v[55] = 3
	if spoken, judged := spokenVerdict(v); spoken || judged {
		t.Error("three seconds of sound was judged")
	}
}

// A release the analysis has only begun to read gets no verdict from it:
// an EP whose ambient intro reads as speech stayed a record until its songs
// were read, and then by playing time it still was one. And the intro is
// music by its release's word: not marked, and offered music as similar.
func TestAReleaseIsJudgedOnlyOnceMostOfItHasBeenRead(t *testing.T) {
	l := quietLib("/m")
	add := func(name string, ms int64) string {
		path := "/m/An EP/" + name
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{artist: "Gorse Beacon", album: "An EP", genre: "Black Metal"}, 1000)
		l.mu.Lock()
		l.items[PathID(path)].Duration = ms
		l.mu.Unlock()
		return PathID(path)
	}
	intro := add("01 intro.mp3", 87_000)
	songs := []string{add("02 song.mp3", 314_000), add("03 song.mp3", 263_000), add("04 song.mp3", 484_000)}
	// Only the intro read so far, and it reads as speech.
	l.SetFeatures(intro, 1, 1000, shapedVector(1, true))
	l.notify()
	if a := l.Albums()[0]; a.Spoken {
		t.Error("a release with one track read out of four was shelved")
	}
	if c := l.Counts(); c.Audiobooks != 0 {
		t.Errorf("audiobooks = %d before the release was read", c.Audiobooks)
	}
	// The songs read: music, and by far the most of the playing time.
	for _, id := range songs {
		l.SetFeatures(id, 1, 1000, shapedVector(1, false))
	}
	l.notify()
	if a := l.Albums()[0]; a.Spoken {
		t.Error("a record with a speech-like intro was shelved")
	}
	for _, it := range l.List(Query{Limit: 10}).Items {
		if it.Spoken {
			t.Errorf("%s is marked as speech on a record", it.Name)
		}
	}
	if got := l.Similar(intro, 10, 0, PathFilter{}); len(got) != 3 {
		t.Errorf("the intro found %d similar tracks, want its release's three songs", len(got))
	}
}

// A release with unmeasured tracks votes by count: one long chapter read
// against two unmeasured songs is one against two, not a minute against two
// seconds.
func TestAnUnmeasuredReleaseVotesByCount(t *testing.T) {
	l := quietLib("/m")
	add := func(name string, ms int64, spoken bool) {
		path := "/m/Mixed/" + name
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		l.mu.Lock()
		l.items[PathID(path)].Duration = ms
		l.mu.Unlock()
		l.SetFeatures(PathID(path), 1, 1000, shapedVector(1, spoken))
	}
	add("01 reading.mp3", 3_600_000, true)
	add("02 song.mp3", 0, false)
	add("03 song.mp3", 0, false)
	l.notify()
	if a := l.Albums()[0]; a.Spoken {
		t.Error("one measured reading outvoted two unmeasured songs by time")
	}
}

// The bounded selection cuts where it is told, and a kind the caller may
// not see is not among the neighbours.
func TestSimilarIsBoundedAndKept(t *testing.T) {
	l := libWithSounds(t)
	seed := PathID("/library/Gorse Beacon/Signal Fires/01 track.mp3")
	if got := l.Similar(seed, 1, 0, PathFilter{}); len(got) != 1 || got[0].Artist != "Gorse Beacon" {
		t.Errorf("the one nearest = %+v, want the performer's own track", got)
	}
	if got := l.Similar(seed, 10, kindBit(KindVideo), PathFilter{}); len(got) != 0 {
		t.Errorf("a caller shown only video was offered %d tracks", len(got))
	}
}

// A release nothing has read has no sound and is not among the similar,
// and a reading is not a seed for anything.
func TestReleasesWithoutASoundAreAbsent(t *testing.T) {
	l := libWithSounds(t)
	l.upsert("/library/Gorse Beacon/Unread/01 track.mp3", KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
	l.setMeta(PathID("/library/Gorse Beacon/Unread/01 track.mp3"), tagMeta{artist: "Gorse Beacon", album: "Unread"}, 1000)
	l.notify()
	var seed, sampler string
	for _, a := range l.Albums() {
		switch a.Name {
		case "Signal Fires":
			seed = a.ID
		case "Sampler":
			sampler = a.ID
		}
	}
	for _, a := range l.SimilarAlbums(seed, AlbumQuery{Desc: true}) {
		if a.Name == "Unread" {
			t.Error("a release nothing has read was offered as similar")
		}
	}
	if got := l.SimilarAlbums(sampler, AlbumQuery{Desc: true}); len(got) != 0 {
		t.Errorf("a reading was a seed for %d releases", len(got))
	}
}

// The groupings from releases: performers by their lowercase key, the
// unmeasured sentinel, a genre's performer count, and audiobooks out of both.
func TestGroupingsFromReleases(t *testing.T) {
	rel := func(artist, genre string, dur int64, spoken bool) *Album {
		return &Album{ID: PathID(artist + genre + fmt.Sprint(dur)), Name: "R", Artist: artist, Genre: genre,
			Genres: []string{genre}, Tracks: 2, Duration: dur, Spoken: spoken, lower: "r"}
	}
	albums := []*Album{
		rel("Gorse Beacon", "Folk", 1000, false),
		rel("gorse beacon", "Folk", 0, false), // the same performer, shouted
		rel("Tern Signal", "Folk", 1000, false),
		rel("A Narrator", "Folk", 1000, true), // a reading, filed under a genre it is not
	}
	artists := artistsFrom(albums)
	if len(artists) != 2 {
		t.Fatalf("performers = %d, want two (one spelled twice, no narrator)", len(artists))
	}
	for _, ar := range artists {
		if ar.Name == "Gorse Beacon" {
			if ar.Albums != 2 || ar.Duration != 0 {
				t.Errorf("the twice-spelled performer = %d releases, %d ms; want 2 and no length (one release unmeasured)", ar.Albums, ar.Duration)
			}
		}
		if ar.Name == "Tern Signal" && ar.Duration != 1000 {
			t.Errorf("a fully measured performer reports %d ms", ar.Duration)
		}
	}
	genres := genresFrom(albums)
	if len(genres) != 1 || genres[0].Artists != 2 || genres[0].Albums != 3 {
		t.Errorf("genres = %+v, want Folk with two performers and three releases, the reading left out", genres)
	}
}
