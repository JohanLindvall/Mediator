// SPDX-License-Identifier: MIT

package library

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

// silentFrames is a track a probe takes for one: MPEG-1 Layer III frames of
// silence at 128 kbit/s and 44.1 kHz, a fortieth of a second each.
func silentFrames(n int) string {
	frame := append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 413)...)
	return string(bytes.Repeat(frame, n))
}

// placeholderRelease writes a release the way a download can leave one: the
// first track twice, once as itself and once as a placeholder of zeros under
// the same name with a space before the extension, its room reserved and
// never written.
func placeholderRelease(t *testing.T) (root, dir, zeros string) {
	t.Helper()
	root = t.TempDir()
	dir = filepath.Join(root, "Pale Harrow", "2012 - Saltings")
	writeFile(t, filepath.Join(dir, "01. Pale Harrow - Lee Shore.mp3"), silentFrames(12))
	zeros = filepath.Join(dir, "01. Pale Harrow - Lee Shore .mp3")
	writeFile(t, zeros, string(make([]byte, 20000)))
	writeFile(t, filepath.Join(dir, "02. Pale Harrow - Windward.mp3"), silentFrames(12))
	return root, dir, zeros
}

func needFFprobe(t *testing.T) {
	t.Helper()
	if FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
}

// totalsInStep checks the running totals against a walk of what is listed:
// every change to an item's verdict moves it between them.
func totalsInStep(t *testing.T, l *Library) {
	t.Helper()
	l.mu.RLock()
	defer l.mu.RUnlock()
	var walked Counts
	for _, it := range l.items {
		if it.listed() {
			addKind(&walked, it.Kind, 1)
		}
	}
	if walked != l.kindCounts {
		t.Errorf("the running totals say %+v, the listed items %+v", l.kindCounts, walked)
	}
}

// A placeholder a download reserved and never wrote — the right size, every
// byte nought, a media name — has nothing to play. ffprobe takes the name's
// word for the format, finds not one frame and exits as though all were
// well, so the verdict is read from what it found: no stream with anything in
// it, and no length. Such a track is in no listing, no count and no release.
func TestAPlaceholderOfZerosIsNoTrack(t *testing.T) {
	needFFprobe(t)
	root, _, zeros := placeholderRelease(t)
	l := quietLib(root)
	l.Scan(nil)
	if got := l.Counts().Audio; got != 3 {
		t.Fatalf("before reading: %d tracks, want 3", got)
	}
	l.EnrichMeta(context.Background(), nil)

	it, _ := l.Get(PathID(zeros))
	if !it.Unreadable {
		t.Fatal("a file of zeros was taken for a track")
	}
	totalsInStep(t, l)
	if got := l.Counts().Audio; got != 2 {
		t.Errorf("the totals count %d tracks, want 2", got)
	}
	res := l.List(Query{Kind: KindAudio})
	if res.Total != 2 || slices.ContainsFunc(res.Items, func(x Item) bool { return x.ID == it.ID }) {
		t.Errorf("the listing holds %d tracks, the placeholder among them or not: %+v", res.Total, res.Items)
	}
	tracks := 0
	for _, a := range l.Albums() {
		tracks += a.Tracks
		if slices.Contains(a.TrackIDs, it.ID) {
			t.Errorf("release %q holds the placeholder", a.Name)
		}
	}
	if tracks != 2 {
		t.Errorf("the releases hold %d tracks, want 2", tracks)
	}
	if got := l.CountsFor(CountQuery{Search: "lee shore"}).Audio; got != 1 {
		t.Errorf("a search counts %d tracks, want the one that is a track", got)
	}
}

// A file of no bytes holds no media whatever it is called, and needs no
// ffprobe to say so — which says only "Invalid argument" about it, and may
// not be there at all, so this runs without it. The size in the index is not
// the verdict on its own: the file is asked.
func TestAnEmptyFileIsNoTrack(t *testing.T) {
	FFprobePath() // settled first, so what is cleared stays cleared
	was := ffprobePath
	ffprobePath = ""
	t.Cleanup(func() { ffprobePath = was })
	root := t.TempDir()
	path := filepath.Join(root, "Pale Harrow", "Saltings", "01 Lee Shore.mp3")
	writeFile(t, path, "")
	l := quietLib(root)
	l.Scan(nil)
	it, _ := l.Get(PathID(path))
	if p := ProbeMedia(context.Background(), it); !p.Unreadable {
		t.Fatal("an empty file was taken for media")
	}
	writeFile(t, path, silentFrames(4))
	if p := ProbeMedia(context.Background(), it); p.Unreadable {
		t.Error("a file with frames in it was judged by an index that had not seen it grow")
	}
}

// The verdict is on the bytes: a placeholder that is written after all, as a
// download finishing writes it, is a track again — counted, listed, and read
// afresh.
func TestAPlaceholderWrittenAfterAllIsATrackAgain(t *testing.T) {
	needFFprobe(t)
	root, _, zeros := placeholderRelease(t)
	l := quietLib(root)
	l.Scan(nil)
	l.EnrichMeta(context.Background(), nil)
	if got := l.Counts().Audio; got != 2 {
		t.Fatalf("%d tracks before the placeholder was written, want 2", got)
	}

	writeFile(t, zeros, silentFrames(20))
	l.Scan(nil)
	totalsInStep(t, l)
	if got := l.Counts().Audio; got != 3 {
		t.Errorf("%d tracks once the placeholder was written, want 3", got)
	}
	l.EnrichMeta(context.Background(), nil)
	if it, _ := l.Get(PathID(zeros)); it.Unreadable || it.Duration == 0 {
		t.Errorf("the written file was not read afresh: unreadable %v, length %d", it.Unreadable, it.Duration)
	}
	totalsInStep(t, l)
}

// A track read before this verdict existed comes back from its record looking
// finished — examined, no length, nothing wrong — and would never be read
// again. A track with no length is asked once a run whether it is media: the
// verdict is written down, and asked, it is not asked again.
func TestATrackReadBeforeTheVerdictIsAskedOnce(t *testing.T) {
	needFFprobe(t)
	root, _, zeros := placeholderRelease(t)
	db, err := blob.Open(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l := quietLib(root)
	l.SetMetaDB(db)
	l.Scan(nil)
	it, _ := l.Get(PathID(zeros))

	// The record an earlier reading left: examined, no length, no verdict.
	if err := db.PutMeta(it.ID, it.ModTime, it.Size, blob.Meta{MTime: it.ModTime, Size: it.Size, Shape: shapeVersion}); err != nil {
		t.Fatal(err)
	}
	asked := func() bool {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return needsEnrich(l.items[it.ID])
	}
	l.mu.Lock()
	l.items[it.ID].enriched, l.items[it.ID].shape = true, shapeVersion
	l.mu.Unlock()
	if !asked() {
		t.Fatal("a track with no length and no verdict was taken as read")
	}

	l.enrichOne(context.Background(), it.ID)
	if got, _ := l.Get(it.ID); !got.Unreadable {
		t.Fatal("the placeholder was not judged")
	}
	if m, ok := l.pendingMeta(it.ID); !ok || !m.Unreadable {
		t.Error("the verdict was not written down")
	}
	if asked() {
		t.Error("a judged track is asked again")
	}
	totalsInStep(t, l)
}

// A release deleted takes its folder, the placeholder in it included. The
// placeholder is in no release, so nothing being deleted names it — and kept
// as something of its own it would hold the folder back.
func TestDeletingAReleaseTakesItsPlaceholder(t *testing.T) {
	needFFprobe(t)
	root, dir, _ := placeholderRelease(t)
	l := quietLib(root)
	l.Scan(nil)
	l.EnrichMeta(context.Background(), nil)
	albums := l.Albums()
	if len(albums) != 1 {
		t.Fatalf("%d releases, want 1", len(albums))
	}
	plan, err := l.PlanDelete(DeleteRequest{Kind: DeleteAlbum, ID: albums[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Folders, []string{dir}) {
		t.Fatalf("the plan takes folders %v, want the release's own", plan.Folders)
	}
	l.DeleteNow(plan)
	if exists(dir) {
		t.Error("the folder was kept, holding the placeholder")
	}
}
