package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"strings"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestRemuxable(t *testing.T) {
	cases := []struct {
		name string
		it   library.Item
		want bool
	}{
		{"h264 + aac in flv", library.Item{
			Kind: library.KindVideo, Name: "clip.flv", VCodec: "h264", ACodec: "aac"}, true},
		{"h264 + mp3 in avi", library.Item{
			Kind: library.KindVideo, Name: "clip.avi", VCodec: "h264", ACodec: "mp3"}, true},
		{"h264, no soundtrack", library.Item{
			Kind: library.KindVideo, Name: "clip.mkv", VCodec: "h264"}, true},
		// Rewrapping HEVC would trade a container one browser will not open
		// for a codec another cannot decode.
		{"hevc", library.Item{
			Kind: library.KindVideo, Name: "clip.mkv", VCodec: "hevc", ACodec: "aac"}, false},
		{"undecodable soundtrack", library.Item{
			Kind: library.KindVideo, Name: "clip.mkv", VCodec: "h264", ACodec: "eac3"}, false},
		// Already a container the browser opens: moving the streams into the
		// same box cannot be the answer, and with no file to read the label
		// off there is nothing else on offer either.
		{"already mp4", library.Item{
			Kind: library.KindVideo, Name: "clip.mp4", VCodec: "h264", ACodec: "aac"}, false},
		{"already mov", library.Item{
			Kind: library.KindVideo, Name: "clip.mov", VCodec: "h264", ACodec: "aac"}, false},
		// Never probed: an empty video codec is "not looked at", not "fine".
		{"codecs unknown", library.Item{
			Kind: library.KindVideo, Name: "clip.flv"}, false},
		{"not a video", library.Item{
			Kind: library.KindAudio, Name: "song.flv", VCodec: "h264", ACodec: "aac"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := remuxable(c.it); got != c.want {
				t.Fatalf("remuxable = %v, want %v", got, c.want)
			}
		})
	}
}

// An MP4 whose picture is HEVC is rewrappable exactly when it carries the
// label Apple refuses — and then the rewrap is the whole fix, since the
// phone decodes the stream itself.
func TestRemuxableRetagsHEVC(t *testing.T) {
	dir := t.TempDir()
	item := func(name, format string) library.Item {
		path := filepath.Join(dir, name)
		data := mp4WithVideoFormat(format)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return library.Item{
			Kind: library.KindVideo, Name: name, Path: path,
			Size: int64(len(data)), VCodec: "hevc", ACodec: "aac",
		}
	}
	if !remuxable(item("hev1.mp4", "hev1")) {
		t.Error("an hev1 MP4 should be rewrappable: the label is the only fault")
	}
	if remuxable(item("hvc1.mp4", "hvc1")) {
		t.Error("an hvc1 MP4 is already labelled correctly; a copy gains nothing")
	}
	if remuxable(item("avc1.mp4", "avc1")) {
		t.Error("H.264 in an MP4 plays everywhere; nothing to rewrap")
	}
}

// mp4WithVideoFormat builds the smallest ISO box tree carrying one video
// track with the given sample format.
func mp4WithVideoFormat(format string) []byte {
	box := func(typ string, parts ...[]byte) []byte {
		var body []byte
		for _, p := range parts {
			body = append(body, p...)
		}
		out := make([]byte, 8, 8+len(body))
		binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
		copy(out[4:8], typ)
		return append(out, body...)
	}
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, 1)
	hdlr := box("hdlr", make([]byte, 8), []byte("vide"), make([]byte, 13))
	stsd := box("stsd", make([]byte, 4), count, box(format, make([]byte, 78)))
	trak := box("trak", box("mdia", hdlr, box("minf", box("stbl", stsd))))
	return append(box("ftyp", []byte("isom")), box("moov", trak)...)
}

// writeFLV writes a real H.264 + AAC clip in a container no browser opens, or
// skips: ffmpeg is optional at runtime, and so are its encoders.
func writeFLV(t *testing.T, path string, seconds int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration="+strconv.Itoa(seconds),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build an h264/aac test clip: %v: %s", err, out)
	}
}

// writeMKV writes a real H.264 + AAC clip in Matroska — what Safari cannot
// open and what therefore has to be converted for it — or skips.
func writeMKV(t *testing.T, path string, seconds int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration="+strconv.Itoa(seconds),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a matroska test clip: %v: %s", err, out)
	}
}

// The whole point of rewrapping rather than converting: what comes back is an
// ordinary file. iOS refuses a media URL that cannot answer a range request,
// which is exactly what the live conversion cannot do.
// writeHEVC writes a real HEVC + AAC clip in an MP4, labelled the way ffmpeg
// labels one unless told otherwise — hev1, which Apple's decoders refuse —
// or skips: ffmpeg is optional at runtime, and an HEVC encoder more so.
func writeHEVC(t *testing.T, path string, seconds int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration="+strconv.Itoa(seconds),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds),
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=none",
		"-pix_fmt", "yuv420p", "-tag:v", "hev1",
		"-c:a", "aac", "-shortest", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build an hev1 test clip: %v: %s", err, out)
	}
	// The fixture has to actually carry the label the test is about: were a
	// later ffmpeg to start writing hvc1 by default, this would otherwise go
	// on passing while testing nothing.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := library.VideoSampleFormat(library.Item{Path: path, Size: st.Size()}); got != "hev1" {
		t.Skipf("this ffmpeg labels HEVC %q, not hev1: nothing for the rewrap to correct", got)
	}
}

// The whole point, end to end: an MP4 the browser opens and cannot decode,
// because of four bytes, comes back decodable without a frame being touched.
func TestRemuxRelabelsHEVC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	writeHEVC(t, path, 2)
	ts, lib := flagServer(t, dir)
	id := library.PathID(path)
	lib.EnrichNow(context.Background(), []string{id})

	res, err := http.Get(ts.URL + "/api/remux/" + id)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: an hev1 MP4 is exactly what a rewrap fixes", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("content type %q", ct)
	}

	out := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(out, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := library.VideoSampleFormat(library.Item{Path: out, Size: int64(len(body))}); got != "hvc1" {
		t.Fatalf("rewrapped label = %q, want hvc1 — the one thing this rewrap is for", got)
	}
	// Copied, not converted: an encode of two seconds of test pattern would
	// not come back the same length as what went in.
	in, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if ratio := float64(len(body)) / float64(in.Size()); ratio < 0.8 || ratio > 1.25 {
		t.Errorf("rewrap is %.2f× the source: that is a re-encode, not a copy", ratio)
	}
	// And it is still a faststart MP4, which is what lets playback begin on
	// the first bytes rather than after the whole file.
	if string(body[4:8]) != "ftyp" {
		t.Fatalf("not an MP4: % x", body[:min(len(body), 12)])
	}
	if !bytes.Contains(body[:min(len(body), 4096)], []byte("moov")) {
		t.Fatal("no moov near the front: faststart did not run")
	}
}

func TestRemuxServesRanges(t *testing.T) {
	dir := t.TempDir()
	writeFLV(t, filepath.Join(dir, "clip.flv"), 3)
	ts, lib := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "clip.flv"))
	// The codecs decide, so they have to be read first — which is what the
	// endpoint itself does; this just makes the test independent of timing.
	lib.EnrichNow(context.Background(), []string{id})

	res, err := http.Get(ts.URL + "/api/remux/" + id)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("content type %q", ct)
	}
	if ar := res.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes — iOS will not play this without it", ar)
	}
	if len(body) == 0 {
		t.Fatal("empty body")
	}
	// An MP4, with its index at the front so playback can start on the first
	// bytes rather than after the whole file has been fetched.
	if string(body[4:8]) != "ftyp" {
		t.Fatalf("not an MP4: % x", body[:min(len(body), 12)])
	}
	if !bytes.Contains(body[:min(len(body), 4096)], []byte("moov")) {
		t.Fatal("no moov near the front: faststart did not run")
	}

	// The range probe iOS opens with.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/remux/"+id, nil)
	req.Header.Set("Range", "bytes=0-1")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusPartialContent {
		t.Fatalf("range probe answered %d, want 206", res2.StatusCode)
	}
	if cr := res2.Header.Get("Content-Range"); cr == "" {
		t.Fatal("206 without Content-Range")
	}
}

// A file the browser can already open is not one rewrapping helps, and the
// 404 is what sends the player on to the converter.
func TestRemuxDeclinesWhatItCannotHelp(t *testing.T) {
	dir := t.TempDir()
	writeClip(t, filepath.Join(dir, "plain.mp4"), 2)
	ts, lib := flagServer(t, dir)
	id := library.PathID(filepath.Join(dir, "plain.mp4"))
	lib.EnrichNow(context.Background(), []string{id})

	res, err := http.Get(ts.URL + "/api/remux/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", res.StatusCode)
	}
}

// The rewrap is cached, and the second ask does not run ffmpeg again.
func TestRemuxCachesAndClosesClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.flv")
	writeFLV(t, path, 2)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	it := library.Item{
		ID: "flv1", Name: "clip.flv", Path: path, Kind: library.KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
		VCodec: "h264", ACodec: "aac",
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	r := NewRemuxer(ffmpeg, NewScratch("", 0), testLog())

	first, err := r.File(context.Background(), it, "", remuxCopy)
	if err != nil {
		t.Fatal(err)
	}
	stat1, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.File(context.Background(), it, "", remuxCopy)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second ask produced %q, want the cached %q", second, first)
	}
	stat2, _ := os.Stat(second)
	if !stat2.ModTime().Equal(stat1.ModTime()) {
		t.Fatal("the file was rewritten: the cache did not hold")
	}

	// Kept, not swept: closing leaves what was converted where it is, so the
	// next run finds it rather than converting the same film again.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("a finished rewrap did not survive Close: %v", err)
	}
}

// The point of keeping them: a later run picks up what an earlier one
// converted, and does not convert it a second time.
func TestRemuxSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.flv")
	writeFLV(t, path, 2)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	it := library.Item{
		ID: "flv9", Name: "clip.flv", Path: path, Kind: library.KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
		VCodec: "h264", ACodec: "aac",
	}

	base := t.TempDir()
	first := NewRemuxer(ffmpeg, NewScratch(base, 0), testLog())
	made, err := first.File(context.Background(), it, "", remuxCopy)
	if err != nil {
		t.Fatal(err)
	}
	stat1, err := os.Stat(made)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// A new run over the same working space.
	second := NewRemuxer(ffmpeg, NewScratch(base, 0), testLog())
	second.Adopt()
	again, err := second.File(context.Background(), it, "", remuxCopy)
	if err != nil {
		t.Fatal(err)
	}
	if again != made {
		t.Fatalf("the second run made %q instead of finding %q", again, made)
	}
	stat2, err := os.Stat(again)
	if err != nil {
		t.Fatal(err)
	}
	if !stat2.ModTime().Equal(stat1.ModTime()) {
		t.Fatal("the file was written again: the earlier run's work was not picked up")
	}
}

// A run interrupted mid-rewrap leaves a part-written file, and adopting that
// would serve a film that stops in the middle — which is the bug all of this
// was chasing in the first place.
func TestRemuxDiscardsAnInterruptedRewrap(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "remux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(dir, "abc-123-456-a0.mp4.part")
	if err := os.WriteFile(part, []byte("half a film"), 0o644); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "not-ours.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "abc-123-456-a0.mp4")
	if err := os.WriteFile(good, []byte("a whole film"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRemuxer("ffmpeg", NewScratch(base, 0), testLog())
	r.Adopt()

	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("a part-written rewrap was kept: %v", err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("something that is not a rewrap was kept: %v", err)
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("a finished rewrap was thrown away: %v", err)
	}
}

func TestRemuxKeyFromName(t *testing.T) {
	for _, c := range []struct {
		name string
		key  string
		ok   bool
	}{
		{"abc123-1700000000000-4096-a0.mp4", "abc123|1700000000000|4096|0|", true},
		{"abc-123-1700000000000-4096-a3.mp4", "abc-123|1700000000000|4096|3|", true},
		// The soundtrack-converted copy of the same film, which is a
		// different file and must not be served under the other's key.
		{"abc123-1700000000000-4096-a0-aac.mp4", "abc123|1700000000000|4096|0|aac", true},
		{"abc123-1700000000000-4096-a0.mp4.part", "", false},
		{"abc123-notanumber-4096-a0.mp4", "", false},
		{"abc123.mp4", "", false},
		{"-1-2-a0.mp4", "", false},
		{"anything.txt", "", false},
		// Written before the soundtrack was part of the name: not ours to
		// adopt, since nothing records which soundtrack is inside it.
		{"abc123-1700000000000-4096.mp4", "", false},
	} {
		key, ok := remuxKeyFromName(c.name)
		if ok != c.ok || key != c.key {
			t.Fatalf("remuxKeyFromName(%q) = %q,%v; want %q,%v", c.name, key, ok, c.key, c.ok)
		}
	}
}

// A request that goes away must not take the rewrap with it: Safari hangs up
// on its opening range probe, and the work has to be there for the next ask.
func TestRemuxSurvivesTheRequestThatStartedIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.flv")
	writeFLV(t, path, 2)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	it := library.Item{
		ID: "flv2", Name: "clip.flv", Path: path, Kind: library.KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
		VCodec: "h264", ACodec: "aac",
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	r := NewRemuxer(ffmpeg, NewScratch("", 0), testLog())
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.File(ctx, it, "", remuxCopy); err == nil {
		t.Fatal("a cancelled caller should not be handed a path")
	}
	// The work carries on regardless, so a caller who waits gets the file.
	if _, err := r.File(context.Background(), it, "", remuxCopy); err != nil {
		t.Fatalf("the rewrap did not survive the caller that started it: %v", err)
	}
}

func TestRemuxWithoutFFmpeg(t *testing.T) {
	r := NewRemuxer("", NewScratch("", 0), testLog())
	it := library.Item{Kind: library.KindVideo, Name: "c.flv", VCodec: "h264", ACodec: "aac"}
	if _, err := r.File(context.Background(), it, "", remuxCopy); err != ErrNoRemux {
		t.Fatalf("err = %v, want ErrNoRemux", err)
	}
}

// The budget prunes to make room — and never takes a file somebody is
// reading, because being over a number the operator chose is a smaller wrong
// than deleting a film out from under whoever is watching it.
func TestRemuxPruneSparesWhatIsInUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.flv")
	writeFLV(t, path, 2)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	// A budget of one file, so the second must displace the first.
	r := NewRemuxer(ffmpeg, NewScratch(t.TempDir(), info.Size()*3/2), testLog())
	defer r.Close()

	item := func(id string) library.Item {
		return library.Item{
			ID: id, Name: "clip.flv", Path: path, Kind: library.KindVideo,
			Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
			VCodec: "h264", ACodec: "aac",
		}
	}

	first, err := r.File(context.Background(), item("one"), "", remuxCopy)
	if err != nil {
		t.Fatal(err)
	}
	// Age it out of the protected window; without this nothing is prunable,
	// which is the other half of the contract and is asserted below.
	r.mu.Lock()
	for _, e := range r.entries {
		e.last = e.last.Add(-2 * remuxKeepFor)
	}
	r.mu.Unlock()

	if _, err := r.File(context.Background(), item("two"), "", remuxCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("the older file survived a budget it did not fit in: %v", err)
	}

	// Now hold the survivor and ask for a third: the held one must stay.
	held := ""
	r.mu.Lock()
	for _, e := range r.entries {
		held = e.path
		e.last = e.last.Add(-2 * remuxKeepFor)
	}
	r.mu.Unlock()
	release := r.Hold(held)
	defer release()

	if _, err := r.File(context.Background(), item("three"), "", remuxCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("a file being read was pruned: %v", err)
	}
}

// A file asked for recently is being watched even when no response is open:
// a player works through a film in range requests minutes apart.
func TestRemuxPruneSparesTheRecentlyAskedFor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.flv")
	writeFLV(t, path, 2)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	r := NewRemuxer(ffmpeg, NewScratch(t.TempDir(), info.Size()*3/2), testLog())
	defer r.Close()

	it := library.Item{
		ID: "one", Name: "clip.flv", Path: path, Kind: library.KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
		VCodec: "h264", ACodec: "aac",
	}
	first, err := r.File(context.Background(), it, "", remuxCopy)
	if err != nil {
		t.Fatal(err)
	}
	it2 := it
	it2.ID = "two"
	if _, err := r.File(context.Background(), it2, "", remuxCopy); err != nil {
		t.Fatal(err)
	}
	// Both are inside the protected window, so the budget is exceeded rather
	// than the first one deleted.
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("a file asked for moments ago was pruned: %v", err)
	}
}

// The two copies of one film are two files. A viewer handed the plain rewrap
// when the soundtrack conversion was asked for gets a film they cannot hear,
// with nothing on screen to say why — so the kind is in the name, and in the
// key that finds it again.
func TestRemuxKindsAreDifferentFiles(t *testing.T) {
	it := library.Item{ID: "abc", ModTime: 17, Size: 42}
	plain, sound := remuxName(it, 0, remuxCopy), remuxName(it, 0, remuxSound)
	if plain == sound {
		t.Fatalf("both copies would be written to %q", plain)
	}
	for name, want := range map[string]remuxKind{plain: remuxCopy, sound: remuxSound} {
		key, ok := remuxKeyFromName(name)
		if !ok {
			t.Fatalf("remuxKeyFromName(%q) did not parse", name)
		}
		if got := key[strings.LastIndex(key, "|")+1:]; got != string(want) {
			t.Fatalf("remuxKeyFromName(%q) kind = %q; want %q", name, got, want)
		}
	}
	// A name written before there was a second kind is a plain copy, and is
	// adopted rather than thrown away and made again.
	if got, ok := remuxKeyFromName(remuxName(it, 0, remuxCopy)); !ok || got != "abc|17|42|0|" {
		t.Fatalf("an unmarked name read as %q,%v", got, ok)
	}
}

func TestSoundFixable(t *testing.T) {
	video := func(v, a string) library.Item {
		return library.Item{Kind: library.KindVideo, VCodec: v, ACodec: a}
	}
	for _, c := range []struct {
		why  string
		it   library.Item
		want bool
	}{
		{"the common television release: a picture everything decodes, a soundtrack nothing does",
			video("h264", "ac3"), true},
		{"the same with the 4K picture, which is where the pipe hurts most",
			video("hevc", "eac3"), true},
		{"a soundtrack the browser already decodes needs no conversion",
			video("h264", "aac"), false},
		{"nor does one it decodes in the other common form", video("h264", "mp3"), false},
		{"a picture an MP4 cannot carry cannot be copied into one",
			video("vc1", "ac3"), false},
		{"a file whose streams are not known yet is nobody's answer",
			video("", ""), false},
		{"and a picture with no soundtrack at all has nothing to fix",
			video("h264", ""), false},
		{"music is not a video", library.Item{Kind: library.KindAudio, VCodec: "h264", ACodec: "ac3"}, false},
	} {
		if got := soundFixable(c.it); got != c.want {
			t.Errorf("soundFixable(%+v) = %v; want %v — %s", c.it, got, c.want, c.why)
		}
	}
}

// writeDubbed writes a clip carrying several soundtracks, each labelled with
// its language — the shape a downloader produces for a video that ships
// automatic dubs beside its original — or skips.
func writeDubbed(t *testing.T, path string, langs ...string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=5:duration=2"}
	for i := range langs {
		// A tone per language, so the tracks are not the same bytes over and
		// the one that comes out can be told from the ones that did not.
		args = append(args, "-f", "lavfi", "-i",
			"sine=frequency="+strconv.Itoa(300+100*i)+":duration=2")
	}
	args = append(args, "-map", "0:v")
	for i := range langs {
		args = append(args, "-map", strconv.Itoa(i+1)+":a")
	}
	args = append(args, "-c:v", "libvpx-vp9", "-b:v", "50k", "-c:a", "libopus")
	for i, l := range langs {
		args = append(args, "-metadata:s:a:"+strconv.Itoa(i), "language="+l)
	}
	args = append(args, "-y", path)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a dubbed clip: %v: %s", err, out)
	}
}

// Which soundtrack cannot be said to a television, or to any browser but
// Safari: the choice is made by handing over a file holding only the one that
// was chosen. For a dubbed download that copy has to keep its own container —
// an MP4 of VP9 and Opus is a container neither a set nor the codecs asked
// for, where a copy into its own kind is lossless and costs a read.
func TestRemuxTrackKeepsOneSoundtrackAndItsContainer(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "a talk.webm")
	writeDubbed(t, src, "ara", "eng", "spa", "por")
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	it := library.Item{
		ID: "abc", Name: "a talk.webm", Path: src, Kind: library.KindVideo,
		Size: info.Size(), ModTime: info.ModTime().UnixMilli(),
		VCodec: "vp9", ACodec: "opus",
		Tracks: []library.AudioTrack{
			{Index: 0, Lang: "ara"}, {Index: 1, Lang: "eng"},
			{Index: 2, Lang: "spa"}, {Index: 3, Lang: "por"},
		},
	}

	r := NewRemuxer(ffmpeg, NewScratch(dir, 0), testLog())
	defer r.Close()
	out, err := r.File(context.Background(), it, "1", remuxTrack)
	if err != nil {
		t.Fatalf("no copy was made: %v", err)
	}
	if filepath.Ext(out) != ".webm" {
		t.Errorf("the copy is called %q; a copy of a WebM is still a WebM", filepath.Base(out))
	}

	probe := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "stream=codec_type,codec_name",
		"-show_entries", "stream_tags=language", "-of", "csv=p=0", out)
	got, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v: %s", err, got)
	}
	streams := strings.Fields(strings.TrimSpace(string(got)))
	if len(streams) != 2 {
		t.Fatalf("the copy holds %d streams, want a picture and one soundtrack: %q", len(streams), got)
	}
	// The chosen one, and not the one the file leads with — which is the
	// whole point, and the thing a set would otherwise have picked for itself.
	if !strings.Contains(string(got), "eng") {
		t.Errorf("the copy carries %q; the soundtrack asked for was eng", got)
	}
	if strings.Contains(string(got), "ara") {
		t.Errorf("the copy still carries the file's own first soundtrack: %q", got)
	}
	// Copied, not re-encoded: a dub is already Opus and converting it would
	// be a generational loss for nothing.
	if !strings.Contains(string(got), "opus") {
		t.Errorf("the soundtrack came out as %q; it should have been copied", got)
	}
	if !strings.Contains(string(got), "vp9") {
		t.Errorf("the picture came out as %q; it should have been copied", got)
	}
}
