package library

import (
	"os"
	"testing"
)

// What a disc image inside a rar set actually yields: the title's segments,
// so the stitching can be checked against the disc rather than trusted.
func TestVobProbe(t *testing.T) {
	first := os.Getenv("RARPROBE")
	if first == "" {
		t.Skip("no RARPROBE")
	}
	entries, _, err := parseRarSet(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range discsInside(first, entries) {
		t.Logf("%q  %d bytes  %d ms  %d segments  index %d",
			e.name, e.size, e.durationMs, len(e.segs), len(e.seek))
		for i, s := range e.seek {
			if i < 4 || i > len(e.seek)-3 {
				t.Logf("     seek[%d] ms=%d off=%d", i, s.ms, s.off)
			}
		}
	}
}
