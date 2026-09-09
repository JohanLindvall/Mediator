package server

import (
	"strings"
	"testing"
)

// The order is the whole of it. Scaling an interlaced frame blends the two
// fields into rows belonging to neither, and nothing afterwards can take
// them apart again — so the deinterlacer goes first, everywhere, and this
// says so in a way a rearrangement would break.
func TestVideoFilterDeinterlacesFirst(t *testing.T) {
	got := videoFilter("scale=w='min(1920,iw)':h=-2")
	if !strings.HasPrefix(got, deinterlacer+",") {
		t.Fatalf("filter chain %q does not begin with the deinterlacer", got)
	}
	if !strings.HasSuffix(got, "scale=w='min(1920,iw)':h=-2") {
		t.Errorf("the rest of the chain was lost: %q", got)
	}
	// It is only ever applied to frames the container flags, which is what
	// makes it safe on everything: a progressive file pays a frame copy.
	if !strings.Contains(deinterlacer, "deint=interlaced") {
		t.Error("the deinterlacer must process flagged frames only")
	}
	// One picture out per picture in: send_field would double the frames in
	// a stream a phone is often fetching over a mobile connection.
	if strings.Contains(deinterlacer, "send_field") {
		t.Error("field-rate output doubles the stream; see the note on the constant")
	}
}

func TestVideoFilterWithNothingElse(t *testing.T) {
	if got := videoFilter(); got != deinterlacer {
		t.Errorf("got %q, want the deinterlacer alone", got)
	}
}

// A still is a picture with nowhere to record a pixel aspect ratio, so the
// frame has to be un-squeezed before it is scaled. Measured on a file
// declaring 720x576 with a pixel aspect of 16:15: a 400-wide tile came out
// 400x320 where the film is 4:3, and for a 16:9 disc the error is a third.
func TestStillsAreUnsqueezedBeforeScaling(t *testing.T) {
	got := square("scale=320:-2")
	parts := strings.Split(got, ",")
	if len(parts) != 3 {
		t.Fatalf("filter chain %q, want the widening, the scale and the mark", got)
	}
	// The widening comes first: scaling an anamorphic frame to a width and
	// then widening it would have thrown the height away already.
	if !strings.Contains(parts[0], "iw*sar") {
		t.Errorf("the frame is not widened by its pixel aspect first: %q", parts[0])
	}
	if parts[1] != "scale=320:-2" {
		t.Errorf("the caller's own scale was lost: %q", parts[1])
	}
	// And the picture that comes out says its pixels are square, so nothing
	// downstream squeezes it a second time.
	if parts[2] != "setsar=1" {
		t.Errorf("the output is not marked square: %q", parts[2])
	}
	// It sits under the deinterlacer like everything else: fields first,
	// always, or the combing is scaled into a smear nothing can undo.
	chain := videoFilter(square("scale=320:-2"))
	if !strings.HasPrefix(chain, deinterlacer+",") {
		t.Errorf("chain %q does not begin with the deinterlacer", chain)
	}
}

// A file whose bitstream declares a pixel aspect ffmpeg will not accept
// gets no picture at all: the filter graph is configured from what the
// stream says, and is refused before a frame is scaled. The repair copies a
// couple of seconds through the filter that rewrites that declaration, and
// only two codecs have one — the rest are left alone rather than guessed at.
func TestMetadataFilterByCodec(t *testing.T) {
	if got := metadataFilter("h264"); got != "h264_metadata" {
		t.Errorf("h264 = %q", got)
	}
	if got := metadataFilter("hevc"); got != "hevc_metadata" {
		t.Errorf("hevc = %q", got)
	}
	for _, c := range []string{"", "vp9", "mpeg2video", "av1", "wmv2"} {
		if got := metadataFilter(c); got != "" {
			t.Errorf("%s was offered %q, which does not exist", c, got)
		}
	}
}
