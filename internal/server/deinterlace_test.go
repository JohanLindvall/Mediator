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
