package server

import (
	"os"
	"path/filepath"
	"testing"
)

// The most recently asked-for rewrap of an item answers its progress, and
// Close stops the ones still being written.
func TestRemuxProgressIsOfTheMostRecentEntryAndCloseStopsThem(t *testing.T) {
	dir := t.TempDir()
	file := func(name string, n int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r := NewRemuxer("", nil, testLog())
	stopped := 0
	r.entries["film|1|2|a0"] = &remuxEntry{path: file("a", 50), want: 100, used: 1, done: make(chan struct{}),
		cancel: func() { stopped++ }}
	r.entries["film|1|2|a1"] = &remuxEntry{path: file("b", 90), want: 100, used: 2, done: make(chan struct{}),
		cancel: func() { stopped++ }}
	got, active := r.Progress("film")
	if !active || got < 0.89 || got > 0.91 {
		t.Errorf("progress = %.2f (active %v), want 0.90 from the newer entry", got, active)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if stopped != 2 {
		t.Errorf("Close stopped %d rewraps, want both", stopped)
	}
	if _, active := r.Progress("film"); active {
		t.Error("a closed remuxer still reports a rewrap running")
	}
}
