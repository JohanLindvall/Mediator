package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
)

func linkServer(t *testing.T, store KeyStore) http.Handler {
	t.Helper()
	log := testLog()
	lib := library.New(nil, log)
	srv := New(lib, nil, NewThumbnailer(nil, nil, log), NewRemuxer("", NewScratch("", 0), log),
		NewHLS("", lib, NewScratch("", 0), log), store, os.DirFS(t.TempDir()), log)
	return srv.Handler()
}

// mintLink asks for a shortlink and returns the code.
func mintLink(t *testing.T, h http.Handler, target string) LinkResponse {
	return mintLinkOn(t, h, "media.example.com", target)
}

// mintLinkOn mints one as it would arrive under a particular hostname.
func mintLinkOn(t *testing.T, h http.Handler, host, target string) LinkResponse {
	t.Helper()
	body := strings.NewReader(`{"target":` + quote(target) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/api/links", body)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("minting %q answered %d: %s", target, rec.Code, rec.Body)
	}
	var out LinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("minting %q answered %q: %v", target, rec.Body, err)
	}
	return out
}

// followOn asks for a shortlink as a visitor arriving at one hostname would.
func followOn(h http.Handler, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The whole of what a shortlink promises: it comes back, and it comes back
// pointing where it was made to point.
func TestShortlinkRoundTrip(t *testing.T) {
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := linkServer(t, store)

	// One of each thing a link can name, in the shape the app writes them.
	for _, target := range []string{
		"m=artists&ar=A+Performer",     // a performer
		"m=genres&g=Some+Genre",        // a genre
		"m=series&tv=A+Programme&se=2", // one season of a programme
		"q=a+search",                   // a search
		"m=video&i=0123456789abcdef",   // one film
		"m=image&i=fedcba9876543210",   // one photograph
		"m=audio&i=00112233445566aa",   // one track
	} {
		link := mintLink(t, h, target)
		if len(link.Code) != linkCodeLen {
			t.Fatalf("code %q is not %d characters", link.Code, linkCodeLen)
		}
		if link.Path != "/s/"+link.Code {
			t.Fatalf("path %q does not name code %q", link.Path, link.Code)
		}
		rec := followOn(h, "media.example.com", link.Path)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s answered %d; want a redirect", link.Path, rec.Code)
		}
		// The state is in the fragment, which is why this is a redirect at
		// all: a server is never sent one and cannot render the view itself.
		if got, want := rec.Header().Get("Location"), "/#"+target; got != want {
			t.Fatalf("%s went to %q; want %q", link.Path, got, want)
		}
	}
}

// Clicking the button twice means "give me that link", not "give me another
// name for the same place" — otherwise the database fills with synonyms and
// the same view is shared under a different link every time.
func TestShortlinkIsStableForOneTarget(t *testing.T) {
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := linkServer(t, store)

	first := mintLink(t, h, "m=albums&ar=A+Performer")
	again := mintLink(t, h, "m=albums&ar=A+Performer")
	if first.Code != again.Code {
		t.Fatalf("the same view was given two links: %q then %q", first.Code, again.Code)
	}
	other := mintLink(t, h, "m=albums&ar=Another+Performer")
	if other.Code == first.Code {
		t.Fatalf("two different views share the link %q", other.Code)
	}
}

// A link outlives the process that minted it, which is the point of keeping
// it in the database rather than in the run.
func TestShortlinkSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	store, err := blob.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	link := mintLink(t, linkServer(t, store), "m=genres&g=Some+Genre")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := blob.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rec := followOn(linkServer(t, reopened), "media.example.com", link.Path)
	if got := rec.Header().Get("Location"); got != "/#m=genres&g=Some+Genre" {
		t.Fatalf("after a restart the link went to %q", got)
	}
}

// With -db off there is nowhere to write them down, so they last the run —
// the same promise playback positions and the signing key make in that mode.
func TestShortlinkWithoutADatabase(t *testing.T) {
	h := linkServer(t, nil)
	link := mintLink(t, h, "q=a+search")
	rec := followOn(h, "media.example.com", link.Path)
	if got := rec.Header().Get("Location"); got != "/#q=a+search" {
		t.Fatalf("a link made without a database went to %q", got)
	}
}

// A code nobody minted — mistyped, or outliving the database it was kept in —
// lands in the library rather than on an error page.
func TestUnknownShortlinkOpensTheLibrary(t *testing.T) {
	rec := followOn(linkServer(t, nil), "media.example.com", "/s/nosuch7")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("an unknown code answered %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLinkTargetOK(t *testing.T) {
	for _, c := range []struct {
		why    string
		target string
		want   bool
	}{
		{"what the app actually writes", "m=albums&ar=A+Performer&q=word", true},
		{"an item on top of a view", "m=video&i=0123456789abcdef", true},
		{"nowhere is not somewhere", "", false},
		{"a second fragment would be a different address", "m=all#m=video", false},
		{"a newline could not have come from a fragment", "m=all\nm=video", false},
		{"nor could a raw space", "m=all q=x", false},
		{"and this is a name, not somewhere to put things",
			strings.Repeat("x", linkMaxTarget+1), false},
	} {
		if got := linkTargetOK(c.target); got != c.want {
			t.Errorf("linkTargetOK(%q) = %v; want %v — %s", c.target, got, c.want, c.why)
		}
	}
}

// The alphabet leaves out the characters that are read wrongly when a link is
// written down or spoken, which is most of what a short code is for.
func TestLinkCodesAreReadable(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, err := newLinkCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != linkCodeLen {
			t.Fatalf("code %q is not %d characters", code, linkCodeLen)
		}
		if strings.ContainsAny(code, "ilo01") {
			t.Fatalf("code %q holds a character that is read wrongly", code)
		}
		seen[code] = true
	}
	if len(seen) < 190 {
		t.Fatalf("only %d distinct codes in 200; they are not random enough", len(seen))
	}
}

// One server answers under several names — a face of music, a face of films,
// the whole library — and a link belongs to the name it was made under. Until
// it did, the hostname in a shortlink was decoration: a code sent to somebody
// for one face opened another the moment they changed the name in front of it.
func TestShortlinkDoesNotCrossHostnames(t *testing.T) {
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := linkServer(t, store)

	link := mintLinkOn(t, h, "music.example.com", "m=artists&ar=A+Performer")
	if got := followOn(h, "music.example.com", link.Path).Header().Get("Location"); got != "/#m=artists&ar=A+Performer" {
		t.Fatalf("the link did not work where it was made: %q", got)
	}
	// Offered to another name it is a code nobody minted, which from there is
	// exactly what it is: the library, not an error page.
	other := followOn(h, "videos.example.com", link.Path)
	if other.Code != http.StatusFound || other.Header().Get("Location") != "/" {
		t.Fatalf("a link made on one host answered %d to %q on another",
			other.Code, other.Header().Get("Location"))
	}
}

// The same view on two faces is two links, so that neither can be reached
// through the other's name.
func TestShortlinkIsMintedPerHostname(t *testing.T) {
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := linkServer(t, store)

	target := "m=albums&g=Some+Genre"
	music := mintLinkOn(t, h, "music.example.com", target)
	videos := mintLinkOn(t, h, "videos.example.com", target)
	if music.Code == videos.Code {
		t.Fatalf("both faces were given the same code %q", music.Code)
	}
	// And each is still stable on its own host, which is what the reverse
	// index is for.
	if again := mintLinkOn(t, h, "music.example.com", target); again.Code != music.Code {
		t.Fatalf("the same view on one host gave two codes: %q then %q", music.Code, again.Code)
	}
}

// A reverse proxy that rewrites Host is the case the scoping has to survive:
// nginx sends the upstream address by default, so without the original name
// every face of a library looks like one host and links cross again.
func TestShortlinkFollowsTheForwardedHost(t *testing.T) {
	h := linkServer(t, nil)
	body := strings.NewReader(`{"target":"q=a+search"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/links", body)
	req.Host = "127.0.0.1:8080" // what the proxy passes if it is not told otherwise
	req.Header.Set("X-Forwarded-Host", "Music.Example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var link LinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}

	// A hostname is not case-sensitive, and a link must not depend on how
	// somebody happened to type it.
	if got := followOn(h, "music.example.com", link.Path).Header().Get("Location"); got != "/#q=a+search" {
		t.Fatalf("a link minted behind a proxy went to %q", got)
	}
	if got := followOn(h, "127.0.0.1:8080", link.Path).Header().Get("Location"); got != "/" {
		t.Fatalf("the link answered for the upstream address as well: %q", got)
	}
}
