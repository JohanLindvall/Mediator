package dlna

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FormatTime writes a position the way UPnP wants it: hours, minutes and
// whole seconds, with no leading zero on the hour.
func FormatTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d / time.Second)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, (s/60)%60, s%60)
}

// ParseTime reads one back. Devices answer NOT_IMPLEMENTED where they do not
// know — a live stream, or a file they have not opened yet — and some write
// fractions onto the seconds; both come back as zero and a real number
// respectively rather than as an error, since a poll has nothing useful to do
// with one.
func ParseTime(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "NOT_IMPLEMENTED") {
		return 0
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	if total < 0 {
		return 0
	}
	return time.Duration(total * float64(time.Second))
}

// UPnPClass is what kind of thing this is, in the vocabulary a renderer
// understands. Sets do use it: one told it is looking at a photograph will
// not offer a transport for it.
func UPnPClass(kind string) string {
	switch kind {
	case "audio":
		return "object.item.audioItem.musicTrack"
	case "image":
		return "object.item.imageItem.photo"
	default:
		return "object.item.videoItem"
	}
}

// Meta is what a renderer is told about the thing it is being given.
type Meta struct {
	Title    string
	Class    string // UPnPClass of the kind
	MIME     string
	URI      string
	Duration time.Duration
	// Artist and Album are what a set puts on the screen beside the artwork
	// while music plays; it has nothing else to show, having been handed one
	// file and no library.
	Artist string
	Album  string
	// Art is a picture to show while it plays — the cover, for music. It is
	// fetched by the set like everything else, so it is an absolute URL on
	// the same address as the media.
	Art string
	// Genre, Year and Track are what a set puts in its own information
	// panel; a release with none of them tagged simply sends none of them.
	Genre string
	Year  int
	Track int
	// Size is the file's length in bytes, and it is worth sending: a
	// renderer uses it to know how much there is rather than discovering the
	// end by arriving at it.
	Size int64
	// Caption is a sidecar subtitle for the set to fetch and draw itself.
	// The set has no idea there are others to choose from: it draws this one
	// or none, which is why the choice is made before it is sent.
	Caption string
}

// Metadata builds the DIDL-Lite document handed over beside the URL.
//
// It is not decoration. A renderer given a bare URL has to guess what is
// behind it, and several will not play what they cannot name; the protocol
// info here is also where the set is told that the source answers byte
// ranges (DLNA.ORG_OP=01), which is what makes seeking on the television's
// own remote work rather than restarting the file. And a television playing
// music shows what this document says and nothing else — without the cover
// and the names, a set that could be showing the sleeve shows its own logo
// on a black screen.
func Metadata(m Meta) string {
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
		` xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"` +
		` xmlns:dlna="urn:schemas-dlna-org:metadata-1-0/"` +
		// Samsung's namespace, which LG reads as well: sidecar subtitles are
		// not in the UPnP vocabulary at all, and this is what the sets that
		// draw them have agreed to look for.
		` xmlns:sec="http://www.sec.co.kr/">`)
	b.WriteString(`<item id="0" parentID="-1" restricted="1">`)
	fmt.Fprintf(&b, "<dc:title>%s</dc:title>", escape(m.Title))
	fmt.Fprintf(&b, "<upnp:class>%s</upnp:class>", escape(m.Class))
	if m.Artist != "" {
		// Both spellings: sets read one or the other and rarely both.
		fmt.Fprintf(&b, "<upnp:artist>%s</upnp:artist><dc:creator>%s</dc:creator>",
			escape(m.Artist), escape(m.Artist))
	}
	if m.Album != "" {
		fmt.Fprintf(&b, "<upnp:album>%s</upnp:album>", escape(m.Album))
	}
	if m.Art != "" {
		fmt.Fprintf(&b, `<upnp:albumArtURI dlna:profileID="JPEG_TN">%s</upnp:albumArtURI>`,
			escape(m.Art))
	}
	if m.Genre != "" {
		fmt.Fprintf(&b, "<upnp:genre>%s</upnp:genre>", escape(m.Genre))
	}
	if m.Year > 0 {
		// dc:date wants a date, and a year is all a tag carries; the first of
		// January is the conventional stand-in and sets show only the year.
		fmt.Fprintf(&b, "<dc:date>%04d-01-01</dc:date>", m.Year)
	}
	if m.Track > 0 {
		fmt.Fprintf(&b, "<upnp:originalTrackNumber>%d</upnp:originalTrackNumber>", m.Track)
	}
	if m.Caption != "" {
		// Both spellings again, and both places: sets differ over which of
		// these they read, and one that reads neither ignores all three
		// without complaint.
		fmt.Fprintf(&b, `<sec:CaptionInfoEx sec:type="srt">%s</sec:CaptionInfoEx>`, escape(m.Caption))
		fmt.Fprintf(&b, `<sec:CaptionInfo sec:type="srt">%s</sec:CaptionInfo>`, escape(m.Caption))
	}
	b.WriteString(`<res protocolInfo="` + escape("http-get:*:"+m.MIME+":DLNA.ORG_OP=01;DLNA.ORG_FLAGS=01700000000000000000000000000000") + `"`)
	if m.Duration > 0 {
		fmt.Fprintf(&b, ` duration="%s"`, FormatTime(m.Duration))
	}
	if m.Size > 0 {
		fmt.Fprintf(&b, ` size="%d"`, m.Size)
		if m.Duration > 0 {
			// UPnP's bitrate is **bytes** per second, not bits: a well-known
			// wart in the specification, and writing bits here makes a set
			// think a film is eight times the size it is.
			fmt.Fprintf(&b, ` bitrate="%d"`, int64(float64(m.Size)/m.Duration.Seconds()))
		}
	}
	fmt.Fprintf(&b, ">%s</res>", escape(m.URI))
	if m.Caption != "" {
		// The subtitle as a resource of its own, which is the third place a
		// set may look for it.
		fmt.Fprintf(&b, `<res protocolInfo="http-get:*:text/srt:*">%s</res>`, escape(m.Caption))
	}
	b.WriteString(`</item></DIDL-Lite>`)
	return b.String()
}
