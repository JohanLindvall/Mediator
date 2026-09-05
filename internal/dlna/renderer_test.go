package dlna

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeRenderer answers SOAP the way a television does, and records what it
// was asked, so the client's side of the exchange — the envelope, the
// argument order, the parsing of what comes back — is pinned without a set
// in the room. The names are invented; the XML shapes are the ones real
// devices send.
type fakeRenderer struct {
	t       *testing.T
	actions []string                   // every action asked, in order
	bodies  []string                   // the raw request bodies, for argument checks
	answer  func(action string) string // inner response elements per action
}

func (f *fakeRenderer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	soapAction := strings.Trim(r.Header.Get("SOAPAction"), `"`)
	_, action, _ := strings.Cut(soapAction, "#")
	f.actions = append(f.actions, action)
	f.bodies = append(f.bodies, string(body))
	inner := ""
	if f.answer != nil {
		inner = f.answer(action)
	}
	fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="%s">%s</u:%sResponse></s:Body></s:Envelope>`,
		action, avTransport, inner, action)
}

func testRenderer(t *testing.T, f *fakeRenderer) *Renderer {
	t.Helper()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return &Renderer{
		ID: "abcd", Name: "Sitting Room", UDN: "uuid:test",
		control: map[string]string{
			avTransport:   ts.URL + "/AVTransport/control",
			renderingCtl:  ts.URL + "/RenderingControl/control",
			connectionMgr: ts.URL + "/ConnectionManager/control",
		},
	}
}

func TestSetURICarriesTheDocument(t *testing.T) {
	f := &fakeRenderer{t: t}
	r := testRenderer(t, f)
	meta := Meta{Title: "A Film & Its Title", Class: "object.item.videoItem", MIME: "video/mp4",
		URI: "http://media.local/api/signed/tok/stream/x"}
	if err := r.SetURI(context.Background(), meta.URI, Metadata(meta)); err != nil {
		t.Fatal(err)
	}
	if len(f.actions) != 1 || f.actions[0] != "SetAVTransportURI" {
		t.Fatalf("asked %v, want one SetAVTransportURI", f.actions)
	}
	body := f.bodies[0]
	// The URI travels twice — as the argument and inside the DIDL — and the
	// ampersand in the title must arrive escaped or the set's parser stops.
	if !strings.Contains(body, "<CurrentURI>http://media.local/api/signed/tok/stream/x</CurrentURI>") {
		t.Error("the URI argument is missing or mangled")
	}
	if !strings.Contains(body, "A Film &amp;amp; Its Title") && !strings.Contains(body, "A Film &amp; Its Title") {
		t.Errorf("the title did not survive escaping: %s", body)
	}
}

func TestStatusReadsWhatTheSetSays(t *testing.T) {
	f := &fakeRenderer{t: t}
	f.answer = func(action string) string {
		switch action {
		case "GetTransportInfo":
			return "<CurrentTransportState>PLAYING</CurrentTransportState>"
		case "GetPositionInfo":
			return "<RelTime>0:12:34</RelTime><TrackDuration>1:23:45</TrackDuration><TrackURI>http://x/film</TrackURI>"
		}
		return ""
	}
	r := testRenderer(t, f)
	st, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "PLAYING" {
		t.Errorf("state %q", st.State)
	}
	if want := 12*time.Minute + 34*time.Second; st.Position != want {
		t.Errorf("position %v, want %v", st.Position, want)
	}
	if want := time.Hour + 23*time.Minute + 45*time.Second; st.Duration != want {
		t.Errorf("duration %v, want %v", st.Duration, want)
	}
	if st.URI != "http://x/film" {
		t.Errorf("uri %q", st.URI)
	}
}

func TestSeekAsksInRelTime(t *testing.T) {
	f := &fakeRenderer{t: t}
	r := testRenderer(t, f)
	if err := r.Seek(context.Background(), 65*time.Minute+7*time.Second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.bodies[0], "<Unit>REL_TIME</Unit>") ||
		!strings.Contains(f.bodies[0], "<Target>1:05:07</Target>") {
		t.Errorf("seek body: %s", f.bodies[0])
	}
}

func TestVolumeRoundTrip(t *testing.T) {
	f := &fakeRenderer{t: t}
	f.answer = func(action string) string {
		if action == "GetVolume" {
			return "<CurrentVolume> 37 </CurrentVolume>"
		}
		return ""
	}
	r := testRenderer(t, f)
	// Out-of-range asks are clamped rather than refused: the slider is ours,
	// the limit is the protocol's.
	if err := r.SetVolume(context.Background(), 150); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.bodies[0], "<DesiredVolume>100</DesiredVolume>") {
		t.Errorf("set body: %s", f.bodies[0])
	}
	if got := r.Volume(context.Background()); got != 37 {
		t.Errorf("volume %d, want 37 (whitespace trimmed)", got)
	}
}
