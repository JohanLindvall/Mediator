package library

import (
	"io"
	"os"
	"sort"
)

// File is readable item content: seekable/random-access and closable.
// *os.File implements it; so does the reader over rar volume sets.
type File interface {
	io.ReadSeekCloser
	io.ReaderAt
}

// OpenItem opens an item's content for reading, whether it is a plain file
// or lives inside a rar volume set.
func OpenItem(it Item) (File, error) {
	if it.stored != nil {
		return newStoredReader(it.stored), nil
	}
	return os.Open(it.Path)
}

// SeekByte maps a time to a byte offset in an item's content, for content
// that carries an index of its own and cannot be seeked by timestamp.
//
// A DVD title is both. Its VOBs are cells stitched end to end and the
// timestamps do not run continuously across the joins, so ffmpeg works out a
// duration of about half the truth — and it will not seek past what it
// believes the end to be. Measured: everything after 26 minutes of a
// 50-minute title returned no picture at all, and a film's last hour was
// simply unreachable. The disc's own cell table says which sector each cell
// starts at and how long it plays, and on five of six measured titles it
// accounts for the bytes exactly, so the seek is done by position instead.
// ffmpeg's http input takes a byte offset, which is why this works only over
// the loopback URL; a pipe cannot seek by anything.
//
// Between cell boundaries it interpolates, and that is where the only error
// is: a cell is four or five minutes of variable-bitrate picture, so landing
// is close rather than exact. Against a seek that does nothing at all, that
// is a trade worth making — and the client is told the stream starts where it
// asked, because approximately there is where it does.
func SeekByte(it Item, seconds float64) (int64, bool) {
	if it.stored == nil || len(it.stored.seek) == 0 || seconds <= 0 {
		return 0, false
	}
	pts := it.stored.seek
	ms := int64(seconds * 1000)
	i := sort.Search(len(pts), func(i int) bool { return pts[i].ms > ms }) - 1
	if i < 0 {
		return 0, false // before the first point is the beginning
	}
	if i+1 >= len(pts) || pts[i+1].ms <= pts[i].ms {
		return pts[i].off, true
	}
	a, b := pts[i], pts[i+1]
	// In floating point: the product of a cell's bytes and its milliseconds
	// fits an int64 for a disc (2^33 by 2^24), and would not for the next
	// thing this is reused on.
	return a.off + int64(float64(b.off-a.off)*float64(ms-a.ms)/float64(b.ms-a.ms)), true
}
