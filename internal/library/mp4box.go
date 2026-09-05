package library

import (
	"encoding/binary"
	"io"
)

// How an ISO base media file labels its video samples decides more than it
// looks like it should. HEVC can be written as either "hev1" or "hvc1" — the
// difference is only whether the parameter sets travel in the stream or in
// the sample description — and Apple's decoders, which is to say Safari and
// every iPhone, play hvc1 and refuse hev1. ffmpeg writes hev1 unless it is
// told otherwise, so a great many files an iPhone can decode in hardware are
// labelled in a way it will not accept, and arrive here looking like video
// that needs re-encoding when all they need is a different four bytes.
//
// Reading that label is a handful of small reads into the box tree, which is
// why it is done here in Go rather than by starting ffprobe: the answer turns
// a full re-encode into a copy, and it should not cost a process to get.

// VideoSampleFormat returns the four-character code labelling the first video
// track's samples ("avc1", "hev1", "hvc1", …), or "" when the item is not an
// ISO base media file, cannot be read, or carries no video track.
func VideoSampleFormat(it Item) string {
	format, _, _ := VideoSampleInfo(it)
	return format
}

// VideoSampleInfo returns that label together with the picture's size, which
// is written in the same box a few bytes further on.
//
// The size is worth having for nothing: a listing wants to say how big a film
// is, and the alternative is ffprobe — one process per file across a library
// of ninety thousand, which is exactly what this file exists to avoid. It
// answers for the ISO base media containers only (mp4, m4v, mov). Everything
// else already pays for ffprobe when its duration cannot be read natively,
// and gets its size from the same answer.
func VideoSampleInfo(it Item) (format string, width, height int) {
	f, err := OpenItem(it)
	if err != nil {
		return "", 0, 0
	}
	defer f.Close()
	format, width, height, _, _ = sampleInfo(f, it.Size)
	return format, width, height
}

// SampleInfo is the same walk, answering for the soundtrack and the frame
// rate as well — a film is described by all of them, and the box tree gives
// the rest for the price of looking at one more track and one more table.
func SampleInfo(it Item) (video string, width, height int, audio string, fps float64) {
	f, err := OpenItem(it)
	if err != nil {
		return "", 0, 0, "", 0
	}
	defer f.Close()
	return sampleInfo(f, it.Size)
}

// eachBox calls fn for every box between pos and end with the bounds of its
// payload, stopping early when fn returns false. A malformed length ends the
// walk rather than being guessed at: this is used to decide whether a copy
// will help, and a wrong answer is worse than no answer.
func eachBox(f io.ReaderAt, pos, end int64, fn func(typ string, start, stop int64) bool) {
	var hdr [16]byte
	for pos+8 <= end {
		if _, err := f.ReadAt(hdr[:8], pos); err != nil {
			return
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		payload := pos + 8
		minSize := int64(8)
		switch boxSize {
		case 0: // to the end of the enclosing space
			boxSize = end - pos
		case 1: // 64-bit size follows the header
			if _, err := f.ReadAt(hdr[8:16], pos+8); err != nil {
				return
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
			payload = pos + 16
			minSize = 16
		}
		if boxSize < minSize || pos+boxSize > end {
			return
		}
		if !fn(string(hdr[4:8]), payload, pos+boxSize) {
			return
		}
		pos += boxSize
	}
}

// child returns the bounds of the first box of this type directly inside the
// given range.
func child(f io.ReaderAt, pos, end int64, typ string) (start, stop int64, ok bool) {
	eachBox(f, pos, end, func(t string, s, e int64) bool {
		if t != typ {
			return true
		}
		start, stop, ok = s, e, true
		return false
	})
	return start, stop, ok
}

func sampleInfo(f io.ReaderAt, size int64) (video string, width, height int, audio string, fps float64) {
	moovS, moovE, ok := child(f, 0, size, "moov")
	if !ok {
		return "", 0, 0, "", 0
	}
	eachBox(f, moovS, moovE, func(t string, s, e int64) bool {
		if t != "trak" {
			return true
		}
		switch kind, format, w, h, rate := trakInfo(f, s, e); kind {
		case "vide":
			if video == "" {
				video, width, height, fps = format, w, h, rate
			}
		case "soun":
			if audio == "" {
				audio = format
			}
		}
		// All of it is wanted, so the walk runs to the end of the tracks
		// rather than stopping at the first that answers.
		return true
	})
	return video, width, height, audio, fps
}

// trakFPS is the track's frame rate, from the table that says how long each
// sample lasts. Samples over the time they take, both summed: a file whose
// frames are not all the same length — which is what a variable rate is —
// then comes out at its average, which is the only single number there is.
func trakFPS(f io.ReaderAt, mdiaS, mdiaE, stblS, stblE int64) float64 {
	mdhdS, mdhdE, ok := child(f, mdiaS, mdiaE, "mdhd")
	if !ok || mdhdS+20 > mdhdE {
		return 0
	}
	var head [20]byte
	if _, err := f.ReadAt(head[:], mdhdS); err != nil {
		return 0
	}
	// mdhd: version+flags (4), then two times and the timescale — four bytes
	// each at version 0, eight at version 1.
	scaleAt := int64(12)
	if head[0] == 1 {
		scaleAt = 20
	}
	var sc [4]byte
	if _, err := f.ReadAt(sc[:], mdhdS+scaleAt); err != nil {
		return 0
	}
	timescale := float64(binary.BigEndian.Uint32(sc[:]))
	if timescale <= 0 {
		return 0
	}

	sttsS, sttsE, ok := child(f, stblS, stblE, "stts")
	if !ok || sttsS+8 > sttsE {
		return 0
	}
	var hdr [8]byte
	if _, err := f.ReadAt(hdr[:], sttsS); err != nil {
		return 0
	}
	entries := int64(binary.BigEndian.Uint32(hdr[4:8]))
	// A table with an entry per frame is a file whose frames all differ in
	// length; reading all of it would be megabytes. The first few hundred
	// describe the rate as well as the whole thing does — and they are read
	// in one go: an entry at a time was a read per entry, which over a
	// stitched archive member is a ranged request per entry.
	entries = min(entries, sttsMaxEntries, (sttsE-sttsS-8)/8)
	if entries <= 0 {
		return 0
	}
	table := make([]byte, entries*8)
	got, err := f.ReadAt(table, sttsS+8)
	if err != nil && got < 8 {
		return 0
	}
	var samples, ticks float64
	for i := 0; i+8 <= got; i += 8 {
		n := float64(binary.BigEndian.Uint32(table[i : i+4]))
		d := float64(binary.BigEndian.Uint32(table[i+4 : i+8]))
		samples += n
		ticks += n * d
	}
	if ticks <= 0 {
		return 0
	}
	return timescale * samples / ticks
}

// sttsMaxEntries bounds that read, in table entries: a well-behaved file
// describes its whole timeline in one entry, and one that needs thousands is
// telling us its rate varies, which the first few hundred say as well.
const sttsMaxEntries = 512

// trakInfo answers for one track: what it carries, how its samples are
// labelled, and — for a picture — how big it is. A file's first track is as
// often the sound as the picture, so which it is comes back with the rest.
func trakInfo(f io.ReaderAt, pos, end int64) (kind, format string, width, height int, fps float64) {
	mdiaS, mdiaE, ok := child(f, pos, end, "mdia")
	if !ok {
		return "", "", 0, 0, 0
	}
	hdlrS, _, ok := child(f, mdiaS, mdiaE, "hdlr")
	if !ok {
		return "", "", 0, 0, 0
	}
	// hdlr: version+flags (4), pre_defined (4), then the handler type.
	var h [12]byte
	if _, err := f.ReadAt(h[:], hdlrS); err != nil {
		return "", "", 0, 0, 0
	}
	kind = string(h[8:12])
	if kind != "vide" && kind != "soun" {
		return "", "", 0, 0, 0
	}
	minfS, minfE, ok := child(f, mdiaS, mdiaE, "minf")
	if !ok {
		return "", "", 0, 0, 0
	}
	stblS, stblE, ok := child(f, minfS, minfE, "stbl")
	if !ok {
		return "", "", 0, 0, 0
	}
	stsdS, stsdE, ok := child(f, stblS, stblE, "stsd")
	if !ok {
		return "", "", 0, 0, 0
	}
	// stsd: version+flags (4), entry_count (4), then entries, each of which
	// begins with its own size and four-character format.
	if stsdS+16 > stsdE {
		return "", "", 0, 0, 0
	}
	var b [8]byte
	if _, err := f.ReadAt(b[:], stsdS+8); err != nil {
		return "", "", 0, 0, 0
	}
	format = string(b[4:8])
	if kind != "vide" {
		return kind, format, 0, 0, 0
	}
	// VisualSampleEntry, measured from the start of the entry box: the eight
	// bytes of box header, then six reserved and the data reference index,
	// then two pre-defined, two reserved and three more pre-defined — and
	// then the picture's size, sixteen bits each.
	var d [4]byte
	if stsdS+8+36 > stsdE {
		return kind, format, 0, 0, 0
	}
	if _, err := f.ReadAt(d[:], stsdS+8+32); err != nil {
		return kind, format, 0, 0, 0
	}
	return kind, format, int(binary.BigEndian.Uint16(d[0:2])), int(binary.BigEndian.Uint16(d[2:4])), trakFPS(f, mdiaS, mdiaE, stblS, stblE)
}
