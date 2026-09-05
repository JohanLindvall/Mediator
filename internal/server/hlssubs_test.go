package server

import (
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
	body := string(masterPlaylist("sess", library.Item{Duration: 60_000, Size: 1 << 20}, subs, "1"))
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
