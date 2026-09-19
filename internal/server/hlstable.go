package server

// The segment table: every cut of a film decided before any of them is made.
//
// A playlist that lists the whole film from the first request — a running
// time, a position, native seeking — has to name every segment and say how
// long each one is, which means knowing where the conversion will cut. For a
// re-encoded picture that is decided here: the encoder is told to put a
// keyframe on every fourth second and the muxer cuts on each. For a copied
// picture the cuts can only fall on the file's own keyframes, so the table is
// read off the container's index (library.Keyframes) and the segment
// boundaries are chosen by the rule ffmpeg's own HLS muxer uses — the k-th
// boundary is the first keyframe at or after k times the segment length,
// counted from the first keyframe — which was checked against a real
// conversion: 259 segments of a two-and-a-half-hour release, every one the
// length the rule predicted.
//
// The muxer is then told those times explicitly, and reports every cut it
// made in a list of its own (`-segment_list`, csv), with the real timestamps.
// That list is what says a segment is finished — a line is written only once
// the file is closed, so a run that is killed lists nothing for the file it
// was writing — and it is checked against the table, segment by segment: a
// cut anywhere but where the table said ends the conversion rather than
// serving a playlist that lies about it.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// hlsTable is where a film is cut: the start of every segment, in seconds of
// the film's own clock, and where the film ends.
type hlsTable struct {
	starts []float64
	end    float64
	// grid says the cuts are on the four-second grid a re-encode is told to
	// keep, rather than on the file's own keyframes.
	grid bool
}

// hlsTableFromKeys chooses the boundaries among a film's keyframes by the
// HLS muxer's rule. Nil where there is nothing to cut: no keyframes, or a
// film that ends before its first one.
func hlsTableFromKeys(keys []float64, end float64) *hlsTable {
	if len(keys) == 0 || end <= keys[0] {
		return nil
	}
	starts := []float64{keys[0]}
	for _, k := range keys[1:] {
		// One keyframe can only be one boundary, so a gap longer than a
		// segment takes the next keyframe after it rather than the same
		// one twice — which is what the muxer does too, its cut waiting
		// for the next keyframe packet to arrive.
		if k >= keys[0]+float64(len(starts))*hlsSegmentSec && k < end-hlsTableTail {
			starts = append(starts, k)
		}
	}
	return &hlsTable{starts: starts, end: end}
}

// hlsTableTail is the shortest last segment worth having: a boundary this
// close to the end would leave a sliver behind it, and the sliver goes into
// the segment before it instead.
const hlsTableTail = 0.25

// hlsGrid is the table for a re-encoded picture: a boundary every
// hlsSegmentSec from zero, the encoder being made to keep to it.
func hlsGrid(end float64) *hlsTable {
	if end <= 0 {
		return nil
	}
	var starts []float64
	for at := 0.0; at < end-hlsTableTail; at += hlsSegmentSec {
		starts = append(starts, at)
	}
	if len(starts) == 0 {
		starts = []float64{0}
	}
	return &hlsTable{starts: starts, end: end, grid: true}
}

// n is how many segments the film is.
func (t *hlsTable) n() int { return len(t.starts) }

// dur is how long segment k lasts.
func (t *hlsTable) dur(k int) float64 {
	if k+1 < len(t.starts) {
		return t.starts[k+1] - t.starts[k]
	}
	return t.end - t.starts[k]
}

// at is the segment a moment of the film falls in.
func (t *hlsTable) at(sec float64) int {
	k := 0
	for k+1 < len(t.starts) && t.starts[k+1] <= sec {
		k++
	}
	return k
}

// playlist is the whole film as a VOD playlist: every segment with its
// length, the end marker from the first request, and — where the viewer
// asked for a moment — where to begin. prefix goes in front of every
// segment name (the session, where the playlist is served from outside its
// path; nothing from inside it).
func (t *hlsTable) playlist(prefix string, startAt float64) []byte {
	var b strings.Builder
	longest := 0.0
	for k := range t.starts {
		longest = math.Max(longest, t.dur(k))
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%d\n"+
		"#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n",
		int(math.Ceil(longest)))
	if startAt > 0 && startAt < t.end {
		// PRECISE: begin at the moment itself, decoding from the segment's
		// keyframe and dropping what comes before — not at the segment.
		fmt.Fprintf(&b, "#EXT-X-START:TIME-OFFSET=%.3f,PRECISE=YES\n", startAt)
	}
	for k := range t.starts {
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s%s\n", t.dur(k), prefix, hlsSegmentFile(k))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return []byte(b.String())
}

// hlsSegmentFile names segment k the way the muxer's pattern does.
func hlsSegmentFile(k int) string { return fmt.Sprintf("seg%05d.ts", k) }

// hlsSegmentIndex reads k back out of a segment name; -1 for a name that is
// not one.
func hlsSegmentIndex(name string) int {
	if !hlsSegmentName(name) {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg"), ".ts"))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// hlsCut is one line of the muxer's list: which file, and the timestamps it
// was cut at.
type hlsCut struct {
	index      int
	start, end float64
}

// parseSegmentList reads the muxer's csv list: one line per finished
// segment, "name,start,end". Lines it cannot read are skipped rather than
// failing the rest — the list is rewritten whole, so a torn read is a
// missing line, not a wrong one.
func parseSegmentList(b []byte) []hlsCut {
	var cuts []hlsCut
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) != 3 {
			continue
		}
		name := parts[0]
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		// Files are named for their run ("run7-seg00150.ts"); the index is
		// what follows the run.
		if i := strings.LastIndexByte(name, '-'); i >= 0 {
			name = name[i+1:]
		}
		k := hlsSegmentIndex(name)
		start, err1 := strconv.ParseFloat(parts[1], 64)
		end, err2 := strconv.ParseFloat(parts[2], 64)
		if k < 0 || err1 != nil || err2 != nil {
			continue
		}
		cuts = append(cuts, hlsCut{index: k, start: start, end: end})
	}
	return cuts
}

// Tolerances on a cut, in seconds. A copied picture is cut on the keyframe
// the table names and its timestamps are the file's own — except that a run
// from the start of a film whose soundtrack begins before zero (the priming
// of an AAC track, which ffmpeg-muxed files carry) is shifted whole by that
// much, so the muxer's clock never goes negative: measured at 46 ms. A cut
// in the wrong place is a keyframe away, which is a frame at the least and
// a group of pictures as a rule, so a tenth of a second tells the two
// apart. A re-encoded picture is cut on the first frame at or after the
// grid point, so it may be a frame late. Ends come from the last packet's
// time plus its length, a rounding away from the next start.
const (
	hlsCutKeyTol  = 0.1
	hlsCutGridTol = 0.25
	hlsCutEndTol  = 0.25
	// hlsLandTol is how close a probe's landing has to be to a boundary to
	// be the boundary: a keyframe's own time, to the container's rounding.
	hlsLandTol = 0.005
	// hlsCutFinalTol is for the film's last segment, whose end is measured
	// against a duration the probe estimated rather than a cut the table
	// chose.
	hlsCutFinalTol = 2.0
)

// verify checks one cut against the table. first says the line is the run's
// first, whose start the muxer always writes as zero (measured: every first
// line of every list reads 0.000000, whatever the timestamps in the file),
// so only its end is checked.
func (t *hlsTable) verify(c hlsCut, first bool) error {
	if c.index < 0 || c.index >= len(t.starts) {
		return fmt.Errorf("segment %d is not in a table of %d", c.index, len(t.starts))
	}
	startTol := hlsCutKeyTol
	if t.grid {
		startTol = hlsCutGridTol
	}
	if !first && math.Abs(c.start-t.starts[c.index]) > startTol {
		return fmt.Errorf("segment %d starts at %.3f, the table says %.3f", c.index, c.start, t.starts[c.index])
	}
	if c.index+1 < len(t.starts) {
		if math.Abs(c.end-t.starts[c.index+1]) > hlsCutEndTol {
			return fmt.Errorf("segment %d ends at %.3f, the table says %.3f", c.index, c.end, t.starts[c.index+1])
		}
		return nil
	}
	if math.Abs(c.end-t.end) > hlsCutFinalTol {
		return fmt.Errorf("the last segment ends at %.3f, the film at %.3f", c.end, t.end)
	}
	return nil
}

// hlsSeekLead is what ffmpeg takes off an input seek before asking the
// demuxer, for a stream whose frames are reordered: 3/23 of a second
// (fftools, `seek_timestamp -= 3*AV_TIME_BASE/23`), on the reasoning that a
// keyframe's decode time runs that far ahead of its presentation time. The
// demuxer then lands on the last keyframe at or before the reduced time —
// which, asked for a keyframe's own time, is the keyframe *before* it. So
// the seek asks for the boundary plus this lead, and lands on the boundary.
// Measured on this ffmpeg; and never relied on: where it lands is read back
// from the probe (landing), and a run is cut from wherever that was.
const hlsSeekLead = 3.0 / 23

// hlsRestartCost is how long a segment request will wait for a running
// conversion to reach it before stopping that conversion and starting one at
// the segment itself — roughly what a fresh start costs: a probe, a process,
// and the first segment.
const hlsRestartCost = 3 * time.Second

// hlsRunState is what a segment request can see of the conversion in
// progress, for deciding whether to wait for it.
type hlsRunState struct {
	next     int       // the segment it is writing now
	until    int       // where it will stop, exclusive
	ended    bool      // it has finished, for good or ill
	started  time.Time // when it began
	produced int       // whole segments it has finished
}

// hlsWaitFor says whether a request for segment k should wait on the
// conversion described, rather than start one at k. Waiting is right when
// the run will reach k about as soon as a fresh one would: the segments it
// has to produce first, at the pace it has shown — a second a segment until
// it has shown any, which lets the first requests of a fresh run wait for
// it rather than restart it.
func hlsWaitFor(r *hlsRunState, k int, now time.Time) bool {
	if r == nil || r.ended || k < r.next || k >= r.until {
		return false
	}
	pace := time.Second
	if r.produced >= 2 {
		pace = now.Sub(r.started) / time.Duration(r.produced)
	}
	return time.Duration(k-r.next)*pace <= hlsRestartCost
}

// tsFirstVideoPTS reads the presentation time of the first video packet in
// a transport stream: the first packet that begins a PES of a video stream,
// and the 33-bit timestamp in its header, in 90 kHz ticks. It is how the
// landing probe learns where a seek put the demuxer, from the one packet
// ffmpeg is asked to copy out.
func tsFirstVideoPTS(b []byte) (float64, bool) {
	const packet = 188
	for pos := 0; pos+packet <= len(b); pos += packet {
		p := b[pos : pos+packet]
		if p[0] != 0x47 || p[1]&0x40 == 0 { // sync, payload_unit_start
			continue
		}
		at := 4
		if p[3]&0x20 != 0 { // adaptation field
			at += 1 + int(p[4])
		}
		if at+14 > packet {
			continue
		}
		pes := p[at:]
		if pes[0] != 0 || pes[1] != 0 || pes[2] != 1 || pes[3] < 0xE0 || pes[3] > 0xEF {
			continue
		}
		if pes[6]&0xC0 != 0x80 || pes[7]&0x80 == 0 { // MPEG-2 PES with a PTS
			return 0, false
		}
		ticks := uint64(pes[9]>>1&7)<<30 | uint64(pes[10])<<22 |
			uint64(pes[11]>>1&0x7F)<<15 | uint64(pes[12])<<7 | uint64(pes[13]>>1)
		return float64(ticks) / 90000, true
	}
	return 0, false
}
