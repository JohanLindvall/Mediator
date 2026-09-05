package server

import (
	"encoding/binary"
	"io"
)

// jpegOrientation extracts the EXIF orientation (1..8) from a JPEG stream.
// Returns 1 (normal) when the file is not a JPEG, has no EXIF, or on any
// parse error. Only the APP1 segment is inspected — a tiny, dependency-free
// subset of TIFF parsing sufficient for tag 0x0112.
func jpegOrientation(r io.Reader) int {
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return 1
	}
	for {
		var marker [2]byte
		if _, err := io.ReadFull(r, marker[:]); err != nil || marker[0] != 0xFF {
			return 1
		}
		m := marker[1]
		// Skip padding bytes between segments.
		for m == 0xFF {
			var b [1]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return 1
			}
			m = b[0]
		}
		if m == 0xDA || m == 0xD9 { // start of scan / end of image: no EXIF
			return 1
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(lenBuf[:])) - 2
		if segLen < 0 {
			return 1
		}
		if m != 0xE1 { // not APP1
			if _, err := io.CopyN(io.Discard, r, int64(segLen)); err != nil {
				return 1
			}
			continue
		}
		seg := make([]byte, min(segLen, 256<<10))
		if _, err := io.ReadFull(r, seg); err != nil {
			return 1
		}
		return parseExifOrientation(seg)
	}
}

func parseExifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 1
	}
	tiff := seg[6:]
	var order binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		order = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	ifdOff := int(order.Uint32(tiff[4:8]))
	if ifdOff+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[ifdOff : ifdOff+2]))
	for i := 0; i < count; i++ {
		e := ifdOff + 2 + i*12
		if e+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[e:e+2]) == 0x0112 { // Orientation tag, type SHORT
			v := int(order.Uint16(tiff[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
