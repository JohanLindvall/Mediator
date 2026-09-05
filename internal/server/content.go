package server

// One library, several faces.
//
// The same server can answer as a music library to one caller and as a video
// library to another: a request carrying `X-Media-Content: music` is shown
// music and nothing else — not in its listings, not in its counts, and not
// if it asks for a video by id.
//
// The header is set by whatever sits in front (a reverse proxy giving one
// hostname to each face), never by the page: a browser cannot put headers on
// the requests that matter here — an <img> asking for a thumbnail or a
// <video> asking for a stream send what they like — which is exactly why the
// filtering is done here rather than in the client. The client is told what
// it may see (`/api/info`) so that it can leave out the views it has no
// business offering, but that is a courtesy; this is the enforcement.
//
// Indexing is untouched. The library holds everything it always did, one
// scan feeds every face, and turning the header off shows the whole library
// again with nothing to rebuild.

import (
	"net/http"
	"strings"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// ContentHeader carries the classes of media a request may see, comma
// separated: "music", "videos", "images". Absent or empty means everything.
const ContentHeader = "X-Media-Content"

// content is the set of classes one request is allowed to see. Music covers
// both audio files and the playlists that list them: a playlist is how a
// release names its own running order, and a music library without them
// would be missing half of what it groups by.
type content struct {
	video bool
	image bool
	music bool
}

var everything = content{video: true, image: true, music: true}

// contentOf reads the restriction a request arrived with.
func contentOf(r *http.Request) content { return parseContent(r.Header.Get(ContentHeader)) }

// parseContent reads the header. Anything it does not recognise is ignored;
// a header naming nothing recognisable is treated as no header at all,
// because the alternative is a face that shows nothing and looks broken.
func parseContent(h string) content {
	var c content
	for _, part := range strings.Split(h, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "music", "audio":
			c.music = true
		case "video", "videos":
			c.video = true
		case "image", "images", "photos", "pictures":
			c.image = true
		}
	}
	if c == (content{}) {
		return everything
	}
	return c
}

// unrestricted reports whether this is the whole library.
func (c content) unrestricted() bool { return c == everything }

// allows reports whether an item of this kind may be seen at all.
func (c content) allows(k library.Kind) bool {
	switch k {
	case library.KindVideo:
		return c.video
	case library.KindImage:
		return c.image
	case library.KindAudio, library.KindPlaylist:
		return c.music
	}
	return false
}

// The sets the three faces draw from, built once: kinds is asked several
// times per request.
var (
	videoKinds = library.KindsOf(library.KindVideo)
	imageKinds = library.KindsOf(library.KindImage)
	musicKinds = library.KindsOf(library.KindAudio, library.KindPlaylist)
)

// kinds is what a listing may draw from.
func (c content) kinds() library.KindSet {
	if c.unrestricted() {
		return 0 // the zero set is every kind
	}
	var set library.KindSet
	if c.video {
		set |= videoKinds
	}
	if c.image {
		set |= imageKinds
	}
	if c.music {
		set |= musicKinds
	}
	return set
}

// mask restricts totals to what may be seen. The per-kind numbers are
// independent, so this is subtraction rather than a second count — and the
// total is recomputed from what is left rather than carried over, or a face
// showing music would report the whole library's size beside it.
func (c content) mask(x library.Counts) library.Counts {
	if c.unrestricted() {
		return x
	}
	if !c.video {
		x.Video = 0
	}
	if !c.image {
		x.Image = 0
	}
	if !c.music {
		x.Audio, x.Playlist, x.Albums, x.Artists, x.Genres, x.Audiobooks = 0, 0, 0, 0, 0, 0
	}
	if !c.video {
		x.Series = 0 // television is video, and this face has none
	}
	x.Total = x.Video + x.Image + x.Audio + x.Playlist
	return x
}

// names is what /api/info tells the client it may show, in a fixed order so
// the answer does not change shape between requests.
func (c content) names() []string {
	if c.unrestricted() {
		return nil // nothing withheld: the client offers everything
	}
	var out []string
	if c.video {
		out = append(out, "videos")
	}
	if c.image {
		out = append(out, "images")
	}
	if c.music {
		out = append(out, "music")
	}
	return out
}

// item resolves the item a request names, and refuses one this request is
// not allowed to see — a direct link to a video is nothing on a music face.
func (s *Server) item(r *http.Request, id string) (library.Item, bool) {
	it, ok := s.lib.Get(id)
	if !ok || !contentOf(r).allows(it.Kind) {
		return library.Item{}, false
	}
	// And the other restriction, which composes with it: whichever of the
	// two says no, the answer is that this caller has no such item. Every
	// by-id handler resolves through here — the stream, the thumbnail, the
	// conversions, the flags, the positions, the subtitles — so this is what
	// makes a path restriction hold for all of them at once.
	if !pathsOf(r).AllowsItem(it) {
		return library.Item{}, false
	}
	return it, true
}

// PathsHeader restricts a request to the parts of the library that live
// under the directories it names, separated by commas (or newlines, for a
// path with a comma in it). Absent or empty means the whole library.
//
// The same posture as the content header above and set the same way — by
// whatever sits in front, never by the page — and for the same reason: a
// browser cannot put a header on the <img> that fetches a thumbnail or the
// <video> that fetches a stream, so the filtering has to be here.
//
// The two compose. A request carrying both is shown the intersection, which
// falls out of each being applied where it belongs rather than either
// knowing anything about the other.
const PathsHeader = "X-Allowed-Paths"

// pathsOf reads the restriction a request arrived with.
func pathsOf(r *http.Request) library.PathFilter {
	return library.ParsePaths(r.Header.Get(PathsHeader))
}
