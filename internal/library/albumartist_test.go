package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Music is filed as performer, then release, then tracks, so an untagged
// release has its performer sitting one level up. Taking that on trust is how
// the artists view fills with rubbish, though: measured over a real library's
// untagged releases, the folder above them was as often a container as it was
// somebody's name. So the name counts only when some other release, tagged
// properly, has already established that performer — the rule can then never
// invent one, and the worst it can do is what happens without it.
func TestArtistFromParent(t *testing.T) {
	// What the tags of the rest of the library have established.
	known := map[string]string{
		"a performer": "A Performer",
		"another one": "Another One",
	}
	for _, c := range []struct {
		why  string
		path string
		want string
	}{
		{"the ordinary shape: performer, release, tracks",
			"/m/music/A Performer/Some Release", "A Performer"},
		// A directory shouts where a tag does not, and what is handed back is
		// the tagged spelling — a second spelling would be a second performer.
		{"a directory shouting is still the same performer",
			"/m/music/A PERFORMER/Some Release", "A Performer"},
		{"somebody this library has never heard of stays unfiled",
			"/m/music/Nobody In Particular/Some Release", ""},
		// The ones that made the corroboration necessary, all real shapes.
		{"a container is not a performer", "/m/downloads/complete/Some Release", ""},
		{"nor is a category", "/m/music/EP, Single, Demo/Some Release", ""},
		{"nor is a qualifier appended to a name",
			"/m/music/A Performer - mp3 CBR 320/Some Release", ""},
		{"and a release with the discs still under it is not one either",
			"/m/music/A Performer - Some Release (2CD) 2018/CD 1", ""},
		{"nothing above it at all", "Some Release", ""},
	} {
		if got := artistFromParent(c.path, known); got != c.want {
			t.Errorf("artistFromParent(%q) = %q; want %q — %s", c.path, got, c.want, c.why)
		}
	}
}

// Only where nothing tagged the release at all. Tracks that disagree are a
// different thing and say something of their own, and a release most of whose
// tracks name a performer has already answered the question.
func TestParentOnlyFillsACompleteBlank(t *testing.T) {
	known := map[string]string{"a performer": "A Performer"}
	track := func(artist string) *Item {
		return &Item{ID: "t" + artist, Kind: KindAudio, Artist: artist,
			Path: "/m/music/A Performer/Some Release/track.mp3"}
	}
	dir := "/m/music/A Performer/Some Release"

	untagged := &Album{Source: "dir"}
	fillAlbum(untagged, dir, []*Item{track(""), track("")}, nil, known)
	if untagged.Artist != "A Performer" {
		t.Errorf("an untagged release was filed under %q", untagged.Artist)
	}

	// One track names somebody, the rest say nothing: that is a release with
	// metadata, however thin, and the directory is not asked.
	thin := &Album{Source: "dir"}
	fillAlbum(thin, dir, []*Item{track("Someone Else"), track(""), track("")}, nil, known)
	if thin.Artist == "A Performer" {
		t.Error("a release that carried a tag was overruled by its directory")
	}

	// And a playlist is not a directory: its tracks may live anywhere, so the
	// folder holding the file says nothing about who made them.
	list := &Album{Source: "m3u"}
	fillAlbum(list, "/m/music/A Performer/a mixtape.m3u", []*Item{track(""), track("")}, nil, known)
	if list.Artist != "" {
		t.Errorf("a playlist was filed under %q by where the file happened to sit", list.Artist)
	}
}

// A disc is sometimes counted in words rather than written in digits, and a
// release split that way came out as two albums named "DISC ONE" and "DISC
// TWO" with nothing to say whose they were.
func TestDiscNumberReadsSpelledOutDiscs(t *testing.T) {
	for _, c := range []struct {
		name string
		want int
	}{
		{"DISC ONE", 1},
		{"Disc Two", 2},
		{"cd three", 3},
		{"disk-four", 4},
		{"CD Ten", 10},
		// Still the shapes it always read.
		{"CD2", 2},
		{"cd 2", 2},
		{"Disc.3", 3},
		{"CD 1-Firstlight", 1},
		// And still not the ones it must never read. A number that is part of
		// a title is a title, and a word that merely begins with the letters
		// of a disc is a word.
		{"CD 1990s Hits", 0},
		{"Discography", 0},
		{"Disco Lantern", 0},
		{"Disconnected", 0},
		{"CD Oneness", 0},
		{"Discretion", 0},
		{"Something Else", 0},
	} {
		if got := discNumber(c.name); got != c.want {
			t.Errorf("discNumber(%q) = %d; want %d", c.name, got, c.want)
		}
	}
}

// The tag has to lose the same marker the directory does. Where the folder
// folds and the tag does not, the discs disagree about the album's name and
// the fold comes out under the directory's name instead of the release's.
func TestAlbumTitleDropsASpelledOutDiscMarker(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Anthology (Disc One)", "Anthology"},
		{"Anthology - Disc Two", "Anthology"},
		{"Anthology (CD3)", "Anthology"},
		// Nothing to take off, and nothing taken.
		{"Anthology", "Anthology"},
		{"Discography", "Discography"},
		{"Disco Lantern", "Disco Lantern"},
		// Taking it off would leave nothing at all, so it is left alone.
		{"Disc One", "Disc One"},
	} {
		if got := albumTitle(c.in); got != c.want {
			t.Errorf("albumTitle(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// The majority test settles a disagreement between tracks. Where there is no
// disagreement there is nothing to settle: a release with one track tagged
// and eleven blank is not an anonymous release, it is that performer's with
// the tagging left half done.
func TestOneUncontradictedArtistIsEnough(t *testing.T) {
	track := func(artist string) *Item {
		return &Item{ID: "t" + artist, Kind: KindAudio, Artist: artist,
			Path: "/m/music/A Folder/Some Release/track.mp3"}
	}
	dir := "/m/music/A Folder/Some Release"

	// One voice in twelve, and nothing against it.
	thin := &Album{Source: "dir"}
	tracks := []*Item{track("A Performer")}
	for range 11 {
		tracks = append(tracks, track(""))
	}
	fillAlbum(thin, dir, tracks, nil, nil)
	if thin.Artist != "A Performer" {
		t.Errorf("one tagged track in twelve gave %q; want the performer named", thin.Artist)
	}

	// Two voices and neither a majority is a disagreement, and that is what
	// "Various Artists" is for — it must not become whichever was commonest.
	mixed := &Album{Source: "dir"}
	fillAlbum(mixed, dir, []*Item{
		track("A Performer"), track("A Performer"), track("Another One"),
		track(""), track(""), track(""), track(""), track(""),
	}, nil, nil)
	if mixed.Artist != "Various Artists" {
		t.Errorf("a release with two performers and no majority gave %q", mixed.Artist)
	}

	// A clear majority still wins outright, whoever else appears.
	most := &Album{Source: "dir"}
	fillAlbum(most, dir, []*Item{
		track("A Performer"), track("A Performer"), track("A Performer"), track("Another One"),
	}, nil, nil)
	if most.Artist != "A Performer" {
		t.Errorf("a release mostly by one performer gave %q", most.Artist)
	}

	// And nothing at all is still nothing at all, where the directory cannot
	// corroborate a name either.
	blank := &Album{Source: "dir"}
	fillAlbum(blank, dir, []*Item{track(""), track("")}, nil, nil)
	if blank.Artist != "" {
		t.Errorf("an untagged release under an unknown folder gave %q", blank.Artist)
	}
}

// A container with no native reader here keeps its shape where only ffprobe
// can reach it. That is a process per file, which is why nothing does it by
// the page — but once, against a record read for the life of the library.
func TestShapeBackfillsWhatTheBoxTreeCannotRead(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	// Matroska: no box tree, so shapeOf answers nothing about it.
	path := filepath.Join(dir, "a film.mkv")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=322x178:rate=25:duration=1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a matroska clip: %v: %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	it := Item{ID: "abc", Name: "a film.mkv", Path: path, Kind: KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli()}

	// What the native reader makes of it: nothing, which is the premise.
	if w, _, _, _ := shapeOf(it, ""); w != 0 {
		t.Fatalf("the box tree read %d out of a Matroska file", w)
	}
	// And what a probe makes of it, which is what the backfill takes.
	p := ProbeMedia(t.Context(), it)
	if p.Width != 322 || p.Height != 178 {
		t.Errorf("probed %dx%d, want 322x178", p.Width, p.Height)
	}
}

// Two facts only a film has, ordered two ways: the biggest picture and the
// heaviest file are different questions, and a release answering one rarely
// answers the other.
func TestPixelsAndBitrateOrderFilms(t *testing.T) {
	// A tall clip from a phone and a wide film with the same area, so that
	// sorting by either edge alone would order them and sorting by the area
	// leaves them tied — which is the point of using the area.
	tall := &Item{Width: 1080, Height: 1920}
	wide := &Item{Width: 1920, Height: 1080}
	if pixelCount(tall) != pixelCount(wide) {
		t.Errorf("a turned picture counted differently: %d against %d",
			pixelCount(tall), pixelCount(wide))
	}
	if pixelCount(&Item{Width: 3840, Height: 2160}) <= pixelCount(wide) {
		t.Error("4K did not count as bigger than 1080p")
	}
	// A file whose shape nobody has read yet counts as nothing, which puts it
	// at the end of the order rather than in the middle under a number nobody
	// measured.
	if pixelCount(&Item{}) != 0 {
		t.Error("an unmeasured picture was given a size")
	}

	// A gigabyte over a thousand seconds is eight megabits a second.
	if got := bitsPerSecond(&Item{Size: 1_000_000_000, Duration: 1_000_000}); got != 8_000_000 {
		t.Errorf("bitsPerSecond = %d; want 8000000", got)
	}
	// The same bytes over twice the time is half the rate — which is the
	// whole reason this is not just a sort by size.
	half := bitsPerSecond(&Item{Size: 1_000_000_000, Duration: 2_000_000})
	if half*2 != bitsPerSecond(&Item{Size: 1_000_000_000, Duration: 1_000_000}) {
		t.Errorf("a film twice as long did not come out at half the rate: %d", half)
	}
	// Nothing to divide by, and nothing claimed.
	if got := bitsPerSecond(&Item{Size: 1_000_000}); got != 0 {
		t.Errorf("bitsPerSecond with no length = %d; want 0", got)
	}
}
