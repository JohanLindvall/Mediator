package server

import (
	"strings"
	"testing"
)

func TestSRTToVTT(t *testing.T) {
	srt := "1\r\n00:00:01,000 --> 00:00:04,500\r\nHello there\r\n\r\n" +
		"2\r\n00:01:02,250 --> 00:01:05,000  X1:10 X2:20\r\nSecond line\r\n"
	got, err := ToVTT("movie.srt", []byte(srt))
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if !strings.HasPrefix(out, "WEBVTT\n\n") {
		t.Fatalf("missing header: %q", out[:min(len(out), 20)])
	}
	for _, want := range []string{"00:00:01.000 --> 00:00:04.500", "Hello there", "00:01:02.250 --> 00:01:05.000", "Second line"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "X1:") {
		t.Error("position coordinates should be stripped")
	}
	if strings.Contains(out, ",000") {
		t.Error("comma decimals should be converted")
	}
}

func TestVTTPassthroughAddsHeader(t *testing.T) {
	withHeader, err := ToVTT("a.vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhi\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(withHeader), "WEBVTT") != 1 {
		t.Error("header should not be duplicated")
	}
	without, err := ToVTT("b.vtt", []byte("00:00.000 --> 00:01.000\nhi\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(without), "WEBVTT") {
		t.Error("header should be added when absent")
	}
}

func TestASSToVTT(t *testing.T) {
	ass := `[Script Info]
Title: test

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,{\an8\i1}Styled, with comma
Dialogue: 0,0:00:04.00,0:00:06.00,Default,,0,0,0,,Two\Nlines
Comment: 0,0:00:07.00,0:00:08.00,Default,,0,0,0,,ignored
`
	got, err := ToVTT("movie.ass", []byte(ass))
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, want := range []string{
		"00:00:01.000 --> 00:00:03.500",
		"Styled, with comma", // text keeps its commas
		"Two\nlines",         // \N becomes a real newline
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "{") || strings.Contains(out, "ignored") {
		t.Errorf("override tags and comments should be dropped\n---\n%s", out)
	}
}

func TestDecodeTextCharsets(t *testing.T) {
	// Latin-1 "Grüß" — invalid UTF-8, must not become replacement chars.
	latin1 := []byte{'G', 'r', 0xFC, 0xDF}
	if got := decodeText(latin1); got != "Grüß" {
		t.Errorf("latin-1 = %q, want %q", got, "Grüß")
	}
	if got := decodeText([]byte("\xEF\xBB\xBFhi")); got != "hi" {
		t.Errorf("utf-8 BOM = %q", got)
	}
	if got := decodeText([]byte{0xFF, 0xFE, 'h', 0, 'i', 0}); got != "hi" {
		t.Errorf("utf-16 LE = %q", got)
	}
	if got := decodeText([]byte{0xFE, 0xFF, 0, 'h', 0, 'i'}); got != "hi" {
		t.Errorf("utf-16 BE = %q", got)
	}
	if got := decodeText([]byte("plain ascii")); got != "plain ascii" {
		t.Errorf("ascii = %q", got)
	}
}

func TestToVTTUnsupported(t *testing.T) {
	if _, err := ToVTT("movie.sub", []byte("junk")); err == nil {
		t.Error("expected an error for an unsupported format")
	}
}

// A television reads the sidecar itself and wants SubRip, so the conversion
// has to survive the same files the browser's WebVTT does — including the
// ones that are not UTF-8 and the ones whose hours are missing.
func TestToSRT(t *testing.T) {
	srt, err := ToSRT("a.vtt", []byte("WEBVTT\n\nNOTE something\n\ncue-7\n01:02.500 --> 01:04.0 line:90%\nHello\nthere\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := "1\n00:01:02,500 --> 00:01:04,000\nHello\nthere\n\n"
	if string(srt) != want {
		t.Errorf("got %q, want %q", srt, want)
	}
}

func TestToSRTFromSRT(t *testing.T) {
	// Round trip: the comma comes back, the numbering is rebuilt from one,
	// and a Latin-1 byte survives as the letter it is.
	in := []byte("7\r\n00:00:01,000 --> 00:00:02,000\r\nCaf\xe9\r\n\r\n")
	srt, err := ToSRT("a.srt", in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(srt), "Café") {
		t.Errorf("encoding lost: %q", srt)
	}
	if !strings.HasPrefix(string(srt), "1\n00:00:01,000 --> 00:00:02,000\n") {
		t.Errorf("timing or numbering wrong: %q", srt)
	}
}
