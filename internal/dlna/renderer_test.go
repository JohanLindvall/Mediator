// SPDX-License-Identifier: MIT

package dlna

import (
	"context"
	"errors"
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
	// refuse answers an action with a UPnP fault instead, where it returns
	// a code: the envelope and the 500 a set sends.
	refuse func(action string) (code int, description string)
}

func (f *fakeRenderer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	soapAction := strings.Trim(r.Header.Get("SOAPAction"), `"`)
	_, action, _ := strings.Cut(soapAction, "#")
	f.actions = append(f.actions, action)
	f.bodies = append(f.bodies, string(body))
	if f.refuse != nil {
		if code, desc := f.refuse(action); code != 0 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>`+
				`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>`+
				`<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode>`+
				`<errorDescription>%s</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code, desc)
			return
		}
	}
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

// A refusal comes back as what it is — the set's code and reason — so a
// caller can tell "not from here" from "not this file", and the words a log
// line has always carried are unchanged.
func TestARefusalCarriesItsCode(t *testing.T) {
	f := &fakeRenderer{t: t, refuse: func(action string) (int, string) {
		return TransitionNotAvailable, "Transition not available"
	}}
	r := testRenderer(t, f)
	err := r.SetURI(context.Background(), "http://x/film", "")
	var fault *Fault
	if !errors.As(err, &fault) || fault.Code != 701 || fault.Action != "SetAVTransportURI" {
		t.Fatalf("got %v, want a 701 fault for SetAVTransportURI", err)
	}
	if want := "dlna: Sitting Room refused SetAVTransportURI: 701 Transition not available"; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
	if !Refused(err, TransitionNotAvailable) || Refused(err, 714) {
		t.Error("Refused does not read the code")
	}
	if Refused(errors.New("dlna: timed out"), TransitionNotAvailable) {
		t.Error("silence read as a refusal")
	}
}

// The measured case: a set sitting where the one action it offers is Stop
// refuses a new file with 701 until it is stopped, and then takes it.
func TestASetThatCannotTakeAFileFromWhereItIsIsStoppedFirst(t *testing.T) {
	stopped := false
	f := &fakeRenderer{t: t}
	f.refuse = func(action string) (int, string) {
		switch action {
		case "Stop":
			stopped = true
		case "SetAVTransportURI":
			if !stopped {
				return TransitionNotAvailable, "Transition not available"
			}
		}
		return 0, ""
	}
	r := testRenderer(t, f)
	didStop, err := r.SetURIFromAnyState(context.Background(), "http://x/film", "")
	if err != nil || !didStop {
		t.Fatalf("got stopped=%v err=%v, want the file taken after a stop", didStop, err)
	}
	if got, want := strings.Join(f.actions, ","), "SetAVTransportURI,Stop,SetAVTransportURI"; got != want {
		t.Errorf("asked %s, want %s", got, want)
	}
}

// Every other answer is left exactly as it was: a set that takes the file is
// not stopped, and one that refuses the file itself is not stopped either —
// stopping it would end whatever it was playing for a refusal no stop cures.
func TestOnlyATransitionRefusalStopsTheSet(t *testing.T) {
	f := &fakeRenderer{t: t}
	r := testRenderer(t, f)
	if didStop, err := r.SetURIFromAnyState(context.Background(), "http://x/film", ""); err != nil || didStop {
		t.Fatalf("a set that took the file: stopped=%v err=%v", didStop, err)
	}
	if len(f.actions) != 1 {
		t.Errorf("asked %v, want one SetAVTransportURI", f.actions)
	}

	f = &fakeRenderer{t: t, refuse: func(action string) (int, string) {
		if action == "SetAVTransportURI" {
			return 714, "Illegal MIME-type"
		}
		return 0, ""
	}}
	r = testRenderer(t, f)
	didStop, err := r.SetURIFromAnyState(context.Background(), "http://x/film", "")
	if didStop || !Refused(err, 714) {
		t.Fatalf("a refused file: stopped=%v err=%v, want the 714 as it came", didStop, err)
	}
	if len(f.actions) != 1 {
		t.Errorf("asked %v, want no Stop for a refusal a Stop does not cure", f.actions)
	}
}

// A set that goes on refusing after the stop is asked a bounded number of
// times, and the answer is still its refusal; one that will not even stop
// says both.
func TestASetStuckPastAStopIsGivenUpOn(t *testing.T) {
	defer func(d time.Duration, n int) { stopSettle, stopRetries = d, n }(stopSettle, stopRetries)
	stopSettle, stopRetries = time.Millisecond, 3

	f := &fakeRenderer{t: t, refuse: func(action string) (int, string) {
		if action == "SetAVTransportURI" {
			return TransitionNotAvailable, "Transition not available"
		}
		return 0, ""
	}}
	r := testRenderer(t, f)
	didStop, err := r.SetURIFromAnyState(context.Background(), "http://x/film", "")
	if !didStop || !Refused(err, TransitionNotAvailable) {
		t.Fatalf("stopped=%v err=%v, want the refusal after a stop", didStop, err)
	}
	// The first ask, the stop, then the ask after it and three more.
	if got := strings.Count(strings.Join(f.actions, ","), "SetAVTransportURI"); got != 1+1+3 {
		t.Errorf("asked for the file %d times (%v), want 5", got, f.actions)
	}

	f = &fakeRenderer{t: t, refuse: func(action string) (int, string) {
		if action == "SetAVTransportURI" {
			return TransitionNotAvailable, "Transition not available"
		}
		return 501, "Action failed"
	}}
	r = testRenderer(t, f)
	didStop, err = r.SetURIFromAnyState(context.Background(), "http://x/film", "")
	if didStop || !Refused(err, TransitionNotAvailable) || !strings.Contains(err.Error(), "would not stop") {
		t.Fatalf("a set that will not stop: stopped=%v err=%v", didStop, err)
	}
	if got, want := strings.Join(f.actions, ","), "SetAVTransportURI,Stop"; got != want {
		t.Errorf("asked %s, want %s: a set that will not stop is not asked again", got, want)
	}
}

// Both of the calls a set may hold until the film is open get the opening
// budget. A set that answers the URI at once and holds Play until the picture
// is up is ordinary, and at the ordinary budget its Play read as a set that
// had gone quiet: the cast was reported failed and the page stopped a film
// that was about to start.
func TestOpeningCallsGetTheOpeningBudget(t *testing.T) {
	for _, action := range []string{"SetAVTransportURI", "Play"} {
		if got := budgetFor(action); got != openTimeout {
			t.Errorf("%s may take %v, want the opening budget %v", action, got, openTimeout)
		}
	}
	for _, action := range []string{"Stop", "Pause", "Seek", "GetTransportInfo", "GetPositionInfo"} {
		if got := budgetFor(action); got != callTimeout {
			t.Errorf("%s may take %v, want the ordinary budget %v", action, got, callTimeout)
		}
	}
}
