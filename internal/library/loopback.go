package library

// The one seekable view of an archived member.
//
// A member of a multi-part archive set is byte runs spread over the volumes;
// there is no path for ffmpeg or ffprobe to open, so both used to be handed a
// bounded prefix on standard input. Over a pipe nothing can seek, which put
// the whole file beyond the first few dozen megabytes out of reach — and that
// is exactly where a release's front matter ends and its picture begins.
//
// This server already serves item content with HTTP Range support over
// OpenItem (see handleStream), which is a seekable view of the very same
// bytes. So the tools are pointed at the server's own loopback address
// instead of at a pipe: ffmpeg opens the URL, ranges straight to the offset
// it wants, and reads a few megabytes there (measured on 22 archived members,
// 140 extractions: 9.3-28.7 MiB and 0.09-1.91 s per frame, against 64-66 MiB
// and 6-9 s for a piped prefix that could only ever reach the first minute).
//
// The address is only known once the listener is bound, which is why main
// calls SetLoopback after binding rather than the flag being read here: with
// -listen :0 the port is the kernel's choice, and with -listen bound to a
// single non-loopback interface there is no loopback address at all. When
// SetLoopback is never called, or was called with "", every caller here
// returns "" and the piped-prefix fallback runs.

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/url"
	"strconv"
	"sync"
)

// InternalHeader marks a request this process made to itself. handleStream
// uses it to keep an internal read from counting as playback, which would
// otherwise make the thumbnailer throttle itself against its own fetch and
// pause tag enrichment for as long as tiles are being generated.
//
// The value is a per-process random token, so the marker cannot be guessed
// from outside; and even if it were, all it buys is skipping that flag and
// accepting a byte ceiling on the response. Nothing is authorised by it.
const InternalHeader = "X-Media-Internal"

// InternalWholeHeader marks an internal read that legitimately needs the
// entire member rather than a piece of it.
//
// handleStream caps an ordinary internal response, because a thumbnail or a
// probe reads a few tens of megabytes and one asking for more is not doing
// the job it was started for. A conversion is the other kind: rewrapping or
// segmenting a two-hour film reads every byte of it, and the cap silently
// truncated the result — a three-gigabyte member came out as a hundred and
// thirty megabytes, which is a file that opens and then stops. This says
// which kind of read this is. It carries the same unguessable token and buys
// nothing else: what bounds one of these is the file's own length and the
// budget of whatever started it.
const InternalWholeHeader = "X-Media-Internal-Whole"

var (
	loopbackMu   sync.RWMutex
	loopbackBase string

	internalTokenOnce sync.Once
	internalTokenVal  string
)

// InternalToken is this process's marker value for InternalHeader.
func InternalToken() string {
	internalTokenOnce.Do(func() {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			// Never observed; a fixed value would still only skip a flag.
			internalTokenVal = "internal"
			return
		}
		internalTokenVal = hex.EncodeToString(b[:])
	})
	return internalTokenVal
}

// SetLoopback records where this process's own HTTP server can be reached
// from this machine, e.g. "http://127.0.0.1:8080". Pass "" to say there is
// no such address, which puts every caller back on the piped-prefix path.
func SetLoopback(base string) {
	loopbackMu.Lock()
	defer loopbackMu.Unlock()
	loopbackBase = base
}

// LoopbackBase returns what SetLoopback was given, "" when unset.
func LoopbackBase() string {
	loopbackMu.RLock()
	defer loopbackMu.RUnlock()
	return loopbackBase
}

// LoopbackURL is the address of an item's content on this server's own
// loopback interface, "" when there is none. It is a seekable input: the
// endpoint behind it answers Range requests.
func LoopbackURL(it Item) string {
	base := LoopbackBase()
	if base == "" || it.ID == "" {
		return ""
	}
	return base + "/api/stream/" + url.PathEscape(it.ID)
}

// LoopbackHeaderArg is the value for ffmpeg's and ffprobe's -headers option:
// the internal marker, CRLF-terminated the way the protocol wants it.
func LoopbackHeaderArg() string {
	return InternalHeader + ": " + InternalToken() + "\r\n"
}

// LoopbackWholeHeaderArg is LoopbackHeaderArg for a reader that needs the
// whole member: a rewrap, or a segmented conversion.
func LoopbackWholeHeaderArg() string {
	return LoopbackHeaderArg() + InternalWholeHeader + ": " + InternalToken() + "\r\n"
}

// LoopbackAddr turns a bound listener address into the base URL loopback can
// reach it on, or "" when loopback cannot: a listener bound to one specific
// non-loopback interface is not reachable from 127.0.0.1, and guessing
// otherwise would send every internal read to whatever else answers there.
func LoopbackAddr(addr net.Addr) string {
	ta, ok := addr.(*net.TCPAddr)
	if !ok || ta.Port <= 0 {
		return ""
	}
	host := ""
	switch {
	case ta.IP == nil || ta.IP.IsUnspecified():
		host = "127.0.0.1" // bound to everything, so also to loopback
	case ta.IP.IsLoopback():
		host = ta.IP.String() // JoinHostPort brackets ::1 for us
	default:
		return ""
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(ta.Port))
}
