package library

import (
	"testing"
	"time"
)

// The live set is what the stores prune by, and an empty index hands them
// nothing rather than an empty set: pruning against a root that failed to
// mount would drop every position and verdict in the house.
func TestLiveIDsRefuseAnEmptyIndex(t *testing.T) {
	l := quietLib("/m")
	if live := l.LiveIDs(); live != nil {
		t.Fatalf("an empty index answered %v, want nil", live)
	}
	l.upsert("/m/a.mp3", KindAudio, 1, time.Unix(1, 0), fileKey{}, false)
	l.upsert("/m/b.mp3", KindAudio, 1, time.Unix(1, 0), fileKey{}, false)
	live := l.LiveIDs()
	if len(live) != 2 {
		t.Fatalf("live = %d ids, want 2", len(live))
	}
	if _, ok := live[PathID("/m/a.mp3")]; !ok {
		t.Error("an indexed file is not live")
	}
}
