package library

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func subLib(t *testing.T, dir string) *Library {
	t.Helper()
	l := New([]string{dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.Scan(nil)
	return l
}

func write(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSubtitleDiscovery(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Movie.mkv"), "video")
	write(t, filepath.Join(dir, "Movie.srt"), "1\n00:00:01,000 --> 00:00:02,000\nhi\n")
	write(t, filepath.Join(dir, "Movie.en.srt"), "x")
	write(t, filepath.Join(dir, "Movie.swedish.forced.srt"), "x")
	write(t, filepath.Join(dir, "Movie.ass"), "x")
	write(t, filepath.Join(dir, "Other.srt"), "x")      // belongs to another video
	write(t, filepath.Join(dir, "Movie.txt"), "x")      // not a subtitle
	write(t, filepath.Join(dir, "MovieExtra.srt"), "x") // no separator: not a match

	l := subLib(t, dir)

	// Subtitle files are attachments, never library items of their own.
	if got := l.List(Query{}).Total; got != 1 {
		t.Fatalf("indexed %d items, want only the video", got)
	}

	var video Item
	for _, it := range l.List(Query{}).Items {
		video = it
	}
	subs := l.Subtitles(video)
	labels := map[string]string{} // label -> lang
	for _, s := range subs {
		labels[s.Label] = s.Lang
	}
	if len(subs) != 4 {
		t.Fatalf("got %d subtitles %v, want 4", len(subs), labels)
	}
	for label, lang := range map[string]string{
		"Subtitles":      "", // Movie.srt and Movie.ass both land here
		"English":        "en",
		"Swedish Forced": "sv",
	} {
		got, ok := labels[label]
		if !ok {
			t.Errorf("missing subtitle %q, have %v", label, labels)
			continue
		}
		if got != lang {
			t.Errorf("subtitle %q has lang %q, want %q", label, got, lang)
		}
	}

	// Indexes are stable and resolvable back to a path.
	for i, s := range subs {
		if s.Index != i {
			t.Errorf("subtitle %d has index %d", i, s.Index)
		}
		if _, ok := l.SubtitlePath(video, s.Index); !ok {
			t.Errorf("index %d does not resolve", s.Index)
		}
	}
	if _, ok := l.SubtitlePath(video, len(subs)); ok {
		t.Error("out-of-range index should not resolve")
	}
}

func TestSubtitleFollowsFilesystem(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Show.mp4"), "video")
	l := subLib(t, dir)
	video := l.List(Query{}).Items[0]

	if n := len(l.Subtitles(video)); n != 0 {
		t.Fatalf("got %d subtitles before any exist", n)
	}

	// Added later (as the watcher would).
	p := filepath.Join(dir, "Show.en.srt")
	write(t, p, "x")
	l.AddFile(p)
	if n := len(l.Subtitles(video)); n != 1 {
		t.Fatalf("got %d subtitles after adding one, want 1", n)
	}

	// Removed again.
	os.Remove(p)
	l.Remove(p)
	if n := len(l.Subtitles(video)); n != 0 {
		t.Fatalf("got %d subtitles after removal, want 0", n)
	}

	// A rescan also reconciles subtitles that vanished unobserved.
	write(t, p, "x")
	l.Scan(nil)
	if n := len(l.Subtitles(video)); n != 1 {
		t.Fatal("rescan should pick the subtitle up")
	}
	os.Remove(p)
	l.Scan(nil)
	if n := len(l.Subtitles(video)); n != 0 {
		t.Fatal("rescan should drop the missing subtitle")
	}
}

func TestSubtitlesOnlyForVideo(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Track.mp3"), "audio")
	write(t, filepath.Join(dir, "Track.srt"), "x")
	l := subLib(t, dir)
	if subs := l.Subtitles(l.List(Query{}).Items[0]); subs != nil {
		t.Errorf("audio item offered subtitles: %v", subs)
	}
}

func TestDescribeSuffix(t *testing.T) {
	cases := []struct{ in, label, lang string }{
		{"", "Subtitles", ""},
		{"en", "English", "en"},
		{"eng", "English", "en"},
		{"english", "English", "en"},
		{"sv", "Swedish", "sv"},
		{"en.forced", "English Forced", "en"},
		{"forced", "Forced", ""},
		{"en_sdh", "English Sdh", "en"},
	}
	for _, c := range cases {
		label, lang := describeSuffix(c.in)
		if label != c.label || lang != c.lang {
			t.Errorf("describeSuffix(%q) = %q/%q, want %q/%q", c.in, label, lang, c.label, c.lang)
		}
	}
}
