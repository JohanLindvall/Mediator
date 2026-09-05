package server

import (
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Which pictures go to the hardware. The list is short on purpose: a codec
// the video engine cannot decode fails silently and completely — a DivX file
// produced no frames at all — and those old codecs are exactly the ones most
// likely to need converting, so being wrong about one is worse than being
// slow about it.
func TestHardwareOnlyForWhatItDecodes(t *testing.T) {
	for _, c := range []struct {
		codec string
		want  bool
	}{
		{"h264", true},
		{"hevc", true},
		{"mpeg2video", true}, // a DVD, which also wants the deinterlacer
		{"vp9", true},
		{"vp8", true},
		// The ones measured or known not to: MPEG-4 part 2 decoded nothing.
		{"mpeg4", false},
		{"msmpeg4v3", false},
		{"wmv3", false},
		{"vc1", false},
		{"av1", false},
		// Nobody has probed it, so nobody knows. Software.
		{"", false},
	} {
		if got := videoEngines[0].decodes[c.codec]; got != c.want {
			t.Errorf("%q: hardware %v, want %v", c.codec, got, c.want)
		}
	}
}

// The frames have to stay on the hardware. Without this the decoder's output
// is copied back to system memory for the filters, and the copying costs more
// than the hardware saved: the same clip measured 5.5 times real time with
// the frames left alone and 0.37 with them brought back.
func TestHardwareKeepsFramesWhereTheyAre(t *testing.T) {
	args := strings.Join(videoEngines[0].input("/dev/dri/renderD128"), " ")
	if !strings.Contains(args, "-hwaccel_output_format vaapi") {
		t.Errorf("input args %q do not keep the frames on the device", args)
	}
	if !strings.Contains(args, "/dev/dri/renderD128") {
		t.Errorf("input args %q name no device", args)
	}
	// Render nodes only: the card node drives the display and wants
	// privileges a media server has no business holding.
	for _, d := range renderNodes() {
		if strings.Contains(d, "card") {
			t.Errorf("%q is not a render node", d)
		}
	}
}

// Deinterlacing has to happen on the hardware too — bwdif works on frames in
// system memory, which these are not — and only where the file says it is
// needed, which is what auto=1 means.
func TestHardwareDeinterlacesAndScales(t *testing.T) {
	args := videoEngines[0].encode("/dev/dri/renderD128", 1920)
	var vf string
	for i, a := range args {
		if a == "-vf" && i+1 < len(args) {
			vf = args[i+1]
		}
	}
	if vf == "" {
		t.Fatal("no filter chain")
	}
	if !strings.HasPrefix(vf, "deinterlace_vaapi=auto=1,") {
		t.Errorf("chain %q does not deinterlace first", vf)
	}
	if !strings.Contains(vf, "scale_vaapi=w='min(1920,iw)'") {
		t.Errorf("chain %q does not cap the width", vf)
	}
	// Never wider than it was: min(), not a bare width, or a phone clip
	// would be blown up to 1920 and cost more to send than it is worth.
	if strings.Contains(vf, "w=1920:") {
		t.Errorf("chain %q would enlarge a small picture", vf)
	}
	if !strings.Contains(strings.Join(args, " "), "h264_vaapi") {
		t.Error("the encoder is not the hardware one")
	}
}

// Every backend has to be coherent even though only one of them can be
// measured here. What cannot be checked without the hardware is checked
// against itself: each is proved by a real conversion before it is used, so
// a wrong definition costs a machine its hardware and nothing more — but a
// definition that is wrong in these ways would be wrong everywhere.
func TestEveryEngineIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range videoEngines {
		t.Run(e.name, func(t *testing.T) {
			if seen[e.name] {
				t.Fatalf("two engines called %q", e.name)
			}
			seen[e.name] = true
			if e.devices == nil || e.input == nil || e.encode == nil {
				t.Fatal("an engine is missing one of its three parts")
			}
			if len(e.decodes) == 0 {
				t.Error("an engine that decodes nothing would never be used")
			}
			// The old codecs stay off every list: hardware that cannot
			// decode one produces no frames at all, silently.
			for _, bad := range []string{"mpeg4", "msmpeg4v3", "wmv3", "vc1", ""} {
				if e.decodes[bad] {
					t.Errorf("%q is not safe to send to hardware", bad)
				}
			}
			args := strings.Join(e.encode("/dev/x", 1920), " ")
			if !strings.Contains(args, "-c:v") {
				t.Error("no encoder")
			}
			if !strings.Contains(args, "min(1920,iw)") {
				t.Errorf("chain %q does not cap the width without enlarging", args)
			}
			if !strings.Contains(args, "-vf") {
				t.Error("no filter chain")
			}
		})
	}
}

// The order is the order they are tried in, and it is deliberate: the one
// measured on real hardware here comes first.
func TestVAAPIIsTriedFirst(t *testing.T) {
	if videoEngines[0].name != "vaapi" {
		t.Errorf("first engine is %q; the measured one should lead", videoEngines[0].name)
	}
}

// Hardware is not simply better, and this is where that is decided. A DVD
// converts at three times real time in software and spends 0.8 Mbit/s; the
// graphics hardware does ten times and spends 4.3, which is five times the
// bytes for speed nobody needed. A 4K60 phone clip converts at 0.41 in
// software, which is a film that stalls for ever. So the line is drawn by
// how much picture there is to get through.
func TestHardwareOnlyWhereSoftwareCannotCope(t *testing.T) {
	for _, c := range []struct {
		name string
		it   library.Item
		want bool
	}{
		// Measured at 0.41 of real time in software: unplayable.
		{"4K60 from a phone", library.Item{Width: 3840, Height: 2160, FPS: 59.94}, true},
		{"4K30", library.Item{Width: 3840, Height: 2160, FPS: 30}, true},
		// Measured at three times real time, and five times smaller.
		{"a PAL DVD", library.Item{Width: 720, Height: 576, FPS: 25}, false},
		{"1080p24 film", library.Item{Width: 1920, Height: 1080, FPS: 23.976}, false},
		{"1080p30", library.Item{Width: 1920, Height: 1080, FPS: 30}, false},
		// Just over the line, deliberately: 1080p60 is where software starts
		// to be thin on a machine doing anything else.
		{"1080p60", library.Item{Width: 1920, Height: 1080, FPS: 60}, true},
		// Nobody has probed it, so nobody knows what it is.
		{"unprobed", library.Item{}, false},
		{"no frame rate", library.Item{Width: 3840, Height: 2160}, false},
		{"no size", library.Item{FPS: 60}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := hwWorthIt(c.it); got != c.want {
				t.Errorf("hardware %v, want %v", got, c.want)
			}
		})
	}
}
