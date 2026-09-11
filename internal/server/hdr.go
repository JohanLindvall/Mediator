package server

// Bringing a high-dynamic-range picture back to ordinary colour.
//
// A 4K release is routinely graded in wide colour with a perceptual transfer
// curve — HDR10, or Dolby Vision over the top of it — and a browser that
// cannot decode it asks this server to convert it. The conversion re-encodes
// the picture as H.264 and, left alone, copies the colour description
// straight through: the stream that comes out says it is BT.2020 with an ST
// 2084 curve, which is a thing H.264 in an HLS presentation is not allowed
// to be. Measured on a phone: every segmented session for such a film
// errored on the first segment, the player fell back to the pipe, which that
// browser cannot play at all, and the film was reported unplayable — while
// the same conversion of an ordinary-colour film played for hours.
//
// So a converted picture is tone-mapped: the graphics engine does it in one
// filter, and the processor does it in the five that libavfilter needs for
// the same journey. Both end at BT.709, which is what an H.264 stream is
// expected to say.

import (
	"os/exec"
	"strings"
	"sync"
)

// tonemapVAAPI is the hardware's own tone-mapper. Measured on an Intel
// engine: 20 seconds of 4K HDR in 6.3 s, which is three times real time, and
// the output reads bt709 throughout.
const tonemapVAAPI = "tonemap_vaapi=format=nv12:matrix=bt709:transfer=bt709:primaries=bt709"

// tonemapSoftware is the same journey through libavfilter: into linear
// light, into BT.709 primaries, through a tone curve, back out to an
// ordinary transfer and range. Hable is the curve chosen for keeping
// highlights rather than clipping them; `desat=0` leaves the colour alone,
// since the desaturation ffmpeg applies by default is visible on skin.
//
// It costs what it costs — measured on 4K, about a third of real time, where
// the hardware manages three times — which is one more reason the pixel-rate
// rule sends anything this large to the hardware in the first place.
const tonemapSoftware = "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709," +
	"tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"

var (
	filtersOnce sync.Once
	filterSet   map[string]bool
)

// haveFilter reports whether this ffmpeg was built with a filter, asked once
// and remembered. The software tone-map needs zscale, which is libzimg and
// not every build has it; where it is missing the picture is described
// honestly as BT.709 instead of being converted to it — wrong colour, but a
// stream that plays, which is the better of the two failures.
func haveFilter(ffmpeg, name string) bool {
	filtersOnce.Do(func() {
		filterSet = map[string]bool{}
		if ffmpeg == "" {
			return
		}
		out, err := exec.Command(ffmpeg, "-hide_banner", "-filters").Output()
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			// " TS. colorspace        V->V       Convert between ..."
			if f := strings.Fields(line); len(f) >= 2 {
				filterSet[f[1]] = true
			}
		}
	})
	return filterSet[name]
}

// softwareColour is what the software conversion does about a wide-colour
// picture: tone-map it where the build can, and otherwise nothing but the
// label the encoder writes (see convertColourArgs).
func softwareColour(ffmpeg string, hdr bool) string {
	if !hdr || !haveFilter(ffmpeg, "zscale") {
		return ""
	}
	return tonemapSoftware
}

// convertColourArgs describes the picture that comes out as ordinary
// colour. Harmless where it was converted — it is then the truth — and the
// only thing that can be done where it could not be.
func convertColourArgs(hdr bool) []string {
	if !hdr {
		return nil
	}
	return []string{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709"}
}
