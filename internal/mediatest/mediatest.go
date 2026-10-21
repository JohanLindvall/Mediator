// SPDX-License-Identifier: MIT

// Package mediatest writes media files for tests in shapes ffmpeg will not
// write directly. It lives outside the packages that use it for rartest's
// reason: the library (which probes such a file) and the server (which
// converts it and takes stills of it) both need one, and two copies of the
// trick would prove nothing about each other.
package mediatest

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The shapes written, so a test can tell which picture it was handed: the
// film is wide and the cover square.
const (
	FilmWidth  = 160
	FilmHeight = 90
	CoverSide  = 64
)

// CoverFirst writes a seconds-long MP4 whose cover — an attached picture, a
// flat square — is listed ahead of its film, as a WMV carrying its
// publisher's picture lists it: the cover is stream 0, then the film
// (MPEG-4 Part 2 test pattern, FilmWidth by FilmHeight, five frames a
// second) and a sine soundtrack.
//
// ffmpeg's demuxer makes a stream of an MP4's artwork where it meets the box
// holding it, and its muxer writes that box after the tracks, so the file is
// written and the box then moved ahead of them. Nothing else changes: the
// movie box keeps its size and place, and the sample offsets point into the
// media data, which does not move.
//
// It skips the test where ffmpeg, ffprobe or an encoder is missing, and
// fails it where the file no longer comes out in that order, since a test
// built on it would then prove nothing.
func CoverFirst(t testing.TB, path string, seconds int) {
	t.Helper()
	write(t, path, seconds, true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	moved, ok := metadataFirst(data)
	if !ok {
		t.Fatalf("no artwork box to move in %s", path)
	}
	if err := os.WriteFile(path, moved, 0o644); err != nil {
		t.Fatal(err)
	}
	if first := firstStream(t, path); first != "png,1" {
		t.Fatalf("the cover is not listed first (stream 0 reads %q); the fixture no longer has the shape", first)
	}
}

// CoverOnly writes a seconds-long MP4 holding a sine soundtrack and a cover
// and no film: sound under a video's name, with its picture.
func CoverOnly(t testing.TB, path string, seconds int) {
	t.Helper()
	write(t, path, seconds, false)
}

func write(t testing.TB, path string, seconds int, film bool) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	run := func(args ...string) {
		t.Helper()
		args = append([]string{"-hide_banner", "-loglevel", "error"}, args...)
		if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Skipf("ffmpeg could not build a test clip: %v: %s", err, out)
		}
	}
	cover := filepath.Join(t.TempDir(), "cover.png")
	side := strconv.Itoa(CoverSide)
	run("-f", "lavfi", "-i", "color=c=red:size="+side+"x"+side, "-frames:v", "1", "-y", cover)

	secs := strconv.Itoa(seconds)
	sound := []string{"-f", "lavfi", "-i", "sine=duration=" + secs}
	if !film {
		run(append(sound, "-i", cover, "-map", "0:a", "-map", "1:v",
			"-c:a", "aac", "-c:v", "copy", "-disposition:v:0", "attached_pic", "-y", path)...)
		return
	}
	size := strconv.Itoa(FilmWidth) + "x" + strconv.Itoa(FilmHeight)
	args := []string{"-f", "lavfi", "-i", "testsrc=size=" + size + ":rate=5:duration=" + secs}
	args = append(args, sound...)
	run(append(args, "-i", cover, "-map", "0:v", "-map", "1:a", "-map", "2:v",
		"-c:v:0", "mpeg4", "-pix_fmt:v:0", "yuv420p", "-c:v:1", "copy", "-disposition:v:1", "attached_pic",
		"-c:a", "aac", "-y", path)...)
}

// metadataFirst moves an MP4's user-data box (moov/udta, which holds the
// artwork) to just after the movie header, ahead of the tracks. False where
// the file has no such box or does not parse.
func metadataFirst(data []byte) ([]byte, bool) {
	start, end, ok := topBox(data, "moov")
	if !ok {
		return nil, false
	}
	body := data[start+8 : end]
	var head, udta, rest []byte
	for off := 0; off < len(body); {
		if off+8 > len(body) {
			return nil, false
		}
		size := int(binary.BigEndian.Uint32(body[off:]))
		if size < 8 || size > len(body)-off {
			return nil, false
		}
		child := body[off : off+size]
		switch string(child[4:8]) {
		case "mvhd":
			head = child
		case "udta":
			udta = child
		default:
			rest = append(rest, child...)
		}
		off += size
	}
	if head == nil || udta == nil {
		return nil, false
	}
	out := slices.Clone(data)
	copy(out[start+8:end], slices.Concat(head, udta, rest))
	return out, true
}

// topBox finds a top-level box with an ordinary 32-bit size, stepping over
// any other box, the 64-bit kind included.
func topBox(data []byte, typ string) (start, end int, ok bool) {
	for off := 0; off+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[off:]))
		header := uint64(8)
		switch size {
		case 0:
			size = uint64(len(data) - off)
		case 1:
			if off+16 > len(data) {
				return 0, 0, false
			}
			size, header = binary.BigEndian.Uint64(data[off+8:]), 16
		}
		if size < header || size > uint64(len(data)-off) {
			return 0, 0, false
		}
		if string(data[off+4:off+8]) == typ {
			return off, off + int(size), header == 8
		}
		off += int(size)
	}
	return 0, 0, false
}

// firstStream is ffprobe's codec and attached-picture mark for stream 0.
func firstStream(t testing.TB, path string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"stream=codec_name:stream_disposition=attached_pic", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return first
}
