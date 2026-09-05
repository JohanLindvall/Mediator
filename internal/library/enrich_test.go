package library

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/rartest"
)

// buildClip writes a real (tiny) video, or skips: ffmpeg and ffprobe are
// optional at runtime, so the tests that need them are optional too.
func buildClip(t *testing.T, path string, seconds int) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=5:duration="+strconv.Itoa(seconds),
		"-c:v", "mpeg4", "-pix_fmt", "yuv420p", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a test clip: %v: %s", err, out)
	}
}

// archivedAndLoose indexes a volume set holding one unreadable member plus a
// plain file of the same unreadable bytes, so both probe to nothing and the
// only difference between them is that one lives inside an archive.
func archivedAndLoose(t *testing.T) (*Library, *blob.DB, Item, Item) {
	t.Helper()
	dir := t.TempDir()
	junk := rartest.Payload(60_000)
	rartest.WriteSet(t, dir, "release", "Feature.mkv", junk, 2, true)
	loose := filepath.Join(dir, "Loose.mkv")
	if err := os.WriteFile(loose, junk, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)

	var archived, plain Item
	for _, it := range l.List(Query{}).Items {
		if it.Archived() {
			archived = it
		} else {
			plain = it
		}
	}
	if archived.ID == "" || plain.ID == "" {
		t.Fatalf("indexed %+v and %+v, want one archived member and one plain file",
			archived, plain)
	}
	return l, db, archived, plain
}

// An empty probe of archived video means "the prefix may have been too
// short", not "this file has nothing". Writing it down would make it
// permanent — the cache key is (id, mtime, size), none of which ever changes
// for an archive member — so it is deliberately not written. For a plain
// file, which ffprobe reads whole, an empty result is an answer and has to be
// recorded, or every later pass re-reads the file.
func TestArchivedProbeMissIsNotCached(t *testing.T) {
	l, db, archived, plain := archivedAndLoose(t)

	l.EnrichNow(context.Background(), []string{archived.ID, plain.ID})
	l.flush(db)

	if _, ok := db.GetMeta(archived.ID, archived.ModTime, archived.Size); ok {
		t.Fatal("an empty verdict on archived video was cached; no later probe could ever undo it")
	}
	if _, ok := db.GetMeta(plain.ID, plain.ModTime, plain.Size); !ok {
		t.Fatal("a plain file that read as nothing was not recorded as looked at")
	}
}

// The database an owner already has holds those empty verdicts, so the fix
// only reaches them if enrichment stops trusting one. A cached record with
// nothing in it is ignored for archived video and honoured for everything
// else.
func TestCachedArchiveMissIsIgnored(t *testing.T) {
	l, db, archived, plain := archivedAndLoose(t)

	// The sentinel is only there to be visible: a real empty record carries
	// no title, and nothing but the cache can put one on these files.
	const sentinel = "from the cache"
	err := db.PutMetas(map[string]blob.Meta{
		archived.ID: {MTime: archived.ModTime, Size: archived.Size, Title: sentinel},
		plain.ID:    {MTime: plain.ModTime, Size: plain.Size, Title: sentinel},
	})
	if err != nil {
		t.Fatal(err)
	}

	l.EnrichNow(context.Background(), []string{archived.ID, plain.ID})

	if got, _ := l.Get(archived.ID); got.Title == sentinel {
		t.Fatal("a cached empty verdict short-circuited the archived probe")
	}
	if got, _ := l.Get(plain.ID); got.Title != sentinel {
		t.Fatal("a cached record for a plain file was ignored; the cache is there to be used")
	}
}

// EnsureCodecs is the second chance for a video the eager pass learned
// nothing about — the archived member whose header outgrew an earlier
// prefix, most of all. The duration comes out of the same ffprobe call as
// the codecs, so throwing it away would leave the item unsortable and the
// player without a timeline for no saving at all.
func TestEnsureCodecsFillsDurationToo(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	buildClip(t, clip, 3)

	l := quietLib(dir)
	l.Scan(nil)
	id := PathID(clip)
	if it, _ := l.Get(id); it.Duration != 0 || it.VCodec != "" {
		t.Fatalf("the scan already filled this in (%+v); the test proves nothing", it)
	}

	l.EnsureCodecs(context.Background(), id)
	it, _ := l.Get(id)
	if it.VCodec == "" {
		t.Fatal("EnsureCodecs left the codec unknown")
	}
	if it.Duration == 0 {
		t.Fatal("EnsureCodecs dropped the duration its own probe returned")
	}
}

// fakeFFprobe swaps the ffprobe binary for a script that answers with the
// given JSON and records that it ran. The real path is restored afterwards,
// resolved the way the package itself would.
func fakeFFprobe(t *testing.T, answer string) func() int {
	return fakeFFprobeSlow(t, 0, answer)
}

// fakeFFprobeSlow is the same, hanging for stall before it would answer.
// It execs the sleep, so the process the context kills is the one that is
// actually sleeping — a shell that forked it would leave the child holding
// the stdout pipe and the kill would not be observable.
func fakeFFprobeSlow(t *testing.T, stall time.Duration, answer string) func() int {
	t.Helper()
	ffprobeOnce.Do(func() { ffprobePath, _ = exec.LookPath("ffprobe") })
	old := ffprobePath
	t.Cleanup(func() { ffprobePath = old })

	dir := t.TempDir()
	counts := filepath.Join(dir, "runs")
	body := "cat <<'JSON'\n" + answer + "\nJSON\n"
	if stall > 0 {
		body = "exec sleep " + strconv.FormatFloat(stall.Seconds(), 'f', 3, 64) + "\n"
	}
	script := "#!/bin/sh\n" +
		"cat > /dev/null\n" + // a piped probe hands its input over stdin
		"printf 'x\\n' >> " + counts + "\n" +
		body
	path := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ffprobePath = path
	return func() int {
		data, err := os.ReadFile(counts)
		if err != nil {
			return 0
		}
		return len(strings.Fields(string(data)))
	}
}

// noFFprobe is what a machine without ffmpeg installed looks like.
func noFFprobe(t *testing.T) {
	t.Helper()
	ffprobeOnce.Do(func() { ffprobePath, _ = exec.LookPath("ffprobe") })
	old := ffprobePath
	t.Cleanup(func() { ffprobePath = old })
	ffprobePath = ""
}

// codecsNoDuration is what an ordinary container gives up over a pipe:
// stream headers, and no duration anywhere (MPEG-TS and PS, live-muxed MKV).
const codecsNoDuration = `{"streams":[` +
	`{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"25/1"},` +
	`{"codec_type":"audio","codec_name":"aac"}],"format":{}}`

// emptyAnswer is what a container with nothing to give up looks like: a
// document, correctly formed, saying nothing at all.
const emptyAnswer = `{"streams":[],"format":{}}`

// "Already looked" has to be a state the probe can actually reach, or every
// request re-probes — and for an archived member every request is a fresh
// read out of the volume set, fired by the player three lines after playback
// starts. Neither of the two things ffprobe commonly answers with can be used
// as the marker: a duration may never come, and codecs may never come either.
// Only "it answered" can.
func TestEnsureCodecsProbesOnce(t *testing.T) {
	for _, c := range []struct {
		name, answer string
		wantVCodec   string
	}{
		// The ordinary case over a pipe: stream headers, no duration
		// anywhere (MPEG-TS and PS, live-muxed MKV). A guard that waits for
		// a duration re-probes forever.
		{"codecs but no duration", codecsNoDuration, "h264"},
		// And the case a guard keyed on the codec cannot express: ffprobe
		// looked, and there was nothing there.
		{"nothing at all", emptyAnswer, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			clip := filepath.Join(dir, "clip.mkv")
			write(t, clip, "not really a video")
			runs := fakeFFprobe(t, c.answer)

			l := quietLib(dir)
			l.Scan(nil)
			id := PathID(clip)
			for range 5 {
				l.EnsureCodecs(context.Background(), id)
			}
			if n := runs(); n != 1 {
				t.Fatalf("ffprobe ran %d times for one item; this answer can never satisfy a guard that demands a duration or a codec", n)
			}
			it, _ := l.Get(id)
			if it.VCodec != c.wantVCodec {
				t.Fatalf("vcodec = %q, want %q", it.VCodec, c.wantVCodec)
			}
			if it.Duration != 0 {
				t.Fatalf("duration = %d, want none: the probe never reported one", it.Duration)
			}

			// A file that changes on disk is a different file: whatever was
			// probed out of it no longer applies, so the next open probes
			// again.
			write(t, clip, "not really a video, but longer")
			l.Scan(nil)
			l.EnsureCodecs(context.Background(), id)
			if n := runs(); n != 2 {
				t.Fatalf("ffprobe ran %d times; the changed file must be probed afresh", n)
			}
		})
	}
}

// "Probed" is "ffprobe answered", never "we tried". A run that never happened
// — no ffprobe on the machine, an unreadable member, a killed process — is
// not a verdict, and recording it as one spends the item's one second chance
// on nothing at all.
func TestEnsureCodecsKeepsTryingWhenNobodyAnswered(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	write(t, clip, "not really a video")

	l := quietLib(dir)
	l.Scan(nil)
	id := PathID(clip)

	noFFprobe(t)
	l.EnsureCodecs(context.Background(), id)
	if it, _ := l.Get(id); it.probed {
		t.Fatal("the item was written off as probed although nothing ran")
	}

	// ffmpeg turns up (or the disk comes back): the item is still owed a look.
	runs := fakeFFprobe(t, codecsNoDuration)
	l.EnsureCodecs(context.Background(), id)
	if n := runs(); n != 1 {
		t.Fatalf("ffprobe ran %d times, want the one real attempt", n)
	}
	if it, _ := l.Get(id); it.VCodec != "h264" {
		t.Fatalf("vcodec = %q after a real answer", it.VCodec)
	}
}

// Enrichment routes through ProbeMedia from EnrichNow and EnrichSoon as well
// as from the background sweep, and EnrichNow runs inside a request that is
// waiting on it — handleItem and handleAlbum both give it a 1.5 s budget. A
// probe that ignores that budget outlives the request by as long as its own
// timeout allows, which for the piped fallback is a minute.
func TestEnrichNowBoundsTheProbe(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	write(t, clip, "not really a video")
	fakeFFprobeSlow(t, 10*time.Second, codecsNoDuration)

	l := quietLib(dir)
	l.Scan(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	l.EnrichNow(ctx, []string{PathID(clip)})
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("EnrichNow took %v for a 200 ms budget; the probe is not bounded by the caller", took)
	}
	// A killed probe is not an answer, so the item is still owed a look.
	if it, _ := l.Get(PathID(clip)); it.probed {
		t.Fatal("a probe that was killed was recorded as having answered")
	}
}

// The probe runs inside the request that is waiting for it, so it belongs to
// that request: a caller that has gone away must not leave an ffprobe reading
// a volume set on a disk playback is using.
func TestEnsureCodecsRespectsCallerContext(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	write(t, clip, "not really a video")
	runs := fakeFFprobe(t, codecsNoDuration)

	l := quietLib(dir)
	l.Scan(nil)
	id := PathID(clip)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.EnsureCodecs(ctx, id)
	if n := runs(); n != 0 {
		t.Fatalf("ffprobe ran %d times for a caller that had already gone", n)
	}

	// And an abandoned request is not an answer: the next one still probes.
	l.EnsureCodecs(context.Background(), id)
	if n := runs(); n != 1 {
		t.Fatalf("ffprobe ran %d times, want the one real attempt", n)
	}
}

// A page of opens must not put one ffprobe per item on the disk. When the
// slots are taken the caller waits for one — and gives up with its own
// deadline rather than queueing behind playback indefinitely.
func TestEnsureCodecsWaitsForAProbeSlot(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	write(t, clip, "not really a video")
	runs := fakeFFprobe(t, codecsNoDuration)

	l := quietLib(dir)
	l.Scan(nil)
	for range cap(l.probeSem) {
		l.probeSem <- struct{}{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.EnsureCodecs(ctx, PathID(clip))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureCodecs ignored its deadline while waiting for a slot")
	}
	if n := runs(); n != 0 {
		t.Fatalf("ffprobe ran %d times with every slot taken", n)
	}
}

// An interrupted probe must leave no trace. The probe takes the caller's
// context, so a request that gives up (the priority wait is short) or a
// shutdown mid-sweep can kill it — and both the "examined" flag and the
// cached metadata are keyed by things that never change for a stable file,
// so writing an empty verdict down would make it permanent.
func TestInterruptedProbeIsNotRecorded(t *testing.T) {
	for _, name := range []string{"clip.mkv", "song.wma"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			write(t, path, "not really media")
			fakeFFprobeSlow(t, 10*time.Second, codecsNoDuration)

			db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			l := quietLib(dir)
			l.SetMetaDB(db)
			l.Scan(nil)
			id := PathID(path)

			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			l.enrichOne(ctx, id)

			if it, _ := l.Get(id); it.enriched {
				t.Error("an item whose probe was killed was marked examined; the next pass will skip it")
			}
			l.metaPendMu.Lock()
			_, queued := l.metaPending[id]
			l.metaPendMu.Unlock()
			if queued {
				t.Error("an empty verdict from a killed probe was queued for the database")
			}
		})
	}
}

// The counterpart: when nothing was interrupted, the verdict IS written down,
// including the empty one — that record is what stops every later pass
// re-reading a file with nothing to say.
func TestUninterruptedProbeIsRecorded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	write(t, path, "not really a video")
	fakeFFprobe(t, codecsNoDuration)

	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	id := PathID(path)

	l.enrichOne(context.Background(), id)

	if it, _ := l.Get(id); !it.enriched {
		t.Error("a completed probe did not mark the item examined")
	}
	l.metaPendMu.Lock()
	_, queued := l.metaPending[id]
	l.metaPendMu.Unlock()
	if !queued {
		t.Error("a completed probe queued nothing for the database")
	}
}

// fakeFFprobeAnswerThenHang prints a complete, parsable answer and only then
// hangs, so the context kills a process whose output is already whole. That
// is the one case the "no output" check cannot catch.
func fakeFFprobeAnswerThenHang(t *testing.T, answer string) {
	t.Helper()
	ffprobeOnce.Do(func() { ffprobePath, _ = exec.LookPath("ffprobe") })
	old := ffprobePath
	t.Cleanup(func() { ffprobePath = old })

	dir := t.TempDir()
	script := "#!/bin/sh\ncat > /dev/null\ncat <<'JSON'\n" + answer + "\nJSON\nexec sleep 10\n"
	path := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ffprobePath = path
}

// A probe killed after it had already written a whole answer is still a probe
// that was killed. Taking the answer would look harmless — the document
// parses — but it would be recorded as the file's verdict on the strength of
// a run nobody waited for, and for a stable file that record never expires.
func TestKilledProbeIsDiscardedEvenWithCompleteOutput(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	write(t, clip, "not really a video")
	fakeFFprobeAnswerThenHang(t, codecsNoDuration)

	l := quietLib(dir)
	l.Scan(nil)
	it, ok := l.Get(PathID(clip))
	if !ok {
		t.Fatal("clip not indexed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	p := ProbeMedia(ctx, it)
	if p.VCodec != "" || p.Probed {
		t.Fatalf("kept the answer of a killed probe: %+v", p)
	}
}

// The probe that runs when a film is opened is the one that has to record
// the picture's shape: it is the only one that runs before something decides
// how to convert the file, and that decision turns on pixels per second.
// Dropping the geometry here left it with nothing to go on, and a 4K clip
// went to the processor that cannot keep up with it.
func TestSetProbeKeepsTheGeometry(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/clip.mp4", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/clip.mp4")
	l.setProbe(id, Probe{VCodec: "hevc", Width: 3840, Height: 2160, FPS: 59.94, Probed: true})

	it, ok := l.Get(id)
	if !ok {
		t.Fatal("the item vanished")
	}
	if it.Width != 3840 || it.Height != 2160 {
		t.Errorf("size %dx%d, want 3840x2160", it.Width, it.Height)
	}
	if it.FPS != 59.94 {
		t.Errorf("frame rate %v, want 59.94", it.FPS)
	}
	// A probe that learned nothing must not erase what is known.
	l.setProbe(id, Probe{Probed: true})
	if it, _ = l.Get(id); it.Width != 3840 || it.FPS != 59.94 {
		t.Errorf("an empty probe overwrote the geometry: %dx%d @ %v", it.Width, it.Height, it.FPS)
	}
}

// The probe that runs when a film is opened writes the item's record again,
// and that record used to leave the picture's shape and its reading marker
// out — so every film that had ever been opened came back from a restart
// marked "shape unread", a header to re-read apiece and, for a Matroska
// file, a probe to re-run. What the eager pass wrote has to survive it.
func TestEnsureCodecsKeepsTheShape(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	buildClip(t, clip, 3)
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	l := quietLib(dir)
	l.SetMetaDB(db)
	l.Scan(nil)
	id := PathID(clip)
	// An MP4's duration is read natively, so the eager pass runs no ffprobe
	// and takes the shape from the box tree — leaving the open-time probe
	// still to come, which is the case that matters.
	l.EnrichNow(context.Background(), []string{id})
	before, _ := l.Get(id)
	if before.probed {
		t.Fatal("the eager pass probed the film; the test proves nothing")
	}
	if before.Width == 0 || before.shape != shapeVersion {
		t.Fatalf("the box tree gave no shape (%dx%d, reading %d); the test proves nothing",
			before.Width, before.Height, before.shape)
	}

	l.EnsureCodecs(context.Background(), id)
	l.flush(db)
	after, _ := l.Get(id)
	if !after.probed {
		t.Fatal("EnsureCodecs did not probe the film")
	}
	m, ok := db.GetMeta(id, after.ModTime, after.Size)
	if !ok {
		t.Fatal("no record written")
	}
	if m.Shape != shapeVersion || m.Width == 0 || m.Height == 0 {
		t.Fatalf("the record written at open lost the shape: reading %d, %dx%d", m.Shape, m.Width, m.Height)
	}
}

// A member replaced inside a volume set is a different file under the same
// name, and everything read from the old bytes has to go with them — as it
// does for a plain file that changed on disk.
func TestReplacedStoredMemberForgetsWhatWasRead(t *testing.T) {
	l := quietLib("/m")
	e := &storedEntry{name: "Feature.mkv", size: 10, segs: []storedSeg{{path: "/m/a.rar", off: 0, n: 10}}}
	l.upsertStored("/m/a.rar", e, time.Unix(1, 0))
	id := PathID("/m/a.rar\x00Feature.mkv")
	l.setProbe(id, Probe{VCodec: "hevc", ACodec: "eac3", Width: 1920, Height: 1080, FPS: 24, DurationMs: 5000,
		Tracks: []AudioTrack{{Index: 0}, {Index: 1}}, Probed: true})
	if it, _ := l.Get(id); it.VCodec == "" || len(it.Tracks) != 2 || !it.probed {
		t.Fatalf("the probe did not land: %+v", it)
	}
	// The same name, more bytes: a re-downloaded release.
	bigger := &storedEntry{name: "Feature.mkv", size: 20, segs: []storedSeg{{path: "/m/a.rar", off: 0, n: 20}}}
	if !l.upsertStored("/m/a.rar", bigger, time.Unix(2, 0)) {
		t.Fatal("a member of a different size was not reported as changed")
	}
	it, _ := l.Get(id)
	if it.VCodec != "" || it.ACodec != "" || it.Width != 0 || it.Duration != 0 || it.Tracks != nil || it.probed {
		t.Errorf("the replaced member still carries the old file's reading: %+v", it)
	}
	// And a DVD title keeps the length its disc declares, whatever changed.
	disc := &storedEntry{name: "Film.vob", size: 30, durationMs: 3_000_000, segs: []storedSeg{{path: "/m/d.iso", off: 0, n: 30}}}
	l.upsertStored("/m/d.iso", disc, time.Unix(1, 0))
	disc2 := &storedEntry{name: "Film.vob", size: 40, durationMs: 3_000_000, segs: []storedSeg{{path: "/m/d.iso", off: 0, n: 40}}}
	l.upsertStored("/m/d.iso", disc2, time.Unix(2, 0))
	if it, _ := l.Get(PathID("/m/d.iso\x00Film.vob")); it.Duration != 3_000_000 {
		t.Errorf("the disc's own length was forgotten with the rest: %d", it.Duration)
	}
}
