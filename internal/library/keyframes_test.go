package library

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// ---- Matroska ----------------------------------------------------------------

// ebml is one element: the id bytes as they are written, and the payload
// behind an eight-byte size, which is the one length every reader has to
// take and keeps the fixture's offsets independent of the numbers in it.
func ebml(id []byte, payload ...[]byte) []byte {
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	size := make([]byte, 8)
	size[0] = 0x01
	binary.BigEndian.PutUint32(size[4:], uint32(len(body)))
	return append(append(append([]byte{}, id...), size...), body...)
}

func ebmlUint(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return b[i:]
}

var (
	idEBML      = []byte{0x1A, 0x45, 0xDF, 0xA3}
	idSegment   = []byte{0x18, 0x53, 0x80, 0x67}
	idSeekHead  = []byte{0x11, 0x4D, 0x9B, 0x74}
	idSeek      = []byte{0x4D, 0xBB}
	idSeekID    = []byte{0x53, 0xAB}
	idSeekPos   = []byte{0x53, 0xAC}
	idInfo      = []byte{0x15, 0x49, 0xA9, 0x66}
	idTimeScale = []byte{0x2A, 0xD7, 0xB1}
	idTracks    = []byte{0x16, 0x54, 0xAE, 0x6B}
	idTrack     = []byte{0xAE}
	idTrackNum  = []byte{0xD7}
	idTrackType = []byte{0x83}
	idCluster   = []byte{0x1F, 0x43, 0xB6, 0x75}
	idCues      = []byte{0x1C, 0x53, 0xBB, 0x6B}
	idCuePoint  = []byte{0xBB}
	idCueTime   = []byte{0xB3}
	idCuePos    = []byte{0xB7}
	idCueTrack  = []byte{0xF7}
	idVoid      = []byte{0xEC}
)

// mkvFixture is a Matroska file the way a muxer writes one: the cues at the
// end, pointed at by the seek head; a video track and an audio track, both
// cued; the clusters between.
func mkvFixture(cuesInFront bool) []byte {
	cue := func(ms uint64, track uint64) []byte {
		return ebml(idCuePoint, ebml(idCueTime, ebmlUint(ms)), ebml(idCuePos, ebml(idCueTrack, ebmlUint(track))))
	}
	cues := ebml(idCues,
		cue(0, 1), cue(3625, 2), cue(10417, 1), cue(10625, 2), cue(20833, 1))
	info := ebml(idInfo, ebml(idTimeScale, ebmlUint(1_000_000)))
	tracks := ebml(idTracks,
		ebml(idTrack, ebml(idTrackNum, ebmlUint(2)), ebml(idTrackType, ebmlUint(2))),
		ebml(idTrack, ebml(idTrackNum, ebmlUint(1)), ebml(idTrackType, ebmlUint(1))))
	void := ebml(idVoid, make([]byte, 40))
	cluster := ebml(idCluster, bytes.Repeat([]byte{0xAB}, 300))
	if cuesInFront {
		return append(ebml(idEBML, make([]byte, 4)),
			ebml(idSegment, info, tracks, cues, cluster)...)
	}
	// The seek head's own length is fixed by the eight-byte sizes, so the
	// position it names can be worked out before it is written.
	seekHeadLen := len(ebml(idSeekHead, ebml(idSeek, ebml(idSeekID, idCues), ebml(idSeekPos, make([]byte, 8)))))
	at := seekHeadLen + len(void) + len(info) + len(tracks) + len(cluster)
	pos := make([]byte, 8)
	binary.BigEndian.PutUint64(pos, uint64(at))
	seekHead := ebml(idSeekHead, ebml(idSeek, ebml(idSeekID, idCues), ebml(idSeekPos, pos)))
	return append(ebml(idEBML, make([]byte, 4)),
		ebml(idSegment, seekHead, void, info, tracks, cluster, cues)...)
}

func near(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			return false
		}
	}
	return true
}

// The cues of the video track are the keyframes, in seconds of the
// timestamp scale; the audio track's cues are not, and are left out.
func TestMatroskaKeyframesAreTheVideoCues(t *testing.T) {
	want := []float64{0, 10.417, 20.833}
	for _, front := range []bool{false, true} {
		f := mkvFixture(front)
		got, ok := mkvKeyframes(bytes.NewReader(f), int64(len(f)))
		if !ok || !near(got, want) {
			t.Errorf("cues in front %v: got %v (%v), want %v", front, got, ok, want)
		}
	}
	// Through the item, by its name, as the converter asks.
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(path, mkvFixture(false), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	got, ok := Keyframes(Item{Name: "clip.mkv", Path: path, Size: fi.Size()})
	if !ok || !near(got, want) {
		t.Errorf("Keyframes = %v (%v), want %v", got, ok, want)
	}
	// A container this reads no index of, and a file that is not one.
	if _, ok := Keyframes(Item{Name: "clip.avi", Path: path, Size: fi.Size()}); ok {
		t.Error("an AVI was given keyframes")
	}
	junk := bytes.Repeat([]byte{0x47}, 400)
	if _, ok := mkvKeyframes(bytes.NewReader(junk), int64(len(junk))); ok {
		t.Error("a file that is not Matroska answered")
	}
	// Without a video track there is nothing to say which cues are the
	// picture's, and nothing is guessed.
	noTracks := append(ebml(idEBML, make([]byte, 4)),
		ebml(idSegment, ebml(idCues, ebml(idCuePoint, ebml(idCueTime, ebmlUint(0)), ebml(idCuePos, ebml(idCueTrack, ebmlUint(1))))))...)
	if _, ok := mkvKeyframes(bytes.NewReader(noTracks), int64(len(noTracks))); ok {
		t.Error("cues were taken for keyframes with no track to say whose they are")
	}
}

// ---- MP4 -----------------------------------------------------------------------

func kfbox(typ string, payload ...[]byte) []byte {
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], typ)
	return append(out, body...)
}

func u32s(v ...uint32) []byte {
	out := make([]byte, 4*len(v))
	for i, x := range v {
		binary.BigEndian.PutUint32(out[i*4:], x)
	}
	return out
}

// mp4Fixture is the index of an MP4 with a hundred frames at 23.976 fps,
// every frame presented two frames after it is decoded, a sync sample
// every twenty-four frames, and an edit list as asked.
func mp4Fixture(edits []byte, withSync bool) []byte {
	mvhd := kfbox("mvhd", u32s(0, 0, 0, 1000, 5000))
	mdhd := kfbox("mdhd", u32s(0, 0, 0, 24000, 100*1001), []byte{0, 0, 0, 0})
	hdlr := func(kind string) []byte {
		return kfbox("hdlr", u32s(0, 0), []byte(kind), make([]byte, 13))
	}
	stbl := []byte{}
	stbl = append(stbl, kfbox("stts", u32s(0, 1, 100, 1001))...)
	stbl = append(stbl, kfbox("ctts", u32s(0, 1, 100, 2002))...)
	if withSync {
		stbl = append(stbl, kfbox("stss", u32s(0, 4, 1, 25, 49, 73))...)
	}
	video := []byte{}
	if edits != nil {
		video = append(video, kfbox("edts", kfbox("elst", edits))...)
	}
	video = append(video, kfbox("mdia", mdhd, hdlr("vide"), kfbox("minf", kfbox("stbl", stbl)))...)
	audio := kfbox("mdia", hdlr("soun"))
	return append(kfbox("ftyp", []byte("isom"), u32s(0)),
		kfbox("moov", mvhd, kfbox("trak", audio), kfbox("trak", video))...)
}

// The sync samples' presentation times, timed the way ffmpeg times them:
// decode time plus composition offset, and the edit list applied — the
// first real edit's media time becomes zero, and an empty edit in front
// delays everything by its length.
func TestMP4KeyframesFollowTheEditList(t *testing.T) {
	frame := 1001.0 / 24000
	cases := []struct {
		name  string
		edits []byte
		sync  bool
		want  []float64
	}{
		{"an edit trimming the composition offset", u32s(0, 1, 5000, 2002, 0x10000), true,
			[]float64{0, 24 * frame, 48 * frame, 72 * frame}},
		{"no edit list", nil, true,
			[]float64{2 * frame, 26 * frame, 50 * frame, 74 * frame}},
		{"an empty edit delaying by half a second", u32s(0, 2, 500, 0xFFFFFFFF, 0x10000, 5000, 2002, 0x10000), true,
			[]float64{0.5, 0.5 + 24*frame, 0.5 + 48*frame, 0.5 + 72*frame}},
	}
	for _, c := range cases {
		f := mp4Fixture(c.edits, c.sync)
		got, ok := mp4Keyframes(bytes.NewReader(f), int64(len(f)))
		if !ok || !near(got, c.want) {
			t.Errorf("%s: got %v (%v), want %v", c.name, got, ok, c.want)
		}
	}
	// No sync-sample table means every sample is one.
	f := mp4Fixture(nil, false)
	got, ok := mp4Keyframes(bytes.NewReader(f), int64(len(f)))
	if !ok || len(got) != 100 || math.Abs(got[1]-got[0]-frame) > 1e-9 {
		t.Errorf("an intra-only file: %d keyframes (%v), want one per frame", len(got), ok)
	}
	if _, ok := mp4Keyframes(bytes.NewReader(f[:20]), 20); ok {
		t.Error("a file with no moov answered")
	}
}
