package server

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/dlna"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// Casting to a television that is not a browser.
//
// AirPlay and the Remote Playback API both hand a *browser's* element to a
// receiver, and neither can reach a set that is merely discovered — a
// television found over DIAL is offered to no picker and never will be. A
// DLNA renderer is driven from here instead: the server calls it over SOAP
// and hands it a URL on this machine's LAN address, and the set fetches the
// file and decodes it itself. What plays is the file, at its own quality,
// with no conversion and nothing held open here.
//
// There is deliberately no authentication on any of this, as there is none
// on anything else here: whoever can reach the port can start something
// playing on a television in the house. That is the same posture as being
// able to point the library at any directory, and the same network is
// assumed.
const (
	// How long a discovered set is trusted before the network is asked
	// again. Discovery is a multicast and a few small fetches, so it is
	// cheap — but it is also two and a half seconds of waiting, which a
	// viewer opening a player should not pay for every time.
	renderersTTL = 60 * time.Second
	// How long to wait for answers. The M-SEARCH says MX 2, so a device may
	// legitimately hold its reply back that long to avoid a stampede.
	searchWait = 2500 * time.Millisecond
	// How large the cover sent to a television is. It is looked at from
	// across a room on a screen measured in feet, so the grid's own size
	// would be a smear; this is one generation and one cache entry per
	// album, which the store keys by width like any other.
	castArtWidth = 640
	// How many times a set is asked whether it took the file, when it did
	// not answer that it had. One a second.
	confirmTries = 6
)

// casting holds what has been found on the network. It is a cache and not a
// registry: a renderer that has been switched off simply stops answering,
// and nothing here needs to notice before the next search.
type casting struct {
	mu    sync.Mutex
	found map[string]*dlna.Renderer
	order []string
	at    time.Time
	// searching is held for the length of a search so that several clients
	// asking at once wait for one answer rather than filling the network
	// with duplicate M-SEARCHes.
	searching sync.Mutex
}

// SetLocalPort tells the server which port it is reachable on, so it can
// give a renderer an address that renderer can fetch from. main knows this
// only after binding, which is why it is not a field of New — the same
// reason the library is told about loopback there.
func (s *Server) SetLocalPort(port int) { s.port = port }

// renderers returns what is on the network, searching again when what we
// have has gone stale.
func (s *Server) renderers(ctx context.Context, force bool) []*dlna.Renderer {
	s.cast.mu.Lock()
	fresh := time.Since(s.cast.at) < renderersTTL && s.cast.found != nil
	s.cast.mu.Unlock()
	if fresh && !force {
		return s.castList()
	}

	s.cast.searching.Lock()
	defer s.cast.searching.Unlock()
	// Someone else may have done it while we waited for the turn.
	s.cast.mu.Lock()
	fresh = time.Since(s.cast.at) < renderersTTL && s.cast.found != nil
	s.cast.mu.Unlock()
	if fresh && !force {
		return s.castList()
	}

	found := dlna.Discover(ctx, searchWait)
	s.cast.mu.Lock()
	defer s.cast.mu.Unlock()
	s.cast.found = map[string]*dlna.Renderer{}
	s.cast.order = nil
	for _, r := range found {
		s.cast.found[r.ID] = r
		s.cast.order = append(s.cast.order, r.ID)
	}
	s.cast.at = time.Now()
	out := make([]*dlna.Renderer, 0, len(found))
	for _, id := range s.cast.order {
		out = append(out, s.cast.found[id])
	}
	return out
}

func (s *Server) castList() []*dlna.Renderer {
	s.cast.mu.Lock()
	defer s.cast.mu.Unlock()
	out := make([]*dlna.Renderer, 0, len(s.cast.order))
	for _, id := range s.cast.order {
		out = append(out, s.cast.found[id])
	}
	return out
}

// renderer looks one up by the id a client was given.
func (s *Server) renderer(ctx context.Context, id string) (*dlna.Renderer, bool) {
	s.cast.mu.Lock()
	r, ok := s.cast.found[id]
	s.cast.mu.Unlock()
	if ok {
		return r, true
	}
	// A client can outlive the cache — a page left open overnight still
	// holds the id it was given. Look again before saying no.
	for _, r := range s.renderers(ctx, false) {
		if r.ID == id {
			return r, true
		}
	}
	return nil, false
}

func (s *Server) handleRenderers(w http.ResponseWriter, r *http.Request) {
	found := s.renderers(r.Context(), r.URL.Query().Get("fresh") == "1")
	out := RenderersResponse{Renderers: make([]RendererInfo, 0, len(found))}
	for _, d := range found {
		out.Renderers = append(out.Renderers, RendererInfo{
			ID: d.ID, Name: d.Name, Volume: d.CanControlVolume(),
		})
	}
	writeJSON(w, out)
}

// handleCast starts an item playing on a renderer.
func (s *Server) handleCast(w http.ResponseWriter, r *http.Request) {
	d, ok := s.renderer(r.Context(), r.PathValue("rid"))
	if !ok {
		http.Error(w, "no such renderer", http.StatusNotFound)
		return
	}
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The soundtrack list and the embedded captions come from the probe that
	// runs when a video is opened. A cast *is* an opening — and the indexes
	// the player sends were numbered against the probed listing, so a cast
	// resolved without the probe would count a shorter list and hand the set
	// the wrong subtitle, or none.
	it = s.probed(r.Context(), it)

	src, mimeType, err := s.castSource(r.Context(), d, it, r.URL.Query().Get("audio"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	meta := s.castMeta(r, d, it, src, mimeType)
	ctx := r.Context()
	if err := d.SetURI(ctx, src, meta); err != nil {
		// A set that has not answered has not necessarily failed. Measured
		// on a television that had another session open, the reply to this
		// took longer than any budget worth waiting on while the film
		// itself was already on screen — and reporting a failure then lost
		// the seek that should have followed, so the film started from the
		// beginning. What matters is whether it is showing what it was
		// given, so ask it that instead of believing the silence.
		if !showing(ctx, d, src) {
			s.log.Warn("cast failed", "renderer", d.Name, "item", it.Name, "err", err)
			http.Error(w, castFault(d.Name, "did not accept the file", err), http.StatusBadGateway)
			return
		}
		s.log.Debug("cast: set was slow to answer but took the file",
			"renderer", d.Name, "item", it.Name, "err", err)
	}
	if err := d.Play(ctx); err != nil {
		s.log.Warn("cast failed to start", "renderer", d.Name, "item", it.Name, "err", err)
		http.Error(w, castFault(d.Name, "would not start playing it", err), http.StatusBadGateway)
		return
	}
	// Seeking is asked for after playback starts: a set that has not opened
	// the file yet has nothing to seek in, and answers the request with a
	// fault rather than with the position.
	if t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64); t > 0 {
		if err := d.Seek(ctx, time.Duration(t*float64(time.Second))); err != nil {
			s.log.Debug("cast seek refused", "renderer", d.Name, "err", err)
		}
	}
	s.log.Info("casting", "renderer", d.Name, "item", it.Name, "type", mimeType)
	writeJSON(w, CastStatus{State: "TRANSITIONING", URI: src})
}

// castMeta describes the file to the set. A television has been handed one
// file and no library: it shows what this says and nothing else.
func (s *Server) castMeta(r *http.Request, d *dlna.Renderer, it library.Item, src, mimeType string) string {
	return dlna.Metadata(dlna.Meta{
		Title:    displayTitle(it),
		Class:    dlna.UPnPClass(string(it.Kind)),
		MIME:     mimeType,
		URI:      src,
		Duration: time.Duration(it.Duration) * time.Millisecond,
		Artist:   it.Artist,
		Album:    it.Album,
		Art:      s.castArt(d, it),
		Genre:    it.Genre,
		Year:     it.Year,
		Track:    it.Track,
		Size:     it.Size,
		Caption:  s.castCaption(d, it, r.URL.Query().Get("sub")),
	})
}

// showing reports whether the renderer is holding the URI it was handed,
// asked a few times over a few seconds: a set that has just been given
// something reports the previous track for a moment, and an empty answer
// while it opens the file.
func showing(ctx context.Context, d *dlna.Renderer, uri string) bool {
	for range confirmTries {
		if st, err := d.Status(ctx); err == nil && st.URI == uri {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
	return false
}

// handleCastNext queues what follows on the set itself, so the boundary
// between two tracks costs nothing: no poll to notice the end, no round trip
// to send the next one, no silence while both happen.
func (s *Server) handleCastNext(w http.ResponseWriter, r *http.Request) {
	d, ok := s.renderer(r.Context(), r.PathValue("rid"))
	if !ok {
		http.Error(w, "no such renderer", http.StatusNotFound)
		return
	}
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Probed for the same reason handleCast probes: what is queued next has
	// the same soundtracks and captions to resolve as what is playing.
	it = s.probed(r.Context(), it)
	src, mimeType, err := s.castSource(r.Context(), d, it, r.URL.Query().Get("audio"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	meta := s.castMeta(r, d, it, src, mimeType)
	if err := d.SetNextURI(r.Context(), src, meta); err != nil {
		// Optional in the specification, and plenty of renderers say no.
		// That is not a failure of the queue: the client goes on sending
		// each track as it sees the last one end.
		s.log.Debug("renderer will not queue ahead", "renderer", d.Name, "err", err)
		http.Error(w, err.Error(), http.StatusNotImplemented)
		return
	}
	writeJSON(w, CastStatus{State: "QUEUED", URI: src})
}

// castSource decides what URL to hand over and what to call it.
//
// The set says which containers it accepts, and where ours is not among them
// there is one thing worth trying before giving up: the rewrap, which is a
// real file with a length and ranges — exactly what a renderer wants, and
// unlike the segmented conversion something it can seek in. A live
// conversion is not offered at all: it answers no ranges and has no length,
// and a renderer given one either refuses it or plays it once from the top.
func (s *Server) castSource(ctx context.Context, d *dlna.Renderer, it library.Item, audio string) (src, mimeType string, err error) {
	mimeType = mimeFor(it)
	path := "stream/" + url.PathEscape(it.ID)

	// **Which soundtrack** cannot be said to a renderer: DLNA hands over a URL
	// and the set decides what is inside it, which is how a release carrying
	// four languages comes out in the one its file leads with. So the choice
	// is made by handing over a file that holds only the chosen one — a copy
	// at disk speed rather than a re-encode, produced here rather than while
	// the television sits on the URL waiting for it.
	//
	// It is settled **before** the name is, and that ordering is the point: a
	// copy may come out in a different container from the file it was made
	// from, and it is the copy the set has to be told about. Deciding the name
	// first meant a file whose container the set did not list took the branch
	// that renames it and never reached this at all — which is every video
	// that ships automatic dubs, the one kind of file where the choice is the
	// whole reason for the feature.
	if kind, ok := castTrackKind(it, audio); ok {
		if _, err := s.remux.File(ctx, it, audio, kind); err == nil {
			path = "remux/" + url.PathEscape(it.ID) + "?a=" + strconv.Itoa(audioTrack(audio)) + remuxQuery(kind)
			mimeType = remuxMime(it, kind)
		} else {
			// Nothing is lost: the file itself still plays, with whichever
			// soundtrack the set picks out of it.
			s.log.Debug("cast: cannot copy out the chosen soundtrack",
				"item", it.Name, "err", err)
		}
	}

	// And whatever it is called now, the set has to take that name.
	if !d.Accepts(mimeType) {
		// Before anything else is copied: another name for the very same
		// bytes, where the set lists one. A container has more than one name
		// in circulation and a set knows the ones its makers chose, so a file
		// it can demux perfectly well is refused over what it was called.
		if alt, ok := castAlias(mimeType, d.Accepts); ok {
			mimeType = alt
		} else if strings.HasPrefix(path, "stream/") && remuxable(it) {
			path = "remux/" + url.PathEscape(it.ID) + "?a=" + strconv.Itoa(audioTrack(audio))
			mimeType = "video/mp4"
		} else {
			return "", "", errCannotPlay{name: d.Name, typ: mimeType}
		}
	}
	base := s.localBase(d)
	if base == "" {
		return "", "", errNoAddress{}
	}
	// The query, where there is one, goes after the path the token covers.
	head, query, _ := strings.Cut(path, "?")
	if query != "" {
		query = "?" + query
	}
	return s.mediaURL(base, head) + query, mimeType, nil
}

// mediaURL is where a set fetches one of our media paths from: our address
// on its network, and the token that lets the request in without a password.
// The link carries its own permission because the television sends no
// credentials and cannot be given any, which is the whole reason signed URLs
// exist. Without a key to sign with (the database is off) the unsigned path
// still works, there being nothing in front of this port then.
func (s *Server) mediaURL(base, path string) string {
	if token, _, ok := s.sign.mint(time.Now()); ok {
		return base + "/api/signed/" + token + "/" + path
	}
	return base + "/api/" + path
}

// displayTitle is what the set puts on the screen: the tag where there is
// one, since a filename with the track number and the release group in it is
// not what anybody wants to read across a room.
func displayTitle(it library.Item) string {
	if it.Title != "" {
		return it.Title
	}
	return it.Name
}

// castArt is the picture to show while a track plays. Only for music: a
// television playing a film is showing the film, and one playing a
// photograph is showing the photograph.
//
// It is our own thumbnail — the embedded cover art, or the picture beside
// the tracks — at a size worth looking at from a sofa rather than the
// hundred-odd pixels a grid cell wants.
func (s *Server) castArt(d *dlna.Renderer, it library.Item) string {
	if it.Kind != library.KindAudio {
		return ""
	}
	base := s.localBase(d)
	if base == "" {
		return ""
	}
	return s.mediaURL(base, "thumb/"+url.PathEscape(it.ID)) + "?w=" + strconv.Itoa(castArtWidth)
}

// castCaption points the set at one of the film's subtitles, converted to
// SubRip — a sidecar, or a track carried inside the file, which the subtitle
// endpoint extracts exactly as it does for the browser. One numbering covers
// both (see Subtitles), and it is the numbering the player's menu used, so
// the index the viewer chose there names the same subtitle here.
//
// A renderer draws one or none: it is handed a URL, not a menu, and knows
// nothing of the others beside the film. So the viewer's own choice comes
// with the request (`?sub=`), and where there is none the first is sent —
// which is what the player itself defaults to. `sub=off` sends nothing,
// there being no way to turn one off from a television's remote once it has
// been given one.
func (s *Server) castCaption(d *dlna.Renderer, it library.Item, choice string) string {
	if it.Kind != library.KindVideo || choice == "off" {
		return ""
	}
	subs := s.lib.Subtitles(it)
	if len(subs) == 0 {
		return ""
	}
	index := 0
	if choice != "" {
		n, err := strconv.Atoi(choice)
		if err != nil || n < 0 || n >= len(subs) {
			return ""
		}
		index = n
	}
	base := s.localBase(d)
	if base == "" {
		return ""
	}
	return s.mediaURL(base, "subs/"+url.PathEscape(it.ID)+"/"+strconv.Itoa(index)) + "?format=srt"
}

type errCannotPlay struct{ name, typ string }

func (e errCannotPlay) Error() string {
	return e.name + " does not play " + e.typ
}

type errNoAddress struct{}

func (errNoAddress) Error() string {
	return "no address on this machine that the receiver could fetch from"
}

// localBase is the URL a renderer should fetch from: our address on the
// network *it* is on, not loopback and not whatever the page was loaded
// from. A viewer may be on the other side of the world; the television is in
// the room with the server.
func (s *Server) localBase(d *dlna.Renderer) string {
	if s.port == 0 {
		return ""
	}
	ip := dlna.LocalIPFor(d.Host)
	if ip == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(s.port))
}

// castTrackKind is which copy the viewer's soundtrack choice needs, if it
// needs one at all. Its own container where the streams belong to no MP4 —
// which is what a dubbed download is — and the MP4 rewrap otherwise.
func castTrackKind(it library.Item, audio string) (remuxKind, bool) {
	if audio == "" || len(it.Tracks) < 2 {
		return remuxCopy, false
	}
	switch {
	case trackCopyable(it):
		return remuxTrack, true
	case remuxable(it):
		return remuxCopy, true
	}
	return remuxCopy, false
}

// remuxMime is what a copy of this kind comes out as.
func remuxMime(it library.Item, kind remuxKind) string {
	if kind == remuxTrack {
		return mimeFor(it)
	}
	return "video/mp4"
}

// remuxQuery asks the endpoint for the same kind of copy again.
func remuxQuery(kind remuxKind) string {
	if kind == remuxTrack {
		return "&mode=track"
	}
	return ""
}

// castAliases are other names the same bytes can honestly be handed over
// under. Not conversions and not guesses — each is one container that two
// names describe, so a set demuxing what it is given finds exactly what the
// name promised.
//
// Whether it can *decode* what is inside is a different question, and one no
// renderer will answer: it says which containers it takes and nothing about
// the codecs in them. Letting it try and fail on screen is better than this
// server refusing on its behalf, which is the same judgement made for a set
// that declines to say what it accepts at all.
var castAliases = map[string][]string{
	// WebM is a profile of Matroska — a constrained one, so every WebM is a
	// Matroska file while the reverse does not hold, which is why this alias
	// runs one way only. A set that lists `video/x-matroska` and not
	// `video/webm` is describing its makers' list, not its demuxer: this is
	// what a video downloaded from a video site arrives as, and it was being
	// turned away at the door.
	"video/webm": {"video/x-matroska"},
	// One container, three spellings. The registered type is the key here and
	// devices list the others as readily.
	"video/x-msvideo": {"video/avi", "video/msvideo"},
	// A transport stream is an MPEG stream, and a set listing only the
	// general name will take one.
	"video/mp2t": {"video/mpeg"},
}

// castAlias returns the first alternative name this set accepts. It takes the
// question rather than the renderer, which is what makes the decision — which
// name is chosen, and that the WebM alias runs only one way — testable
// without a television on the network.
func castAlias(mimeType string, accepts func(string) bool) (string, bool) {
	for _, alt := range castAliases[strings.ToLower(mimeType)] {
		if accepts(alt) {
			return alt, true
		}
	}
	return "", false
}

// mimeFor names the container for the renderer's benefit. The extension is
// the honest answer here: it is what the set will decide by anyway, and for
// an archived member the member's own name carries it.
func mimeFor(it library.Item) string {
	ext := strings.ToLower(filepath.Ext(it.Name))
	if typ := mime.TypeByExtension(ext); typ != "" {
		if base, _, err := mime.ParseMediaType(typ); err == nil {
			return base
		}
		return typ
	}
	switch it.Kind {
	case library.KindAudio:
		return "audio/mpeg"
	case library.KindImage:
		return "image/jpeg"
	default:
		return "video/mp4"
	}
}

// handleCastStatus asks the set where it has got to, which is what turns the
// player into a remote control.
func (s *Server) handleCastStatus(w http.ResponseWriter, r *http.Request) {
	d, ok := s.renderer(r.Context(), r.PathValue("rid"))
	if !ok {
		http.Error(w, "no such renderer", http.StatusNotFound)
		return
	}
	st, err := d.Status(r.Context())
	if err != nil {
		http.Error(w, castFault(d.Name, "did not answer", err), http.StatusBadGateway)
		return
	}
	writeJSON(w, CastStatus{
		State:    st.State,
		Position: st.Position.Seconds(),
		Duration: st.Duration.Seconds(),
		URI:      st.URI,
	})
}

// handleCastControl drives it: the transport, and the volume where the set
// has one to set.
func (s *Server) handleCastControl(w http.ResponseWriter, r *http.Request) {
	d, ok := s.renderer(r.Context(), r.PathValue("rid"))
	if !ok {
		http.Error(w, "no such renderer", http.StatusNotFound)
		return
	}
	var req CastControl
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var err error
	switch req.Action {
	case "play":
		err = d.Play(ctx)
	case "pause":
		err = d.Pause(ctx)
	case "stop":
		err = d.Stop(ctx)
	case "seek":
		err = d.Seek(ctx, time.Duration(req.Seconds*float64(time.Second)))
	case "volume":
		err = d.SetVolume(ctx, req.Volume)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.log.Debug("cast control failed", "renderer", d.Name, "action", req.Action, "err", err)
		http.Error(w, castFault(d.Name, "did not take the "+req.Action, err), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// castFault is what the viewer is told when a set refuses or goes quiet: a
// sentence naming the set and what it would not do. The raw SOAP fault —
// an error code inside an XML envelope — goes to the log, where somebody
// diagnosing the set can read it, and not onto a screen where it reads as
// the page having broken. A set that answered nothing at all is said to
// have gone quiet, which is the one distinction a viewer can act on.
func castFault(name, what string, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return name + " did not answer in time"
	}
	return name + " " + what
}
