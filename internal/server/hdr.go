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

// tonemapSoftware is the journey through libavfilter: into linear light,
// through a tone curve, and back out to an ordinary transfer, matrix and
// range. Hable is the curve, for rolling the highlights off rather than
// clipping them; `desat=0` leaves the colour alone, the desaturation ffmpeg
// applies by default being visible on skin.
//
// **The hardware's own tone-mapper is not used, and that is measured rather
// than assumed.** `tonemap_vaapi` runs on this driver, reports success and
// writes the right colour tags on a stream with **no picture in it**: 90
// bytes a frame for 1080p, against 27,000 for the same frames scaled and
// left alone. What the viewer got was a black screen with sound — which is
// what a tag-only check misses, so the test that matters here is bytes per
// frame.
const tonemapSoftware = "zscale=t=linear:npl=100," +
	"tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:p=bt709:r=tv"

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

// toneCurve is the tone-map to splice into a conversion, or "" where there
// is nothing to do or nothing to do it with. It is the same chain wherever
// the frames are: the graphics engine cannot do this itself (see above), so
// even a hardware conversion comes back to the processor for these few
// filters — after the engine has scaled the picture down, which is what
// makes it affordable.
//
// Measured on a 4K HDR film, eight seconds of it, on a machine whose
// processor was already busy with something else:
//
//	tone-mapped at 4K, frames never leaving the engine .. blank (see above)
//	tone-mapped at 4K on the processor ................. 0.15x real time
//	scaled on the engine, tone-mapped at 1080p ......... 0.86x real time
//	scaled on the engine, colour converted, no curve ... 1.09x real time
//
// So the picture is scaled first and the curve is paid at 1080p. It is
// close to real time on a loaded machine and several times it on an idle
// one, which is the cost of converting this kind of film at all: the
// hardware can decode, scale and encode it, and cannot do its colour.
func toneCurve(ffmpeg string, hdr bool) string {
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
