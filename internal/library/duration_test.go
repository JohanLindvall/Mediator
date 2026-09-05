package library

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/rartest"
)

func writeFixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// probeFile wraps a plain file in an Item for ProbeDuration.
func probeFile(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return ProbeDuration(context.Background(), Item{Name: filepath.Base(path), Path: path, Size: info.Size()})
}

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
func le16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }

func TestMP4Duration(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(be32(16))
	buf.WriteString("ftyp")
	buf.WriteString("isom")
	buf.Write(be32(0))
	// moov > mvhd v0: timescale 1000, duration 90000 → 90s.
	mvhd := make([]byte, 32)
	copy(mvhd[12:16], be32(1000))
	copy(mvhd[16:20], be32(90000))
	buf.Write(be32(8 + 8 + int32len(mvhd)))
	buf.WriteString("moov")
	buf.Write(be32(8 + int32len(mvhd)))
	buf.WriteString("mvhd")
	buf.Write(mvhd)

	path := writeFixture(t, "clip.mp4", buf.Bytes())
	if ms := probeFile(t, path); ms != 90000 {
		t.Fatalf("mp4: got %d, want 90000", ms)
	}
}

func int32len(b []byte) uint32 { return uint32(len(b)) }

func TestMP3DurationXing(t *testing.T) {
	// MPEG1 Layer III, 44.1 kHz, stereo: Xing at 4+32 with 1000 frames.
	frame := make([]byte, 200)
	frame[0], frame[1], frame[2], frame[3] = 0xFF, 0xFB, 0x90, 0x40
	copy(frame[36:40], "Xing")
	copy(frame[40:44], be32(1)) // frames-field-present flag
	copy(frame[44:48], be32(1000))
	path := writeFixture(t, "vbr.mp3", frame)
	want := int64(1000) * 1152 * 1000 / 44100 // 26122
	if ms := probeFile(t, path); ms != want {
		t.Fatalf("mp3 xing: got %d, want %d", ms, want)
	}
}

func TestMP3DurationCBR(t *testing.T) {
	// ID3v2 header (100-byte tag), then a plain 128 kbps CBR stream:
	// 160000 audio bytes * 8 / 128 kbps = 10000 ms.
	data := make([]byte, 110+160000)
	copy(data, "ID3")
	data[3], data[4] = 4, 0
	data[9] = 100 // synchsafe tag size
	data[110], data[111], data[112], data[113] = 0xFF, 0xFB, 0x90, 0x40
	path := writeFixture(t, "cbr.mp3", data)
	if ms := probeFile(t, path); ms != 10000 {
		t.Fatalf("mp3 cbr: got %d, want 10000", ms)
	}
}

func TestFLACDuration(t *testing.T) {
	// STREAMINFO: rate 44100, total samples 441000 → 10s.
	si := make([]byte, 34)
	si[10], si[11], si[12] = 0x0A, 0xC4, 0x40
	copy(si[14:18], be32(441000))
	var buf bytes.Buffer
	buf.WriteString("fLaC")
	buf.WriteByte(0x80) // last block, type 0
	buf.Write([]byte{0, 0, 34})
	buf.Write(si)
	path := writeFixture(t, "song.flac", buf.Bytes())
	if ms := probeFile(t, path); ms != 10000 {
		t.Fatalf("flac: got %d, want 10000", ms)
	}
}

func TestOggOpusDuration(t *testing.T) {
	var buf bytes.Buffer
	// First page: one 19-byte segment holding OpusHead (pre-skip 312).
	buf.WriteString("OggS")
	buf.Write(make([]byte, 22)) // version..sequence/crc zeroed
	buf.WriteByte(1)            // one segment
	buf.WriteByte(19)
	buf.WriteString("OpusHead")
	buf.Write([]byte{1, 2}) // version, channels
	buf.Write(le16(312))    // pre-skip
	buf.Write(le32(48000))
	buf.Write([]byte{0, 0, 0}) // gain, mapping
	// Last page: granule = 312 + 5 s of 48 kHz samples.
	buf.WriteString("OggS")
	buf.WriteByte(0)
	buf.WriteByte(4) // end-of-stream
	g := make([]byte, 8)
	binary.LittleEndian.PutUint64(g, 312+48000*5)
	buf.Write(g)
	buf.Write(make([]byte, 13))
	path := writeFixture(t, "voice.opus", buf.Bytes())
	if ms := probeFile(t, path); ms != 5000 {
		t.Fatalf("opus: got %d, want 5000", ms)
	}
}

func TestWavDuration(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	buf.Write(le32(36 + 32000))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	buf.Write(le32(16))
	buf.Write(le16(1)) // PCM
	buf.Write(le16(1)) // mono
	buf.Write(le32(8000))
	buf.Write(le32(16000)) // byte rate
	buf.Write(le16(2))
	buf.Write(le16(16))
	buf.WriteString("data")
	buf.Write(le32(32000)) // 32000 / 16000 B/s = 2 s
	buf.Write(make([]byte, 32000))
	path := writeFixture(t, "beep.wav", buf.Bytes())
	if ms := probeFile(t, path); ms != 2000 {
		t.Fatalf("wav: got %d, want 2000", ms)
	}
}

func TestProbeDurationGarbage(t *testing.T) {
	for _, name := range []string{"x.mp3", "x.mp4", "x.flac", "x.ogg", "x.wav", "x.mkv"} {
		path := writeFixture(t, name, bytes.Repeat([]byte{0xAB}, 512))
		if ms := probeFile(t, path); ms != 0 && ffprobePath == "" {
			t.Fatalf("%s: got %d from garbage, want 0", name, ms)
		}
	}
}

// paddedClip writes a real clip whose container header is deliberately
// larger than probePrefix, by attaching a couple of blobs the way releases
// attach subtitle fonts. That is the shape of the bug this guards: the
// stream list sits behind the attachments, so a demuxer fed less than the
// whole header reports nothing at all.
func paddedClip(t *testing.T, dir string) []byte {
	t.Helper()
	base := filepath.Join(dir, "base.mkv")
	buildClip(t, base, 4)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-i", base}
	for i := range 2 {
		pad := filepath.Join(dir, fmt.Sprintf("pad%d.bin", i))
		// Two of these clear probePrefix between them, with room to spare.
		if err := os.WriteFile(pad, rartest.Payload(probePrefix*3/4), 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-attach", pad)
	}
	padded := filepath.Join(dir, "padded.mkv")
	args = append(args, "-metadata:s:t", "mimetype=application/octet-stream",
		"-c", "copy", "-y", padded)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not attach to the test clip: %v: %s", err, out)
	}
	data, err := os.ReadFile(padded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A video inside an archive is probed by piping a prefix of it, and the
// prefix has to be long enough for the whole container header — a release
// carries its stream list behind however many fonts and covers it embeds.
func TestArchivedVideoProbePipesEnoughPrefix(t *testing.T) {
	build := t.TempDir()
	member := paddedClip(t, build)

	dir := t.TempDir()
	rartest.WriteSet(t, dir, "release", "Feature.mkv", member, 3, false)
	l := quietLib(dir)
	l.Scan(nil)
	res := l.List(Query{})
	if res.Total != 1 {
		t.Fatalf("indexed %d items, want the archived member", res.Total)
	}
	it := res.Items[0]
	if !it.Archived() {
		t.Fatal("item should be archived")
	}

	// The fixture is only worth anything if it reproduces the failure: the
	// prefix the audio path uses must fall short of this header.
	f, err := OpenItem(it)
	if err != nil {
		t.Fatal(err)
	}
	short := ffprobe(context.Background(), "pipe:0", io.LimitReader(f, probePrefix))
	f.Close()
	if short.vcodec != "" || short.durationMs != 0 {
		t.Fatalf("fixture header fits in %d bytes; it no longer reproduces the bug: %+v",
			probePrefix, short)
	}

	l.EnrichNow(context.Background(), []string{it.ID})
	got, _ := l.Get(it.ID)
	if got.Duration == 0 || got.VCodec == "" {
		t.Fatalf("archived video probed to duration=%d vcodec=%q; want both filled in",
			got.Duration, got.VCodec)
	}
}
