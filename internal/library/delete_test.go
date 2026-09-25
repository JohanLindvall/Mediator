package library

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// A library on disk to delete from. Names are invented; the shapes are a
// scene film release, a film sharing a folder, a clip among photographs, a
// film loose in a root, a two-disc album, a release shipped with its own
// playlist, a mixtape naming other releases' tracks, a show in season
// folders and a film in a rar set.
func deletable(t *testing.T) (*Library, string) {
	t.Helper()
	root := t.TempDir()
	w := func(rel, body string) {
		writeFile(t, filepath.Join(root, rel), body)
	}
	w("Films/Pale.Harrow.2019.1080p-GRP/pale.harrow.2019.mkv", "film film film")
	w("Films/Pale.Harrow.2019.1080p-GRP/pale.harrow.2019.nfo", "about")
	w("Films/Pale.Harrow.2019.1080p-GRP/pale.harrow.2019.en.srt", "1\n00:00:01,000 --> 00:00:02,000\nhi\n")
	w("Films/Pale.Harrow.2019.1080p-GRP/poster.jpg", "jpeg")
	w("Films/Pale.Harrow.2019.1080p-GRP/Sample/pale.harrow.2019-sample.mkv", "sample")
	w("Films/Shared/first.mkv", "one")
	w("Films/Shared/first.nfo", "about one")
	w("Films/Shared/second.mkv", "two")
	w("Pictures/Holiday/IMG_0001.jpg", "p1")
	w("Pictures/Holiday/IMG_0002.jpg", "p2")
	w("Pictures/Holiday/clip.mp4", "clip")
	w("loose.mkv", "loose")
	w("Music/Lee Shore/Saltings/CD1/01 - first.mp3", "a")
	w("Music/Lee Shore/Saltings/CD1/02 - second.mp3", "b")
	w("Music/Lee Shore/Saltings/CD2/01 - third.mp3", "c")
	w("Music/Lee Shore/Saltings/CD2/02 - fourth.mp3", "d")
	w("Music/Lee Shore/Saltings/folder.jpg", "art")
	w("Music/Lee Shore/Saltings/saltings.log", "rip log")
	w("Music/Lee Shore/Windward/01 - out.mp3", "e")
	w("Music/Lee Shore/Windward/02 - back.mp3", "f")
	w("Music/Lee Shore/Windward/00-windward.m3u", "01 - out.mp3\n02 - back.mp3\n")
	w("Music/Lee Shore/Windward/cover.jpg", "art")
	w("Lists/evening.m3u", "../Music/Lee Shore/Windward/01 - out.mp3\n")
	w("TV/Harbour Lights/Season 1/Harbour.Lights.S01E01.mkv", "e1")
	w("TV/Harbour Lights/Season 1/Harbour.Lights.S01E02.mkv", "e2")
	w("TV/Harbour Lights/Season 1/season.nfo", "about")
	w("TV/Harbour Lights/Season 2/Harbour.Lights.S02E01.mkv", "e3")
	w("TV/Harbour Lights/Season 2/Harbour.Lights.S02E02.mkv", "e4")
	release := filepath.Join(root, "Films", "Night.Tide.2020-GRP")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	rartest.WriteSet(t, release, "night.tide.2020", "night.tide.2020.mkv", rartest.Payload(40_000), 2, true)
	w("Films/Night.Tide.2020-GRP/night.tide.2020.nfo", "about")
	w("Films/Night.Tide.2020-GRP/night.tide.2020.sfv", "crc")
	l := quietLib(root)
	l.Scan(nil)
	return l, root
}

func itemAt(t *testing.T, l *Library, path string) Item {
	t.Helper()
	l.mu.RLock()
	defer l.mu.RUnlock()
	for p, it := range l.byPath {
		if p == path || len(p) > len(path) && p[:len(path)] == path && p[len(path)] == 0 {
			return *it
		}
	}
	t.Fatalf("%s is not indexed", path)
	return Item{}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A film in a release folder of its own takes the folder with it: the .nfo,
// its subtitles, its poster and the sample are that release's furniture.
func TestDeletingAFilmTakesItsReleaseFolder(t *testing.T) {
	l, root := deletable(t)
	rel := filepath.Join(root, "Films", "Pale.Harrow.2019.1080p-GRP")
	film := itemAt(t, l, filepath.Join(rel, "pale.harrow.2019.mkv"))
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: film.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Folders, []string{rel}) {
		t.Fatalf("plan: folders %v; want the release folder whole", plan.Folders)
	}
	for _, f := range plan.Files {
		if !f.InFolder {
			t.Errorf("%s was planned on its own, inside a folder that goes whole", f.Path)
		}
	}
	if !slices.Equal(plan.Items, []string{film.ID}) {
		t.Errorf("items %v, want only the film", plan.Items)
	}
	out := l.DeleteNow(plan)
	if exists(rel) || out.Folders != 1 || len(out.Kept) != 0 {
		t.Fatalf("outcome %+v, folder still there: %v", out, exists(rel))
	}
	if !exists(filepath.Join(root, "Films", "Shared", "first.mkv")) {
		t.Fatal("a film in another folder went too")
	}
	if _, ok := l.Get(film.ID); ok {
		t.Error("the film is still in the index")
	}
}

// A film sharing a folder is the file and its own subtitles, and the folder
// and everything else in it stay.
func TestDeletingAFilmInASharedFolderTakesOnlyTheFilm(t *testing.T) {
	l, root := deletable(t)
	dir := filepath.Join(root, "Films", "Shared")
	first := itemAt(t, l, filepath.Join(dir, "first.mkv"))
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Folders) != 0 || len(plan.Files) != 1 || plan.Files[0].Path != filepath.Join(dir, "first.mkv") {
		t.Fatalf("plan: folders %v, files %v", plan.Folders, plan.Files)
	}
	l.DeleteNow(plan)
	if exists(filepath.Join(dir, "first.mkv")) || !exists(filepath.Join(dir, "second.mkv")) || !exists(filepath.Join(dir, "first.nfo")) {
		t.Error("wrong files went")
	}
}

// Photographs are not a clip's furniture: the last clip in a folder of them
// goes alone.
func TestDeletingAClipKeepsThePhotographs(t *testing.T) {
	l, root := deletable(t)
	dir := filepath.Join(root, "Pictures", "Holiday")
	clip := itemAt(t, l, filepath.Join(dir, "clip.mp4"))
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: clip.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Folders) != 0 {
		t.Fatalf("the folder of photographs was planned to go: %v", plan.Folders)
	}
	l.DeleteNow(plan)
	if !exists(filepath.Join(dir, "IMG_0001.jpg")) || exists(filepath.Join(dir, "clip.mp4")) {
		t.Error("wrong files went")
	}
}

// A root is never removed, however empty deleting leaves it.
func TestARootIsNeverRemoved(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "loose.mkv"), "loose")
	l := quietLib(root)
	l.Scan(nil)
	it := itemAt(t, l, filepath.Join(root, "loose.mkv"))
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: it.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Folders) != 0 || len(plan.Files) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	l.DeleteNow(plan)
	if !exists(root) || exists(filepath.Join(root, "loose.mkv")) {
		t.Error("the root went, or the file stayed")
	}
}

// A release over two discs is one release: both disc folders, its art and
// its rip log go, and the performer's other release stays.
func TestDeletingAnAlbumTakesItsFolderAndItsDiscs(t *testing.T) {
	l, root := deletable(t)
	dir := filepath.Join(root, "Music", "Lee Shore", "Saltings")
	var id string
	for _, a := range l.Albums() {
		if a.Name == "Saltings" {
			id = a.ID
		}
	}
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteAlbum, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Folders, []string{dir}) || len(plan.Items) != 4 {
		t.Fatalf("plan: folders %v, items %d", plan.Folders, len(plan.Items))
	}
	for _, f := range plan.Files {
		if !f.InFolder {
			t.Errorf("%s is inside the folder and was planned on its own", f.Path)
		}
	}
	l.DeleteNow(plan)
	if exists(dir) || !exists(filepath.Join(root, "Music", "Lee Shore", "Windward", "01 - out.mp3")) {
		t.Error("wrong things went")
	}
	// A track's id names a track, never the release it is on.
	track := itemAt(t, l, filepath.Join(root, "Music", "Lee Shore", "Windward", "01 - out.mp3"))
	if _, err := l.PlanDelete(DeleteRequest{Kind: DeleteAlbum, ID: track.ID}); err == nil {
		t.Error("a track's id was taken for its release's")
	}
}

// A release shipped with its own playlist is its tracks, the playlist and its
// folder; a mixtape naming other releases' tracks is the list alone.
func TestDeletingAPlaylist(t *testing.T) {
	l, root := deletable(t)
	var own, mix string
	for _, a := range l.Albums() {
		switch {
		case a.Source == "m3u" && a.Name == "00-windward":
			own = a.ID
		case a.Source == "m3u" && a.Name == "evening":
			mix = a.ID
		}
	}
	if own == "" || mix == "" {
		t.Fatalf("fixture: %v", l.Albums())
	}
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteAlbum, ID: mix})
	if err != nil {
		t.Fatal(err)
	}
	// The list alone — and the folder it leaves empty, which is not a
	// release's but holds nothing any more.
	if !slices.Equal(plan.Folders, []string{filepath.Join(root, "Lists")}) || len(plan.Items) != 1 ||
		len(plan.Files) != 1 || filepath.Base(plan.Files[0].Path) != "evening.m3u" {
		t.Fatalf("mixtape plan: folders %v, files %v, items %v", plan.Folders, plan.Files, plan.Items)
	}
	l.DeleteNow(plan)
	if !exists(filepath.Join(root, "Music", "Lee Shore", "Windward", "01 - out.mp3")) {
		t.Fatal("deleting the list deleted a track it named")
	}
	windward := filepath.Join(root, "Music", "Lee Shore", "Windward")
	plan, err = l.PlanDelete(DeleteRequest{Kind: DeleteAlbum, ID: own})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Folders, []string{windward}) || len(plan.Items) != 3 {
		t.Fatalf("release plan: folders %v, items %v", plan.Folders, plan.Items)
	}
}

// A season is its episodes and its folder; the show and its other season
// stay.
func TestDeletingASeason(t *testing.T) {
	l, root := deletable(t)
	show := filepath.Join(root, "TV", "Harbour Lights")
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteSeason, ID: "Harbour Lights", Season: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Folders, []string{filepath.Join(show, "Season 1")}) || len(plan.Items) != 2 {
		t.Fatalf("plan: folders %v, items %v", plan.Folders, plan.Items)
	}
	l.DeleteNow(plan)
	if exists(filepath.Join(show, "Season 1")) || !exists(filepath.Join(show, "Season 2", "Harbour.Lights.S02E01.mkv")) {
		t.Error("wrong things went")
	}
	// And the whole show: its seasons, and the show's folder after them —
	// but not the folder shows are filed in, however empty it is left.
	plan, err = l.PlanDelete(DeleteRequest{Kind: DeleteSeries, ID: "harbour lights"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Folders, []string{show}) {
		t.Fatalf("show plan: folders %v", plan.Folders)
	}
	l.DeleteNow(plan)
	if exists(show) || !exists(filepath.Join(root, "TV")) {
		t.Error("the show stayed, or the folder it was filed in went")
	}
}

// A film inside a rar set goes as the set: every volume, and with them the
// release folder its .nfo and .sfv are furniture of.
func TestDeletingAFilmInsideARarSetTakesTheSet(t *testing.T) {
	l, root := deletable(t)
	rel := filepath.Join(root, "Films", "Night.Tide.2020-GRP")
	film := itemAt(t, l, filepath.Join(rel, "night.tide.2020.part1.rar"))
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: film.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Folders, []string{rel}) {
		t.Fatalf("plan: folders %v, files %v", plan.Folders, plan.Files)
	}
	l.DeleteNow(plan)
	if exists(rel) {
		t.Error("the set's folder is still there")
	}
	if _, ok := l.Get(film.ID); ok {
		t.Error("the member is still in the index")
	}
}

// What goes is what was shown: a file that changed since the plan was made
// is left where it is, and a folder that gained something keeps it and
// itself.
func TestOnlyWhatWasShownGoes(t *testing.T) {
	l, root := deletable(t)
	dir := filepath.Join(root, "Films", "Shared")
	first := itemAt(t, l, filepath.Join(dir, "first.mkv"))
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Rewritten after it was shown.
	later := time.Now().Add(time.Minute)
	writeFile(t, filepath.Join(dir, "first.mkv"), "a different film entirely")
	_ = os.Chtimes(filepath.Join(dir, "first.mkv"), later, later)
	out := l.DeleteNow(plan)
	if !exists(filepath.Join(dir, "first.mkv")) || out.Files != 0 || len(out.Kept) != 1 {
		t.Errorf("a changed file went: %+v", out)
	}

	rel := filepath.Join(root, "Films", "Pale.Harrow.2019.1080p-GRP")
	film := itemAt(t, l, filepath.Join(rel, "pale.harrow.2019.mkv"))
	plan, err = l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: film.ID})
	if err != nil || len(plan.Folders) != 1 {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	// Something arrives in the folder after the plan was shown.
	writeFile(t, filepath.Join(rel, "extras.pdf"), "a document")
	out = l.DeleteNow(plan)
	if !exists(filepath.Join(rel, "extras.pdf")) || out.Folders != 0 {
		t.Errorf("a folder that gained something went: %+v", out)
	}
}

// A link inside a folder could point anywhere, and keeps the folder.
func TestALinkKeepsItsFolder(t *testing.T) {
	l, root := deletable(t)
	rel := filepath.Join(root, "Films", "Pale.Harrow.2019.1080p-GRP")
	if err := os.Symlink(filepath.Join(root, "Films", "Shared"), filepath.Join(rel, "elsewhere")); err != nil {
		t.Skip(err)
	}
	film := itemAt(t, l, filepath.Join(rel, "pale.harrow.2019.mkv"))
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: film.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Folders) != 0 {
		t.Fatalf("a folder with a link in it was planned to go: %v", plan.Folders)
	}
}

// Beside "Film.mkv" and "Film.Part2.mkv", the subtitles named for the second
// answer to the first's name too; each video takes only its own.
func TestASubtitleGoesWithTheVideoItNamesMost(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Films", "Two Parts")
	for name, body := range map[string]string{
		"Night.Tide.mkv":          "one",
		"Night.Tide.en.srt":       "1\n00:00:01,000 --> 00:00:02,000\none\n",
		"Night.Tide.Part2.mkv":    "two",
		"Night.Tide.Part2.en.srt": "1\n00:00:01,000 --> 00:00:02,000\ntwo\n",
	} {
		writeFile(t, filepath.Join(dir, name), body)
	}
	l := quietLib(root)
	l.Scan(nil)
	files := func(video string) []string {
		t.Helper()
		plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteItem, ID: itemAt(t, l, filepath.Join(dir, video)).ID})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range plan.Files {
			out = append(out, filepath.Base(f.Path))
		}
		return out
	}
	if got, want := files("Night.Tide.mkv"), []string{"Night.Tide.en.srt", "Night.Tide.mkv"}; !slices.Equal(got, want) {
		t.Errorf("the first part takes %v, want %v", got, want)
	}
	if got, want := files("Night.Tide.Part2.mkv"), []string{"Night.Tide.Part2.en.srt", "Night.Tide.Part2.mkv"}; !slices.Equal(got, want) {
		t.Errorf("the second part takes %v, want %v", got, want)
	}
}
