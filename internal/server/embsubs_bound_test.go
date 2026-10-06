package server

import "testing"

// The cache of extracted captions is bounded by what it holds: it makes room
// for what fits and refuses what never could, rather than emptying itself to
// admit one extraction larger than the whole bound.
func TestEmbeddedSubtitleCacheIsBounded(t *testing.T) {
	c := &embSubs{}
	big := make([]byte, embSubCacheMax/2+1) // two cannot fit
	c.admit("a", big)
	c.admit("b", big)
	if _, ok := c.cache["a"]; ok {
		t.Error("the older extraction was kept past the bound")
	}
	if _, ok := c.cache["b"]; !ok || c.total != len(big) {
		t.Errorf("the newer extraction was not admitted, total %d", c.total)
	}
	// One read admits every track of a file, so a track already held is
	// admitted again: it replaces itself rather than counting twice.
	c.admit("b", big)
	if c.total != len(big) {
		t.Errorf("a track admitted twice is counted %d, want %d", c.total, len(big))
	}
	c.admit("c", make([]byte, embSubCacheMax+1))
	if _, ok := c.cache["c"]; ok {
		t.Error("an extraction larger than the bound was stored")
	}
	if _, ok := c.cache["b"]; !ok || c.total != len(big) {
		t.Errorf("refusing the oversize one evicted what fit: total %d", c.total)
	}
}
