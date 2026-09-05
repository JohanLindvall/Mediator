package library

import (
	"testing"
	"time"
)

// The rule the grid draws with, at its boundaries. Under five seconds is not
// a start, and past WatchedFraction is finished — the same numbers the
// player resumes and stops offering to resume by, deliberately, so the chip,
// the tiles and the player can never disagree about what a word means.
func TestWatchState(t *testing.T) {
	for _, c := range []struct {
		name string
		w    Watch
		want WatchState
	}{
		{"never opened", Watch{}, WatchNone},
		{"opened and shut", Watch{Pos: 3, Len: 7200}, WatchNone},
		{"exactly the floor", Watch{Pos: 5, Len: 7200}, WatchStarted},
		{"half way", Watch{Pos: 3600, Len: 7200}, WatchStarted},
		{"the credits are running", Watch{Pos: 7100, Len: 7200}, WatchDone},
		{"exactly the fraction is not yet done", Watch{Pos: 7200 * WatchedFraction, Len: 7200}, WatchStarted},
		// A record with no length says nothing — the bug class resumeStart
		// disarms on the client is records exactly like this.
		{"a position with no length", Watch{Pos: 3600}, WatchNone},
		// Five seconds into a five-second clip is finished, not started:
		// the fraction is asked after the floor.
		{"a clip watched whole", Watch{Pos: 5, Len: 5}, WatchDone},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.w.State(); got != c.want {
				t.Errorf("State(%+v) = %v, want %v", c.w, got, c.want)
			}
		})
	}
}

// A position is saved every few seconds while something plays, so the
// version the listing caches against must move only when the *state*
// changes — otherwise every heartbeat rebuilds the filtered listing.
func TestSetWatchBumpsOnlyOnChange(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/film.mkv", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/film.mkv")

	v0 := l.watchVersion()
	l.SetWatch(id, Watch{Pos: 60, Len: 7200})
	v1 := l.watchVersion()
	if v1 == v0 {
		t.Fatal("becoming started must move the watch version")
	}
	// The same state again, further in: the heartbeat case.
	l.SetWatch(id, Watch{Pos: 65, Len: 7200})
	if l.watchVersion() != v1 {
		t.Error("a saved position with the same state must not move the version")
	}
	// Finishing is a change again.
	l.SetWatch(id, Watch{Pos: 7150, Len: 7200})
	if l.watchVersion() == v1 {
		t.Error("finishing must move the version")
	}
	// And clearing the record takes the item out of the filter.
	l.SetWatch(id, Watch{})
	if got := l.watchStateOf(id); got != WatchNone {
		t.Errorf("after clearing, state = %v, want none", got)
	}
}

// The two chip totals intersect the watch map with the index: positions
// outlive the files they belong to until the next prune, and a chip must
// not count ghosts.
func TestWatchTotalsIntersectTheIndex(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/a.mkv", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	l.upsert("/m/b.mkv", KindVideo, 10, time.Unix(2, 0), fileKey{}, false)
	l.SetWatchAll(map[string]Watch{
		PathID("/m/a.mkv"):    {Pos: 60, Len: 7200},   // started
		PathID("/m/b.mkv"):    {Pos: 7150, Len: 7200}, // done
		PathID("/m/gone.mkv"): {Pos: 60, Len: 7200},   // a ghost
	})
	started, done := l.watchTotals()
	if started != 1 || done != 1 {
		t.Fatalf("totals = %d started, %d done; want 1 and 1", started, done)
	}
	// The cache answers again, and a new position invalidates it.
	l.SetWatch(PathID("/m/a.mkv"), Watch{Pos: 7150, Len: 7200})
	started, done = l.watchTotals()
	if started != 0 || done != 2 {
		t.Fatalf("after finishing: %d started, %d done; want 0 and 2", started, done)
	}
}

// keepWatched is the filter behind the two watch views.
func TestKeepWatched(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/film.mkv", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/film.mkv")
	l.SetWatch(id, Watch{Pos: 60, Len: 7200})
	if !keepWatched(l.watchStateOf(id), "started") || keepWatched(l.watchStateOf(id), "done") {
		t.Error("a started film belongs to the started view and not the done one")
	}
	if !keepWatched(l.watchStateOf(id), "") {
		t.Error("no filter keeps everything")
	}
}

// A play bumps the library's own version — that is the machinery that
// refreshes every cached collection — where a position deliberately does not.
func TestSetPlaysNotifies(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/track.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/track.mp3")

	v0 := l.Version()
	l.SetPlays(id, 1)
	if l.Version() == v0 {
		t.Fatal("a play must bump the library version")
	}
	v1 := l.Version()
	l.SetPlays(id, 1)
	if l.Version() != v1 {
		t.Error("the same count again must not")
	}
	if got := l.playsOf(id); got != 1 {
		t.Errorf("playsOf = %d, want 1", got)
	}
	// The Popular chip intersects with the index like the watch totals do.
	l.SetPlaysAll(map[string]int{id: 3, "feedfacedeadbeef": 9})
	if got := l.playedTotal(); got != 1 {
		t.Errorf("playedTotal = %d, want the one the index still holds", got)
	}
}
