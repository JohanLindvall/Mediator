package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// newEmbSubs is a server with the extraction cache made up, so a test can
// stand a read in the middle of one without any ffmpeg being involved.
func newEmbSubs() (*Server, library.Item, string) {
	s := &Server{thumbs: &Thumbnailer{}}
	s.embsubs.cache = map[string][]byte{}
	s.embsubs.inflight = map[string]*embSub{}
	s.embsubs.sem = make(chan struct{}, embSubReaders)
	it := library.Item{ID: "harbourlights", ModTime: 7, Size: 9}
	return s, it, fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, 0)
}

// The reader belongs to no request. The player re-points its <track> at a new
// ?shift= on every conversion reopen — which is every seek — so the fetch in
// flight is abandoned; tied to that request, the whole demux went with it and
// nothing carried the work on, so each seek threw away a pass over gigabytes
// and began another.
func TestExtractionOutlivesTheRequestThatAskedForIt(t *testing.T) {
	s, it, key := newEmbSubs()
	e := &embSub{done: make(chan struct{})}
	s.embsubs.inflight[key] = e

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.extractEmbSub(gone, it, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the asker's own cancellation", err)
	}

	s.embsubs.mu.Lock()
	_, reading := s.embsubs.inflight[key]
	s.embsubs.mu.Unlock()
	if !reading {
		t.Fatal("the asker leaving tore down the read, so the next ask starts again from the first byte")
	}

	// And what it finally reads answers whoever is there by then.
	e.data = []byte("WEBVTT\n")
	close(e.done)
	got, err := s.extractEmbSub(context.Background(), it, 0)
	if err != nil || string(got) != "WEBVTT\n" {
		t.Fatalf("got %q, %v — want the extraction the abandoned request started", got, err)
	}
}

// A failure is the leader's answer, handed to everyone waiting on it rather
// than sending each of them off to read the container again — and it is not
// remembered: the entry goes, so a later ask is a fresh attempt and not a
// cached refusal.
func TestExtractionFailureIsSharedAndNotRemembered(t *testing.T) {
	s, it, key := newEmbSubs()
	e := &embSub{done: make(chan struct{})}
	e.err = errors.New("the container would not be read")
	close(e.done)
	s.embsubs.inflight[key] = e

	if _, err := s.extractEmbSub(context.Background(), it, 0); err != e.err {
		t.Fatalf("err = %v, want the reader's own verdict", err)
	}

	// The next ask reads afresh — here there is no ffmpeg, so it fails at
	// once — and leaves nothing behind that could answer for it later.
	delete(s.embsubs.inflight, key)
	if _, err := s.extractEmbSub(context.Background(), it, 0); err == nil {
		t.Fatal("an extraction with no ffmpeg reported success")
	}
	s.embsubs.mu.Lock()
	defer s.embsubs.mu.Unlock()
	if _, stuck := s.embsubs.inflight[key]; stuck {
		t.Error("a finished read stayed in flight")
	}
	if _, cached := s.embsubs.cache[key]; cached {
		t.Error("a failure was admitted to the cache")
	}
}
