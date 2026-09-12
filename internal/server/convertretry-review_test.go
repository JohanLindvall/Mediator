package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The aspect repair can only be made for a plain file in a codec whose
// declaration the bitstream filter can rewrite, with something to run it
// with; every retry that counts on the repair is gated by the same rule,
// or a file it cannot help would be attempted twice identically.
func TestRepairableIsTheOneRuleForEveryRetry(t *testing.T) {
	plain := library.Item{Path: "/library/film.mkv", VCodec: "h264"}
	if !repairable("ffmpeg", plain) {
		t.Error("an H.264 file with ffmpeg present is not repairable")
	}
	hevc := plain
	hevc.VCodec = "hevc"
	if !repairable("ffmpeg", hevc) {
		t.Error("an HEVC file is not repairable")
	}
	if repairable("", plain) {
		t.Error("repairable with no ffmpeg to repair with")
	}
	for _, codec := range []string{"", "vp9", "mpeg2video", "av1"} {
		other := plain
		other.VCodec = codec
		if repairable("ffmpeg", other) {
			t.Errorf("%q has no metadata filter and was called repairable", codec)
		}
	}
	// An archived member has no path of its own for the copy to open, and
	// is refused too — but whether an item is archived is the library's
	// own unexported fact, so that half of the rule is read in the code
	// rather than pinned here.
}

// The filter list is read off ffmpeg's own output, one name per line after
// the flags column; the header and the blank lines fall out of the rule.
func TestParseFiltersReadsTheNameColumn(t *testing.T) {
	out := "Filters:\n" +
		"  T.. = Timeline support\n" +
		"  .S. = Slice threading\n" +
		" TS. colorspace        V->V       Convert between colorspaces.\n" +
		" .S. tonemap           V->V       Conversion to/from different dynamic ranges.\n" +
		" .SC zscale            V->V       Apply resizing, colorspace and bit depth conversion.\n" +
		" ... tonemap_vaapi     V->V       VAAPI VPP for tone-mapping\n\n"
	set := parseFilters(out)
	for _, want := range []string{"colorspace", "tonemap", "zscale", "tonemap_vaapi"} {
		if !set[want] {
			t.Errorf("%s was not read from the list", want)
		}
	}
	for _, not := range []string{"Filters:", "=", "T..", "Timeline"} {
		if set[not] {
			t.Errorf("%q was taken for a filter", not)
		}
	}
	if haveFilter("", "zscale") {
		t.Error("no ffmpeg at all was said to have a filter")
	}
}

// Before an attempt is made again, the last one's output goes and the key
// file that says what the session is a conversion of stays — ffmpeg will
// not write over a playlist it finds, and stale segments would count
// towards the progress and the budget.
func TestClearSessionKeepsOnlyTheKey(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{hlsKeyFile, "index.m3u8", "seg00000.ts", "seg00001.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clearSession(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != hlsKeyFile {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("left behind: %v, want only the key file", names)
	}
	if !strings.Contains(strings.Join(hlsOutputArgs(dir), " "), " -y ") {
		t.Error("the segmented output does not overwrite, so a second attempt would be refused")
	}
}

var (
	errTest  = errors.New("the first verdict")
	errOther = errors.New("a later one")
)

// The session's gate opens once, whoever gets there first, and an error
// recorded after something is playable is not one: the waiters woken by the
// first segment must not be sent away from a conversion that is playing.
func TestSessionStateIsDecidedOnce(t *testing.T) {
	s := &hlsSession{ready: make(chan struct{})}
	s.finish()
	s.finish() // a second opening must not panic
	s.failIfEmpty(errTest)
	if s.failure() != nil {
		t.Error("an error was recorded after the gate had opened")
	}
	if !s.playable() {
		t.Error("an opened gate is not playable")
	}
	// One that never opened keeps the verdict recorded for it through the
	// opening, and takes no further one afterwards.
	f := &hlsSession{ready: make(chan struct{})}
	f.failIfEmpty(errTest)
	f.finish()
	f.fail(errOther)
	if f.failure() != errTest {
		t.Errorf("failure = %v, want the verdict recorded before the gate opened", f.failure())
	}
	if !f.playable() {
		t.Error("finish did not open the gate for the waiters")
	}
}

// The latest of an item's conversions is the one a readout is about.
func TestNewestOfPicksTheLatestForTheItem(t *testing.T) {
	entries := map[string]int64{"a|1": 3, "a|2": 7, "b|1": 9}
	got, ok := newestOf(entries, "a", func(v int64) int64 { return v })
	if !ok || got != 7 {
		t.Errorf("got %d (%v), want the entry used last", got, ok)
	}
	if _, ok := newestOf(entries, "c", func(v int64) int64 { return v }); ok {
		t.Error("an item with no entries found one")
	}
}
