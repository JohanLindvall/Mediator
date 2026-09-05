package dlna

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// callTimeout bounds one SOAP exchange. A television is a small computer
// doing something else, and one that has gone away must not hold a request
// here open behind it.
const callTimeout = 8 * time.Second

// setURITimeout is the exception, and it is not a small one. A set handed a
// URL goes and opens it before it answers — measured against a television on
// the same wire, that is several seconds for a file it takes to at once, and
// the answer is what says it worked. Timing out at the ordinary budget
// reported a failure for a film that was already playing.
const setURITimeout = 45 * time.Second

// arg is one SOAP argument. Order is not decoration: UPnP matches arguments
// by position within the action, so a map would break every call.
type arg struct{ name, value string }

// Status is where a renderer says it has got to.
type Status struct {
	State    string        // PLAYING, PAUSED_PLAYBACK, STOPPED, TRANSITIONING, NO_MEDIA_PRESENT
	Position time.Duration // where in the file it is
	Duration time.Duration // how long the file is, as the set measures it
	URI      string        // what it is playing, so a caller can tell it is still ours
}

// call performs one SOAP action and returns the response's out arguments.
func (r *Renderer) call(ctx context.Context, service, action string, args ...arg) (map[string]string, error) {
	control := r.control[service]
	if control == "" {
		return nil, fmt.Errorf("dlna: %s has no %s", r.Name, service)
	}
	budget := callTimeout
	if action == "SetAVTransportURI" {
		budget = setURITimeout
	}

	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	body.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	fmt.Fprintf(&body, `<u:%s xmlns:u="%s">`, action, service)
	for _, a := range args {
		fmt.Fprintf(&body, "<%s>%s</%s>", a.name, escape(a.value), a.name)
	}
	fmt.Fprintf(&body, `</u:%s></s:Body></s:Envelope>`, action)

	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, control, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+service+"#"+action+`"`)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, describeMax))
	if err != nil {
		return nil, err
	}
	out := outArgs(answer)
	if resp.StatusCode != http.StatusOK {
		// A refusal carries its reason in the same envelope; saying which
		// code came back is the difference between a fault we can look up
		// and "it did not work".
		if code := out["errorCode"]; code != "" {
			return nil, fmt.Errorf("dlna: %s refused %s: %s %s", r.Name, action, code, out["errorDescription"])
		}
		return nil, fmt.Errorf("dlna: %s refused %s: %s", r.Name, action, resp.Status)
	}
	return out, nil
}

// outArgs collects every element in the envelope that holds text, which for
// UPnP is exactly the flat list of named values a response or a fault is —
// the containers around them hold nothing but whitespace. Metadata comes
// back as escaped text rather than as elements, so a document inside a value
// cannot be mistaken for the values themselves.
func outArgs(doc []byte) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(doc))
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if v := strings.TrimSpace(text.String()); v != "" {
				out[t.Name.Local] = v
			}
			text.Reset()
		}
	}
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// SetURI hands the device what to play and the DIDL-Lite description of it.
func (r *Renderer) SetURI(ctx context.Context, uri, metadata string) error {
	_, err := r.call(ctx, avTransport, "SetAVTransportURI",
		arg{"InstanceID", "0"}, arg{"CurrentURI", uri}, arg{"CurrentURIMetaData", metadata})
	return err
}

// SetNextURI tells the set what to play *after* the current file, which is
// what makes a queue gapless: it has the next track open before the last one
// ends, so nothing has to be noticed here and sent afterwards. Optional in
// the specification — a renderer without it refuses, and the caller falls
// back to sending the next track when it sees the current one finish.
func (r *Renderer) SetNextURI(ctx context.Context, uri, metadata string) error {
	_, err := r.call(ctx, avTransport, "SetNextAVTransportURI",
		arg{"InstanceID", "0"}, arg{"NextURI", uri}, arg{"NextURIMetaData", metadata})
	return err
}

// Play starts it, at ordinary speed.
func (r *Renderer) Play(ctx context.Context) error {
	_, err := r.call(ctx, avTransport, "Play", arg{"InstanceID", "0"}, arg{"Speed", "1"})
	return err
}

// Pause holds it where it is.
func (r *Renderer) Pause(ctx context.Context) error {
	_, err := r.call(ctx, avTransport, "Pause", arg{"InstanceID", "0"})
	return err
}

// Stop ends it and gives the screen back.
func (r *Renderer) Stop(ctx context.Context) error {
	_, err := r.call(ctx, avTransport, "Stop", arg{"InstanceID", "0"})
	return err
}

// Seek moves to a position in the file. REL_TIME is seeking by the clock,
// which is what a viewer means, and works because the file is served with
// ranges — the set fetches the bytes it needs for that moment.
func (r *Renderer) Seek(ctx context.Context, at time.Duration) error {
	_, err := r.call(ctx, avTransport, "Seek",
		arg{"InstanceID", "0"}, arg{"Unit", "REL_TIME"}, arg{"Target", FormatTime(at)})
	return err
}

// Status asks where it has got to. Two calls: the transport says whether it
// is playing, the position says where — and a set that has finished reports
// the first without the second.
func (r *Renderer) Status(ctx context.Context) (Status, error) {
	var st Status
	info, err := r.call(ctx, avTransport, "GetTransportInfo", arg{"InstanceID", "0"})
	if err != nil {
		return st, err
	}
	st.State = info["CurrentTransportState"]
	// A set holding nothing has no position to report; asking costs a
	// round trip for an answer that is always empty.
	if st.State == "NO_MEDIA_PRESENT" {
		return st, nil
	}
	pos, err := r.call(ctx, avTransport, "GetPositionInfo", arg{"InstanceID", "0"})
	if err != nil {
		return st, nil // it said what it is doing, which is the half that matters
	}
	st.Position = ParseTime(pos["RelTime"])
	st.Duration = ParseTime(pos["TrackDuration"])
	st.URI = pos["TrackURI"]
	return st, nil
}

// SetVolume sets it, 0 to 100.
func (r *Renderer) SetVolume(ctx context.Context, level int) error {
	if level < 0 {
		level = 0
	}
	if level > 100 {
		level = 100
	}
	_, err := r.call(ctx, renderingCtl, "SetVolume",
		arg{"InstanceID", "0"}, arg{"Channel", "Master"}, arg{"DesiredVolume", strconv.Itoa(level)})
	return err
}

// Volume reads it back, -1 where the device has no such control.
func (r *Renderer) Volume(ctx context.Context) int {
	out, err := r.call(ctx, renderingCtl, "GetVolume", arg{"InstanceID", "0"}, arg{"Channel", "Master"})
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(out["CurrentVolume"]))
	if err != nil {
		return -1
	}
	return n
}
