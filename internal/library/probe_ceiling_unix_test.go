// SPDX-License-Identifier: MIT

//go:build unix

package library

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// And the ceiling ffprobe installs of its own is the half of this that the
// caller cannot see: it fires on a disk that has gone slow while the caller's
// context is perfectly alive, and the empty result is otherwise
// indistinguishable from a container with nothing in it.
func TestAProbeStoppedByItsOwnCeilingSaysSo(t *testing.T) {
	if FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	// A named pipe with nobody writing to it is a file whose open never
	// returns — which is what a device that has stopped answering looks like
	// from here, and the one way to reach the ceiling without waiting it out.
	path := filepath.Join(t.TempDir(), "clip.mkv")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	was := ffprobeTimeout
	ffprobeTimeout = 200 * time.Millisecond
	defer func() { ffprobeTimeout = was }()

	ctx := context.Background()
	out := ffprobe(ctx, path, nil)
	if ctx.Err() != nil {
		t.Fatal("the caller's context was the thing that expired")
	}
	if out.answered {
		t.Fatal("a probe that was killed reported an answer")
	}
	if !out.cutShort {
		t.Fatal("a probe its own ceiling killed read as a file with nothing to say")
	}
}
