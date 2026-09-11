package server

import (
	"strconv"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Every rendition needs a NAME of its own, or a player folds the ones that
// share it into one entry. Three tracks labelled alike used to come out as
// the same name twice: the count was kept under the label as it arrived
// rather than the name as written, so the third was numbered like the second.
func TestMasterPlaylistNamesEveryRendition(t *testing.T) {
	subs := []library.Subtitle{
		{Index: 0, Label: "English", Lang: "en"},
		{Index: 1, Label: "English", Lang: "en"},
		{Index: 2, Label: "English", Lang: "en"},
		{Index: 3, Label: ""},
		{Index: 4, Label: `Say "hello"`},
	}
	body := string(masterPlaylist("sess", library.Item{Duration: 60_000, Size: 1 << 20}, subs, "1", true))
	for _, want := range []string{
		`NAME="English",`, `NAME="English 2",`, `NAME="English 3",`,
		`NAME="Track 4",`, `NAME="Say 'hello'",`,
	} {
		if strings.Count(body, want) != 1 {
			t.Errorf("%s appears %d times in\n%s", want, strings.Count(body, want), body)
		}
	}
	if strings.Count(body, "DEFAULT=YES") != 1 || !strings.Contains(body, `NAME="English 2",LANGUAGE="en",DEFAULT=YES`) {
		t.Errorf("the chosen rendition is not the one marked DEFAULT:\n%s", body)
	}
	for i := range subs {
		if !strings.Contains(body, "URI=\"sess/sub"+string(rune('0'+i))+".m3u8\"") {
			t.Errorf("rendition %d is not addressed under the session:\n%s", i, body)
		}
	}
	if !strings.Contains(body, "sess/media.m3u8") {
		t.Errorf("no media playlist beside the renditions:\n%s", body)
	}
}

// What BANDWIDTH describes is the stream being served, not the file it was
// made from. A 4K release of 29 Mbit/s comes out as a 1920-wide stream of
// six or eight, and declaring the original tells a player on a thin
// connection it cannot afford what it is about to be sent.
func TestMasterBandwidthDescribesTheStream(t *testing.T) {
	// Roughly 29 Mbit/s: a 25 GB film of 115 minutes.
	big := library.Item{Duration: 6_913_376, Size: 24_970_724_493}
	rate := func(body string) int64 {
		for _, line := range strings.Split(body, "\n") {
			if after, ok := strings.CutPrefix(line, "#EXT-X-STREAM-INF:BANDWIDTH="); ok {
				n, _ := strconv.ParseInt(strings.Split(after, ",")[0], 10, 64)
				return n
			}
		}
		return 0
	}
	// The picture re-encoded: what goes out is the conversion's own rate.
	if got := rate(string(masterPlaylist("s", big, nil, "", false))); got != convertBitrateGuess {
		t.Errorf("re-encoded stream declared %d, want the conversion's own %d", got, convertBitrateGuess)
	}
	// The picture copied through: the file's average is the honest figure.
	if got := rate(string(masterPlaylist("s", big, nil, "", true))); got < 25_000_000 {
		t.Errorf("copied stream declared %d, want the file's own average", got)
	}
	// And a file whose length nobody measured falls back rather than
	// declaring nothing, which the specification does not allow.
	if got := rate(string(masterPlaylist("s", library.Item{Size: 1 << 30}, nil, "", true))); got != convertBitrateGuess {
		t.Errorf("unmeasured file declared %d", got)
	}
}
