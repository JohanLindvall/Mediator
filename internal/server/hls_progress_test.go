package server

import (
	"os"
	"path/filepath"
	"testing"
)

// Of an item's sessions, the most recently asked for answers the progress
// readout: it is the one the player waiting on the readout started.
func TestHLSProgressIsOfTheMostRecentSession(t *testing.T) {
	segs := func(n int) string {
		dir := t.TempDir()
		for i := range n {
			if err := os.WriteFile(filepath.Join(dir, "seg"+string(rune('0'+i))+".ts"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	h := &HLS{sessions: map[string]*hlsSession{
		"film|1|2|0.000|full|":  {dir: segs(1), used: 1, converting: true},
		"film|1|2|60.000|full|": {dir: segs(3), used: 2, converting: true},
	}}
	got, active := h.Progress("film", int64(10*hlsSegmentSec)*1000)
	if !active {
		t.Fatal("a running conversion was reported as none")
	}
	if want := 0.3; got < want-0.001 || got > want+0.001 {
		t.Errorf("progress = %.2f, want %.2f from the newer session's three segments", got, want)
	}
	if _, active := h.Progress("other", 1000); active {
		t.Error("an item with no session was reported as converting")
	}
}
