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

// square undoes an anamorphic frame before it is scaled into a picture.
//
// A DVD codes a 16:9 picture in a 4:3 grid and says so in a pixel aspect
// ratio; a browser honours that and draws the film correctly, and so does
// the player here. A JPEG has nowhere to say it — so a still taken with a
// plain scale comes out squeezed, and the tile, the hover preview and the
// scrub bar are all a picture of the wrong shape. Measured on a file
// declaring 720x576 with a pixel aspect of 16:15: a 400-wide tile came out
// 400x320 where the film is 4:3, and for a 16:9 disc the error is a third.
//
// The frame is widened to what the pixel aspect says it means, then scaled
// as before, and the output is marked square so nothing downstream squeezes
// it again. On a file with square pixels — which is nearly all of them —
// the first scale is the same size in and out.
func square(scale string) string {
	return strings.Join([]string{"scale='trunc(iw*sar/2)*2':ih", scale, "setsar=1"}, ",")
}

const (
	// convertMaxWidth is as wide as a conversion is ever made. A viewer's
	// screen is not four thousand pixels across, and the bytes have to cross
	// whatever connection they are watching over.
	convertMaxWidth = 1920
	// convertScale says the same thing to the software scaler.
	convertScale = "scale=w='min(1920,iw)':h=-2"
	// convertBitrateGuess is what a re-encoded stream is declared to cost,
	// for the one place that has to say so before any of it exists (the HLS
	// master playlist). The hardware path is capped at 6 Mbit/s of picture
	// with a 10 Mbit/s ceiling, and the software path aims at a quality
	// rather than a rate; eight plus the soundtrack is the honest middle. It
	// is a description and not a budget — nothing converts to it.
	convertBitrateGuess = 8_200_000
)
