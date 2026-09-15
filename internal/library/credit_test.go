package library

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// A catalogue holding the four shapes a release's performer can take when its
// tracks are handed out: a release nothing tagged under a directory naming a
// performer the library already knows, a release most of whose tracks name
// one performer and one of whose tracks names a guest, a release nothing
// tagged under a directory that names nobody, and a compilation.
//
// The file names are the shape the untagged releases really arrive in —
// performer in capitals, a two-digit track number, a full stop with no space,
// and a bitrate marker before the extension — since that is the row the queue
// was drawing with an empty artist column beside it.
func libForCredit(t *testing.T) *Library {
	t.Helper()
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(path, artist, album string) {
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		if artist != "" || album != "" {
			l.setMeta(PathID(path), tagMeta{artist: artist, album: album}, 1000)
		}
	}
	// Tagged, and so the voice that establishes this performer for the
	// directory rule below.
	add("/library/Kestrel Vane/Signal Fires/01 track.mp3", "Kestrel Vane", "Signal Fires")
	// Nothing tagged at all, under a directory naming that same performer.
	add("/library/Kestrel Vane/Quiet Rooms/KESTREL VANE - 01.At the Harbour Wall_320.mp3", "", "")
	add("/library/Kestrel Vane/Quiet Rooms/KESTREL VANE - 02.Low Tide_320.mp3", "", "")
	// A release with a performer of its own, a guest on one track, and one
	// file the tagging never reached.
	add("/library/Tern Signal/Harbour Lights/01 track.mp3", "Tern Signal", "Harbour Lights")
	add("/library/Tern Signal/Harbour Lights/02 track.mp3", "Tern Signal", "Harbour Lights")
	add("/library/Tern Signal/Harbour Lights/03 guest.mp3", "Gorse Beacon", "Harbour Lights")
	add("/library/Tern Signal/Harbour Lights/04 untagged.mp3", "", "")
	// Nothing tagged, and a directory above that names nobody this library
	// has ever heard of.
	add("/library/odds and ends/Unmarked/01.First_320.mp3", "", "")
	add("/library/odds and ends/Unmarked/02.Second_320.mp3", "", "")
	// Two performers and a file nobody tagged: a compilation.
	add("/library/comp/Winter Sampler/01 one.mp3", "Kestrel Vane", "Winter Sampler")
	add("/library/comp/Winter Sampler/02 two.mp3", "Tern Signal", "Winter Sampler")
	add("/library/comp/Winter Sampler/03 three.mp3", "", "")
	return l
}

func releaseNamed(t *testing.T, l *Library, name string) *Album {
	t.Helper()
	for _, a := range l.Albums() {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no release named %q", name)
	return nil
}

func trackNamed(t *testing.T, tracks []Item, name string) Item {
	t.Helper()
	for _, it := range tracks {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("no track named %q among %d", name, len(tracks))
	return Item{}
}

// Every door a track leaves by names the release it is on, where the file
// itself names nobody — the sheet, the queue, and an ordinary listing, which
// is the one that has no release in hand and so could not have been given the
// answer at the point of handing out.
func TestReleasePerformerFillsInAnUntaggedTrack(t *testing.T) {
	l := libForCredit(t)
	a := releaseNamed(t, l, "Quiet Rooms")
	if a.Artist != "Kestrel Vane" {
		t.Fatalf("release performer = %q, want %q", a.Artist, "Kestrel Vane")
	}
	_, sheet, ok := l.AlbumByID(a.ID)
	if !ok {
		t.Fatalf("release %q did not resolve by id", a.Name)
	}
	queue := l.TracksOf([]*Album{a}, PathFilter{}, 100)
	listed := []Item{}
	for _, it := range l.List(Query{Kind: KindAudio, Limit: 100}).Items {
		if _, ok := trackOf(a, it.ID); ok {
			listed = append(listed, it)
		}
	}
	for _, door := range []struct {
		what   string
		tracks []Item
	}{{"sheet", sheet}, {"queue", queue}, {"listing", listed}} {
		if len(door.tracks) != 2 {
			t.Fatalf("%s: tracks = %d, want 2", door.what, len(door.tracks))
		}
		for _, it := range door.tracks {
			if it.Performer != "Kestrel Vane" {
				t.Errorf("%s: %q names the release %q, want %q", door.what, it.Name, it.Performer, "Kestrel Vane")
			}
			// And the tag itself is untouched, which is what keeps one file
			// to one identity however it was asked for.
			if it.Artist != "" {
				t.Errorf("%s: %q had a performer written into its tag (%q)", door.what, it.Name, it.Artist)
			}
		}
	}
}

// trackOf reports whether an id is one of a release's tracks.
func trackOf(a *Album, id string) (int, bool) {
	for i, tid := range a.TrackIDs {
		if tid == id {
			return i, true
		}
	}
	return 0, false
}

// A track that names its own performer has said something and is not
// corrected — the mirror of artistFromParent, which fires only where nothing
// tagged the release at all.
func TestTaggedTrackKeepsItsOwnPerformer(t *testing.T) {
	l := libForCredit(t)
	a := releaseNamed(t, l, "Harbour Lights")
	if a.Artist != "Tern Signal" {
		t.Fatalf("release performer = %q, want %q", a.Artist, "Tern Signal")
	}
	_, tracks, ok := l.AlbumByID(a.ID)
	if !ok {
		t.Fatalf("release %q did not resolve by id", a.Name)
	}
	guest := trackNamed(t, tracks, "03 guest.mp3")
	if guest.Artist != "Gorse Beacon" {
		t.Errorf("the guest's track names %q, want %q", guest.Artist, "Gorse Beacon")
	}
	if guest.Performer != "" {
		t.Errorf("the guest's track was also given the release's name (%q); its own tag settles it", guest.Performer)
	}
	untagged := trackNamed(t, tracks, "04 untagged.mp3")
	if untagged.Performer != "Tern Signal" {
		t.Errorf("the untagged track names the release %q, want %q", untagged.Performer, "Tern Signal")
	}
}

// The release's name is stamped on the copy that goes out and never on the
// indexed item: the album build votes on the indexed tags, CountsFor filters
// on them and RecordingKey folds on them, so a derived performer written
// there would become evidence for the next build of itself.
//
// And it is never written into Artist, on any copy. That is what keeps one
// file to one identity: RecordingKey is the performer and the title, the
// client builds the same key over the queue to keep radio from offering back
// what is already queued, and a file whose key depended on which endpoint it
// left by would defeat exactly that comparison.
func TestCreditingLeavesTheIndexedItemAlone(t *testing.T) {
	l := libForCredit(t)
	a := releaseNamed(t, l, "Quiet Rooms")
	if _, _, ok := l.AlbumByID(a.ID); !ok {
		t.Fatalf("release %q did not resolve by id", a.Name)
	}
	l.TracksOf([]*Album{a}, PathFilter{}, 100)
	for _, id := range a.TrackIDs {
		l.mu.RLock()
		indexed := l.items[id].Artist
		indexedPerformer := l.items[id].Performer
		l.mu.RUnlock()
		if indexed != "" || indexedPerformer != "" {
			t.Errorf("the indexed item now names %q/%q; it was handed out, not rewritten", indexed, indexedPerformer)
		}
		it, ok := l.Get(id)
		if !ok {
			t.Fatalf("%s did not resolve", id)
		}
		if it.Artist != "" {
			t.Errorf("Get put %q in the tag, want nothing there", it.Artist)
		}
		// Every door agrees, which is the point of answering from the build
		// rather than at the point of handing out.
		if it.Performer != "Kestrel Vane" {
			t.Errorf("Get names the release %q, want %q", it.Performer, "Kestrel Vane")
		}
		if RecordingKey(it.Artist, it.Title) != RecordingKey("", it.Title) {
			t.Error("the recording key moved: one file would have two identities")
		}
	}
}

// A release whose performer the library never worked out says nothing: there
// is nothing to say, and an empty line says it.
func TestReleaseWithNoPerformerCreditsNothing(t *testing.T) {
	l := libForCredit(t)
	a := releaseNamed(t, l, "Unmarked")
	if a.Artist != "" {
		t.Fatalf("release performer = %q, want nothing", a.Artist)
	}
	_, tracks, ok := l.AlbumByID(a.ID)
	if !ok {
		t.Fatalf("release %q did not resolve by id", a.Name)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	for _, it := range tracks {
		if it.Performer != "" {
			t.Errorf("%q names the release %q, want nothing", it.Name, it.Performer)
		}
	}
	// And the release next door, whose performer *is* known, still says so —
	// so this test fails when the rule stops firing rather than passing
	// because nothing fires at all.
	other := releaseNamed(t, l, "Quiet Rooms")
	if _, sheet, _ := l.AlbumByID(other.ID); sheet[0].Performer != "Kestrel Vane" {
		t.Fatalf("the control release names %q; this test proves nothing without it", sheet[0].Performer)
	}
}

// A compilation names nobody. Its marker means "more than one", so writing it
// under a single track states something that is false of every track. The
// comparison is without case, since a release whose own tags spell it that
// way is the same release.
func TestCompilationCreditsNobody(t *testing.T) {
	l := libForCredit(t)
	a := releaseNamed(t, l, "Winter Sampler")
	if a.Artist != variousArtists {
		t.Fatalf("release performer = %q, want %q", a.Artist, variousArtists)
	}
	_, tracks, ok := l.AlbumByID(a.ID)
	if !ok {
		t.Fatalf("release %q did not resolve by id", a.Name)
	}
	if got := trackNamed(t, tracks, "03 three.mp3").Performer; got != "" {
		t.Errorf("the untagged track names %q, want nothing", got)
	}
}

// The same, where the release's own tags spell the marker rather than
// fillAlbum writing it: one performer, whose name is not a name.
func TestLowerCaseVariousArtistsIsStillNobody(t *testing.T) {
	l := New([]string{"/library"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	add := func(path, artist string) {
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		if artist != "" {
			l.setMeta(PathID(path), tagMeta{artist: artist, album: "Sampler Two"}, 1000)
		}
	}
	add("/library/comp/Sampler Two/01 one.mp3", "various artists")
	add("/library/comp/Sampler Two/02 two.mp3", "various artists")
	add("/library/comp/Sampler Two/03 three.mp3", "")
	a := releaseNamed(t, l, "Sampler Two")
	_, tracks, ok := l.AlbumByID(a.ID)
	if !ok {
		t.Fatalf("release %q did not resolve by id", a.Name)
	}
	if got := trackNamed(t, tracks, "03 three.mp3").Performer; got != "" {
		t.Errorf("the untagged track names %q, want nothing: that is not somebody's name", got)
	}
}
