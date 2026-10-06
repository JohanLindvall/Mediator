// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/dlna"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// Go's built-in mime table calls a WebM file `audio/webm` — which is what one
// holding only sound is, and not what a video downloaded from a video site is.
// A television decides from that name alone, so it declined a film for being
// a soundtrack.
func TestVideoContainersAreNamedAsVideo(t *testing.T) {
	for _, ext := range []string{
		".mp4", ".mkv", ".webm", ".mov", ".avi", ".m4v", ".mpg", ".mpeg",
		".wmv", ".flv", ".3gp", ".vob", ".ts", ".mts", ".m2ts", ".divx", ".f4v", ".ogv",
	} {
		it := library.Item{Name: "a clip" + ext, Kind: library.KindVideo}
		if got := mimeFor(it); !strings.HasPrefix(got, "video/") {
			t.Errorf("%s is offered as %q; a set decides from that name alone", ext, got)
		}
	}
}

// A container has more than one name in circulation, and a set knows the ones
// its makers chose. Refusing a file it can demux perfectly well, over what it
// was called, is the one failure worth taking a second look at before giving
// up — measured against a real set, which lists video/x-matroska and neither
// spelling of WebM.
func TestCastFindsAnotherNameForTheSameBytes(t *testing.T) {
	takes := func(types ...string) func(string) bool {
		return func(t string) bool {
			for _, ok := range types {
				if ok == t {
					return true
				}
			}
			return false
		}
	}
	for _, c := range []struct {
		why    string
		typ    string
		accept []string
		want   string
	}{
		{"a WebM is a Matroska file, and this set says so in the other name",
			"video/webm", []string{"video/x-matroska", "video/mp4"}, "video/x-matroska"},
		{"one container, another spelling",
			"video/x-msvideo", []string{"video/avi"}, "video/avi"},
		{"and the third spelling of it",
			"video/x-msvideo", []string{"video/msvideo"}, "video/msvideo"},
		{"a transport stream is an MPEG stream",
			"video/mp2t", []string{"video/mpeg"}, "video/mpeg"},
		{"a set that lists none of them leaves nothing to try",
			"video/webm", []string{"video/mp4"}, ""},
		// A Matroska file is not necessarily a WebM one, so that alias runs
		// one way: naming an arbitrary MKV as WebM would promise a set VP8 or
		// VP9 and hand it anything at all.
		{"Matroska is never offered as WebM",
			"video/x-matroska", []string{"video/webm"}, ""},
	} {
		got, ok := castAlias(c.typ, takes(c.accept...))
		if !ok {
			got = ""
		}
		if got != c.want {
			t.Errorf("%s: found %q; want %q", c.why, got, c.want)
		}
	}
}

// A video that ships automatic dubs arrives as Matroska carrying one Opus
// track per language, and the only way to tell a television which language is
// wanted is to hand it a file holding just that one. The copy keeps the
// container: an MP4 of VP9 and Opus is a container neither the set nor the
// codecs asked for, where a copy into its own kind is lossless.
func TestCastChoosesTheSoundtrackByContainerCopy(t *testing.T) {
	dubbed := library.Item{
		ID: "abc", Name: "a talk.webm", Kind: library.KindVideo,
		VCodec: "vp9", ACodec: "opus",
		Tracks: []library.AudioTrack{{Index: 0, Lang: "ara"}, {Index: 1, Lang: "eng"}},
	}
	if kind, ok := castTrackKind(dubbed, "1"); !ok || kind != remuxTrack {
		t.Fatalf("a dubbed WebM asked for kind %q (%v); want the container copy", kind, ok)
	}
	if got := remuxMime(dubbed, remuxTrack); got != "video/webm" {
		t.Errorf("the copy is offered as %q; it is still a WebM", got)
	}
	// Nothing to choose between, so nothing to copy.
	one := dubbed
	one.Tracks = one.Tracks[:1]
	if _, ok := castTrackKind(one, "0"); ok {
		t.Error("a file with one soundtrack was copied to choose it")
	}
	// And a viewer who has chosen nothing is handed the file as it is.
	if _, ok := castTrackKind(dubbed, ""); ok {
		t.Error("a copy was made for a choice nobody expressed")
	}
	// An MP4-shaped release still takes the MP4 rewrap, which is what it has
	// always done and what its streams belong in.
	mp4ish := library.Item{
		ID: "def", Name: "a film.mkv", Kind: library.KindVideo,
		VCodec: "h264", ACodec: "aac",
		Tracks: []library.AudioTrack{{Index: 0}, {Index: 1}},
	}
	// Matroska is copyable in its own right, so it takes that path first —
	// lossless either way, and it keeps a container this set already listed.
	if kind, _ := castTrackKind(mp4ish, "1"); kind != remuxTrack {
		t.Errorf("an MKV asked for kind %q; want the container copy", kind)
	}
	avi := library.Item{
		ID: "ghi", Name: "a film.avi", Kind: library.KindVideo,
		VCodec: "h264", ACodec: "aac",
		Tracks: []library.AudioTrack{{Index: 0}, {Index: 1}},
	}
	if kind, ok := castTrackKind(avi, "1"); !ok || kind != remuxCopy {
		t.Errorf("an AVI asked for kind %q (%v); want the MP4 rewrap", kind, ok)
	}
}

// The two copies of one film are two files, and a third kind is a third file:
// serving one under another's name hands a viewer the wrong soundtrack with
// nothing on screen to say why.
func TestRemuxTrackIsItsOwnFile(t *testing.T) {
	it := library.Item{ID: "abc", ModTime: 17, Size: 42, Name: "a talk.webm"}
	name := remuxName(it, 1, remuxTrack)
	if !strings.HasSuffix(name, ".webm") {
		t.Errorf("a soundtrack copy of a WebM is called %q; it is still a WebM", name)
	}
	key, ok := remuxKeyFromName(name)
	if !ok {
		t.Fatalf("remuxKeyFromName(%q) did not parse", name)
	}
	if got := key[strings.LastIndex(key, "|")+1:]; got != string(remuxTrack) {
		t.Errorf("read back kind %q; want %q", got, remuxTrack)
	}
	for _, other := range []remuxKind{remuxCopy, remuxSound} {
		if remuxName(it, 1, other) == name {
			t.Errorf("kind %q is written to the same file as %q", other, remuxTrack)
		}
	}
	// A run interrupted mid-copy is still not ours to adopt.
	if _, ok := remuxKeyFromName(name + ".part"); ok {
		t.Error("a part-written copy parsed as a finished one")
	}
}

// A television's fetch is marked as one, because the endpoint refuses a copy
// for a reason that is a browser's alone: a picture that reorders further
// than it declares plays correctly only where something re-encodes it, which
// a browser needs and a set does not. Unmarked, that refusal reached a
// television as "716 Resource not found" with the copy sitting ready on disk.
func TestARewrapForASetSaysSo(t *testing.T) {
	for _, c := range []struct {
		kind remuxKind
		want string
	}{
		{remuxCopy, "&tv=1"},
		{remuxSound, "&tv=1"},
		{remuxTrack, "&tv=1&mode=track"},
	} {
		if got := remuxQuery(c.kind); got != c.want {
			t.Errorf("kind %q asks %q, want %q", c.kind, got, c.want)
		}
	}
}

// A soundtrack no television decodes is converted before the set is given
// it, because that failure is silent: the film plays, there is no error, and
// a set has no menu to put it right. The picture is the other way round — it
// fails visibly — which is why only the sound is decided here.
func TestASoundtrackNoSetDecodesIsConvertedFirst(t *testing.T) {
	takesAnything := func(string) bool { return true }
	takesNoDTS := func(mime string) bool { return !strings.Contains(mime, "dts") }

	cinema := library.Item{Name: "a film.mkv", Kind: library.KindVideo, VCodec: "h264", ACodec: "dts"}
	kind, ok := castSoundKind(cinema, takesNoDTS)
	if !ok || kind != remuxSound {
		t.Errorf("a DTS film asked for %q (%v); want the sound copy", kind, ok)
	}
	// A set that says it decodes DTS is taken at its word and handed the file.
	if _, ok := castSoundKind(cinema, takesAnything); ok {
		t.Error("a set that lists DTS was made to wait for a copy anyway")
	}
	// Everything a television does decode is left alone.
	for _, codec := range []string{"aac", "ac3", "eac3", "mp3", "opus", "flac", ""} {
		it := cinema
		it.ACodec = codec
		if _, ok := castSoundKind(it, takesNoDTS); ok {
			t.Errorf("a %q soundtrack was converted for no reason", codec)
		}
	}
	// And a film whose picture cannot be copied into an MP4 is left alone
	// too: the copy would not be a copy.
	odd := library.Item{Name: "a film.mkv", Kind: library.KindVideo, VCodec: "mpeg2video", ACodec: "dts"}
	if _, ok := castSoundKind(odd, takesNoDTS); ok {
		t.Error("a picture no MP4 can hold was sent through the sound copy")
	}
}

// A native MP4 is not rewrapped for its container — the browser opens it —
// but it is for its index, where that sits behind the data: the copy asks
// for faststart, which is exactly the thing that moves it to the front.
func TestAnIndexAtTheBackIsWorthTheCopy(t *testing.T) {
	it := library.Item{Name: "a clip.mp4", Kind: library.KindVideo, VCodec: "h264", ACodec: "aac", Path: "/nowhere/a clip.mp4"}
	if remuxable(it) {
		t.Error("an ordinary MP4 was offered a copy that would hand back the same file")
	}
	it.MoovLate = true
	if !remuxable(it) {
		t.Error("an MP4 whose index sits behind its data was refused the copy that moves it")
	}
}

// A file the probe found is not media is not handed to a set, for play or
// queued next. Measured, a television given one fetched its first megabytes,
// sat for minutes in a state whose one action is Stop, hung the Play that
// followed and refused the next cast until it was stopped — and the player
// had rolled on into that file by itself at the end of the one before.
func TestADamagedFileIsNotHandedToASet(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("the verdict is ffprobe's")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(path, []byte("not really a film"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, srv, _ := serverUnderTest(t, dir)
	// A set offering no services: anything said to it fails, so a 422 is
	// an answer given before anything was.
	set := &dlna.Renderer{ID: "tv-1", Name: "Set One"}
	srv.cast.discover = func(context.Context, time.Duration) []*dlna.Renderer { return []*dlna.Renderer{set} }

	for _, route := range []string{"play", "next"} {
		res, err := http.Post(ts.URL+"/api/renderers/tv-1/"+route+"/"+library.PathID(path), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusUnprocessableEntity || strings.TrimSpace(string(body)) != damagedFile {
			t.Errorf("%s: %d %q, want 422 %q", route, res.StatusCode, body, damagedFile)
		}
	}
}

// fakeSet is a television on a port of its own: a device description, and
// SOAP answered by refuse (a UPnP code, or 0 to answer normally). It records
// the actions it was asked, in order, and the URI it was last handed, which
// its status reports back with state.
type fakeSet struct {
	mu      sync.Mutex
	actions []string
	refuse  func(action string, asked []string) int
	// silent, where it says so, drops the connection instead of answering —
	// a set that never answered, without a test waiting out the budget.
	silent func(action string) bool
	// before sees each action and its body before the set answers.
	before func(action, body string)
	state  string // what GetTransportInfo reports
	uri    string
}

func (f *fakeSet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		fmt.Fprint(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>`+
			`<friendlyName>Sitting Room</friendlyName><UDN>uuid:sitting-room</UDN><serviceList>`+
			`<service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType><controlURL>/avt</controlURL></service>`+
			`</serviceList></device></root>`)
		return
	}
	_, action, _ := strings.Cut(strings.Trim(r.Header.Get("SOAPAction"), `"`), "#")
	raw, _ := io.ReadAll(r.Body)
	body := html.UnescapeString(string(raw))
	f.mu.Lock()
	f.actions = append(f.actions, action)
	code := 0
	if f.refuse != nil {
		code = f.refuse(action, f.actions)
	}
	if action == "SetAVTransportURI" && code == 0 {
		_, after, _ := strings.Cut(body, "<CurrentURI>")
		f.uri, _, _ = strings.Cut(after, "</CurrentURI>")
	}
	silent := f.silent != nil && f.silent(action)
	before, state, uri := f.before, f.state, f.uri
	f.mu.Unlock()
	if before != nil {
		before(action, body)
	}
	if silent {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	out := ""
	switch action {
	case "GetTransportInfo":
		out = "<CurrentTransportState>" + state + "</CurrentTransportState>"
	case "GetPositionInfo":
		out = "<TrackURI>" + html.EscapeString(uri) + "</TrackURI>"
	}
	if code != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>`+
			`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">`+
			`<errorCode>%d</errorCode><errorDescription>Transition not available</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code)
		return
	}
	fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`+
		`<u:%sResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">%s</u:%sResponse></s:Body></s:Envelope>`, action, out, action)
}

func (f *fakeSet) asked() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.actions, ",")
}

// castTo stands a fake set up, points the server at it, and plays one clip on
// it, answering the status and body.
func castTo(t *testing.T, set *fakeSet) (int, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	writeMKV(t, path, 2)
	ts, _, _, d := castSetUp(t, set, dir)
	return castPlay(t, ts, d, library.PathID(path), "")
}

// castSetUp stands a fake set up and a server over dir pointed at it.
func castSetUp(t *testing.T, set *fakeSet, dir string) (*httptest.Server, *Server, *library.Library, *dlna.Renderer) {
	t.Helper()
	ts, srv, lib := serverUnderTest(t, dir)
	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())
	srv.SetLocalPort(port)
	fake := httptest.NewServer(set)
	t.Cleanup(fake.Close)
	d, err := dlna.Describe(context.Background(), fake.URL+"/desc.xml")
	if err != nil || d == nil {
		t.Fatalf("describing the fake set: %v", err)
	}
	srv.cast.discover = func(context.Context, time.Duration) []*dlna.Renderer { return []*dlna.Renderer{d} }
	return ts, srv, lib, d
}

// castPlay asks the server to play one item on the set, answering the status
// and body.
func castPlay(t *testing.T, ts *httptest.Server, d *dlna.Renderer, id, query string) (int, string) {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/renderers/"+d.ID+"/play/"+id+query, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(body))
}

// The measured case end to end: a set that will take no new file from where
// it is until it is stopped is stopped, handed the file again and played —
// the cast that failed on the first press and worked on the second, working
// on the first.
func TestABusySetIsStoppedAndThenPlays(t *testing.T) {
	set := &fakeSet{refuse: func(action string, asked []string) int {
		if action == "SetAVTransportURI" && !slices.Contains(asked, "Stop") {
			return 701
		}
		return 0
	}}
	code, body := castTo(t, set)
	if code != http.StatusOK {
		t.Fatalf("the cast answered %d %q", code, body)
	}
	if got, want := set.asked(), "SetAVTransportURI,Stop,SetAVTransportURI,Play"; got != want {
		t.Errorf("the set was asked %s, want %s", got, want)
	}
}

// A refusal is an answer. One that outlasts the stop is reported at once and
// in words that say what to do — never asked about for six seconds as though
// the set had merely gone quiet, which is what silence gets.
func TestARefusalIsNotWaitedOnAsSilence(t *testing.T) {
	set := &fakeSet{refuse: func(action string, _ []string) int {
		if action == "SetAVTransportURI" {
			return 701
		}
		return 0
	}}
	code, body := castTo(t, set)
	if code != http.StatusBadGateway || body != "Sitting Room is busy and would not take the file" {
		t.Errorf("the cast answered %d %q", code, body)
	}
	if asked := set.asked(); strings.Contains(asked, "GetTransportInfo") || strings.Contains(asked, "Play") {
		t.Errorf("a set that refused was asked %s: polled as if silent, or played regardless", asked)
	}
}

// A set holds Play until the picture is up, and one that never answers it
// may have started all the same: it is asked, and a set playing what it was
// handed is a cast that worked — where reporting a failure is what made the
// page stop a film that was starting.
func TestASilentPlayIsAskedAbout(t *testing.T) {
	set := &fakeSet{silent: func(action string) bool { return action == "Play" }, state: "PLAYING"}
	code, body := castTo(t, set)
	if code != http.StatusOK {
		t.Fatalf("the cast answered %d %q", code, body)
	}
	if got := set.asked(); !strings.HasPrefix(got, "SetAVTransportURI,Play,GetTransportInfo,GetPositionInfo") {
		t.Errorf("the set was asked %s; want its silence over Play asked about", got)
	}
}

// A refusal of Play is an answer, and is not asked about as silence is.
func TestARefusedPlayIsNotAskedAbout(t *testing.T) {
	set := &fakeSet{refuse: func(action string, _ []string) int {
		if action == "Play" {
			return 701
		}
		return 0
	}, state: "PLAYING"}
	code, body := castTo(t, set)
	if code != http.StatusBadGateway || body != "Sitting Room would not start playing it" {
		t.Errorf("the cast answered %d %q", code, body)
	}
	if asked := set.asked(); strings.Contains(asked, "GetTransportInfo") {
		t.Errorf("a set that refused Play was asked %s, as if it had gone quiet", asked)
	}
}

// A set fetches the subtitle when it opens the film and holds Play until it
// has it, and reading one out of a large film is a read of the whole film. So
// the subtitle is read before the set is handed anything: when the set is
// given the film, the subtitle it names is already there to be served.
func TestACastReadsItsSubtitleBeforeTheSetIsHandedTheFilm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "An Episode.mkv")
	writeSubbedMKV(t, path)
	set := &fakeSet{}
	ts, srv, lib, d := castSetUp(t, set, dir)
	id := library.PathID(path)
	lib.EnsureCodecs(t.Context(), id)
	it, _ := lib.Get(id)
	var ready, named bool
	set.mu.Lock()
	set.before = func(action, body string) {
		if action != "SetAVTransportURI" {
			return
		}
		srv.embsubs.mu.Lock()
		_, ready = srv.embsubs.cache[embSubKey(embSubFile(it), 0)]
		srv.embsubs.mu.Unlock()
		named = strings.Contains(body, "/subs/"+id+"/0?format=srt")
	}
	set.mu.Unlock()
	if code, body := castPlay(t, ts, d, id, "?sub=0"); code != http.StatusOK {
		t.Fatalf("the cast answered %d %q", code, body)
	}
	if !ready {
		t.Error("the set was handed the film before its subtitle had been read out of it")
	}
	if !named {
		t.Error("the set was not told where the subtitle is")
	}
}
