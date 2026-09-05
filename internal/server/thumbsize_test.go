package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"strings"
	"testing"
)

// pngHeader writes only a PNG signature and IHDR chunk claiming the given
// dimensions — the header is all DecodeConfig reads, and all the size gate
// needs to refuse from.
func pngHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // truecolour
	var chunk bytes.Buffer
	binary.Write(&chunk, binary.BigEndian, uint32(len(ihdr)))
	chunk.WriteString("IHDR")
	chunk.Write(ihdr)
	crc := crc32.NewIEEE()
	crc.Write([]byte("IHDR"))
	crc.Write(ihdr)
	binary.Write(&chunk, binary.BigEndian, crc.Sum32())
	buf.Write(chunk.Bytes())
	return buf.Bytes()
}

// An image whose header already says it is too large to decode is refused
// from that header, before the whole stream — up to half a gigabyte — has
// been buffered into memory to find the same answer.
func TestEncodeResizedRefusesOversizedFromHeader(t *testing.T) {
	th := NewThumbnailer(nil, nil, testLogger())
	// 200000 x 200000 claims 40 gigapixels; nothing after the header exists,
	// so only the header gate can be what refuses it.
	data := pngHeader(200000, 200000)
	_, err := th.encodeResized(context.Background(), bytes.NewReader(data), 360)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want the size refusal, got %v", err)
	}
}

// And an ordinary image still comes through the same path.
func TestEncodeResizedStillEncodes(t *testing.T) {
	th := NewThumbnailer(nil, nil, testLogger())
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	out, err := th.encodeResized(context.Background(), bytes.NewReader(buf.Bytes()), 360)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("output does not decode: %v", err)
	}
}
