package server

import "strings"

// Interlaced video, which is most of what a DVD holds.
//
// A PAL disc carries 576i: every frame is two fields taken a fiftieth of a
// second apart and combed together into one picture. A television separated
// them again; a browser does not, so anything moving arrives wearing the
// horizontal teeth the format is famous for — measured on a real disc, every
// frame flagged interlaced, top field first.
//
// Nothing can be done about that in a copy: the fields are in the bitstream.
// But wherever a picture is being re-encoded or a frame extracted, it costs
// one filter, and this is that filter.
const (
	// deinterlacer is bwdif — Bob Weaver, motion-adaptive, the best of the
	// ones ffmpeg ships without hardware behind it.
	//
	// `deint=interlaced` is what makes this safe to apply everywhere: it
	// processes only the frames the container *flags* as interlaced and
	// passes the rest through untouched, so a progressive file pays a frame
	// copy and nothing else. MPEG-2 from a disc flags every frame, which is
	// exactly the case this is for.
	//
	// The mode is left at `send_frame`: one picture out per picture in, which
	// keeps a PAL disc at 25 fps. `send_field` would emit a frame per field
	// and move more smoothly on footage that really was shot at fifty — it
	// costs only about a tenth more processor (measured: 13.24 s against
	// 11.86 s for twenty seconds of a disc) — but it doubles the frames in
	// the stream, and the viewer on the other end of this is often a phone on
	// a mobile connection. Combing is what is being complained about, and
	// both modes end it.
	deinterlacer = "bwdif=deint=interlaced"
)

// videoFilter puts the deinterlacer in front of whatever else the picture
// needs.
//
// The order is the whole of it: **deinterlacing has to happen before any
// scale**. Scaling an interlaced frame blends the two fields into rows that
// belong to neither, and no deinterlacer afterwards can take them apart
// again — the combing becomes a permanent smear instead.
func videoFilter(rest ...string) string {
	return strings.Join(append([]string{deinterlacer}, rest...), ",")
}

const (
	// convertMaxWidth is as wide as a conversion is ever made. A viewer's
	// screen is not four thousand pixels across, and the bytes have to cross
	// whatever connection they are watching over.
	convertMaxWidth = 1920
	// convertScale says the same thing to the software scaler.
	convertScale = "scale=w='min(1920,iw)':h=-2"
)
