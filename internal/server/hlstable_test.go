package server

import (
	"math"
	"strings"
	"testing"
	"time"
)

// The boundary rule is the HLS muxer's own, and it was checked against a
// real conversion: 259 segments of a long release, every one the length the
// rule predicted. What the rule has to get right is the cumulative count —
// the k-th boundary is the first keyframe at or after k segment-lengths from
// the first keyframe, not four seconds on from the previous boundary — and
// that one keyframe is only ever one boundary.
func TestHLSTableFollowsTheMuxersRule(t *testing.T) {
	// Keyframes every three seconds: the first cut wants four and takes the
	// keyframe at six; the second wants eight and takes nine; the third
	// wants twelve and lands on it; the fourth wants sixteen and takes
	// eighteen. Counted from the previous boundary instead, every cut
	// would have come a keyframe later.
	tb := hlsTableFromKeys([]float64{0, 3, 6, 9, 12, 15, 18}, 21)
	same := func(got, want []float64) bool {
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
	if want := []float64{0, 6, 9, 12, 18}; !same(tb.starts, want) {
		t.Fatalf("starts = %v, want %v", tb.starts, want)
	}
	if d := tb.dur(4); math.Abs(d-3) > 1e-9 {
		t.Errorf("the last segment runs to the end of the film: %.3f, want 3", d)
	}
	// Keyframes ten seconds apart: one keyframe is only ever one boundary,
	// so the second cut — wanting eight — takes the keyframe at twenty,
	// not the one at ten again; and the segments are as long as the gaps.
	sparse := hlsTableFromKeys([]float64{0, 10.417, 20.833, 31.25}, 40)
	if want := []float64{0, 10.417, 20.833, 31.25}; !same(sparse.starts, want) {
		t.Fatalf("sparse starts = %v, want %v", sparse.starts, want)
	}
	// A keyframe a sliver before the end is not worth a segment of its own.
	if got := hlsTableFromKeys([]float64{0, 5, 9.9}, 10); len(got.starts) != 2 {
		t.Errorf("a boundary a tenth of a second from the end was kept: %v", got.starts)
	}
	if hlsTableFromKeys(nil, 10) != nil || hlsTableFromKeys([]float64{12}, 10) != nil {
		t.Error("a table was made with nothing to cut")
	}
	// Which segment a moment falls in.
	for _, c := range []struct {
		at   float64
		want int
	}{{0, 0}, {5.9, 0}, {6, 1}, {11.99, 2}, {12, 3}, {20.9, 4}, {99, 4}} {
		if got := tb.at(c.at); got != c.want {
			t.Errorf("at(%.3f) = %d, want %d", c.at, got, c.want)
		}
	}
}

// A re-encode is cut on the four-second grid from zero, whatever the film's
// own keyframes, with the remainder in the last segment.
func TestHLSGridIsEveryFourSeconds(t *testing.T) {
	tb := hlsGrid(13.5)
	if len(tb.starts) != 4 || tb.starts[3] != 12 || tb.dur(3) != 1.5 || !tb.grid {
		t.Errorf("grid over 13.5 s = %v (last %.2f), want four segments ending in 1.5 s", tb.starts, tb.dur(3))
	}
	if tb := hlsGrid(12.1); len(tb.starts) != 3 {
		t.Errorf("a tenth of a second past the grid got a segment of its own: %v", tb.starts)
	}
	if tb := hlsGrid(1); len(tb.starts) != 1 || tb.dur(0) != 1 {
		t.Errorf("a one-second film is one segment: %v", tb.starts)
	}
	if hlsGrid(0) != nil {
		t.Error("a film of no length got a table")
	}
}

// The playlist is the whole film, complete from the first request: every
// segment named with its real length, the end marker, and the moment the
// viewer asked for.
func TestHLSTablePlaylistIsCompleteFromTheStart(t *testing.T) {
	tb := hlsTableFromKeys([]float64{0, 10.417, 20.833}, 25)
	body := string(tb.playlist("sess/", 12.5))
	for _, want := range []string{
		"#EXT-X-PLAYLIST-TYPE:VOD\n",
		"#EXT-X-TARGETDURATION:11\n",
		"#EXT-X-INDEPENDENT-SEGMENTS\n",
		"#EXT-X-START:TIME-OFFSET=12.500,PRECISE=YES\n",
		"#EXTINF:10.417000,\nsess/seg00000.ts\n",
		"#EXTINF:10.416000,\nsess/seg00001.ts\n",
		"#EXTINF:4.167000,\nsess/seg00002.ts\n",
		"#EXT-X-ENDLIST\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("playlist lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "EVENT") {
		t.Errorf("a complete playlist calls itself an event:\n%s", body)
	}
	// From the start, nothing to say about where to begin.
	if strings.Contains(string(tb.playlist("", 0)), "EXT-X-START") {
		t.Error("a playlist from zero names a start offset")
	}
}

// The muxer's list is what says a segment is finished, and it is read back
// against the table: the first line's start is always written as zero and
// is not judged; every other start has to be the table's, and every end the
// next start, or the table was wrong about this file.
func TestHLSSegmentListIsReadAndJudged(t *testing.T) {
	cuts := parseSegmentList([]byte(
		"seg00149.ts,0.000000,1198.624000\n" +
			"seg00150.ts,1198.625000,1203.499000\n" +
			"/tmp/x/run7-seg00151.ts,1203.500000,1213.874000\n" +
			"garbage\nseg00152.ts,nope,1\n"))
	if len(cuts) != 3 || cuts[0].index != 149 || cuts[2].index != 151 || cuts[1].start != 1198.625 {
		t.Fatalf("parsed %+v", cuts)
	}
	tb := &hlsTable{starts: []float64{1188.25, 1198.625, 1203.5, 1213.875}, end: 1220}
	tb.starts, tb.end = append([]float64{}, tb.starts...), 1220
	// Index the fixture's numbers onto this table: 149 → 0 and so on.
	for i := range cuts {
		cuts[i].index -= 149
	}
	if err := tb.verify(cuts[0], true); err != nil {
		t.Errorf("the first line, whose start reads zero, was refused: %v", err)
	}
	if err := tb.verify(cuts[0], false); err == nil {
		t.Error("a start of zero on a later line passed")
	}
	for _, c := range cuts[1:] {
		if err := tb.verify(c, false); err != nil {
			t.Errorf("a cut where the table said was refused: %v", err)
		}
	}
	// A run from the film's start shifted whole by its soundtrack's priming
	// is still cut where the table says.
	if err := tb.verify(hlsCut{index: 1, start: 1198.671, end: 1203.546}, false); err != nil {
		t.Errorf("a cut shifted by a soundtrack's priming was refused: %v", err)
	}
	if err := tb.verify(hlsCut{index: 1, start: 1199.125, end: 1203.5}, false); err == nil {
		t.Error("a cut half a second off passed")
	}
	wrong := hlsCut{index: 2, start: 1203.5, end: 1224.292} // an eight-second segment: a cut was missed
	if err := tb.verify(wrong, false); err == nil {
		t.Error("a segment that ran through the next boundary passed")
	}
	// The last segment ends where the film does, give or take the probe's
	// estimate of that.
	if err := tb.verify(hlsCut{index: 3, start: 1213.875, end: 1219.2}, false); err != nil {
		t.Errorf("the last segment was refused: %v", err)
	}
	if err := tb.verify(hlsCut{index: 3, start: 1213.875, end: 1216}, false); err == nil {
		t.Error("a last segment cut four seconds short passed")
	}
	// A re-encode may be a frame late on its grid point, never a segment.
	grid := hlsGrid(20)
	if err := grid.verify(hlsCut{index: 1, start: 4.042, end: 8.02}, false); err != nil {
		t.Errorf("a frame late on the grid was refused: %v", err)
	}
	if err := grid.verify(hlsCut{index: 1, start: 8, end: 12}, false); err == nil {
		t.Error("a re-encode cut a whole segment late passed")
	}
}

// Whether a request waits for the running conversion or restarts it: wait
// while the run is about to get there, restart for anything behind it,
// past its end, or too far ahead at the pace it has shown.
func TestHLSWaitOrRestart(t *testing.T) {
	now := time.Now()
	fresh := &hlsRunState{next: 10, until: 100, started: now}
	if !hlsWaitFor(fresh, 10, now) || !hlsWaitFor(fresh, 12, now) {
		t.Error("the first requests of a fresh run restarted it")
	}
	if hlsWaitFor(fresh, 20, now) {
		t.Error("a request ten segments ahead of a run that has shown no pace waited on it")
	}
	if hlsWaitFor(fresh, 9, now) || hlsWaitFor(fresh, 100, now) || hlsWaitFor(nil, 10, now) {
		t.Error("a segment behind the run, past its end, or with no run waited")
	}
	// A copy at forty times real time makes a segment every tenth of a
	// second: twenty segments ahead is two seconds, worth waiting for.
	quick := &hlsRunState{next: 10, until: 100, started: now.Add(-time.Second), produced: 10}
	if !hlsWaitFor(quick, 30, now) {
		t.Error("a fast run twenty segments away was restarted")
	}
	// A re-encode at real time makes one every four seconds: two ahead is
	// eight, and a fresh start is quicker.
	slow := &hlsRunState{next: 10, until: 100, started: now.Add(-8 * time.Second), produced: 2}
	if hlsWaitFor(slow, 12, now) || !hlsWaitFor(slow, 10, now) {
		t.Error("a slow run was waited on too far ahead, or not at all")
	}
	ended := &hlsRunState{next: 10, until: 100, ended: true}
	if hlsWaitFor(ended, 10, now) {
		t.Error("a run that has ended was waited on")
	}
}

// The landing probe reads one packet: the first PES of a video stream, and
// the 33-bit presentation time in its header.
func TestTSFirstVideoPTS(t *testing.T) {
	pkt := func(pid int, start bool, payload []byte) []byte {
		p := make([]byte, 188)
		p[0] = 0x47
		p[1] = byte(pid >> 8)
		if start {
			p[1] |= 0x40
		}
		p[2] = byte(pid)
		p[3] = 0x10 // payload only, no adaptation field
		copy(p[4:], payload)
		return p
	}
	pes := func(streamID byte, ticks uint64) []byte {
		h := []byte{0, 0, 1, streamID, 0, 0, 0x80, 0x80, 5, 0, 0, 0, 0, 0}
		h[9] = 0x21 | byte(ticks>>29)&0x0E
		h[10] = byte(ticks >> 22)
		h[11] = 0x01 | byte(ticks>>14)&0xFE
		h[12] = byte(ticks >> 7)
		h[13] = 0x01 | byte(ticks<<1)
		return h
	}
	ticks := uint64(1188.25 * 90000)
	stream := append(append(append([]byte{},
		pkt(0, true, []byte{0, 0, 0xB0})...), // a PAT, which is not a PES
		pkt(0x101, true, pes(0xC0, 99))...), // audio: the wrong stream
		pkt(0x100, true, pes(0xE0, ticks))...) // the picture
	got, ok := tsFirstVideoPTS(stream)
	if !ok || math.Abs(got-1188.25) > 1e-6 {
		t.Fatalf("pts = %.6f, %v; want 1188.25", got, ok)
	}
	// With an adaptation field in front of the payload.
	withAF := pkt(0x100, true, nil)
	withAF[3] = 0x30
	withAF[4] = 7 // seven bytes of adaptation field follow
	copy(withAF[12:], pes(0xE0, 90000))
	if got, ok := tsFirstVideoPTS(withAF); !ok || got != 1 {
		t.Errorf("behind an adaptation field: %.3f, %v", got, ok)
	}
	if _, ok := tsFirstVideoPTS(pkt(0x101, true, pes(0xC0, 1))); ok {
		t.Error("a stream with no picture answered")
	}
	if _, ok := tsFirstVideoPTS(nil); ok {
		t.Error("nothing answered")
	}
}
