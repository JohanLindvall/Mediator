package library

// What shape a picture is, read from the file's own header.
//
// A listing wants to say how big a photograph is and what a film was encoded
// with, and both are wanted for every item on the screen at once. That rules
// out asking ffprobe, which is a process per file across a library of a
// hundred thousand — the same reasoning that put the video sample label in
// mp4box.go. What is here is the cheap half: a still's size from its header,
// and the name a four-character sample code stands for.

import (
	"image"
	"strings"

	// Registered for their DecodeConfig alone: the header is read, the
	// picture is not. GIF and WebP come with the decoders the thumbnailer
	// already pulls in.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// shapeVersion is which reading of a file's shape the code here performs.
// Raising it is how a new fact learned from the same header — the frame rate
// was the first — reaches the files that were read before it existed: every
// record below this is read again, once, and written back at this number.
const shapeVersion = 2

// imageSize reads a still's dimensions, as they are stored.
//
// DecodeConfig reads the header and stops — a few hundred bytes against the
// several megabytes a photograph is, which is what makes it affordable to ask
// of every image in a library. What it returns is the size the pixels are
// stored at, before any orientation the camera recorded: turning those round
// is `pictureSize`, which needs the tag the thumbnailer reads.
func imageSize(it Item) (width, height int) {
	f, err := OpenItem(it)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// sampleFormatCodecs names the codec behind a video sample entry's
// four-character code, in the spelling ffprobe uses for the same stream — so
// that a film described by the box tree and one described by a probe read the
// same way in a listing.
var sampleFormatCodecs = map[string]string{
	"avc1": "h264", "avc3": "h264",
	"hev1": "hevc", "hvc1": "hevc",
	"av01": "av1",
	"vp09": "vp9", "vp08": "vp8",
	"mp4v": "mpeg4",
	// Soundtracks. "mp4a" is very nearly always AAC — the entry can carry
	// other things through its descriptor, but a file that does is a rarity
	// against a format that is the reason the box exists, and both answers
	// route playback the same way.
	"mp4a": "aac", "ac-3": "ac3", "ec-3": "eac3",
	"alac": "alac", "opus": "opus", "flac": "flac",
	"samr": "amr", "twos": "pcm", "sowt": "pcm",
}

// codecOfSampleFormat translates one, or returns "" for a code nothing here
// recognises — a guess would be worse than the silence, since this is what a
// listing shows and what the conversion routes read.
func codecOfSampleFormat(format string) string {
	return sampleFormatCodecs[strings.ToLower(format)]
}

// shapeOf reads what a file says about its own picture, cheaply: a still's
// dimensions from its header, and a film's from the box tree where it is one
// of the ISO base media containers. Everything else answers nothing here and
// gets its shape from the ffprobe its duration already needed.
//
// codec is what is already known, and is handed back unchanged unless nothing
// knew it: a probe's answer is the better one, this being a translation of a
// four-character code into the name a probe would have used.
func shapeOf(it Item, codec string) (width, height int, vcodec string, fps float64) {
	switch it.Kind {
	case KindImage:
		w, h := imageSize(it)
		return w, h, codec, 0
	case KindVideo:
		format, w, h, _, rate := SampleInfo(it)
		if w == 0 {
			return 0, 0, codec, 0
		}
		if codec == "" {
			codec = codecOfSampleFormat(format)
		}
		return w, h, codec, rate
	}
	return 0, 0, codec, 0
}

// soundtrackOf is the same walk asked about the sound, for a film whose
// duration a native parser read and which therefore never saw an ffprobe.
func soundtrackOf(it Item) string {
	if it.Kind != KindVideo {
		return ""
	}
	_, _, _, audio, _ := SampleInfo(it)
	return codecOfSampleFormat(audio)
}

// pixelCount is how big a picture is as one number, for ordering. Width alone
// puts a tall clip from a phone above a film and height alone does the
// reverse; the area is what somebody sorting by resolution means.
//
// A file whose shape has not been read yet counts as nothing, which puts it at
// the unsorted end rather than in the middle of the order under a number
// nobody measured.
func pixelCount(it *Item) int64 {
	return int64(it.Width) * int64(it.Height)
}

// bitsPerSecond is what a file spends per second, its size over its playing
// time. The whole file rather than the picture alone — that is the only rate
// obtainable without reading the stream, and for a variable-rate file it is
// the truer one, the rate such a file declares being a fiction.
//
// Rounded to whole bits, which is far finer than any two files differ by, and
// zero where there is no length to divide by.
func bitsPerSecond(it *Item) int64 {
	if it.Duration <= 0 {
		return 0
	}
	return it.Size * 8 * 1000 / it.Duration
}
