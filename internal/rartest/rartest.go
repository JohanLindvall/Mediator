// Package rartest writes minimal but spec-correct store-mode rar volume
// sets for tests. It lives outside the packages that use it because both
// the library (which indexes and reads such sets) and the server (which
// pipes their content to ffmpeg) need to build one, and a second, subtly
// different copy of these headers would prove nothing.
//
// TestRarFixturesAgainstUnrar in internal/library keeps the volumes here
// honest by extracting them with the reference unrar.
package rartest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// vint encodes a RAR5 variable-length integer.
func vint(v uint64) []byte {
	var b []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
		} else {
			return append(b, c)
		}
	}
}

// Options says how a member is described: stored, as everything here really
// is, or claiming to be compressed — and solid, compressed against the
// members before it. Nothing here can compress the way rar does, so a member
// claiming to be compressed carries its bytes as they are; the parser reads
// only headers, and a test that reads the content stands in for the decoder.
type Options struct {
	Compressed bool
	Solid      bool
}

// Volume4 builds one RAR4 volume holding a slice of `name`'s data.
// fileCRC is the part's crc32 for intermediate volumes and the whole file's
// crc32 in the last one (that is what unrar verifies at file completion).
func Volume4(name string, unpSize int64, part []byte, fileCRC uint32, splitBefore, splitAfter bool) []byte {
	return volume4(name, unpSize, part, fileCRC, splitBefore, splitAfter, Options{})
}

func volume4(name string, unpSize int64, part []byte, fileCRC uint32, splitBefore, splitAfter bool, opt Options) []byte {
	var out bytes.Buffer
	out.Write([]byte("Rar!\x1a\x07\x00"))

	block := func(typ byte, flags uint16, body []byte) {
		h := make([]byte, 7+len(body))
		h[2] = typ
		binary.LittleEndian.PutUint16(h[3:5], flags)
		binary.LittleEndian.PutUint16(h[5:7], uint16(7+len(body)))
		copy(h[7:], body)
		binary.LittleEndian.PutUint16(h[0:2], uint16(crc32.ChecksumIEEE(h[2:])))
		out.Write(h)
	}

	block(0x73, 0x0011, make([]byte, 6)) // MAIN: volume + new-style .partN numbering

	fileFlags := uint16(0x8000)
	if splitBefore {
		fileFlags |= 0x01
	}
	if splitAfter {
		fileFlags |= 0x02
	}
	if opt.Solid {
		fileFlags |= 0x10
	}
	body := make([]byte, 25+len(name))
	binary.LittleEndian.PutUint32(body[0:4], uint32(len(part))) // pack size
	binary.LittleEndian.PutUint32(body[4:8], uint32(unpSize))   // unpacked size
	body[8] = 3                                                 // unix
	binary.LittleEndian.PutUint32(body[9:13], fileCRC)
	body[17] = 20   // unpack version
	body[18] = 0x30 // method: store
	if opt.Compressed {
		body[18] = 0x33 // method: normal compression
	}
	binary.LittleEndian.PutUint16(body[19:21], uint16(len(name)))
	binary.LittleEndian.PutUint32(body[21:25], 0x20)
	copy(body[25:], name)
	block(0x74, fileFlags, body)
	out.Write(part)

	endFlags := uint16(0)
	if splitAfter {
		endFlags = 0x0001 // next volume exists
	}
	block(0x7B, endFlags, nil)
	return out.Bytes()
}

// Volume5 builds one RAR5 volume holding a slice of `name`'s data.
func Volume5(name string, unpSize int64, part []byte, volNo int, splitBefore, splitAfter bool) []byte {
	return volume5(name, unpSize, part, volNo, splitBefore, splitAfter, Options{})
}

func volume5(name string, unpSize int64, part []byte, volNo int, splitBefore, splitAfter bool, opt Options) []byte {
	var out bytes.Buffer
	out.Write([]byte("Rar!\x1a\x07\x01\x00"))

	block := func(hdr []byte, data []byte) {
		// The header CRC32 covers the size vint plus the header itself.
		sz := vint(uint64(len(hdr)))
		sum := crc32.ChecksumIEEE(append(append([]byte{}, sz...), hdr...))
		var crc [4]byte
		binary.LittleEndian.PutUint32(crc[:], sum)
		out.Write(crc[:])
		out.Write(sz)
		out.Write(hdr)
		out.Write(data)
	}

	// Main archive header (type 1).
	var mh bytes.Buffer
	mh.Write(vint(1)) // type
	mh.Write(vint(0)) // header flags
	if volNo > 1 {
		mh.Write(vint(0x03)) // archive flags: volume + number present
		mh.Write(vint(uint64(volNo - 1)))
	} else {
		mh.Write(vint(0x01)) // volume
	}
	block(mh.Bytes(), nil)

	// File header (type 2).
	hf := uint64(0x02) // data area present
	if splitBefore {
		hf |= 0x08
	}
	if splitAfter {
		hf |= 0x10
	}
	var fh bytes.Buffer
	fh.Write(vint(2))                 // type
	fh.Write(vint(hf))                // header flags
	fh.Write(vint(uint64(len(part)))) // data size
	fh.Write(vint(0))                 // file flags
	fh.Write(vint(uint64(unpSize)))   // unpacked size
	fh.Write(vint(0))                 // attributes
	comp := uint64(0)                 // compression: store
	if opt.Compressed {
		comp = 3 << 7 // method 3
	}
	if opt.Solid {
		comp |= 0x40
	}
	fh.Write(vint(comp))
	fh.Write(vint(1)) // host os: unix
	fh.Write(vint(uint64(len(name))))
	fh.WriteString(name)
	block(fh.Bytes(), part)

	// End of archive (type 5).
	var eh bytes.Buffer
	eh.Write(vint(5))
	eh.Write(vint(0))
	end := uint64(0)
	if splitAfter {
		end = 1
	}
	eh.Write(vint(end))
	block(eh.Bytes(), nil)
	return out.Bytes()
}

// Payload returns n bytes of reproducible filler.
func Payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

// WriteSet writes payload into dir as a `stem`.partN.rar set of `parts`
// volumes holding one stored member, and returns the volume paths in order.
func WriteSet(t testing.TB, dir, stem, member string, payload []byte, parts int, v5 bool) []string {
	t.Helper()
	return WriteSetWith(t, dir, stem, member, payload, parts, v5, Options{})
}

// WriteSetWith is WriteSet with the member described as opt says.
func WriteSetWith(t testing.TB, dir, stem, member string, payload []byte, parts int, v5 bool, opt Options) []string {
	t.Helper()
	per := (len(payload) + parts - 1) / parts
	var vols []string
	for i := 0; i < parts; i++ {
		lo, hi := i*per, min((i+1)*per, len(payload))
		last := i == parts-1
		var data []byte
		if v5 {
			data = volume5(member, int64(len(payload)), payload[lo:hi], i+1, i > 0, !last, opt)
		} else {
			crc := crc32.ChecksumIEEE(payload[lo:hi])
			if last {
				crc = crc32.ChecksumIEEE(payload) // last volume: whole-file crc
			}
			data = volume4(member, int64(len(payload)), payload[lo:hi], crc, i > 0, !last, opt)
		}
		p := filepath.Join(dir, fmt.Sprintf("%s.part%d.rar", stem, i+1))
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		vols = append(vols, p)
	}
	return vols
}
