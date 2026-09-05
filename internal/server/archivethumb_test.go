package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// archivedItem indexes payload as the single member of a store-mode volume
// set and returns the archived item. An item's archive entry is unexported,
// so this is the only way to come by one: it has to be scanned out of a real
// set, the same way the library builds it.
func archivedItem(t *testing.T, payload []byte, member string) library.Item {
	t.Helper()
	_, it := archivedLibrary(t, payload, member)
	return it
}

// archivedLibrary is the same, handing back the library too — which the
// loopback tests need, because the stream endpoint resolves items through it.
func archivedLibrary(t *testing.T, payload []byte, member string) (*library.Library, library.Item) {
	t.Helper()
	dir := t.TempDir()
	rartest.WriteSet(t, dir, "release", member, payload, 3, false)
	lib := library.New([]string{dir}, testLog())
	lib.Scan(nil)
	items := lib.List(library.Query{}).Items
	if len(items) != 1 || !items[0].Archived() {
		t.Fatalf("indexed %d items, want one archived member", len(items))
	}
	return lib, items[0]
}

// archivedClip is the same thing holding a real, decodable video.
func archivedClip(t *testing.T, seconds int) library.Item {
	t.Helper()
	_, it := archivedClipLibrary(t, seconds)
	return it
}

func archivedClipLibrary(t *testing.T, seconds int) (*library.Library, library.Item) {
	t.Helper()
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	writeClip(t, clip, seconds)
	payload, err := os.ReadFile(clip)
	if err != nil {
		t.Fatal(err)
	}
	return archivedLibrary(t, payload, "Feature.mkv")
}

// withLoopback points the library at a base URL for the duration of a test,
// the way main does once the listener is bound.
func withLoopback(t *testing.T, base string) {
	t.Helper()
	t.Cleanup(func() { library.SetLoopback("") })
	library.SetLoopback(base)
}

// The seeking recipe is the whole design of the archived thumbnail: a real
// offset into a real seekable input, which is what a URL backed by Range
// requests gives and a pipe never can. Locked down here because it runs
// everywhere, ffmpeg or no ffmpeg.
func TestArchiveSeekArgs(t *testing.T) {
	args := archiveSeekArgs("out.jpg", "http://127.0.0.1:9/api/stream/abc", 360, 612.5)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-noaccurate_seek",                     // the keyframe at or before is the same picture
		"-ss 612.500",                          // an offset into the film, not into its first minute
		"-i http://127.0.0.1:9/api/stream/abc", // ... which only a seekable input allows
		"-headers " + library.InternalHeader,   // our own read; not playback
		"-rw_timeout 20000000",                 // a wedged socket must not eat the budget
		"scale=360:-2",                         // the requested width
		"-frames:v 1",                          // one image out of one run
		"out.jpg",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in: %s", want, joined)
		}
	}
	// Nothing samples and nothing scores: with a seek there is one candidate
	// and it is the one that was asked for.
	for _, unwanted := range []string{"pipe:0", "thumbnail=", "fps=1/"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("the seeking recipe still carries %q: %s", unwanted, joined)
		}
	}
	// -ss must reach ffmpeg before -i or it becomes an output seek, which
	// decodes everything up to the offset instead of ranging to it.
	if indexOf(args, "-ss") > indexOf(args, "-i") {
		t.Fatalf("-ss must precede -i: %s", joined)
	}
	if indexOf(args, "-headers") > indexOf(args, "-i") {
		t.Fatalf("-headers is a protocol option and must precede -i: %s", joined)
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return len(args)
}

// The piped fallback: no seek is possible, so it samples a window and lets
// the thumbnail filter score the samples. Keyframes only — measured at 1.44 s
// against 6.13 s for decoding every frame, and it picked the better frame.
func TestArchivePipeArgs(t *testing.T) {
	joined := strings.Join(archivePipeArgs("out.jpg", 360), " ")
	for _, want := range []string{
		"-skip_frame nokey -t 60", // a decoder option, ahead of its input
		"-i pipe:0",               // the member has no path; its bytes arrive on stdin
		"fps=1/10",                // one candidate every ten seconds
		"scale=360:-2",
		"thumbnail=30", // ... scored, and the least ordinary one kept
		"-frames:v 1",
		"out.jpg",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in: %s", want, joined)
		}
	}
	// -nostdin belongs to the recipes that read a path; here stdin is the input.
	if strings.Contains(joined, "-nostdin") {
		t.Fatalf("stdin is the input, it cannot be disabled: %s", joined)
	}
}

// The frame policy, which is where the measurements live: a fraction of the
// way in, floored so a short item is not sampled in its first seconds and
// capped so the floor cannot seek past the end.
func TestThumbOffsets(t *testing.T) {
	for _, c := range []struct {
		name          string
		durationMs    int64
		first, second float64
	}{
		// A feature: 10% is 10 minutes in, long past the measured 0-240 s
		// of front matter.
		{"feature", 100 * 60 * 1000, 600, 2100},
		// A 46-minute episode: 10% is 276 s, still past it.
		{"episode", 46 * 60 * 1000, 276, 966},
		// A quarter of an hour: 10% would be 90 s, so the floor lifts it.
		{"short", 15 * 60 * 1000, 120, 315},
		// A clip: the floor would seek past the end, so the midpoint cap
		// takes over.
		{"clip", 6000, 3, 2.1},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := thumbOffsets(c.durationMs)
			if len(got) != 2 {
				t.Fatalf("got %d offsets, want two", len(got))
			}
			if math.Abs(got[0]-c.first) > 0.001 || math.Abs(got[1]-c.second) > 0.001 {
				t.Fatalf("offsets %v, want [%v %v]", got, c.first, c.second)
			}
			if got[0] > float64(c.durationMs)/1000 {
				t.Fatalf("first offset %v is past the end of a %d ms item", got[0], c.durationMs)
			}
		})
	}
}

// uniformJPEG and variedJPEG stand in for what ffmpeg writes: the flat frame
// of a release's opening black or logo card, and an ordinary picture.
func uniformJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 160, 90))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func variedJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 160, 90))
	for y := range 90 {
		for x := range 160 {
			if (x/8+y/8)%2 == 0 {
				img.SetGray(x, y, color.Gray{Y: 0xf0})
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// colourJPEG is the shape ffmpeg actually writes — three planes, so
// jpeg.Decode hands back a *image.YCbCr and the luma fast path is the one
// that runs. flat: one colour everywhere.
func colourJPEG(t *testing.T, flat bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 160, 90))
	for y := range 90 {
		for x := range 160 {
			c := color.RGBA{R: 20, G: 24, B: 40, A: 0xff}
			if !flat && (x/8+y/8)%2 == 0 {
				c = color.RGBA{R: 210, G: 180, B: 90, A: 0xff}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The frames the thumbnail is judged on have to be judged the way the fix
// judges them, so the fixtures are pinned against the threshold itself.
func TestLumaStdDevSeparatesFlatFromPicture(t *testing.T) {
	for _, c := range []struct {
		name string
		flat []byte
		pic  []byte
	}{
		{"grayscale", uniformJPEG(t), variedJPEG(t)},
		{"colour", colourJPEG(t, true), colourJPEG(t, false)},
	} {
		t.Run(c.name, func(t *testing.T) {
			flat, err := lumaStdDev(c.flat)
			if err != nil {
				t.Fatal(err)
			}
			if flat >= archiveThumbMinStdDev {
				t.Fatalf("a single-colour frame scored %v, at or above the threshold %v",
					flat, archiveThumbMinStdDev)
			}
			picture, err := lumaStdDev(c.pic)
			if err != nil {
				t.Fatal(err)
			}
			if picture < archiveThumbMinStdDev {
				t.Fatalf("a picture scored %v, below the threshold %v",
					picture, archiveThumbMinStdDev)
			}
		})
	}
}

// The rule that lets a frame be trusted at all, stated on its own: what
// ffmpeg finished writing ends in the end-of-image marker, and what it was
// killed in the middle of does not.
func TestCompleteJPEG(t *testing.T) {
	full := variedJPEG(t)
	if !completeJPEG(full) {
		t.Fatal("a finished JPEG was rejected")
	}
	for _, cut := range []int{1, 2, 3, 32, 512} {
		if completeJPEG(full[:len(full)-cut]) {
			t.Fatalf("a JPEG missing its last %d bytes passed as complete", cut)
		}
	}
	// Decodable, but not what ffmpeg wrote: the marker is not the last thing
	// in the file, so something else got in.
	if completeJPEG(append(append([]byte{}, full...), 0x00, 0x11)) {
		t.Fatal("trailing bytes after the end marker passed as complete")
	}
	if completeJPEG(nil) || completeJPEG([]byte{0xFF, 0xD9}) {
		t.Fatal("a stub passed as complete")
	}
}

// ffRun is one recorded invocation of the stand-in ffmpeg: the command line
// it was given and how many bytes it was handed on standard input. The byte
// count is how a test sees whether anything was pulled out of the volume set
// through a pipe; the command line is how it sees which recipe ran.
type ffRun struct {
	args  string
	stdin int
}

// fakeFFmpeg installs a stand-in for ffmpeg on th: a script that drains its
// standard input, records the run, and copies a prepared file to the output
// path — the last argument of every recipe here. frames[i] is what run i+1
// writes; the last entry repeats, and a nil entry means "write nothing".
func fakeFFmpeg(t *testing.T, th *Thumbnailer, stall bool, frames ...[]byte) func() []ffRun {
	t.Helper()
	dir := t.TempDir()
	counts := filepath.Join(dir, "runs")
	argv := filepath.Join(dir, "argv")
	list := filepath.Join(dir, "frames")

	var names []string
	for i, f := range frames {
		name := "-"
		if f != nil {
			name = filepath.Join(dir, fmt.Sprintf("frame%d.jpg", i))
			if err := os.WriteFile(name, f, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		names = append(names, name)
	}
	if err := os.WriteFile(list, []byte(strings.Join(names, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sleep := ""
	if stall {
		sleep = "sleep 5\n"
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do out=$a; done\n" +
		// One line per run: the -headers value carries a CRLF of its own.
		"printf '%s' \"$*\" | tr -d '\\r\\n' >> " + argv + "\n" +
		"printf '\\n' >> " + argv + "\n" +
		"n=$(cat | wc -c)\n" +
		"printf '%s\\n' \"$n\" >> " + counts + "\n" +
		sleep +
		"runs=$(wc -l < " + counts + ")\n" +
		"total=$(wc -l < " + list + ")\n" +
		"[ \"$runs\" -gt \"$total\" ] && runs=$total\n" +
		"src=$(sed -n \"${runs}p\" " + list + ")\n" +
		"[ \"$src\" = \"-\" ] && exit 1\n" +
		"cp \"$src\" \"$out\"\n"
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	th.ffmpeg = path
	return func() []ffRun {
		lines, err := os.ReadFile(argv)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(counts)
		if err != nil {
			return nil
		}
		var out []ffRun
		nums := strings.Fields(string(data))
		for i, l := range strings.Split(strings.TrimRight(string(lines), "\n"), "\n") {
			r := ffRun{args: l}
			if i < len(nums) {
				if n, err := strconv.Atoi(nums[i]); err == nil {
					r.stdin = n
				}
			}
			out = append(out, r)
		}
		return out
	}
}

// archivedFeature is an archived item that claims to be a hundred minutes
// long. The payload is filler, so nothing decodes it — these tests are about
// which recipe runs and where it seeks, which the stand-in ffmpeg records.
func archivedFeature(t *testing.T, payload int) library.Item {
	t.Helper()
	it := archivedItem(t, rartest.Payload(payload), "Feature.mkv")
	it.Duration = 100 * 60 * 1000
	return it
}

// The point of the whole rework: with a seekable input the frame comes from a
// tenth of the way into the film, where the picture is, instead of from a
// prefix that can only ever reach the front matter.
func TestArchivedThumbSeeksIntoTheFilm(t *testing.T) {
	withLoopback(t, "http://127.0.0.1:9")
	it := archivedFeature(t, 8<<10)

	th := NewThumbnailer(nil, nil, testLog())
	runs := fakeFFmpeg(t, th, false, variedJPEG(t))

	data, err := th.Get(context.Background(), it, 160)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, variedJPEG(t)) {
		t.Fatal("the frame returned is not the one ffmpeg wrote")
	}
	got := runs()
	if len(got) != 1 {
		t.Fatalf("ffmpeg ran %d times (%v), want one seek", len(got), got)
	}
	if !strings.Contains(got[0].args, "-ss 600.000") {
		t.Fatalf("did not seek to a tenth of the duration: %s", got[0].args)
	}
	if !strings.Contains(got[0].args, "http://127.0.0.1:9/api/stream/"+it.ID) {
		t.Fatalf("did not read the item over the loopback stream URL: %s", got[0].args)
	}
	if strings.Contains(got[0].args, "pipe:0") {
		t.Fatalf("still piping a prefix: %s", got[0].args)
	}
	if got[0].stdin != 0 {
		t.Fatalf("%d bytes were pulled out of the volume set through a pipe", got[0].stdin)
	}
}

// A flat frame buys exactly one more seek, at a different fraction — not a
// wider prefix, which is what the old ladder did and what could never reach
// past the first minute anyway.
func TestArchivedThumbRetriesAtAnotherOffset(t *testing.T) {
	withLoopback(t, "http://127.0.0.1:9")
	it := archivedFeature(t, 8<<10)
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	th := NewThumbnailer(store, nil, testLog())
	good := variedJPEG(t)
	runs := fakeFFmpeg(t, th, false, uniformJPEG(t), good)

	data, err := th.Get(context.Background(), it, 160)
	if err != nil {
		t.Fatalf("the second offset did not produce a thumbnail: %v", err)
	}
	if !bytes.Equal(data, good) {
		t.Fatal("the flat first frame was returned instead of the second one")
	}
	if stored, ok := store.GetThumb(it.ID, it.ModTime, it.Size, 160); !ok || !bytes.Equal(stored, good) {
		t.Fatal("the second frame was not the one stored")
	}
	got := runs()
	if len(got) != 2 {
		t.Fatalf("ffmpeg ran %d times (%v), want two seeks", len(got), got)
	}
	if !strings.Contains(got[0].args, "-ss 600.000") || !strings.Contains(got[1].args, "-ss 2100.000") {
		t.Fatalf("the two attempts did not seek to 10%% and 35%%: %v", got)
	}
}

// Two attempts and no more: a tile is not worth a third pull out of a volume
// set, and a flat frame must never be stored.
func TestArchivedThumbGivesUpAfterTwoOffsets(t *testing.T) {
	withLoopback(t, "http://127.0.0.1:9")
	it := archivedFeature(t, 8<<10)
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	th := NewThumbnailer(store, nil, testLog())
	runs := fakeFFmpeg(t, th, false, uniformJPEG(t))

	if _, err := th.Get(context.Background(), it, 160); !errors.Is(err, ErrNoThumb) {
		t.Fatalf("err = %v, want ErrNoThumb", err)
	}
	if _, ok := store.GetThumb(it.ID, it.ModTime, it.Size, 160); ok {
		t.Fatal("a flat frame was stored permanently")
	}
	if got := runs(); len(got) != 2 {
		t.Fatalf("ffmpeg ran %d times (%v), want exactly the two offsets", len(got), got)
	}
}

// Without a loopback address there is no seekable input, and the piped
// prefix is what is left. It is the fallback, and it must still work.
func TestArchivedThumbFallsBackToPipeWithoutLoopback(t *testing.T) {
	it := archivedFeature(t, 8<<10) // no withLoopback: nothing to seek over

	th := NewThumbnailer(nil, nil, testLog())
	runs := fakeFFmpeg(t, th, false, variedJPEG(t))

	if _, err := th.Get(context.Background(), it, 160); err != nil {
		t.Fatal(err)
	}
	got := runs()
	if len(got) != 1 {
		t.Fatalf("ffmpeg ran %d times (%v), want one piped pass", len(got), got)
	}
	if !strings.Contains(got[0].args, "pipe:0") || strings.Contains(got[0].args, "-ss ") {
		t.Fatalf("expected the piped fallback, got: %s", got[0].args)
	}
	if got[0].stdin != 8<<10 {
		t.Fatalf("the pass drew %d bytes, want the whole 8192-byte member", got[0].stdin)
	}
}

// A loopback address is not enough: the offset is a fraction of the length,
// so an item whose length nothing can determine has no fraction to take, and
// sampling a prefix is the tool that does not need one.
func TestArchivedThumbFallsBackToPipeWithoutDuration(t *testing.T) {
	// Port 9 refuses connections, so the duration probe comes back empty.
	withLoopback(t, "http://127.0.0.1:9")
	it := archivedItem(t, rartest.Payload(8<<10), "Feature.mkv")
	if it.Duration != 0 {
		t.Fatalf("duration = %d; this test needs an item of unknown length", it.Duration)
	}

	th := NewThumbnailer(nil, nil, testLog())
	runs := fakeFFmpeg(t, th, false, variedJPEG(t))

	if _, err := th.Get(context.Background(), it, 160); err != nil {
		t.Fatal(err)
	}
	got := runs()
	if len(got) != 1 || !strings.Contains(got[0].args, "pipe:0") {
		t.Fatalf("expected the piped fallback, got: %v", got)
	}
}

// A torn frame must never be stored: for a member of a completed volume set
// the key never rotates, so a torn image would be permanent in the database
// and in every browser that saw it.
//
// Two guards stand behind that, and this pins both. A JPEG cut short is
// refused — by completeJPEG, and by the decoder behind it, which measured
// here rejects even a one-byte truncation, so that half of the test does not
// tell the two apart. A JPEG with anything after its end marker is not what
// ffmpeg wrote either, and there only completeJPEG refuses it: the decoder
// stops at the marker and ignores the rest.
func TestArchivedThumbNeverStoresTornFrame(t *testing.T) {
	full := variedJPEG(t)
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"cut short", full[:len(full)-32]},
		{"trailing bytes", append(append([]byte{}, full...), 0x00, 0x11)},
	} {
		t.Run(c.name, func(t *testing.T) {
			it := archivedFeature(t, 8<<10)
			store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			th := NewThumbnailer(store, nil, testLog())
			fakeFFmpeg(t, th, false, c.data)

			if _, err := th.Get(context.Background(), it, 160); !errors.Is(err, ErrNoThumb) {
				t.Fatalf("err = %v, want ErrNoThumb", err)
			}
			if data, ok := store.GetThumb(it.ID, it.ModTime, it.Size, 160); ok {
				t.Fatalf("a torn frame was stored permanently (%d bytes)", len(data))
			}
		})
	}
}

// The timeout exists for a busy disk, so firing it says nothing about the
// item — least of all "this item has no thumbnail, for the life of the
// process", which is what the negative cache means. And the ffmpeg slot has
// to come back: the run is dead, so nothing may still be holding it.
func TestArchivedThumbTimeoutIsNotNegativeCached(t *testing.T) {
	withLoopback(t, "http://127.0.0.1:9")
	it := archivedFeature(t, 8<<10)
	th := NewThumbnailer(nil, nil, testLog())
	fakeFFmpeg(t, th, true, variedJPEG(t))

	old := archiveThumbTimeout
	archiveThumbTimeout = 100 * time.Millisecond
	t.Cleanup(func() { archiveThumbTimeout = old })

	if _, err := th.Get(context.Background(), it, 160); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if len(th.ffSem) != 0 {
		t.Fatal("an ffmpeg slot is still held after the run was killed")
	}

	// The disk is quiet again: the same item must be tried again, not
	// written off.
	archiveThumbTimeout = old
	fakeFFmpeg(t, th, false, variedJPEG(t))
	data, err := th.Get(context.Background(), it, 160)
	if err != nil {
		t.Fatalf("the item was written off after a timeout: %v", err)
	}
	if !bytes.Equal(data, variedJPEG(t)) {
		t.Fatal("the retry did not return the frame ffmpeg produced")
	}
}

// The same thing with the real encoder in the loop: a genuinely black video
// produces a genuinely black frame, and it must not be remembered.
func TestArchivedBlackVideoIsNotStored(t *testing.T) {
	clip := filepath.Join(t.TempDir(), "black.mkv")
	writeBlackClip(t, clip, 6)
	payload, err := os.ReadFile(clip)
	if err != nil {
		t.Fatal(err)
	}
	it := archivedItem(t, payload, "Feature.mkv")
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	th := NewThumbnailer(store, nil, testLog())
	if _, err := th.Get(context.Background(), it, 160); !errors.Is(err, ErrNoThumb) {
		t.Fatalf("err = %v, want ErrNoThumb", err)
	}
	if _, ok := store.GetThumb(it.ID, it.ModTime, it.Size, 160); ok {
		t.Fatal("a black frame was stored permanently")
	}
}

// The reported case, over the piped fallback: a video that only exists as
// byte ranges inside a volume set still gets a tile, and it is cached like
// any other.
func TestArchivedVideoThumbnail(t *testing.T) {
	it := archivedClip(t, 6)
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	data, err := NewThumbnailer(store, nil, testLog()).Get(context.Background(), it, 160)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || cfg.Width != 160 {
		t.Fatalf("got a %s of %dx%d, want a 160-wide jpeg", format, cfg.Width, cfg.Height)
	}

	// The second request must not spawn ffmpeg again: a thumbnailer with no
	// ffmpeg at all can still serve it from the store.
	th := NewThumbnailer(store, nil, testLog())
	th.ffmpeg = ""
	again, err := th.Get(context.Background(), it, 160)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data) {
		t.Fatal("the cached thumbnail differs from the generated one")
	}
}

// loopbackServer runs the real handler over loopback and counts the stream
// requests that carried the internal marker — which is how these tests see
// that ffmpeg really did read through the server rather than through a pipe.
func loopbackServer(t *testing.T, lib *library.Library) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var internal atomic.Int64
	srv := New(lib, nil, NewThumbnailer(nil, nil, testLog()), NewRemuxer("", NewScratch("", 0), testLog()), NewHLS("", lib, NewScratch("", 0), testLog()), nil, os.DirFS(t.TempDir()), testLog())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/stream/") &&
			r.Header.Get(library.InternalHeader) == library.InternalToken() {
			internal.Add(1)
		}
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	withLoopback(t, ts.URL)
	return ts, &internal
}

// End to end with the real ffmpeg and the real endpoint: the thumbnail for an
// archived member is decoded out of bytes that came back over HTTP Range,
// from an offset a pipe could never have reached.
func TestArchivedVideoThumbnailOverLoopback(t *testing.T) {
	lib, it := archivedClipLibrary(t, 6)
	_, internal := loopbackServer(t, lib)
	it.Duration = 6000

	data, err := NewThumbnailer(nil, nil, testLog()).Get(context.Background(), it, 160)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || cfg.Width != 160 {
		t.Fatalf("got a %s of %dx%d, want a 160-wide jpeg", format, cfg.Width, cfg.Height)
	}
	if n := internal.Load(); n == 0 {
		t.Fatal("the frame did not come through the loopback stream endpoint")
	}
}

// The metadata probe takes the same road, and for the same reason: the
// container's index sits at the end of the file, where no prefix reaches.
func TestArchivedProbeOverLoopback(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	lib, it := archivedClipLibrary(t, 6)
	_, internal := loopbackServer(t, lib)

	p := library.ProbeMedia(context.Background(), it)
	if !p.Probed {
		t.Fatal("the probe did not report an answer")
	}
	if p.VCodec == "" {
		t.Fatal("the probe learned no codec")
	}
	if p.DurationMs < 4000 || p.DurationMs > 8000 {
		t.Fatalf("duration = %d ms, want about 6000", p.DurationMs)
	}
	if n := internal.Load(); n == 0 {
		t.Fatal("the probe did not read through the loopback stream endpoint")
	}
}

// Grid cells abort their thumbnail fetch whenever they scroll out of view,
// so this happens constantly. An abort says nothing about the item: it must
// not be written off.
func TestArchivedThumbCancelIsNotNegativeCached(t *testing.T) {
	it := archivedClip(t, 6)
	th := NewThumbnailer(nil, nil, testLog())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := th.Get(ctx, it, 160); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	data, err := th.Get(context.Background(), it, 160)
	if err != nil {
		t.Fatalf("the item was written off after an abort: %v", err)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || format != "jpeg" {
		t.Fatalf("second attempt produced %s (%v)", format, err)
	}
}

// Without ffmpeg the archived branch is unreachable and the endpoint answers
// exactly as it did before there was one.
func TestArchivedThumbNeedsFFmpeg(t *testing.T) {
	it := archivedItem(t, rartest.Payload(30_000), "Feature.mkv")
	th := NewThumbnailer(nil, nil, testLog())
	th.ffmpeg = ""
	if _, err := th.Get(context.Background(), it, 160); !errors.Is(err, ErrNoThumb) {
		t.Fatalf("err = %v, want ErrNoThumb", err)
	}
}

// Seeking can fail for reasons that say nothing about the film — an ffmpeg
// built without the http protocol, a loopback this process cannot reach. The
// piped path is right there and still works, so a dead input must fall back
// to it rather than leave the item with no thumbnail. Flat frames are the
// opposite case (TestArchivedThumbGivesUpAfterTwoOffsets): those are an
// answer, and piping would sample the opening minute the seek exists to skip.
func TestArchivedThumbFallsBackWhenSeekingProducesNothing(t *testing.T) {
	withLoopback(t, "http://127.0.0.1:9")
	it := archivedFeature(t, 8<<10)
	store, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	th := NewThumbnailer(store, nil, testLog())
	// Neither seek writes a frame; the piped attempt that follows does.
	runs := fakeFFmpeg(t, th, false, nil, nil, variedJPEG(t))

	data, err := th.Get(context.Background(), it, 160)
	if err != nil {
		t.Fatalf("no thumbnail even though the piped path works: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty thumbnail")
	}
	got := runs()
	if len(got) != 3 {
		t.Fatalf("ffmpeg ran %d times (%v), want two seeks then the pipe", len(got), got)
	}
	if !strings.Contains(got[2].args, "-i pipe:0") {
		t.Fatalf("the third attempt was not the piped fallback: %s", got[2].args)
	}
}
