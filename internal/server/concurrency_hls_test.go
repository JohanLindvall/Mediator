package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// writeSession lays out a session directory: the playlist naming a segment,
// and, where asked for, the segment itself. A name in the playlist is not a
// file on disk, which is the distinction everything here turns on.
func writeSession(t *testing.T, dir string, listed, present bool, bytes int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hlsKeyFile), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:EVENT\n"
	if listed {
		body += "#EXTINF:4,\nseg00000.ts\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if present {
		if err := os.WriteFile(filepath.Join(dir, "seg00000.ts"), make([]byte, bytes), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Whether a conversion produced anything is a question about the disk, not
// about a watcher on a 150 ms tick. A run that wrote a segment and died
// before the watcher's next tick used to be judged empty — its error
// recorded, its waiters answered 503 and its directory removed by forget —
// where the same run a tick later was served.
func TestEmptinessIsDecidedByTheDisk(t *testing.T) {
	base := t.TempDir()

	wrote := filepath.Join(base, "wrote")
	writeSession(t, wrote, true, true, 16)
	s := &hlsSession{dir: wrote, ready: make(chan struct{})}
	s.failIfEmpty(fmt.Errorf("ffmpeg died"))
	if s.failure() != nil {
		// The gate has deliberately not been opened here: this is the
		// window between the segment landing and the watcher seeing it.
		t.Errorf("a conversion with a playable prefix was failed: %v", s.failure())
	}

	// A name in the playlist with no file behind it is not something a
	// player can start on, so it is still empty.
	listed := filepath.Join(base, "listed")
	writeSession(t, listed, true, false, 0)
	if hasSegment(listed) {
		t.Error("a segment named but not written counted as playable")
	}

	nothing := filepath.Join(base, "nothing")
	writeSession(t, nothing, false, false, 0)
	f := &hlsSession{dir: nothing, ready: make(chan struct{})}
	f.failIfEmpty(errTest)
	if f.failure() != errTest {
		t.Errorf("failure = %v, want the verdict for a conversion that wrote nothing", f.failure())
	}

	// And a gate already opened still outranks a later error, which is what
	// this guard was always for.
	open := filepath.Join(base, "open")
	writeSession(t, open, false, false, 0)
	o := &hlsSession{dir: open, ready: make(chan struct{})}
	o.finish()
	o.failIfEmpty(errTest)
	if o.failure() != nil {
		t.Error("an error was recorded after the waiters had been let through")
	}
}

// A session is dropped only from the entries that still name it. A key
// vacated and taken again between a measurement and the eviction that acts on
// it used to delete whatever now occupied it: a live conversion, still
// reachable by its token, that no later measurement could see, no reaper
// managed and Close never cancelled.
func TestDropLeavesTheSessionThatTookTheKey(t *testing.T) {
	h := NewHLS("", nil, NewScratch(t.TempDir(), 0), testLog())
	old := &hlsSession{key: "k", id: "aaaa", dir: "old"}
	fresh := &hlsSession{key: "k", id: "bbbb", dir: "fresh"}
	h.sessions["k"] = fresh
	h.byID["aaaa"] = old
	h.byID["bbbb"] = fresh

	h.mu.Lock()
	h.dropLocked(old)
	h.mu.Unlock()

	if h.sessions["k"] != fresh {
		t.Error("evicting the old session took the one that had replaced it")
	}
	if h.byID["bbbb"] != fresh {
		t.Error("the live session lost its token")
	}
	if _, ok := h.byID["aaaa"]; ok {
		t.Error("the dropped session kept its token")
	}
}

// What the budget is told is summed from the sessions that are live when it
// is written. Reported only from the reap ticker, the figure was never
// written at all for a conversion that finished inside one tick, and stood
// still at whatever was last measured for as long as nothing was converting.
func TestScratchFigureFollowsWhatTheSessionsHold(t *testing.T) {
	base := t.TempDir()
	scratch := NewScratch(base, 100)
	h := NewHLS("", nil, scratch, testLog())

	dir := filepath.Join(base, "hls", "one")
	writeSession(t, dir, true, true, 150)
	h.sessions["k"] = &hlsSession{
		key: "k", id: "aaaa", dir: dir, ready: make(chan struct{}),
		last: time.Now(), cancel: func() {},
	}
	h.byID["aaaa"] = h.sessions["k"]

	h.account()
	if got := scratch.Excess(); got <= 0 {
		t.Fatalf("excess = %d, want the session's own bytes counted against the budget", got)
	}
	// One session is left alone however large it is — that is a budget set
	// too small for one film — but forgetting it takes its bytes with it.
	h.forget(h.sessions["k"])
	if got := scratch.Excess(); got != 0 {
		t.Errorf("excess = %d after the session was forgotten, want 0", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a forgotten session kept its directory: %v", err)
	}
}

// The budget frees the least recently wanted, and only what has gone unasked
// for long enough to be sure nobody is watching it.
func TestBudgetDropsTheOldestQuietSession(t *testing.T) {
	base := t.TempDir()
	scratch := NewScratch(base, 100)
	h := NewHLS("", nil, scratch, testLog())

	for i, name := range []string{"older", "newer"} {
		dir := filepath.Join(base, "hls", name)
		writeSession(t, dir, true, true, 80)
		s := &hlsSession{
			key: name, id: name, dir: dir, ready: make(chan struct{}),
			used: int64(i), last: time.Now().Add(-2 * hlsKeepFor), cancel: func() {},
		}
		h.sessions[name] = s
		h.byID[name] = s
	}

	h.account()

	if _, ok := h.sessions["older"]; ok {
		t.Error("the least recently wanted conversion was kept while over budget")
	}
	if _, ok := h.sessions["newer"]; !ok {
		t.Error("the most recently wanted conversion was taken instead")
	}
	if _, err := os.Stat(filepath.Join(base, "hls", "older")); !os.IsNotExist(err) {
		t.Error("the dropped conversion kept its files")
	}
	// One session left is one session left alone, however large: that is a
	// budget set too small for a film, and stopping playback is not the way
	// to say so. What must be true is that the figure now describes what is
	// still on the disk and not what was taken off it.
	if got, want := scratch.Excess(), dirBytes(filepath.Join(base, "hls", "newer"))-100; got != want {
		t.Errorf("excess = %d after the eviction, want %d — the figure to follow the disk", got, want)
	}
}

// A conversion asked for after shutdown is refused rather than started: its
// context hangs off Background, and the maps holding the only handle on it
// have been emptied, so nothing would ever cancel the ffmpeg it spawned.
func TestNoSessionStartsAfterClose(t *testing.T) {
	base := t.TempDir()
	h := NewHLS("ffmpeg", nil, NewScratch(base, 0), testLog())
	h.Close()

	if _, err := h.session(context.Background(), library.Item{ID: "x"}, 0, true, ""); err == nil {
		t.Fatal("a conversion was started after the converter was closed")
	}
	h.mu.Lock()
	n := len(h.sessions)
	h.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions after close, want none", n)
	}
	if entries, err := os.ReadDir(filepath.Join(base, "hls")); err == nil && len(entries) > 0 {
		t.Errorf("a refused conversion left %d directories behind", len(entries))
	}
}

// A verdict is recorded once. The attempt that died records what ffmpeg
// actually said; run asks again at the end of the loop with a sentence of
// its own, for the runs that recorded nothing at all. The second must not
// paint over the first, or every failure a viewer is shown reads "the
// conversion produced nothing" whatever the converter complained of.
func TestTheFirstVerdictIsTheOneKept(t *testing.T) {
	dir := t.TempDir() // no playlist: nothing playable was written
	s := &hlsSession{dir: dir, ready: make(chan struct{})}
	s.failIfEmpty(errTest)
	s.failIfEmpty(errOther)
	if s.failure() != errTest {
		t.Errorf("failure = %v, want the verdict the attempt recorded", s.failure())
	}
}

// A session with no directory has written nothing. Joined onto the
// playlist's name, an empty directory asks about whatever sits in the
// process's own working directory — an answer about a different file.
func TestNoDirectoryIsNoSegment(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, true, true, 8)
	t.Chdir(dir)
	if hasSegment("") {
		t.Error("a session with no directory was answered about the working directory")
	}
}

// aspectFake is a converter that writes a playable prefix and then refuses
// on the declared shape of the file's pixels — the shape of an attempt that
// has something to show and something to learn from. It writes only once, so
// a second attempt leaves the directory as it found it.
func aspectFake(t *testing.T, dir string) string {
	t.Helper()
	script := filepath.Join(dir, "ffmpeg")
	body := fmt.Sprintf(`#!/bin/sh
for a; do out="$a"; done
n=0
[ -e %[1]q ] && n=$(cat %[1]q)
n=$((n+1))
echo $n > %[1]q
if [ "$n" = 1 ]; then
  d=$(dirname "$out")
  : > "$d/seg00000.ts"
  printf '#EXTM3U\n#EXTINF:4,\nseg00000.ts\n' > "$out"
fi
echo "Value -11.666667 for parameter 'pixel_aspect' out of range" >&2
exit 1
`, filepath.Join(dir, "runs"))
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// A retry begins by wiping what the last attempt wrote, so it is only ever
// worth having where that was nothing. An attempt that left a playable
// prefix and something to learn from used to have the prefix deleted under
// the waiters it had already let through: the gate was open, so nothing
// recorded a failure, forget never ran, and that key answered for the film
// at that resume point with an empty directory for the life of the process.
func TestARetryNeverWipesAPlayablePrefix(t *testing.T) {
	base := t.TempDir()
	h := NewHLS(aspectFake(t, base), nil, NewScratch(base, 0), testLog())
	defer h.Close()

	dir := filepath.Join(base, "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	it := library.Item{
		ID: "aaaaaaaaaaaaaaaa", Kind: library.KindVideo, VCodec: "h264",
		Path: filepath.Join(base, "in.mkv"), ModTime: 1, Size: 1,
	}
	s := &hlsSession{
		key: "k", id: "aaaa", dir: dir, ready: make(chan struct{}),
		cancel: func() {}, converting: true, last: time.Now(),
	}
	h.sessions["k"] = s
	h.byID["aaaa"] = s

	h.run(context.Background(), s, it, 0, true, "")

	if !hasSegment(dir) {
		t.Error("the retry threw away the prefix the waiters had been let through to")
	}
	if err := s.failure(); err != nil {
		t.Errorf("failure = %v, want a conversion with a playable prefix to be served", err)
	}
	h.mu.Lock()
	_, live := h.sessions["k"]
	h.mu.Unlock()
	if !live {
		t.Error("the session with a playable prefix was forgotten")
	}
}

// Forgetting a session measures what is left rather than subtracting what
// went. The figure is a sum of what each session was last measured to hold,
// and a session younger than its reaper's first tick has been measured at
// nothing — so a forget in that window told the budget that a conversion
// writing to the disk right now was not there, and the Remuxer frees its own
// rewraps on that same figure.
func TestForgetMeasuresWhatIsLeft(t *testing.T) {
	base := t.TempDir()
	scratch := NewScratch(base, 1)
	h := NewHLS("", nil, scratch, testLog())

	live := filepath.Join(base, "hls", "live")
	writeSession(t, live, true, true, 200)
	kept := &hlsSession{
		key: "live", id: "aaaa", dir: live, ready: make(chan struct{}),
		last: time.Now(), cancel: func() {},
	}
	h.sessions["live"] = kept
	h.byID["aaaa"] = kept

	failed := filepath.Join(base, "hls", "failed")
	writeSession(t, failed, false, false, 0)
	gone := &hlsSession{
		key: "failed", id: "bbbb", dir: failed, ready: make(chan struct{}),
		last: time.Now(), cancel: func() {},
	}
	h.sessions["failed"] = gone
	h.byID["bbbb"] = gone

	h.forget(gone)

	if want := dirBytes(live) - 1; scratch.Excess() != want {
		t.Errorf("excess = %d after a forget, want %d — the conversion still on the disk counted",
			scratch.Excess(), want)
	}
}
