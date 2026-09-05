package library

// What a file is when its name will not say.
//
// A name is how this library decides what it holds — `extKind` in scan.go,
// measured against real disks — and that is right nearly always: it costs
// nothing, it is what every tool in the chain agrees on, and a file called
// `.mkv` is one. But some downloads arrive with no extension at all: three
// members of one release, several hundred megabytes each, plainly MP4 from
// their first eight bytes and invisible to a library that reads only names.
//
// So a file with **no extension whatsoever** gets its opening bytes read.
// Not a file with an unknown extension — one that says `.nfo` or `.par2` has
// told us what it is and is not media, and sniffing those would be a read
// per file for an answer already given. Only the ones that said nothing.
//
// Measured before it was written: across these disks there are 9,024 files
// with no extension, of which **37** are a megabyte or larger. So the size
// floor is what makes this free — nine thousand opens become thirty-seven —
// and it is not arbitrary either: an extensionless file under a megabyte is
// far likelier to be a git object or a lock file than anything anybody wants
// to watch. Of the 37, all but a handful were MP4.

import (
	"bytes"
	"os"
	"path/filepath"
)

// sniffMinSize is the floor described above. A media file worth indexing is
// not smaller than this; a repository's loose objects mostly are.
const sniffMinSize = 1 << 20

// sniffLen is how much of the opening is read. Enough for every magic below
// with room to spare, and one read of it is a single disk seek.
const sniffLen = 32

// ClassifyContent is the second chance a nameless file gets: its kind read
// out of its first bytes, or "" for anything this does not recognise —
// which, being the fallback rather than the rule, is left alone exactly as
// it was before.
func ClassifyContent(path string, size int64) Kind {
	if size < sniffMinSize || filepath.Ext(path) != "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var head [sniffLen]byte
	n, _ := f.Read(head[:])
	return kindOfMagic(head[:n])
}

// kindOfMagic reads the opening bytes. Pure, so the table below is testable
// without a disk.
//
// Only the containers a library like this actually receives without a name.
// Every entry is a signature at a fixed place — nothing here scans, guesses
// or falls back on statistics, because a wrong answer indexes a disk image
// or a database as a film and hands it to a player.
func kindOfMagic(b []byte) Kind {
	switch {
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		// ISO base media: MP4, M4V, MOV, M4A and the rest. The brand says
		// which, and only the audio-only brands are not video — an unknown
		// brand is far likelier to be a film than a song, and `EnsureCodecs`
		// settles what is really inside when the file is opened.
		switch string(b[8:12]) {
		case "M4A ", "M4B ", "M4P ":
			return KindAudio
		}
		return KindVideo
	case bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		// EBML: Matroska or WebM. Which of the two is in the DocType a few
		// bytes further on, and it does not matter here — both are video to
		// this library, and a WebM holding only sound is rare enough to be
		// left to the probe.
		return KindVideo
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "AVI ":
		return KindVideo
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return KindAudio
	case bytes.HasPrefix(b, []byte("OggS")):
		// Ogg carries both; the codec is in the first page's header, and
		// video in an Ogg stream is rare enough that this follows the
		// extension table, which calls a bare .ogg audio.
		return KindAudio
	case bytes.HasPrefix(b, []byte("fLaC")):
		return KindAudio
	case bytes.HasPrefix(b, []byte("ID3")):
		return KindAudio
	case bytes.HasPrefix(b, []byte{0x30, 0x26, 0xb2, 0x75}):
		// ASF: what .wmv and .wma both are. Video, for the same reason an
		// unknown ISO brand is: the probe settles it either way.
		return KindVideo
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return KindImage
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return KindImage
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return KindImage
	case bytes.HasPrefix(b, []byte("GIF8")):
		return KindImage
	}
	return ""
}
