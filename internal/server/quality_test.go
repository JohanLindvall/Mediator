package server

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// The ladder is fixed, and a rate off it is refused: an arbitrary ceiling is
// a budget nobody set and a way to spend the encoder on nothing.
func TestQualityIsARungOrNothing(t *testing.T) {
	for _, c := range []struct {
		q    string
		ok   bool
		kbps int
	}{
		{"", true, 0}, {"0", true, 0}, {"6000", true, 6000}, {"3000", true, 3000}, {"1500", true, 1500},
		{"2500", false, 0}, {"abc", false, 0}, {"-1", false, 0}, {"3000k", false, 0},
	} {
		got, ok := parseQuality(c.q)
		if ok != c.ok || got.kbps != c.kbps {
			t.Errorf("parseQuality(%q) = %+v, %v; want kbps %d, ok %v", c.q, got, ok, c.kbps, c.ok)
		}
	}
	// The box is 16:9 as wide as it is tall, and even.
	for _, c := range []struct{ h, w int }{{1080, 1920}, {720, 1280}, {480, 854}} {
		if got := (quality{height: c.h}).width(); got != c.w {
			t.Errorf("a %d-high box is %d wide, want %d", c.h, got, c.w)
		}
	}
}

// A rung is a re-encode whatever was asked for: a copied picture is the
// file's own rate, which is the thing the viewer asked to have less of.
func TestARungIsNeverACopy(t *testing.T) {
	if effectiveCopy(true, quality{}) != true {
		t.Error("no rung chosen, and the copy the caller asked for was refused")
	}
	if effectiveCopy(true, quality{3000, 720}) {
		t.Error("a rung was chosen and the picture was still copied through at the file's own rate")
	}
	if effectiveCopy(false, quality{}) {
		t.Error("a re-encode became a copy")
	}
}

// What a rung does to the plan: the box replaces the width cap, and the
// encoder gets a ceiling it otherwise has none of. Without a rung the plan
// is exactly what it was.
func TestARungCapsTheRateAndBoxesThePicture(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	it := library.Item{ID: "abc", Name: "a film.mkv", Path: "/nowhere/a film.mkv", Kind: library.KindVideo,
		VCodec: "h264", ACodec: "dts", Width: 1920, Height: 1080, FPS: 24}

	plain, err := planConversion(context.Background(), "ffmpeg", it, 0, false, "", quality{}, false, false, log)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plain.args, " ")
	if strings.Contains(joined, "-maxrate") || strings.Contains(joined, "force_original_aspect_ratio") {
		t.Errorf("no rung chosen, and the plan carries a rung's ceiling or box: %s", joined)
	}
	if !strings.Contains(joined, convertScale) {
		t.Errorf("no rung chosen, and the ordinary width cap is gone: %s", joined)
	}

	rung, err := planConversion(context.Background(), "ffmpeg", it, 0, false, "", quality{3000, 720}, false, false, log)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(rung.args, " ")
	for _, want := range []string{"-maxrate 3000k", "-b:v 3000k", "-bufsize 6000k",
		"scale=w='min(1280,iw)':h='min(720,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the rung's plan is missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, convertScale) {
		t.Errorf("the rung's plan still carries the ordinary width cap beside its box: %s", joined)
	}
	// And a rung asked for with the picture copied is a re-encode.
	copied, err := planConversion(context.Background(), "ffmpeg", it, 0, true, "", quality{3000, 720}, false, false, log)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(copied.args, "copy") {
		t.Error("a rung was chosen and the picture was copied through")
	}
}

// Two rungs of one film are two conversions, so the rung is part of what a
// segmented session is keyed by — or a viewer who moved down the ladder
// would be handed the session made before they did.
func TestARungIsPartOfTheSessionKey(t *testing.T) {
	it := library.Item{ID: "abc", ModTime: 5, Size: 9}
	a := hlsKey(it, 12, false, "0", quality{})
	b := hlsKey(it, 12, false, "0", quality{3000, 720})
	c := hlsKey(it, 12, false, "0", quality{1500, 480})
	if a == b || b == c || a == c {
		t.Errorf("rungs share a session key: %q %q %q", a, b, c)
	}
	if hlsKey(it, 12, false, "0", quality{}) != a {
		t.Error("the same question gave a different key")
	}
}
