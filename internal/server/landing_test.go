// SPDX-License-Identifier: MIT

package server

import (
	"bufio"
	"bytes"
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// writeReorderedClip writes H.264 with B-frames — the reordering is what
// makes ffmpeg take its 3/23 s lead off a seek — a keyframe every two
// seconds and nowhere else, and a soundtrack the conversion has to
// re-encode. Or skips: ffmpeg is optional, and so are its encoders.
func writeReorderedClip(t *testing.T, path string) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=24000/1001:duration=16",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=16",
		"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-g", "50", "-keyint_min", "50",
		"-sc_threshold", "0", "-pix_fmt", "yuv420p",
		"-c:a", "mp2", "-shortest", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build a reordered test clip: %v: %s", err, out)
	}
	return ffmpeg
}

// firstPackets runs ffmpeg to framecrc and answers the first line of each
// stream there: index, dts, pts, duration, size and hash, in that order.
func firstPackets(t *testing.T, ffmpeg string, args ...string) (head string, first map[string][]string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), ffmpeg, append(args, "-f", "framecrc", "-")...).Output()
	if err != nil {
		t.Fatalf("ffmpeg %q: %v", args, err)
	}
	var h strings.Builder
	first = map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			h.WriteString(line + "\n")
			continue
		}
		f := strings.Split(line, ",")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if len(f) >= 6 && first[f[0]] == nil {
			first[f[0]] = f
		}
	}
	return h.String(), first
}

// timesOf reads a packet's times back through framecrcFirst.
func timesOf(t *testing.T, head string, packet []string) packetTimes {
	t.Helper()
	at, ok := framecrcFirst([]byte(head + strings.Join(packet, ", ") + "\n"))
	if !ok {
		t.Fatalf("no times in %q", packet)
	}
	return at
}

// keyframeHash is the checksum of the picture's packet presented at k,
// read from a copy of the whole picture rather than through any seek.
func keyframeHash(t *testing.T, ffmpeg, path string, k float64) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), ffmpeg, "-nostdin", "-loglevel", "error",
		"-copyts", "-i", path, "-map", "0:v:0", "-c", "copy", "-f", "framecrc", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	var head strings.Builder
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "#") {
			head.WriteString(line + "\n")
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 6 {
			continue
		}
		at, ok := framecrcFirst([]byte(head.String() + line + "\n"))
		if ok && math.Abs(at.pts-k) < 0.0005 {
			return strings.TrimSpace(f[5])
		}
	}
	t.Fatalf("no packet presented at %.3f s", k)
	return ""
}

// A conversion that copies the picture begins its picture and its sound
// together, at the keyframe /api/keyframe told the client it begins at.
// The client asks for the conversion at that keyframe, and ffmpeg takes 3/23
// s off a seek in a reordered stream: asked for exactly a keyframe, the
// copied picture began at the keyframe before it while the re-encoded sound
// began where it was told — the sound and the subtitles ahead of the picture
// by a keyframe interval, for as long as the conversion played.
//
// The clip is also the case that ruled out predicting where a seek lands:
// its soundtrack starts a few milliseconds below zero, as a primed encoder's
// does, and ffmpeg adds that to every seek — so a seek made a millisecond
// past the keyframe, which is where the lead alone says to ask, landed a
// keyframe early all the same.
func TestACopiedConversionBeginsAtItsKeyframe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "film.mkv")
	ffmpeg := writeReorderedClip(t, path)
	ctx := context.Background()
	it := library.Item{ID: "f", Path: path, Kind: library.KindVideo, ACodec: "mp2"}

	// The client's question first: a seek to 9.3 s lands on the keyframe at
	// 8.342 s, the lead taking it below 9.17 and nothing between.
	k := streamStart(ctx, ffmpeg, []string{"-i", path}, 9.3)
	if k < 8.3 || k > 8.4 {
		t.Fatalf("a seek to 9.3 s was measured landing at %v, want the keyframe at 8.342 s", k)
	}

	// Then the conversion it asks for, at that keyframe.
	plan, err := planConversion(ctx, ffmpeg, it, k, true, "", quality{}, false, false, testLog())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.landCopy(ctx, ffmpeg, it, k, testLog()) {
		t.Fatalf("no seek was found landing on the keyframe at %.3f s", k)
	}
	// Three seconds of picture: enough to reach the sound however far before
	// it the picture began, so a failure says where each began.
	head, first := firstPackets(t, ffmpeg, append(slices.Clone(plan.args), "-frames:v", "75")...)
	if first["0"] == nil || first["1"] == nil {
		t.Fatalf("the conversion did not carry both streams: %q", first)
	}
	if got, want := first["0"][5], keyframeHash(t, ffmpeg, path, k); got != want {
		t.Errorf("the picture begins on the packet hashed %s, want the keyframe at %.3f s, hashed %s", got, k, want)
	}
	// The sound begins with the picture: where the picture is decoded from —
	// a frame or two before the keyframe is shown, the stream being
	// reordered — less the half frame the cut leaves before it, and AAC's
	// priming before that. Later is a hole at the head of the sound, which a
	// browser closes by playing it early; earlier is the cut in the wrong
	// place.
	video, audio := timesOf(t, head, first["0"]), timesOf(t, head, first["1"])
	if audio.pts > video.dts+0.005 || audio.pts < video.dts-video.dur/2-0.03 {
		t.Errorf("the sound begins at %.3f s, the picture is decoded from %.3f s", audio.pts, video.dts)
	}
}

// A time that is not a keyframe — one nothing measured, which is what the
// client is left asking for where the measurement could not be made — has no
// seek landing on it, and the conversion is left as it was planned.
func TestATimeNoSeekLandsOnIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "film.mkv")
	ffmpeg := writeReorderedClip(t, path)
	ctx := context.Background()
	it := library.Item{ID: "f", Path: path, Kind: library.KindVideo, ACodec: "mp2"}
	plan, err := planConversion(ctx, ffmpeg, it, 9.3, true, "", quality{}, false, false, testLog())
	if err != nil {
		t.Fatal(err)
	}
	planned := slices.Clone(plan.args)
	if plan.landCopy(ctx, ffmpeg, it, 9.3, testLog()) {
		t.Fatal("a seek was claimed to land on 9.3 s, where there is no keyframe")
	}
	if !slices.Equal(plan.args, planned) {
		t.Errorf("the plan was changed:\n%q\nwas\n%q", plan.args, planned)
	}
}

// The landing replaces the planned seek where it stood — on the film's
// clock, with ffmpeg's own trim of the sound off — and cuts both streams at
// the keyframe's decode time on the output side. A run without a seek by
// time, or one that re-encodes the picture, is not touched.
func TestALandingReplacesThePlannedSeek(t *testing.T) {
	c := &conversion{args: []string{"-nostdin", "-ss", "56.515", "-copyts", "-i", "in", "-c:v", "copy", "-c:a", "aac"}, seekArg: 2, copied: true}
	c.landOn(packetTimes{dts: 56.432, pts: 56.515, dur: 0.041})
	want := []string{"-nostdin", "-seek_timestamp", "1", "-ss", "56.515000", "-noaccurate_seek", "-copyts", "-i", "in",
		"-c:v", "copy", "-c:a", "aac", "-ss", "56.411500"}
	if !slices.Equal(c.args, want) {
		t.Errorf("landed as\n%q, want\n%q", c.args, want)
	}
	if c.args[c.seekArg] != "56.515000" {
		t.Errorf("the seek is no longer where seekArg says: %q", c.args[c.seekArg])
	}

	base := []string{"-i", "in", "-map", "0:v:0", "-c:v", "copy", "-c:a", "aac"}
	c = &conversion{args: slices.Clone(base), copied: true}
	if c.landCopy(context.Background(), "ffmpeg", library.Item{Path: "in"}, 42, testLog()) || !slices.Equal(c.args, base) {
		t.Errorf("a run with no seek was changed: %q", c.args)
	}
	seeking := []string{"-ss", "42.000", "-copyts", "-i", "in", "-c:v", "libx264", "-c:a", "aac"}
	c = &conversion{args: slices.Clone(seeking), seekArg: 1}
	if c.landCopy(context.Background(), "ffmpeg", library.Item{Path: "in"}, 42, testLog()) || !slices.Equal(c.args, seeking) {
		t.Errorf("a re-encode was changed: %q", c.args)
	}
}

// /api/keyframe measures through the very input the conversion reads. For
// content inside another file that is this server's own stream, and the two
// must name it alike — the answer used to be the time asked for, unmeasured,
// from when such content reached ffmpeg only through a pipe.
func TestKeyframeInputIsTheConversions(t *testing.T) {
	plain := library.Item{ID: "p", Path: "/films/a.mkv", Kind: library.KindVideo}
	in, ok := timeSeekInput(plain, 30)
	conv, _, _ := convertInput(plain, 30)
	if !ok || !slices.Equal(in, conv.args) {
		t.Errorf("a plain file: %q against the conversion's %q", in, conv.args)
	}

	withLoopback(t, "http://127.0.0.1:9")
	_, member := archivedLibrary(t, bytes.Repeat([]byte{7}, 4096), "Feature.mkv")
	in, ok = timeSeekInput(member, 30)
	conv, byPosition, err := convertInput(member, 30)
	if err != nil || byPosition {
		t.Fatalf("convertInput: %v, by position %v", err, byPosition)
	}
	if !ok || !slices.Equal(in, conv.args) {
		t.Errorf("an archived member: %q against the conversion's %q", in, conv.args)
	}
	if !strings.Contains(strings.Join(in, " "), "/api/stream/"+member.ID) {
		t.Errorf("an archived member is not measured over the loopback stream: %q", in)
	}

	// With no loopback stream there is no seek by time to measure.
	library.SetLoopback("")
	if _, ok := timeSeekInput(member, 30); ok {
		t.Error("an archived member with only a pipe to read it through claims a time seek")
	}
}
