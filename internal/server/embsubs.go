package server

// Extraction of the subtitles a video carries inside itself.
//
// A television release ships its captions muxed into the MKV, not laid
// beside it — the file in front of this feature had exactly one subtitle
// stream and no sidecar anywhere on the disk. A browser will not surface
// embedded text from a stream it is playing, and once playback is a
// conversion there is nothing to surface it from; the only way to a <track>
// element is extraction.
//
// Extraction is not free the way a sidecar is: ffmpeg has to demux the whole
// container to collect every cue, which for a 4K episode is a couple of
// gigabytes of reading for a hundred kilobytes of text. Three things follow.
// It is **cached** by the file's identity, because the player re-points its
// <track> at a new ?shift= on every conversion reopen — every seek — and
// re-reading the film per seek would be absurd; the shift is applied to the
// cached cues afterwards, which is arithmetic. It is **deduplicated**, so a
// second ask while the first is reading waits for it instead of starting a
// second pass over the same file. And it is **counted as streaming**, so
// the thumbnailer and enrichment stand down while it reads — it is the
// viewer's own playback this read is racing.
//
// Two more follow from the same cost. The read **outlives the request that
// started it**, as a rewrap and a segmented conversion do: the <track> is
// re-pointed on every seek, so the fetch in flight is abandoned, and tied to
// that request the whole demux went with it with nothing admitted — each seek
// threw away a pass over gigabytes and began another. And it **takes a slot**
// (embSubReaders), because this is the ffmpeg here that reads the whole
// container and it had no bound of any kind: two asks for different streams,
// or for two films, were two unbounded passes at once on the one disk.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// embSubTimeout bounds one extraction. The read is the whole file at disk
// speed — seconds for most, tens of seconds for a 4K season finale over a
// loopback archive read — so this is generous and exists to stop a wedged
// mount holding the slot forever.
const embSubTimeout = 4 * time.Minute

// embSubCacheMax bounds the cache by what it holds, not how many: subtitle
// files are tens of kilobytes, so this is hundreds of films' worth, and one
// pathological file cannot pin the lot.
const embSubCacheMax = 16 << 20

// embSubReaders is how many extractions may run at once. Every other ffmpeg
// in this package takes a slot of some kind, and this is the one that reads
// the *whole* container — so two concurrent asks for different streams of one
// film, or for two films, were two unbounded passes over gigabytes each,
// beside the playback they are for. Two rather than one: a second viewer
// should not wait out a 4K demux, and the pair is what the thumbnailer allows
// itself for the same disk. A slot of its own rather than the thumbnailer's,
// which one extraction could hold for the whole of embSubTimeout — four
// minutes of no tiles and no crop detection.
const embSubReaders = 2

// embSub is one extraction, and what came of it. The outcome is published on
// the entry rather than only into the cache: a waiter has to be able to tell
// "it failed" from "it has not happened yet", or every failure sends whoever
// was parked off to read the container again.
type embSub struct {
	done chan struct{}
	data []byte
	err  error
}

type embSubs struct {
	mu       sync.Mutex
	cache    map[string][]byte
	total    int
	inflight map[string]*embSub
	sem      chan struct{}
}

// extract returns the stream as WebVTT, reading the file only the first time.
//
// The read outlives the request that started it, exactly as a rewrap and a
// segmented conversion do, and for the same reason: the player re-points its
// <track> at a new ?shift= on every conversion reopen — every seek — so the
// browser abandons the fetch in flight and the extraction was cancelled with
// it, having admitted nothing. Nothing carried the work on for whoever asked
// next, so each seek threw away a whole demux and began another, and a large
// film being seeked through never finished one at all. Now the ask is what
// *starts* the read; the asker waits on it, and if the asker leaves the read
// goes on for the next one.
func (s *Server) extractEmbSub(ctx context.Context, it library.Item, stream int) ([]byte, error) {
	key := fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, stream)
	s.embsubs.mu.Lock()
	if s.embsubs.cache == nil {
		s.embsubs.cache = map[string][]byte{}
		s.embsubs.inflight = map[string]*embSub{}
		s.embsubs.sem = make(chan struct{}, embSubReaders)
	}
	if v, ok := s.embsubs.cache[key]; ok {
		s.embsubs.mu.Unlock()
		return v, nil
	}
	e, ok := s.embsubs.inflight[key]
	if !ok {
		// Nobody is reading it: start one that belongs to no request.
		e = &embSub{done: make(chan struct{})}
		s.embsubs.inflight[key] = e
		go s.fillEmbSub(context.WithoutCancel(ctx), it, stream, key, e)
	}
	s.embsubs.mu.Unlock()

	select {
	case <-e.done:
	case <-ctx.Done():
		// The reader carries on without us; whoever asks next gets it.
		return nil, ctx.Err()
	}
	// A failure is the leader's answer and is given to everyone waiting on
	// it: re-reading the container behind a pass that has just proved it
	// cannot be read is the one thing that helps nobody. It is not
	// remembered, though — the entry is gone, so a later ask deserves and
	// gets a fresh attempt, which is the rule the Remuxer and the sessions
	// keep too.
	return e.data, e.err
}

// fillEmbSub does the reading and publishes what came of it, once.
func (s *Server) fillEmbSub(ctx context.Context, it library.Item, stream int, key string, e *embSub) {
	data, err := s.runEmbSub(ctx, it, stream)
	s.embsubs.mu.Lock()
	e.data, e.err = data, err
	delete(s.embsubs.inflight, key)
	if err == nil {
		s.embsubs.admit(key, data)
	}
	s.embsubs.mu.Unlock()
	close(e.done)
}

// admit stores an extraction under the bound, making room first: a cache
// that admits before it evicts is bounded only on average. One larger than
// the whole bound is not stored at all — it used to evict everything and go
// in regardless, which is a bound in name only. Caller holds the lock.
func (c *embSubs) admit(key string, data []byte) {
	if len(data) > embSubCacheMax {
		return
	}
	if c.cache == nil {
		c.cache = map[string][]byte{}
	}
	for k, v := range c.cache {
		if c.total+len(data) <= embSubCacheMax {
			break
		}
		c.total -= len(v)
		delete(c.cache, k)
	}
	c.cache[key] = data
	c.total += len(data)
}

func (s *Server) runEmbSub(ctx context.Context, it library.Item, stream int) ([]byte, error) {
	ffmpeg := s.thumbs.FFmpegPath()
	if ffmpeg == "" {
		return nil, fmt.Errorf("no ffmpeg")
	}
	ctx, cancel := context.WithTimeout(ctx, embSubTimeout)
	defer cancel()
	// A slot first, and outside the cache's lock: the whole container is
	// read for a hundred kilobytes of text, and several of those at once on
	// one disk is the playback this read is meant to be racing gently.
	select {
	case s.embsubs.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.embsubs.sem }()
	// The read competes with the playback of the very film it is for.
	defer s.lib.StartStream()()

	in, _, err := convertInput(it, 0)
	if err != nil {
		return nil, err
	}
	var stdin io.ReadCloser
	if in.pipe != nil {
		defer in.pipe.Close()
		stdin = in.pipe
	}
	args := []string{"-v", "error", "-nostdin"}
	args = append(args, in.args...)
	args = append(args,
		"-map", "0:s:"+strconv.Itoa(stream),
		"-c:s", "webvtt", "-f", "webvtt", "pipe:1",
	)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stdin = stdin
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("extract subtitles: %w: %s", err, bytes.TrimSpace(errBuf.Bytes()))
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("extract subtitles: nothing came out")
	}
	return out.Bytes(), nil
}
