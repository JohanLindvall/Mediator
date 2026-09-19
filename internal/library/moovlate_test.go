package library

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// rawBox is one ISO base media box: a 32-bit size, a four-character type, and
// whatever it holds.
func rawBox(typ string, payload []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(8+len(payload)))
	b.WriteString(typ)
	b.Write(payload)
	return b.Bytes()
}

// A recorder writes the index after the data, since the index is not known
// until the recording ends; a muxer asked for faststart writes it first. The
// walk that finds the index for the shape reads its position off the same
// pass, at no extra cost, because that position is what decides whether a
// file the browser already opens is still worth a copy.
func TestTheIndexBehindTheDataIsNoticed(t *testing.T) {
	ftyp := rawBox("ftyp", []byte("isom\x00\x00\x02\x00isom"))
	mdat := rawBox("mdat", make([]byte, 64))
	moov := rawBox("moov", nil) // empty: no tracks, so no shape — the position is the question
	for _, c := range []struct {
		what string
		file []byte
		late bool
	}{
		{"index after the data, as a phone records it", bytes.Join([][]byte{ftyp, mdat, moov}, nil), true},
		{"index first, as faststart writes it", bytes.Join([][]byte{ftyp, moov, mdat}, nil), false},
		{"index first and a free box between", bytes.Join([][]byte{ftyp, moov, rawBox("free", nil), mdat}, nil), false},
	} {
		_, _, _, _, _, late := sampleInfo(bytes.NewReader(c.file), int64(len(c.file)))
		if late != c.late {
			t.Errorf("%s: moovLate = %v, want %v", c.what, late, c.late)
		}
	}
	// No index at all is not "late": there is nothing to move, and the walk
	// answers nothing about a file it does not understand.
	noMoov := bytes.Join([][]byte{ftyp, mdat}, nil)
	if _, _, _, _, _, late := sampleInfo(bytes.NewReader(noMoov), int64(len(noMoov))); late {
		t.Error("a file with no index was called late")
	}
}

// Only the box reading says the index is late, and only a change of file
// unsays it: the ffprobe that runs when a film is opened carries no such
// fact and must not wipe one already read.
func TestALaterProbeDoesNotUnsayWhereTheIndexIs(t *testing.T) {
	l := quietLib("/m")
	l.upsert("/m/clip.mp4", KindVideo, 10, time.Unix(1, 0), fileKey{}, false)
	id := PathID("/m/clip.mp4")
	l.setProbe(id, Probe{Width: 886, Height: 1920, MoovLate: true})
	if it, _ := l.Get(id); !it.MoovLate {
		t.Fatal("the box reading's answer was not recorded")
	}
	l.setProbe(id, Probe{VCodec: "h264", Width: 886, Height: 1920, Probed: true})
	if it, _ := l.Get(id); !it.MoovLate {
		t.Error("an ffprobe of the same file wiped where the index sits")
	}
	// A replaced file is a different file, and is read again.
	l.upsert("/m/clip.mp4", KindVideo, 11, time.Unix(2, 0), fileKey{}, false)
	if it, _ := l.Get(id); it.MoovLate {
		t.Error("a replaced file kept the old file's index position")
	}
}
