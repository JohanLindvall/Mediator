// SPDX-License-Identifier: MIT

package library

// What a file is when its name will not say.
//
// A name is how this library decides what it holds — `extKind` in scan.go —
// and nearly always that is right: it costs nothing, and a file called `.mkv`
// is one. But downloads arrive with no extension at all, with a dot in a name
// that is no extension, or with a suffix a tool cut short; and an archive is
// one whatever it is called. So a file whose name says nothing this library
// knows — no extension, or one it does not recognise (`nameSaysNothing`) —
// has its opening read, and is indexed as what that says: media of a kind, or
// a ZIP or RAR archive whose members are read as any other archive's are.
//
// Two guards keep it cheap. The size floor (`sniffMinSize`): a file under a
// megabyte is far likelier a repository object or a lock file than anything
// worth watching. And a file a downloader is still writing (`unfinished`) is
// left alone: it is renamed to its real name when it is whole and indexed
// then, where read now a download in progress would be shown twice (measured
// over these disks: of 138,579 files of a megabyte or more, 462 had no
// extension or an unknown one, and 301 of those were unfinished downloads).
//
// Nothing here guesses: a signature at a fixed offset, or nothing. A wrong
// answer indexes a program as a film and hands it to a player.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// sniffMinSize is the floor described above. A media file worth indexing is
// not smaller than this; a repository's loose objects mostly are.
const sniffMinSize = 1 << 20

// sniffLen is how much of the opening is read: enough for a transport
// stream's third packet, and still a single read.
const sniffLen = 512

// unfinished is the suffixes downloaders give a file until it is whole.
var unfinished = map[string]bool{
	".part": true, ".partial": true, ".crdownload": true, ".download": true,
	".!qb": true, ".!ut": true, ".tmp": true,
}

// Sniffed is what a file's opening says it holds: media of a kind, or an
// archive ("zip" or "rar") whose members are the media.
type Sniffed struct {
	Kind    Kind
	Archive string
}

// SniffContent reads the opening of a file whose name says nothing this
// library knows, and says what it holds; the zero value where the name has
// spoken, the file is small or unfinished or cannot be opened, or the bytes
// match nothing.
func SniffContent(path string, size int64) Sniffed {
	if size < sniffMinSize || !nameSaysNothing(path) {
		return Sniffed{}
	}
	f, err := os.Open(path)
	if err != nil {
		return Sniffed{}
	}
	defer f.Close()
	var head [sniffLen]byte
	n, _ := io.ReadFull(f, head[:])
	return Sniffed{Kind: kindOfMagic(head[:n]), Archive: archiveOfMagic(head[:n])}
}

// nameSaysNothing is a name with no extension, or with one this library does
// not know — a dotted release name, a truncated suffix, a type it has no use
// for — and never a download still being written, nor a part of an archive
// set, whose bytes say nothing alone and which its set's first part answers
// for.
func nameSaysNothing(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return true
	}
	if unfinished[ext] || Classify(path) != "" || IsSubtitle(path) || isDiscImage(path) || isRarRelated(path) {
		return false
	}
	base := filepath.Base(path)
	return !isZip(base) && !zipSplitRe.MatchString(base) && !zipSpanRe.MatchString(base)
}

// archiveOfMagic names the archive the opening bytes begin, where this
// library reads that kind: a ZIP's first local header, a RAR's marker in
// either of its versions.
func archiveOfMagic(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("PK\x03\x04")):
		return "zip"
	case bytes.HasPrefix(b, []byte("Rar!\x1a\x07\x00")), bytes.HasPrefix(b, []byte("Rar!\x1a\x07\x01\x00")):
		return "rar"
	}
	return ""
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
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "CDXA":
		// A Video CD's MPEG, as AVSEQ01.DAT carries it: the name says
		// nothing, which is why .dat is in no table, and this says it all.
		return KindVideo
	case bytes.HasPrefix(b, []byte{0x00, 0x00, 0x01, 0xba}):
		// An MPEG program stream's pack header: a DVD's VOB, a VCD's MPEG.
		return KindVideo
	case len(b) > 376 && b[0] == 0x47 && b[188] == 0x47 && b[376] == 0x47:
		// A transport stream: three packets' sync bytes 188 apart, since one
		// 0x47 opens all manner of files.
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
