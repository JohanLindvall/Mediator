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
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
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
	filtersMu  sync.Mutex
	filterSets = map[string]map[string]bool{} // by ffmpeg path
	filterRuns = map[string]chan struct{}{}   // the reading in flight, by path
)

// probeFilters is a variable so the tests can put two askers in the order
// that matters — one inside the reading, one arriving behind it — without a
// real ffmpeg and without sleeping to get there.
var probeFilters = readFilters

// haveFilter reports whether this ffmpeg was built with a filter, asked once
// and remembered. The software tone-map needs zscale, which is libzimg and
// not every build has it; where it is missing the picture is described
// honestly as BT.709 instead of being converted to it — wrong colour, but a
// stream that plays, which is the better of the two failures.
//
// Remembered only once the list has actually been read: a `-filters` run
// that failed — a process limit, a disk that was briefly away — used to be
// cached as an empty list for the life of the process, which turned the
// tone-map off for every film after it and reproduced the very fault this
// file exists to fix. Keyed by path, since the answer is the binary's.
//
// **The reading happens with nothing held**, and that is the shape of the
// rest of it. This lock is the process's only gate on the answer and every
// conversion plan for a wide-colour film comes through it, so holding it
// across a child process made the slowest ffmpeg on the machine the speed
// of all of them at once — and since a failure is deliberately not
// remembered, that queue was paid afresh by every later HDR conversion
// rather than once. A binary on a mount that had stopped answering held it
// for as long as the mount did. So the first asker registers the reading,
// runs it with the lock released, and publishes what came back; anyone
// arriving meanwhile waits for that reading rather than starting a second
// one beside it.
//
// A waiter whose reading came back with nothing is told no rather than
// looking again itself: the question has just been asked and answered
// "cannot tell", and asking it again in the same instant would spend
// another process on the same silence. The next request looks again, which
// is what "asked again next time" has always meant here.
func haveFilter(ffmpeg, name string) bool {
	if ffmpeg == "" {
		return false
	}
	filtersMu.Lock()
	if set, ok := filterSets[ffmpeg]; ok {
		filtersMu.Unlock()
		return set[name]
	}
	if wait, running := filterRuns[ffmpeg]; running {
		filtersMu.Unlock()
		// Bounded by the reader's own budget, below, and released from a
		// defer, so it is closed even where the reading comes apart.
		<-wait
		filtersMu.Lock()
		set, ok := filterSets[ffmpeg]
		filtersMu.Unlock()
		return ok && set[name]
	}
	wait := make(chan struct{})
	filterRuns[ffmpeg] = wait
	filtersMu.Unlock()

	// Published from a defer, because a waiter has no way out of `<-wait`
	// other than the leader closing it: there is no context in hand here,
	// haveFilter being asked in the middle of planning a conversion rather
	// than on behalf of one request. A reading that came apart would
	// otherwise leave the channel open and every later asker for this
	// binary waiting on it for the life of the process — and a panic here
	// is survivable, the server above recovering it and closing only that
	// connection.
	var set map[string]bool
	var ok bool
	defer func() {
		filtersMu.Lock()
		if ok {
			filterSets[ffmpeg] = set
		}
		delete(filterRuns, ffmpeg)
		close(wait)
		filtersMu.Unlock()
	}()
	set, ok = probeFilters(ffmpeg)
	return ok && set[name]
}

// readFilters asks the binary what it was built with. It is bounded like
// every other child process here — hwProbeBudget is the neighbouring one —
// because listing filters reads no media and answers in milliseconds, so a
// run that does not is a binary that has stopped answering and nothing
// should wait on it for ever. A budget that expires is not an answer, and
// like any other failure it is not written down.
func readFilters(ffmpeg string) (map[string]bool, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), hwProbeBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-filters")
	// The budget has to bound the wait and not only the process. Output()
	// waits for the pipe to close as well as for the child to die, and a
	// grandchild holding that pipe open outlives the kill — which here is a
	// reading that everybody else is waiting on. Every other child process
	// in this package says the same thing.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return parseFilters(string(out)), true
}

// parseFilters reads the names out of `ffmpeg -filters`, one per line after
// the flags column (" TS. colorspace        V->V       Convert between ...").
// The header lines have no such column and fall out of the same rule.
func parseFilters(out string) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// A filter line has the flags, the name and then what it takes to
		// what ("V->V", "|->A" for a source); the legend above them has
		// the flags column too, and nothing with an arrow after it.
		if len(f) >= 3 && strings.Contains(f[2], "->") {
			set[f[1]] = true
		}
	}
	return set
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
