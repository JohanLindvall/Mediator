package server

// A file can declare a pixel aspect ratio that ffmpeg will not accept, and
// then nothing can be made from it at all.
//
// Measured on a film here — one that plays perfectly well in some browsers —
// the container says the pixels are square and the bitstream says the ratio
// is -35:3. ffmpeg configures its filter graph from what the stream
// declares, so every still, every conversion and every segmented session
// died on "Value -11.666667 for parameter 'pixel_aspect' out of range"
// before a frame was read: no tile, no hover preview, no scrub bar, and a
// player that had nowhere left to go and said the format could not be
// played.
//
// The bytes are fine. So a couple of things are copied through the
// bitstream filter that rewrites that declaration — no decoding, no
// re-encoding — and whatever needed the picture reads the copy instead.

import (
	"context"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// aspectRefused reports whether ffmpeg gave up over the declared pixel
// aspect. Matched on the parameter's own name, which is what ffmpeg puts in
// every wording of the complaint ("Setting 'pixel_aspect' to value '-35/3'",
// then the range, then the failure to apply it).
func aspectRefused(stderr string) bool {
	return strings.Contains(stderr, "pixel_aspect")
}

// badAspect remembers the files whose declaration was refused, for the run,
// keyed by identity like every other per-file verdict here. Nothing is
// written down: a file is judged again after a restart, at the cost of one
// failed attempt, and a file that is replaced on disk is a different key.
type badAspect struct {
	mu   sync.Mutex
	seen map[string]bool
}

func aspectKey(it library.Item) string {
	return it.ID + "|" + strconv.FormatInt(it.ModTime, 10) + "|" + strconv.FormatInt(it.Size, 10)
}

func (b *badAspect) note(it library.Item) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	b.seen[aspectKey(it)] = true
}

func (b *badAspect) has(it library.Item) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen[aspectKey(it)]
}

// repairSeconds is how much of a film is copied to take one still from.
// Enough to be sure of a decodable picture after the keyframe the seek lands
// on, and little enough that the copy is a disk read and nothing more.
const repairSeconds = 2

// metadataFilter is the bitstream filter that rewrites what a stream says
// about itself, by codec. Only the two that have one: anything else is left
// as it is rather than guessed at.
func metadataFilter(vcodec string) string {
	switch vcodec {
	case "h264":
		return "h264_metadata"
	case "hevc":
		return "hevc_metadata"
	}
	return ""
}

// repairArgs copies the stream through that filter, seeking first, with the
// declaration replaced by square pixels.
//
// Matroska rather than MP4 deliberately: the same copy into an MP4 came back
// out with the bad ratio still on it, so only this container actually
// carries the repair. secs bounds the copy where one picture is wanted; zero
// copies to the end, which is what a conversion reads.
func repairArgs(it library.Item, t float64, secs int, out string) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if t > 0 {
		args = append(args, "-ss", strconv.FormatFloat(t, 'f', 3, 64))
	}
	args = append(args, "-i", it.Path, "-map", "0:v:0", "-map", "0:a?")
	if secs > 0 {
		args = append(args, "-t", strconv.Itoa(secs))
	}
	return append(args,
		"-c", "copy", "-bsf:v", metadataFilter(it.VCodec)+"=sample_aspect_ratio=1/1",
		"-f", "matroska", "-y", out)
}

// aspects is what has been found to declare an aspect ffmpeg refuses. One
// per process, as the reorder verdicts are, and shared by the thumbnailer
// and the converters — whichever of them meets a file first tells the
// others, so a tile made through the repair spares the player from
// discovering the same thing again.
var aspects badAspect

// startRepair runs that copy into a pipe, for a conversion to read instead
// of the file. Closing the reader ends the process and reaps it: an
// abandoned copy of a film is a process that would otherwise go on reading
// a disk nobody is waiting for.
func startRepair(ctx context.Context, ffmpeg string, it library.Item, t float64) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, repairArgs(it, t, 0, "pipe:1")...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Its complaints go nowhere: what matters is whether the conversion
	// reading this produced anything, which its own caller already judges.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &repairReader{ReadCloser: out, cmd: cmd}, nil
}

type repairReader struct {
	io.ReadCloser
	cmd  *exec.Cmd
	once sync.Once
}

func (r *repairReader) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(func() {
		_ = r.cmd.Process.Kill()
		_ = r.cmd.Wait()
	})
	return err
}
