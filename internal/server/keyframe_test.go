package server

import "testing"

func TestFirstPTS(t *testing.T) {
	// ffprobe's csv output for the packets of a copied stream: the first line
	// is where the stream begins. Later packets can carry earlier timestamps
	// (an open GOP's leading pictures) and must not be mistaken for it.
	cases := []struct {
		name string
		out  string
		at   float64
		want float64
		ok   bool
	}{
		{"first line wins", "600.063000\n600.021000\n600.104000\n", 601.7, 600.063, true},
		{"seek landed accurately", "1230.151000\n", 1234.5, 1230.151, true},
		{"empty output", "\n\n", 601.7, 0, false},
		{"unparsable", "N/A\nN/A\n", 601.7, 0, false},
		// A landing point past the seek would mean content was skipped: the
		// file's clock does not start at zero, so the answer is unusable.
		{"past the seek", "3700.000000\n", 100, 0, false},
		{"negative", "-1.5\n", 100, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := firstPTS(c.out, c.at)
			if ok != c.ok || (ok && got != c.want) {
				t.Fatalf("firstPTS(%q, %v) = %v, %v; want %v, %v",
					c.out, c.at, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestStreamStartWithoutTools(t *testing.T) {
	// No ffmpeg: the caller must be told to assume an accurate seek rather
	// than be handed a made-up offset.
	if got := streamStart(t.Context(), "", "/nonexistent.mkv", 601.7); got != 601.7 {
		t.Fatalf("streamStart without ffmpeg = %v; want the time asked for", got)
	}
	// Seeking to the start needs no probe at all.
	if got := streamStart(t.Context(), "/nonexistent/ffmpeg", "/nonexistent.mkv", 0); got != 0 {
		t.Fatalf("streamStart at zero = %v; want 0", got)
	}
}
