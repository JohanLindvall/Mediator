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

type embSubs struct {
	mu       sync.Mutex
	cache    map[string][]byte
	total    int
	inflight map[string]chan struct{}
}

// extract returns the stream as WebVTT, reading the file only the first time.
func (s *Server) extractEmbSub(ctx context.Context, it library.Item, stream int) ([]byte, error) {
	key := fmt.Sprintf("%s|%d|%d|%d", it.ID, it.ModTime, it.Size, stream)
	for {
		s.embsubs.mu.Lock()
		if s.embsubs.cache == nil {
			s.embsubs.cache = map[string][]byte{}
			s.embsubs.inflight = map[string]chan struct{}{}
		}
		if v, ok := s.embsubs.cache[key]; ok {
			s.embsubs.mu.Unlock()
			return v, nil
		}
		if ch, ok := s.embsubs.inflight[key]; ok {
			// Somebody is already reading the film for this; wait for them
			// rather than reading it again beside them.
			s.embsubs.mu.Unlock()
			select {
			case <-ch:
				continue // the answer (or its failure) is settled; re-check
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		s.embsubs.inflight[key] = ch
		s.embsubs.mu.Unlock()

		data, err := s.runEmbSub(ctx, it, stream)
		s.embsubs.mu.Lock()
		delete(s.embsubs.inflight, key)
		close(ch)
		if err == nil {
			s.embsubs.admit(key, data)
		}
		s.embsubs.mu.Unlock()
		return data, err
	}
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
