package server

import (
	"strings"
	"testing"
)

func TestShiftVTT(t *testing.T) {
	const in = `WEBVTT

1
00:00:05.000 --> 00:00:07.500
gone entirely

2
00:10:00.000 --> 00:10:03.000
straddles the new origin

3
00:10:05.250 --> 00:10:08.000 line:90%
kept, settings intact
`
	// A stream opened at 10:00:00.5 of the film starts its own clock at zero.
	got := string(shiftVTT([]byte(in), 600.5))

	if strings.Contains(got, "gone entirely") {
		t.Error("a cue ending before the stream starts was kept")
	}
	// The straddling cue is clamped to zero rather than dropped: it is still
	// on screen when the viewer arrives.
	if !strings.Contains(got, "00:00:00.000 --> 00:00:02.500\nstraddles the new origin") {
		t.Errorf("straddling cue not clamped:\n%s", got)
	}
	if !strings.Contains(got, "00:00:04.750 --> 00:00:07.500 line:90%\nkept, settings intact") {
		t.Errorf("cue not shifted, or settings lost:\n%s", got)
	}
	if !strings.HasPrefix(got, "WEBVTT\n") {
		t.Errorf("header lost:\n%s", got)
	}
	// The dropped cue must take its identifier with it, or the document is
	// left with a stray line where a cue used to be.
	if strings.Contains(got, "\n1\n") {
		t.Errorf("identifier of the dropped cue survived:\n%s", got)
	}
}

func TestShiftVTTZeroIsUntouched(t *testing.T) {
	const in = "WEBVTT\n\n00:00:05.000 --> 00:00:07.500\nhello\n"
	if got := string(shiftVTT([]byte(in), 0)); got != in {
		t.Fatalf("shift of zero rewrote the document:\n%s", got)
	}
}

func TestParseVTTTimeHoursOptional(t *testing.T) {
	cases := map[string]float64{
		"00:02:26.136": 146.136,
		"02:26.136":    146.136,
		"01:00:00.000": 3600,
		"10:00:00.500": 36000.5,
	}
	for in, want := range cases {
		got, ok := parseVTTTime(in)
		if !ok || got != want {
			t.Errorf("parseVTTTime(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
}

func TestFormatVTTTimeRoundTrip(t *testing.T) {
	for _, want := range []string{"00:00:00.000", "00:02:26.136", "01:59:59.999"} {
		v, ok := parseVTTTime(want)
		if !ok {
			t.Fatalf("parseVTTTime(%q) failed", want)
		}
		if got := formatVTTTime(v); got != want {
			t.Errorf("formatVTTTime(parseVTTTime(%q)) = %q", want, got)
		}
	}
}
