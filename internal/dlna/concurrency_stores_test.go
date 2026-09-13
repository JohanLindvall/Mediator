package dlna

import (
	"context"
	"testing"
	"time"
)

// A search whose caller has gone must not go on listening for the whole
// window. Discover is called from handlers, whose context carries no deadline
// at all, so the only thing that could shorten the receive loop was a
// deadline it never had: a viewer who navigated away left a socket and a
// goroutine per interface running, and the search mutex held against the next
// client, for an answer nobody was left to read.
func TestSearchStopsWhenNobodyIsWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	search(ctx, 10*time.Second)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a cancelled search took %v; it should end as soon as its caller has gone", d)
	}
}

// And Discover stops handing out work at the same moment: every description
// fetch under a dead context fails as it is made, so the answer is the same
// either way and the fan-out buys nothing.
func TestDiscoverStopsWhenNobodyIsWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if found := Discover(ctx, 10*time.Second); len(found) != 0 {
		t.Errorf("found %d renderers under a cancelled search", len(found))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a cancelled discovery took %v", d)
	}
}
