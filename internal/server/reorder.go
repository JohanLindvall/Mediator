package server

// Streams that lie about how far their frames are reordered.
//
// H.264 carries `max_num_reorder_frames` in its parameter set: how many
// decoded frames a player must hold before it can present them in order. A
// stream that puts more B-frames between its references than that is lying
// about itself, and what happens next depends on how forgiving the player is.
// ffmpeg and VLC buffer generously and show it correctly. A browser that takes
// the declaration at its word emits frames too early and then drops the ones
// that arrive behind what it has already shown.
//
// Measured on the file this came from — an AVI holding H.264 with three
// B-frames between references and a declaration of one — Chrome reported
// "Dropping frame with timestamp 0.48 s, which is earlier than the last
// rendered frame (0.52 s)" once every four frames, which is 25 fps arriving
// as nineteen with a limp. The same file plays perfectly in VLC, and the copy
// this server made of it is faithful to the byte: the first forty frames
// identical and in the same order, every presentation gap exactly 40 ms.
// Nothing was wrong with the copy. The stream is wrong, and a copy preserves
// it exactly.
//
// So the cure is the one thing a copy is not: re-encoding, which rewrites the
// frame structure and the declaration together. Verified on the same file —
// re-encoded, its timestamps come out strictly in order and the stutter is
// gone.
//
// AVI is where this turns up, that container having no way to express
// reordering at all, so whatever its encoders wrote went unchecked. It is not
// asked of a television: a set fetches the file and decodes it with the same
// generosity VLC has, and re-encoding a film for a player that was never going
// to drop a frame would be paying the whole cost for nothing.

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

const (
	// reorderProbeFrames is how far in to look. The pattern repeats every few
	// frames, so this is many times more than it takes to see one, and it is
	// bounded because the answer must cost a read of the opening and not a
	// pass over the film.
	reorderProbeFrames = 60
	// reorderProbeTimeout bounds the look. A film that will not answer this
	// quickly is one we say nothing about, which leaves today's behaviour.
	reorderProbeTimeout = 20 * time.Second
)

// reorderCache remembers the answer for the run, keyed by the file's identity.
// One ffprobe of the opening per film per process: the same shape of cost
// EnsureCodecs already pays at the moment a film is opened, and for the same
// reason — it is the moment something has to decide how to serve it.
type reorderCache struct {
	mu   sync.Mutex
	seen map[string]bool
	// looking is the look in flight, by key. Opening a film fires several
	// requests at once — the item, the rewrap or the playlist — and each of
	// them asks this question about the same file, so without it the same
	// ffprobe ran two or three times over, against the very disk the
	// playback it is preparing is about to read from.
	looking map[string]chan struct{}
}

func (c *reorderCache) get(key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.seen[key]
	return v, ok
}

func (c *reorderCache) put(key string, v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[key] = v
}

// claim answers where the film has been judged, and otherwise either hands
// the caller the job — a nil channel — or the look already in flight, to
// wait on rather than run a second one beside it.
func (c *reorderCache) claim(key string) (v, known bool, wait chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.seen[key]; ok {
		return v, true, nil
	}
	if ch, running := c.looking[key]; running {
		return false, false, ch
	}
	if c.looking == nil {
		c.looking = map[string]chan struct{}{}
	}
	c.looking[key] = make(chan struct{})
	return false, false, nil
}

// settle writes down what the look found and releases whoever waited on it.
// A look that could not finish is released without a verdict: nothing is
// remembered, and whoever was waiting looks again with a budget of their
// own, which is what "interrupted is not answered" means here.
func (c *reorderCache) settle(key string, v, answered bool) {
	if answered {
		c.put(key, v)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.looking[key]; ok {
		delete(c.looking, key)
		close(ch)
	}
}

// mustReencode reports a picture that cannot be handed to a browser by
// copying — or played as it is, which is a copy by another route — however
// playable its codec is.
//
// ctx bounds the look, and a look that could not finish is not a verdict:
// it is not remembered, so the next asker — the same one with a longer
// budget, or the conversion with its own — looks again. It used to be
// cached as an acquittal, which with the short budget an item fetch gives
// it would have excused a film for the life of the process.
//
// One look at a time per film, too. Opening a film asks this from more than
// one place at once — the item endpoint, and behind it the rewrap or the
// first playlist — and each of them used to start an ffprobe of its own on
// the same opening, log the same line and write down the same verdict. A
// second asker waits for the first instead, and where the first came back
// with nothing it looks for itself, since that answer belongs to whoever
// asked with the budget that ran out and not to everyone behind them.
func (s *Server) mustReencode(ctx context.Context, it library.Item) bool {
	if it.Kind != library.KindVideo || it.VCodec == "" {
		return false
	}
	key := itemKey(it)
	for {
		v, known, wait := s.reorder.claim(key)
		if known {
			return v
		}
		if wait != nil {
			select {
			case <-wait:
				continue // settled, or nothing to settle; ask again
			case <-ctx.Done():
				return false
			}
		}
		v, answered := reorderUnderstated(ctx, it)
		s.reorder.settle(key, v, answered)
		if answered && v {
			s.log.Info("picture must be re-encoded rather than copied",
				"path", it.Rel, "why", "the stream reorders further than it declares")
		}
		return answered && v
	}
}

// reorderUnderstated runs the look itself. answered says the file was read
// and judged; without it the first answer means nothing, since no ffprobe,
// a pipe that cannot be read twice, a timeout and an unreadable document
// all leave the film exactly as it was.
func reorderUnderstated(ctx context.Context, it library.Item) (understated, answered bool) {
	probe := library.FFprobePath()
	if probe == "" {
		return false, false
	}
	in, _, err := convertInput(it, 0)
	if err != nil {
		return false, false
	}
	if in.pipe != nil {
		// The only view of this file is a pipe, which cannot be read twice
		// and would be spent on the question rather than on the answer.
		in.pipe.Close()
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, reorderProbeTimeout)
	defer cancel()
	args := append([]string{
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=has_b_frames:frame=pict_type,pts_time",
		"-read_intervals", "%+#" + strconv.Itoa(reorderProbeFrames),
		"-of", "json",
	}, in.args...)
	out, err := exec.CommandContext(ctx, probe, args...).Output()
	if err != nil || ctx.Err() != nil {
		return false, false // no answer is not an accusation
	}
	var doc struct {
		Frames []struct {
			Type string `json:"pict_type"`
			PTS  string `json:"pts_time"`
		} `json:"frames"`
		Streams []struct {
			Declared int `json:"has_b_frames"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &doc) != nil || len(doc.Streams) == 0 {
		return false, false
	}
	if len(doc.Frames) < 8 {
		return false, true // too short to reorder anything worth the name
	}
	run, longest, inversions := 0, 0, 0
	seen := -1.0
	for _, f := range doc.Frames {
		if f.Type == "B" {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
		// The frames come back in the order the decoder emitted them, which
		// is the order a player would show them — so a timestamp below one
		// already emitted is the fault itself, observed directly.
		if pts, err := strconv.ParseFloat(f.PTS, 64); err == nil {
			if pts < seen-0.001 {
				inversions++
			} else if pts > seen {
				seen = pts
			}
		}
	}
	return reorderVerdict(doc.Streams[0].Declared, longest, inversions), true
}

// reorderVerdict is the decision, pure so it can be tested against the
// numbers real files produce without those files.
//
// Two signals, either sufficient. **Emitted frames whose timestamps go
// backwards** are the fault observed directly: ffmpeg's decoder honours the
// declaration exactly as a browser does, so a stream that lies emits frames
// out of order right here in the probe. Two are required rather than one,
// since an edit-list oddity at the start of a file is one inversion and not
// a lie. **A B-run longer than the declaration** is the structural signal,
// and it is only trusted where the declaration is under two: three
// consecutive B-frames under a declaration of two is an ordinary B-pyramid —
// the middle frame is itself a reference and a delay of two is genuinely
// enough — and the first version of this rule, which had no such guard,
// accused every modern encode in the library of lying.
func reorderVerdict(declared, longestRun, inversions int) bool {
	if inversions >= 2 {
		return true
	}
	return declared < 2 && longestRun > declared
}
