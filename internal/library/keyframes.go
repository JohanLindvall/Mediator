package library

// Where a film can be cut without decoding it: its keyframes, read off the
// container's own index rather than by scanning the stream.
//
// A segmented conversion that copies the picture can only cut at a keyframe,
// and a playlist that lists every segment of a film before any of them exist
// (server/hls.go) has to know where those cuts will fall. The stream would say
// — one ffprobe over every packet — but that is a read of the whole file:
// measured on a 14 GB release, 19 s from a warm cache and a full pass over the
// disk from a cold one, paid while the viewer waits for a playlist. The
// containers keep an index instead. Matroska writes a cue point per video
// keyframe — measured on that release, 697 cues on the video track against
// 697 keyframes in the stream, every one coinciding — and an MP4 lists its
// sync samples in the `stss` table. Both are a handful of reads, at the end
// of the file or the front of it, and both go through OpenItem, so a member
// inside an archive answers as a plain file does.
//
// What comes back is offered, not trusted: every cut a conversion makes is
// checked against the table afterwards (server/hlstable.go), so an index that
// lies costs one conversion and not a wrong playlist.

import (
	"encoding/binary"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// Keyframes lists the times, in seconds, at which the item's picture can be
// cut, ascending. ok is false where the container keeps no such index this
// reads, or it could not be read — never a guess.
func Keyframes(it Item) (times []float64, ok bool) {
	if it.Size <= 0 {
		return nil, false
	}
	var read func(io.ReaderAt, int64) ([]float64, bool)
	switch strings.ToLower(filepath.Ext(it.Name)) {
	case ".mkv", ".webm":
		read = mkvKeyframes
	case ".mp4", ".m4v", ".mov":
		read = mp4Keyframes
	default:
		return nil, false
	}
	f, err := OpenItem(it)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	times, ok = read(f, it.Size)
	if !ok || len(times) == 0 {
		return nil, false
	}
	slices.Sort(times)
	return slices.Compact(times), true
}

// Matroska element ids, as they read with their marker bits on.
const (
	ebmlHeaderID      = 0x1A45DFA3
	mkvSegmentID      = 0x18538067
	mkvSeekHeadID     = 0x114D9B74
	mkvSeekID         = 0x4DBB
	mkvSeekTargetID   = 0x53AB
	mkvSeekPosID      = 0x53AC
	mkvInfoID         = 0x1549A966
	mkvTimeScaleID    = 0x2AD7B1
	mkvTracksID       = 0x1654AE6B
	mkvTrackEntryID   = 0xAE
	mkvTrackNumberID  = 0xD7
	mkvTrackTypeID    = 0x83
	mkvClusterID      = 0x1F43B675
	mkvCuesID         = 0x1C53BB6B
	mkvCuePointID     = 0xBB
	mkvCueTimeID      = 0xB3
	mkvCuePositionsID = 0xB7
	mkvCueTrackID     = 0xF7

	mkvTrackVideo = 1
	// mkvReadCap bounds any one element read whole. The cues of a long film
	// are tens of kilobytes; an element past this is not one this reads.
	mkvReadCap = 64 << 20
	// mkvHeadElements bounds the walk before the first cluster: the head of
	// a file is a handful of elements, and a walk that has not met the
	// clusters after this many is reading something else.
	mkvHeadElements = 256
)

// ebmlReader reads element headers off a ReaderAt.
type ebmlReader struct {
	f    io.ReaderAt
	size int64
}

// vint reads a variable-length integer at pos: its value, with the length
// marker kept (an id) or stripped (a size), and how many bytes it took.
// unknown is a size of all ones, which Matroska uses for an element whose
// length was not known when it was written.
func (r *ebmlReader) vint(pos int64, keepMarker bool) (v uint64, n int, unknown, ok bool) {
	var b [8]byte
	got, err := r.f.ReadAt(b[:], pos)
	if got == 0 && err != nil {
		return 0, 0, false, false
	}
	if b[0] == 0 {
		return 0, 0, false, false
	}
	n = 1
	for b[0]&(0x80>>(n-1)) == 0 {
		n++
	}
	if got < n {
		return 0, 0, false, false
	}
	v = uint64(b[0])
	mask := uint64(0xFF >> n)
	if !keepMarker {
		v &= mask
	}
	all := v == mask // every value bit set so far
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
		all = all && b[i] == 0xFF
	}
	return v, n, !keepMarker && all, true
}

// elem reads the header at pos: the id, and the bounds of the data. An
// element of unknown size runs to the end of the enclosing space.
func (r *ebmlReader) elem(pos, end int64) (id uint64, start, stop int64, ok bool) {
	id, n, _, ok := r.vint(pos, true)
	if !ok {
		return 0, 0, 0, false
	}
	size, m, unknown, ok := r.vint(pos+int64(n), false)
	if !ok {
		return 0, 0, 0, false
	}
	start = pos + int64(n+m)
	if unknown {
		return id, start, end, true
	}
	stop = start + int64(size)
	if size < 0 || stop > end {
		return 0, 0, 0, false
	}
	return id, start, stop, true
}

// uint reads an unsigned integer element's data.
func (r *ebmlReader) uint(start, stop int64) (uint64, bool) {
	n := stop - start
	if n < 0 || n > 8 {
		return 0, false
	}
	var b [8]byte
	if _, err := r.f.ReadAt(b[:n], start); err != nil && n > 0 {
		return 0, false
	}
	var v uint64
	for i := int64(0); i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, true
}

// mkvKeyframes reads the cue points of the first video track.
//
// The head of the file is walked element by element — the seek head, which
// says where the cues are; the info, for the timestamp scale; the tracks,
// for which one is the picture — and stops at the first cluster, which is
// where the media begins. The cues themselves are usually at the end of the
// file, where the seek head points; a file that keeps them in front is read
// on the walk.
func mkvKeyframes(f io.ReaderAt, size int64) ([]float64, bool) {
	r := &ebmlReader{f: f, size: size}
	id, _, stop, ok := r.elem(0, size)
	if !ok || id != ebmlHeaderID {
		return nil, false
	}
	id, segStart, segEnd, ok := r.elem(stop, size)
	if !ok || id != mkvSegmentID {
		return nil, false
	}

	scale := uint64(1_000_000) // nanoseconds per tick, the default
	videoTrack := uint64(0)
	cuesAt := int64(-1) // relative to the segment's data
	var cuesStart, cuesStop int64
	haveCues := false
	pos := segStart
	for i := 0; i < mkvHeadElements && pos < segEnd; i++ {
		id, start, stop, ok := r.elem(pos, segEnd)
		if !ok {
			break
		}
		switch id {
		case mkvSeekHeadID:
			r.eachChild(start, stop, func(id uint64, s, e int64) {
				if id != mkvSeekID {
					return
				}
				var target, where uint64
				r.eachChild(s, e, func(id uint64, s, e int64) {
					switch id {
					case mkvSeekTargetID:
						target, _ = r.uint(s, e)
					case mkvSeekPosID:
						where, _ = r.uint(s, e)
					}
				})
				if target == mkvCuesID {
					cuesAt = int64(where)
				}
			})
		case mkvInfoID:
			r.eachChild(start, stop, func(id uint64, s, e int64) {
				if id == mkvTimeScaleID {
					if v, ok := r.uint(s, e); ok && v > 0 {
						scale = v
					}
				}
			})
		case mkvTracksID:
			r.eachChild(start, stop, func(id uint64, s, e int64) {
				if id != mkvTrackEntryID || videoTrack != 0 {
					return
				}
				var number, kind uint64
				r.eachChild(s, e, func(id uint64, s, e int64) {
					switch id {
					case mkvTrackNumberID:
						number, _ = r.uint(s, e)
					case mkvTrackTypeID:
						kind, _ = r.uint(s, e)
					}
				})
				if kind == mkvTrackVideo && number > 0 {
					videoTrack = number
				}
			})
		case mkvCuesID:
			cuesStart, cuesStop, haveCues = start, stop, true
		}
		if id == mkvClusterID || stop >= segEnd {
			break
		}
		pos = stop
	}
	if !haveCues {
		if cuesAt < 0 {
			return nil, false
		}
		id, start, stop, ok := r.elem(segStart+cuesAt, segEnd)
		if !ok || id != mkvCuesID {
			return nil, false
		}
		cuesStart, cuesStop = start, stop
	}
	if videoTrack == 0 || cuesStop-cuesStart > mkvReadCap {
		return nil, false
	}

	var times []float64
	r.eachChild(cuesStart, cuesStop, func(id uint64, s, e int64) {
		if id != mkvCuePointID {
			return
		}
		var at uint64
		haveTime, ours := false, false
		r.eachChild(s, e, func(id uint64, s, e int64) {
			switch id {
			case mkvCueTimeID:
				at, haveTime = r.uint(s, e)
			case mkvCuePositionsID:
				r.eachChild(s, e, func(id uint64, s, e int64) {
					if id == mkvCueTrackID {
						if track, ok := r.uint(s, e); ok && track == videoTrack {
							ours = true
						}
					}
				})
			}
		})
		if haveTime && ours {
			times = append(times, float64(at)*float64(scale)/1e9)
		}
	})
	return times, len(times) > 0
}

// eachChild calls fn for every element between start and stop.
func (r *ebmlReader) eachChild(start, stop int64, fn func(id uint64, s, e int64)) {
	for pos := start; pos < stop; {
		id, s, e, ok := r.elem(pos, stop)
		if !ok || e <= pos {
			return
		}
		fn(id, s, e)
		pos = e
	}
}

// mp4TableCap bounds a sample table read whole: the sync-sample table of a
// long film is kilobytes, the time-to-sample table of a variable-rate one a
// few megabytes.
const mp4TableCap = 64 << 20

// mp4Keyframes reads the sync samples of the first video track and works
// out when each is presented: its decode time from the time-to-sample table,
// its composition offset from `ctts`, and the edit list's shift — which is
// how ffmpeg times the same packets, so the times here are the ones the
// conversion's own cuts will be compared against.
func mp4Keyframes(f io.ReaderAt, size int64) ([]float64, bool) {
	moovS, moovE, ok := child(f, 0, size, "moov")
	if !ok {
		return nil, false
	}
	movieScale := mp4Timescale(f, moovS, moovE, "mvhd")
	var times []float64
	found := false
	eachBox(f, moovS, moovE, func(t string, trakS, trakE int64) bool {
		if t != "trak" {
			return true
		}
		mdiaS, mdiaE, ok := child(f, trakS, trakE, "mdia")
		if !ok || !mp4IsVideo(f, mdiaS, mdiaE) {
			return true
		}
		times, found = mp4TrackKeyframes(f, trakS, trakE, mdiaS, mdiaE, movieScale)
		return false
	})
	return times, found && len(times) > 0
}

// mp4IsVideo says whether a media box is the picture's.
func mp4IsVideo(f io.ReaderAt, mdiaS, mdiaE int64) bool {
	hdlrS, _, ok := child(f, mdiaS, mdiaE, "hdlr")
	if !ok {
		return false
	}
	var h [12]byte
	if _, err := f.ReadAt(h[:], hdlrS); err != nil {
		return false
	}
	return string(h[8:12]) == "vide"
}

// mp4Timescale reads the timescale out of an `mvhd` or `mdhd` box: version
// and flags, then two times and the scale — four bytes each at version 0,
// eight at version 1.
func mp4Timescale(f io.ReaderAt, start, end int64, box string) float64 {
	s, e, ok := child(f, start, end, box)
	if !ok || s+4 > e {
		return 0
	}
	var version [1]byte
	if _, err := f.ReadAt(version[:], s); err != nil {
		return 0
	}
	at := int64(12)
	if version[0] == 1 {
		at = 20
	}
	if s+at+4 > e {
		return 0
	}
	var sc [4]byte
	if _, err := f.ReadAt(sc[:], s+at); err != nil {
		return 0
	}
	return float64(binary.BigEndian.Uint32(sc[:]))
}

// mp4Table reads a whole sample table box: its entries after the version,
// flags and count, each `width` bytes.
func mp4Table(f io.ReaderAt, s, e int64, width int64) ([]byte, int, bool) {
	if s+8 > e {
		return nil, 0, false
	}
	var hdr [8]byte
	if _, err := f.ReadAt(hdr[:], s); err != nil {
		return nil, 0, false
	}
	n := int64(binary.BigEndian.Uint32(hdr[4:8]))
	n = min(n, (e-s-8)/width)
	if n <= 0 || n*width > mp4TableCap {
		return nil, 0, false
	}
	b := make([]byte, n*width)
	if _, err := f.ReadAt(b, s+8); err != nil {
		return nil, 0, false
	}
	return b, int(n), true
}

func mp4TrackKeyframes(f io.ReaderAt, trakS, trakE, mdiaS, mdiaE int64, movieScale float64) ([]float64, bool) {
	scale := mp4Timescale(f, mdiaS, mdiaE, "mdhd")
	if scale <= 0 {
		return nil, false
	}
	minfS, minfE, ok := child(f, mdiaS, mdiaE, "minf")
	if !ok {
		return nil, false
	}
	stblS, stblE, ok := child(f, minfS, minfE, "stbl")
	if !ok {
		return nil, false
	}
	sttsS, sttsE, ok := child(f, stblS, stblE, "stts")
	if !ok {
		return nil, false
	}
	stts, nstts, ok := mp4Table(f, sttsS, sttsE, 8)
	if !ok {
		return nil, false
	}
	var ctts []byte
	nctts := 0
	if s, e, ok := child(f, stblS, stblE, "ctts"); ok {
		ctts, nctts, _ = mp4Table(f, s, e, 8)
	}
	// Which samples are sync samples. No table at all means every sample
	// is one, which is what an intra-only file says by leaving it out.
	var sync []uint32
	if s, e, ok := child(f, stblS, stblE, "stss"); ok {
		b, n, ok := mp4Table(f, s, e, 4)
		if !ok {
			return nil, false
		}
		sync = make([]uint32, n)
		for i := range n {
			sync[i] = binary.BigEndian.Uint32(b[i*4:])
		}
	}

	// The edit list, read the way ffmpeg applies it: leading empty edits
	// delay the presentation, and the first real one says which media time
	// becomes zero.
	shift := 0.0
	if edtsS, edtsE, ok := child(f, trakS, trakE, "edts"); ok {
		if s, e, ok := child(f, edtsS, edtsE, "elst"); ok && s+8 <= e {
			var hdr [8]byte
			if _, err := f.ReadAt(hdr[:], s); err == nil {
				width := int64(12)
				if hdr[0] == 1 {
					width = 20
				}
				if b, n, ok := mp4Table(f, s, e, width); ok {
					for i := range n {
						var dur uint64
						var media int64
						row := b[int64(i)*width:]
						if width == 20 {
							dur = binary.BigEndian.Uint64(row)
							media = int64(binary.BigEndian.Uint64(row[8:]))
						} else {
							dur = uint64(binary.BigEndian.Uint32(row))
							media = int64(int32(binary.BigEndian.Uint32(row[4:])))
						}
						if media < 0 {
							if movieScale > 0 {
								shift += float64(dur) / movieScale
							}
							continue
						}
						shift -= float64(media) / scale
						break
					}
				}
			}
		}
	}

	// Walk the samples in order, keeping the decode time from the run
	// lengths of stts and the composition offset from those of ctts, and
	// keep the presentation time of each sync sample.
	var times []float64
	var dts int64
	sample := uint32(1)
	si := 0
	ci, cleft := 0, 0
	var coff int64
	nextCtts := func() {
		for cleft == 0 && ci < nctts {
			row := ctts[ci*8:]
			cleft = int(binary.BigEndian.Uint32(row))
			coff = int64(int32(binary.BigEndian.Uint32(row[4:])))
			ci++
		}
		if cleft > 0 {
			cleft--
		} else {
			coff = 0
		}
	}
	for i := 0; i < nstts; i++ {
		row := stts[i*8:]
		count := binary.BigEndian.Uint32(row)
		delta := int64(binary.BigEndian.Uint32(row[4:]))
		for range count {
			nextCtts()
			want := sync == nil || (si < len(sync) && sync[si] == sample)
			if want {
				if t := float64(dts+coff)/scale + shift; t >= 0 {
					times = append(times, t)
				}
				if sync != nil {
					si++
				}
			}
			dts += delta
			sample++
			if sync != nil && si >= len(sync) {
				return times, true
			}
		}
	}
	return times, true
}
