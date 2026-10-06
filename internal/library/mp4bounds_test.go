// SPDX-License-Identifier: MIT

package library

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"testing"
)

// A nested box cannot borrow its extended header or payload from a sibling.
func TestEachBoxBounds(t *testing.T) {
	extended := append(u32(1), []byte("free")...)
	extended = binary.BigEndian.AppendUint64(extended, math.MaxInt64)
	data := append(make([]byte, 8), extended...)
	for _, end := range []int64{16, 24} {
		r := &boxBoundReader{ReaderAt: bytes.NewReader(data), end: end}
		eachBox(r, 8, end, func(typ string, start, stop int64) bool {
			t.Errorf("end=%d: accepted malformed box %q [%d,%d)", end, typ, start, stop)
			return false
		})
		if r.crossed {
			t.Errorf("end=%d: read outside the parent box", end)
		}
	}
}

type boxBoundReader struct {
	io.ReaderAt
	end     int64
	crossed bool
}

func (r *boxBoundReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > r.end || int64(len(p)) > r.end-off {
		r.crossed = true
		return 0, io.EOF
	}
	return r.ReaderAt.ReadAt(p, off)
}

func TestTrackFPSRejectsTruncatedAndUnknownHeaders(t *testing.T) {
	for _, version := range []byte{1, 2} {
		head := make([]byte, 20)
		head[0] = version
		binary.BigEndian.PutUint32(head[12:], 1000)
		mdia := append(box("mdhd", head), box("free")...)
		stbl := box("stts", u32(0), u32(1), u32(25), u32(1))
		data := append(bytes.Clone(mdia), stbl...)
		if got := trakFPS(bytes.NewReader(data), 0, int64(len(mdia)), int64(len(mdia)), int64(len(data))); got != 0 {
			t.Errorf("mdhd version %d: read %g fps from an invalid header", version, got)
		}
	}
}

func TestMovieDurationStaysInsideItsHeader(t *testing.T) {
	header := mvhdBytes(0, 1000, 60_000)
	for _, n := range []int{0, 12, 16, 19} {
		// The file carries enough bytes for the read, but the mvhd box does not.
		data := box("moov", box("mvhd", header[:n]), header[n:])
		if got := mp4Duration(bytes.NewReader(data), int64(len(data))); got != 0 {
			t.Errorf("header of %d bytes borrowed %d ms from its sibling", n, got)
		}
	}
	header[0] = 2
	if got := mp4Mvhd(bytes.NewReader(header), 0); got != 0 {
		t.Errorf("unsupported movie header version read as %d ms", got)
	}
}

func TestSampleEntryOwnsItsDimensions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     uint32
		entrySize uint32
		format    string
	}{
		{"empty table", 0, 8, ""},
		{"invalid length", 1, 7, ""},
		{"oversized entry", 1, 1000, ""},
		{"short entry", 1, 8, "avc1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := append(u32(tc.entrySize), []byte("avc1")...)
			// Bytes belonging to later entries cannot supply this entry's size.
			entry = append(entry, bytes.Repeat([]byte{0x01}, 40)...)
			stsd := box("stsd", u32(0), u32(tc.count), entry)
			hdlr := box("hdlr", make([]byte, 8), []byte("vide"))
			data := box("mdia", hdlr, box("minf", box("stbl", stsd)))
			_, format, w, h, _ := trakInfo(bytes.NewReader(data), 0, int64(len(data)))
			if format != tc.format || w != 0 || h != 0 {
				t.Errorf("got %q %dx%d, want %q without dimensions", format, w, h, tc.format)
			}
		})
	}
}

func FuzzMediaBoxBounds(f *testing.F) {
	f.Add(box("moov", track("vide", "avc1")))
	f.Add(append(append(u32(1), []byte("free")...), bytes.Repeat([]byte{0x7f}, 8)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		end := int64(len(data))
		r := &boxBoundReader{ReaderAt: bytes.NewReader(data), end: end}
		eachBox(r, 0, end, func(_ string, start, stop int64) bool {
			if start < 8 || start > stop || stop > end {
				t.Fatalf("invalid box bounds [%d,%d), file ends at %d", start, stop, end)
			}
			return true
		})
		if r.crossed {
			t.Fatal("box parser read outside its input")
		}
		sampleInfo(bytes.NewReader(data), end)
	})
}
