package server

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// buildJPEGWithOrientation assembles a minimal JPEG header: SOI + APP1(EXIF
// with the given orientation) + SOS.
func buildJPEGWithOrientation(orientation byte, bigEndian bool) []byte {
	var tiff []byte
	if bigEndian {
		tiff = []byte{
			'M', 'M', 0, 42, 0, 0, 0, 8, // header, IFD0 at 8
			0, 1, // 1 entry
			0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orientation, 0, 0, // tag 0x0112 SHORT
			0, 0, 0, 0, // next IFD
		}
	} else {
		tiff = []byte{
			'I', 'I', 42, 0, 8, 0, 0, 0,
			1, 0,
			0x12, 0x01, 3, 0, 1, 0, 0, 0, orientation, 0, 0, 0,
			0, 0, 0, 0,
		}
	}
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(app1) + 2
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}
	out = append(out, app1...)
	out = append(out, 0xFF, 0xDA) // SOS
	return out
}

func TestJPEGOrientation(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want int
	}{
		{"little-endian 6", buildJPEGWithOrientation(6, false), 6},
		{"big-endian 8", buildJPEGWithOrientation(8, true), 8},
		{"no exif", []byte{0xFF, 0xD8, 0xFF, 0xDA}, 1},
		{"not a jpeg", []byte("PNG whatever"), 1},
		{"empty", nil, 1},
		{"invalid value", buildJPEGWithOrientation(200, false), 1},
	} {
		if got := jpegOrientation(bytes.NewReader(tc.data)); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestOrientTransforms(t *testing.T) {
	// 2x1 image: red at (0,0), blue at (1,0).
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	src.SetRGBA(0, 0, red)
	src.SetRGBA(1, 0, blue)

	// Orientation 6 (rotate 90 CW): red should end up at the top, blue below.
	out := orient(src, 6)
	if b := out.Bounds(); b.Dx() != 1 || b.Dy() != 2 {
		t.Fatalf("rotated bounds wrong: %v", b)
	}
	if out.RGBAAt(0, 0) != red || out.RGBAAt(0, 1) != blue {
		t.Fatalf("rotate 90 CW wrong: top=%v bottom=%v", out.RGBAAt(0, 0), out.RGBAAt(0, 1))
	}

	// Orientation 3 (rotate 180): order flips.
	out = orient(src, 3)
	if out.RGBAAt(0, 0) != blue || out.RGBAAt(1, 0) != red {
		t.Fatalf("rotate 180 wrong: %v %v", out.RGBAAt(0, 0), out.RGBAAt(1, 0))
	}

	// Orientation 1: unchanged, same instance.
	if orient(src, 1) != src {
		t.Fatal("orientation 1 should be a no-op")
	}
}
