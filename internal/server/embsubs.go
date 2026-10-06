// SPDX-License-Identifier: MIT

package server

// Extraction of the subtitles a video carries inside itself.
//
// A television release ships its captions muxed into the MKV, not laid
// beside it. A browser will not surface embedded text from a stream it is
// playing, and once playback is a conversion there is nothing to surface it
// from; the only way to a <track> element, or to a television, is extraction.
//
// Extraction is not free the way a sidecar is: ffmpeg has to demux the whole
// container to collect every cue, which for a 4K film is tens of gigabytes of
// reading for a hundred kilobytes of text. So:
//
//   - **One read takes every text track the file carries.** The cost is the
//     read and not the tracks, and a release carries dozens of languages:
//     asked for one at a time, every language is another pass over the whole
//     file. Measured on a 27 GB film with 38 tracks, a browser opening it, a
//     viewer choosing another language and a television asking for that one
//     were four whole-file reads of 60 to 90 s each, two at a time, with the
//     television's queued behind the browser's until the set gave up.
//   - It is **cached** per track by the file's identity, because the player
//     re-points its <track> at a new ?shift= on every conversion reopen —
//     every seek — and the shift is arithmetic on the cached cues.
//   - It is **deduplicated** per file: an ask for any track while a read is
//     running waits for that read instead of starting another.
//   - It is **counted as streaming**, so the thumbnailer and enrichment stand
//     down while it reads — it is the viewer's own playback it races.
//   - The read **outlives the request that started it**, as a rewrap and a
//     segmented conversion do: the <track> is re-pointed on every seek, so the
//     fetch in flight is abandoned, and a read tied to it would be thrown away
//     and begun again each time.
//   - It **takes a slot** (embSubReaders): it is the ffmpeg here that reads
//     the whole container, and two films read at once share one disk.
//   - It **says how far it has got** (progress): a television is not handed a
//     film until the subtitle it will ask for exists (castCaption), and for a
//     large file that wait is a minute or more.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// embSubFloor and embSubRate bound one read. The read is the whole file, so
// the bound grows with it (embSubBudget): a fixed one either strands a large
// film's subtitles or lets a wedged mount hold the slot for as long as the
// largest film would need. The rate is far under what the disk does alone,
// since the read shares it with the playback it is for.
const (
	embSubFloor = 4 * time.Minute
	embSubRate  = 64 << 20 // bytes a second
)

// embSubBudget is how long a read of a file this size may take.
func embSubBudget(size int64) time.Duration {
	return max(embSubFloor, time.Duration(size/embSubRate)*time.Second)
}

// embSubCacheMax bounds the cache by what it holds, not how many: one
// track is tens of kilobytes and a release's every language a few
// megabytes, so this is a dozen such films, and one pathological file cannot
// pin the lot.
const embSubCacheMax = 64 << 20

// embSubReaders is how many reads may run at once. Every other ffmpeg in
// this package takes a slot of some kind, and this is the one that reads the
// *whole* container — two films at once are two passes over gigabytes each,
// beside the playback they are for. Two rather than one: a second viewer
// should not wait out a 4K demux, and the pair is what the thumbnailer allows
// itself for the same disk. A slot of its own rather than the thumbnailer's,
// which one read could hold for minutes — no tiles and no crop detection
// meanwhile.
const embSubReaders = 2

// embSub is one read of a file, and what came of it. The outcome is
// published on the entry rather than only into the cache: a waiter has to be
// able to tell "it failed" from "it has not happened yet", or every failure
// sends whoever was parked off to read the container again.
type embSub struct {
	done chan struct{}
	id   string       // the item, which is what progress is asked about by
	size int64        // the file's, which is what progress is measured against
	pid  atomic.Int64 // ffmpeg's while it reads, so progress can ask it
	data map[int][]byte
	err  error
}

type embSubs struct {
	mu       sync.Mutex
	cache    map[string][]byte // by embSubKey: one file's one track
	total    int
	inflight map[string]*embSub // by embSubFile: one read per file
	sem      chan struct{}
}

// embSubFile names a file as it is now: a file changed on disk is another
// read and other tracks.
func embSubFile(it library.Item) string {
	return fmt.Sprintf("%s|%d|%d", it.ID, it.ModTime, it.Size)
}

func embSubKey(file string, stream int) string { return file + "|" + strconv.Itoa(stream) }

// extract returns the stream as WebVTT, reading the file only the first time.
//
// The read outlives the request that started it, exactly as a rewrap and a
// segmented conversion do, and for the same reason: the player re-points its
// <track> at a new ?shift= on every conversion reopen — every seek — so the
// browser abandons the fetch in flight. The ask is what *starts* the read;
// the asker waits on it, and if the asker leaves the read goes on for the
// next one.
func (s *Server) extractEmbSub(ctx context.Context, it library.Item, stream int) ([]byte, error) {
	file := embSubFile(it)
	s.embsubs.mu.Lock()
	if s.embsubs.cache == nil {
		s.embsubs.cache = map[string][]byte{}
		s.embsubs.inflight = map[string]*embSub{}
		s.embsubs.sem = make(chan struct{}, embSubReaders)
	}
	if v, ok := s.embsubs.cache[embSubKey(file, stream)]; ok {
		s.embsubs.mu.Unlock()
		return v, nil
	}
	e, ok := s.embsubs.inflight[file]
	if !ok {
		// Nobody is reading it: start one that belongs to no request.
		e = &embSub{done: make(chan struct{}), id: it.ID, size: it.Size}
		s.embsubs.inflight[file] = e
		go s.fillEmbSub(context.WithoutCancel(ctx), it, stream, file, e)
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
	if e.err != nil {
		return nil, e.err
	}
	if v, ok := e.data[stream]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("extract subtitles: nothing came out of stream %d", stream)
}

// textStreams is every text track the file carries, by ffmpeg's ordinal,
// with the one asked for among them whatever the listing says.
func textStreams(it library.Item, asked int) []int {
	out := []int{asked}
	for _, t := range it.EmbSubs {
		out = append(out, t.Stream)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// fillEmbSub does the reading and publishes what came of it, once.
//
// Every track at once, and the one asked for alone only where that fails: a
// read that will not produce one track's file is no reason to deny the
// viewer the track they asked for, and asking about it alone costs a second
// pass only where the first went wrong.
func (s *Server) fillEmbSub(ctx context.Context, it library.Item, asked int, file string, e *embSub) {
	streams := textStreams(it, asked)
	data, err := s.runEmbSub(ctx, it, streams, e)
	if err != nil && len(streams) > 1 && ctx.Err() == nil {
		data, err = s.runEmbSub(ctx, it, []int{asked}, e)
	}
	s.embsubs.mu.Lock()
	e.data, e.err = data, err
	delete(s.embsubs.inflight, file)
	for stream, v := range data {
		s.embsubs.admit(embSubKey(file, stream), v)
	}
	s.embsubs.mu.Unlock()
	close(e.done)
}

// admit stores an extraction under the bound, making room first: a cache
// that admits before it evicts is bounded only on average. One larger than
// the whole bound is not stored at all, and one replacing an entry gives
// back what that entry held before counting itself. Caller holds the lock.
func (c *embSubs) admit(key string, data []byte) {
	if len(data) > embSubCacheMax {
		return
	}
	if c.cache == nil {
		c.cache = map[string][]byte{}
	}
	if old, ok := c.cache[key]; ok {
		c.total -= len(old)
		delete(c.cache, key)
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

// progress says how far a read for this item has got, as a fraction of the
// file: the bytes ffmpeg has read, which is the whole of what the read costs.
// Where nothing is reading for the item it says so; where nobody can say how
// much the process has read yet, the read is under way at nought.
func (c *embSubs) progress(id string) (float64, bool) {
	c.mu.Lock()
	var e *embSub
	for _, x := range c.inflight {
		if x.id == id {
			e = x
			break
		}
	}
	c.mu.Unlock()
	if e == nil {
		return 0, false
	}
	pid := e.pid.Load()
	if pid == 0 || e.size <= 0 {
		return 0, true
	}
	n, ok := bytesRead(int(pid))
	if !ok {
		return 0, true
	}
	return min(float64(n)/float64(e.size), 0.99), true
}

// runEmbSub reads the file once and writes each stream asked for as WebVTT
// into a file of its own, which is the only way one ffmpeg hands back more
// than one output; each is bounded (-fs) the way a single one is. A stream
// that produced nothing is left out of the answer, and the answer is an
// error only where nothing at all came out.
func (s *Server) runEmbSub(ctx context.Context, it library.Item, streams []int, e *embSub) (map[int][]byte, error) {
	ffmpeg := s.thumbs.FFmpegPath()
	if ffmpeg == "" {
		return nil, fmt.Errorf("no ffmpeg")
	}
	ctx, cancel := context.WithTimeout(ctx, embSubBudget(it.Size))
	defer cancel()
	// A slot first, and outside the cache's lock: the whole container is
	// read for a few kilobytes of text a track, and several of those at once
	// on one disk is the playback this read is meant to be racing gently.
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
	dir, err := os.MkdirTemp("", "subs-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	named := func(stream int) string { return filepath.Join(dir, strconv.Itoa(stream)+".vtt") }

	args := []string{"-v", "error", "-nostdin"}
	args = append(args, in.args...)
	for _, st := range streams {
		args = append(args, "-map", "0:s:"+strconv.Itoa(st), "-c:s", "webvtt",
			"-fs", strconv.Itoa(subtitleMaxBytes+1), "-f", "webvtt", named(st))
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stdin = stdin
	errBuf := boundedBuffer{max: 64 << 10}
	cmd.Stderr = &errBuf
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("extract subtitles: %w", err)
	}
	if e != nil {
		e.pid.Store(int64(cmd.Process.Pid))
		defer e.pid.Store(0)
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("extract subtitles: %w: %s", err, bytes.TrimSpace(errBuf.Bytes()))
	}
	out := map[int][]byte{}
	for _, st := range streams {
		fi, err := os.Stat(named(st))
		if err != nil || fi.Size() == 0 || fi.Size() > subtitleMaxBytes {
			continue
		}
		if data, err := os.ReadFile(named(st)); err == nil {
			out[st] = data
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("extract subtitles: nothing came out")
	}
	return out, nil
}
