// SPDX-License-Identifier: MIT

package dlna

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
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

// openTimeout is the exception, and it is not a small one. A set opens the
// file it has been handed before it answers, and which call it answers late
// depends on the set: one answers SetAVTransportURI only once it has the
// file open, another answers that at once and holds Play until the picture
// is up — measured on an LG, Play took 1.4 to 7 s across ordinary films and
// past the ordinary budget for a 4K one. The answer is what says it worked,
// so timing out at the ordinary budget reports a failure for a film that is
// about to play, and the page then stops it.
const openTimeout = 45 * time.Second

// budgetFor is how long one action may take to be answered.
func budgetFor(action string) time.Duration {
	if action == "SetAVTransportURI" || action == "Play" {
		return openTimeout
	}
	return callTimeout
}

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
	budget := budgetFor(action)

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
	answer, err := readXMLResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	out, parseErr := outArgs(answer)
	if resp.StatusCode != http.StatusOK || out["faultcode"] != "" || out["errorCode"] != "" {
		// A refusal carries its reason in the same envelope; saying which
		// code came back is the difference between a fault we can look up
		// and "it did not work".
		f := &Fault{Renderer: r.Name, Action: action, Description: resp.Status}
		if code := out["errorCode"]; code != "" {
			f.Description = out["errorDescription"]
			if n, err := strconv.Atoi(code); err == nil {
				f.Code = n
			} else {
				f.Description = strings.TrimSpace(code + " " + f.Description)
			}
		}
		return nil, f
	}
	if parseErr != nil {
		return nil, fmt.Errorf("dlna: %s returned an invalid %s response: %w", r.Name, action, parseErr)
	}
	return out, nil
}

// Fault is a set's refusal of one action: the reason it gave, in the same
// envelope an answer would have come in. It is an answer, which silence is
// not — a set that refused has said so, where one that did not reply may
// still be doing what it was asked — and the code is what a caller can act
// on: 701 says the transport cannot get there from where it is, which a Stop
// cures, where 714 says the set will not play this kind of file, which
// nothing here can.
type Fault struct {
	Renderer string
	Action   string
	// Code is the UPnP errorCode, 0 where the set answered with an HTTP
	// status and no envelope.
	Code int
	// Description is the set's errorDescription, or the HTTP status where
	// it gave none.
	Description string
}

func (f *Fault) Error() string {
	if f.Code != 0 {
		return fmt.Sprintf("dlna: %s refused %s: %d %s", f.Renderer, f.Action, f.Code, f.Description)
	}
	return fmt.Sprintf("dlna: %s refused %s: %s", f.Renderer, f.Action, f.Description)
}

// TransitionNotAvailable is UPnP's 701: the action cannot be taken from the
// state the transport is in.
const TransitionNotAvailable = 701

// Refused reports whether err is a set's refusal with the given UPnP code.
func Refused(err error, code int) bool {
	var f *Fault
	return errors.As(err, &f) && f.Code == code
}

// outArgs collects every element in the envelope that holds text, which for
// UPnP is exactly the flat list of named values a response or a fault is —
// the containers around them hold nothing but whitespace. Metadata comes
// back as escaped text rather than as elements, so a document inside a value
// cannot be mistaken for the values themselves.
func outArgs(doc []byte) (map[string]string, error) {
	out := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(doc))
	var text strings.Builder
	depth, roots := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF && roots == 1 && depth == 0 {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || t.Name.Local != "Envelope" {
					return nil, errors.New("expected one SOAP Envelope")
				}
			}
			depth++
			text.Reset()
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("text outside SOAP Envelope")
			}
			text.Write(t)
		case xml.EndElement:
			depth--
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

// How a set that would not take a new file from where it was is asked again
// once it has been stopped: at once, then this far apart, this many times
// more. Variables only so a test need not wait them out.
var (
	stopSettle  = 400 * time.Millisecond
	stopRetries = 5
)

// SetURIFromAnyState is SetURI for a set that may be in the middle of
// something else, and it reports whether the set had to be stopped first.
//
// The transport is a state machine, and a set may refuse a new file from a
// state it cannot leave that way — UPnP's 701, "Transition not available".
// Measured on a television: a cast was refused with 701 and worked on the
// second press, only because the page's clean-up after the failure had sent
// a Stop; and handed a file it could not open, the same set sat for minutes
// in a state of its maker's own (LG_TRANSITIONING) whose one available
// action is Stop — Play hung there, and a new file would have been refused.
// So that one refusal is answered by doing what the set asks: stop, and
// hand it the file again. Any other refusal is the set's word about the file
// and comes back as it came.
func (r *Renderer) SetURIFromAnyState(ctx context.Context, uri, metadata string) (stopped bool, err error) {
	err = r.SetURI(ctx, uri, metadata)
	if !Refused(err, TransitionNotAvailable) {
		return false, err
	}
	if serr := r.Stop(ctx); serr != nil {
		return false, fmt.Errorf("%w (and it would not stop: %v)", err, serr)
	}
	for try := 0; ; try++ {
		err = r.SetURI(ctx, uri, metadata)
		// A set on its way to stopped can refuse once more on the way; one
		// that goes on refusing is stuck somewhere a Stop does not reach.
		if !Refused(err, TransitionNotAvailable) || try >= stopRetries {
			return true, err
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(stopSettle):
		}
	}
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
