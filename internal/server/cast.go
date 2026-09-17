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
	"slices"
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
	// How long a set stays offerable after the last search that saw it.
	//
	// Discovery is lossy in both directions — a datagram either way, and a
	// description fetch that can time out — and measured against a
	// television that was on and answering throughout, one round in
	// seventy-two came back with nothing. At a search every renderersTTL,
	// five minutes rides out four or five consecutive misses, which at that
	// rate is a chance in millions, while a set genuinely switched off is gone from
	// the menu inside the time it takes to notice it was ever there.
	rendererMemory = 5 * time.Minute
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
	// known is every set this process has been told about lately, with when
	// it was last seen. A search loses things — the M-SEARCH goes out twice
	// per interface precisely because nobody retries a datagram, and a
	// description is a fetch that can time out — so a losing round used to
	// evict a television that was still in the room, and every transport
	// command, status poll and handover for it answered "no such renderer"
	// until the stamp went stale a minute later.
	//
	// **The picker is answered from here too**, which it was not at first:
	// it was shown only what the round just found, on the reasoning that a
	// stale entry in a menu is worse than a missing one. Measured against a
	// television that was on and answering throughout, one search in
	// seventy-two found nothing at all — so the button offering it vanished for
	// the length of the client's own minute of caching, which is what a
	// viewer sees as "no television to cast to" while watching that
	// television across the room. A set that really has been switched off
	// leaves the list after rendererMemory instead, and pressing it before
	// then fails with "did not answer", which is the wording a viewer gets
	// anyway and a far better answer than no button.
	//
	// It grows by the number of distinct renderers on the network, which is
	// a handful, and entries older than rendererMemory are dropped as the
	// list is built.
	known map[string]seenRenderer
	// driving is the request currently walking one set through a play
	// sequence, by renderer id (see castClaim), and gen names them in turn.
	driving map[string]*castDrive
	gen     uint64
	// searching is held for the length of a search so that several clients
	// asking at once wait for one answer rather than filling the network
	// with duplicate M-SEARCHes.
	searching sync.Mutex
	// discover is the search itself. A test stands in for the network here;
	// nil is the network.
	discover func(ctx context.Context, wait time.Duration) []*dlna.Renderer
}

// seenRenderer is a set and when a search last reported it.
type seenRenderer struct {
	r  *dlna.Renderer
	at time.Time
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

	found := s.search(ctx)
	if ctx.Err() != nil {
		// Interrupted is not answered. A request that went away during the
		// search — a page reloaded, a tab closed, a connection dropped —
		// leaves every device description fetching on a dead context, so
		// what comes back is an empty list that says nothing about the
		// network. Published with a fresh stamp it became the minute's
		// authoritative answer: every later caller took the freshness path,
		// an empty map being as non-nil as a full one, and for the whole of
		// the TTL the house had no televisions in it. Nothing is written
		// down, so the next asker searches again.
		return s.castList()
	}
	s.cast.mu.Lock()
	defer s.cast.mu.Unlock()
	s.cast.found = map[string]*dlna.Renderer{}
	s.cast.order = nil
	if s.cast.known == nil {
		s.cast.known = map[string]seenRenderer{}
	}
	now := time.Now()
	for _, r := range found {
		s.cast.found[r.ID] = r
		s.cast.order = append(s.cast.order, r.ID)
		s.cast.known[r.ID] = seenRenderer{r: r, at: now}
	}
	s.cast.at = now
	return s.castListLocked()
}

// search asks the network, or whatever a test has put in its place.
//
// A device that answered and could not then be described is **said out
// loud**. That loss used to be silent — describeAll passed over the error —
// and a set missing from the picker looked exactly like a set that was
// switched off, which is how a fault that shows up about once in seventy
// searches went unexplained: nothing recorded whether the datagram had
// failed to arrive or the description had failed to be fetched, and those
// are different faults with different cures. One line, at the moment it
// happens, is what the next occurrence needs to name itself.
func (s *Server) search(ctx context.Context) []*dlna.Renderer {
	if s.cast.discover != nil {
		return s.cast.discover(ctx, searchWait)
	}
	found, skipped := dlna.DiscoverReport(ctx, searchWait)
	for _, sk := range skipped {
		s.log.Warn("a device answered the search and could not be described",
			"location", sk.Location, "err", sk.Err)
	}
	return found
}

func (s *Server) castList() []*dlna.Renderer {
	s.cast.mu.Lock()
	defer s.cast.mu.Unlock()
	return s.castListLocked()
}

// castListLocked is castList with the lock already held: what the last search
// found, and then anything seen lately that it missed.
//
// The order matters and is not alphabetical: the sets that answered this
// round come first, in the order they answered, so the menu a viewer reads
// is led by what is certainly there. A set the round lost follows, and one
// nothing has heard from for rendererMemory is dropped from the memory as we
// pass — this is the only place that walks it, so it is the only place that
// needs to forget.
func (s *Server) castListLocked() []*dlna.Renderer {
	out := make([]*dlna.Renderer, 0, len(s.cast.order))
	for _, id := range s.cast.order {
		out = append(out, s.cast.found[id])
	}
	cutoff := time.Now().Add(-rendererMemory)
	missed := make([]seenRenderer, 0, len(s.cast.known))
	for id, k := range s.cast.known {
		if k.at.Before(cutoff) {
			delete(s.cast.known, id)
			continue
		}
		if _, answered := s.cast.found[id]; !answered {
			missed = append(missed, k)
		}
	}
	// Newest first among them, so the one most recently heard from leads.
	slices.SortFunc(missed, func(a, b seenRenderer) int { return b.at.Compare(a.at) })
	for _, k := range missed {
		out = append(out, k.r)
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
	// And one search missing a set is not the set being gone: the reply is a
	// datagram and the description a fetch, either of which can be lost
	// while the television goes on playing the film. So an id a client is
	// already holding is answered from everything this process has been told
	// about, rather than from the one round that happened to lose it. A set
	// that really has been switched off then fails the command instead,
	// which is what the viewer is told about anyway ("did not answer") and a
	// far better answer than a film that cannot be paused.
	s.cast.mu.Lock()
	defer s.cast.mu.Unlock()
	k, ok := s.cast.known[id]
	if !ok || k.at.Before(time.Now().Add(-rendererMemory)) {
		return nil, false
	}
	return k.r, true
}

// castDrive is the request currently walking one renderer through a play
// sequence, with the generation that says whether it is still that request.
type castDrive struct {
	gen    uint64
	cancel context.CancelFunc
}

// castClaim hands a set to this request and takes it from whatever was
// driving it.
//
// A cast is four steps — the URI, the confirmation that the set took it,
// play, and the seek — and the last two are unconditional. A second request
// landing in the middle of the first therefore had its film played and
// seeked by the first: the resume point of a film nobody was watching any
// more, applied to the one that had just replaced it.
//
// The set itself cannot be owned — somebody with the remote is assumed
// throughout, and reconciled with by polling — but a superseded request on
// this side can be made to stop talking to it. Its context is cancelled, and
// every SOAP call is built on that context, so it issues nothing further and
// gives up. Nothing queues: the newest request drives the set, which is the
// order the gestures were made in, and waiting behind a slow set's 45-second
// budget would be the worse failure.
func (s *Server) castClaim(parent context.Context, rid string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	s.cast.mu.Lock()
	if s.cast.driving == nil {
		s.cast.driving = map[string]*castDrive{}
	}
	if prev := s.cast.driving[rid]; prev != nil {
		prev.cancel()
	}
	s.cast.gen++
	mine := s.cast.gen
	s.cast.driving[rid] = &castDrive{gen: mine, cancel: cancel}
	s.cast.mu.Unlock()
	return ctx, func() {
		s.cast.mu.Lock()
		// Only while this request is still the one driving: a later claim
		// owns the slot now, and clearing it would leave the next request
		// with nothing to take the set from.
		if d := s.cast.driving[rid]; d != nil && d.gen == mine {
			delete(s.cast.driving, rid)
		}
		s.cast.mu.Unlock()
		cancel()
	}
}

// superseded reports whether a step failed because another request took the
// set rather than because the set refused it. The two look identical from
// the SOAP call and want opposite things said about them.
func superseded(parent, ctx context.Context) bool {
	return ctx.Err() != nil && parent.Err() == nil
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
	// This request drives the set from here to the seek, and takes it from
	// whatever was driving it before (castClaim).
	//
	// **The claim is taken before the preparation, not after it**, and that
	// ordering is the whole of what it is worth. Nearly all the waiting in a
	// cast is above the first SOAP call: the probe, and — where the viewer
	// chose a soundtrack the set cannot be told about — a copy of the whole
	// film, which has a budget of minutes and which the page does not cancel
	// when it changes its mind. Claimed at the first SOAP call, the order
	// the slot was taken in was the order the *copies finished* in and not
	// the order the gestures were made in: a film abandoned in favour of
	// another could finish its copy afterwards, find the slot free, and put
	// itself on the set at its own resume point, minutes after the viewer
	// asked for something else. Claimed here, the newer request's claim
	// cancels the older one's wait, and the copy carries on for whoever asks
	// for it next (a production outlives the request that started it).
	ctx, done := s.castClaim(r.Context(), d.ID)
	defer done()

	// The soundtrack list and the embedded captions come from the probe that
	// runs when a video is opened. A cast *is* an opening — and the indexes
	// the player sends were numbered against the probed listing, so a cast
	// resolved without the probe would count a shorter list and hand the set
	// the wrong subtitle, or none.
	it = s.probed(ctx, it)

	src, mimeType, note, err := s.castSourceNoted(ctx, d, it, r.URL.Query().Get("audio"))
	if err != nil {
		if s.castTaken(ctx, w, r, d, it) {
			return
		}
		castSourceError(w, err)
		return
	}
	// Preparation can also succeed after the set has been taken — a copy
	// that was already on disk, or one that finished in the moment between.
	// Nothing further may be said to the set in that case.
	if s.castTaken(ctx, w, r, d, it) {
		return
	}

	meta := s.castMeta(r, d, it, src, mimeType)
	if err := d.SetURI(ctx, src, meta); err != nil {
		if s.castTaken(ctx, w, r, d, it) {
			return
		}
		// A set that has not answered has not necessarily failed. Measured
		// on a television that had another session open, the reply to this
		// took longer than any budget worth waiting on while the film
		// itself was already on screen — and reporting a failure then lost
		// the seek that should have followed, so the film started from the
		// beginning. What matters is whether it is showing what it was
		// given, so ask it that instead of believing the silence.
		if !showing(ctx, d, src) {
			// The supersession can equally have landed while we were
			// asking — that is six seconds of polling — and a set that
			// answered nothing because we stopped talking to it has
			// refused nothing. Reporting that as "did not accept the
			// file" is the very mis-report the 409 exists to end.
			if s.castTaken(ctx, w, r, d, it) {
				return
			}
			s.log.Warn("cast failed", "renderer", d.Name, "item", it.Name, "err", err)
			http.Error(w, castFault(d.Name, "did not accept the file", err), http.StatusBadGateway)
			return
		}
		s.log.Debug("cast: set was slow to answer but took the file",
			"renderer", d.Name, "item", it.Name, "err", err)
	}
	if err := d.Play(ctx); err != nil {
		if s.castTaken(ctx, w, r, d, it) {
			return
		}
		s.log.Warn("cast failed to start", "renderer", d.Name, "item", it.Name, "err", err)
		http.Error(w, castFault(d.Name, "would not start playing it", err), http.StatusBadGateway)
		return
	}
	// Seeking is asked for after playback starts: a set that has not opened
	// the file yet has nothing to seek in, and answers the request with a
	// fault rather than with the position.
	if t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64); t > 0 {
		if err := d.Seek(ctx, time.Duration(t*float64(time.Second))); err != nil {
			if s.castTaken(ctx, w, r, d, it) {
				return
			}
			s.log.Debug("cast seek refused", "renderer", d.Name, "err", err)
		}
	}
	// And the last word is the same question: every step above can succeed
	// and still be overtaken, in which case this answer describes a film
	// that is no longer the one on the set.
	if s.castTaken(ctx, w, r, d, it) {
		return
	}
	s.log.Info("casting", "renderer", d.Name, "item", it.Name, "type", mimeType)
	writeJSON(w, CastStatus{State: "TRANSITIONING", URI: src, Note: note})
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
	src, mimeType, note, err := s.castSourceNoted(r.Context(), d, it, r.URL.Query().Get("audio"))
	if err != nil {
		castSourceError(w, err)
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
	writeJSON(w, CastStatus{State: "QUEUED", URI: src, Note: note})
}

// castSource decides what URL to hand over and what to call it.
//
// The set says which containers it accepts, and where ours is not among them
// there is one thing worth trying before giving up: the rewrap, which is a
// real file with a length and ranges — exactly what a renderer wants, and
// unlike the segmented conversion something it can seek in. A live
// conversion is not offered at all: it answers no ranges and has no length,
// and a renderer given one either refuses it or plays it once from the top.
// castSourceNoted is castSource with the one thing the viewer has to be told
// when it does not work out: whether the soundtrack they chose is actually
// in what the set was handed. A set plays what is in the file and has no
// menu to change it, so a choice that could not be copied out is a film
// speaking the wrong language with nothing on screen to explain it.
func (s *Server) castSourceNoted(ctx context.Context, d *dlna.Renderer, it library.Item, audio string) (src, mimeType, note string, err error) {
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
	kind, wantCopy := castTrackKind(it, audio)
	if !wantCopy {
		// No choice to honour, but the soundtrack may still be one no
		// television decodes — and that failure is silent, so it is worth
		// the copy rather than a film playing mutely with nothing to say
		// why.
		kind, wantCopy = castSoundKind(it, d.Accepts)
	}
	if wantCopy {
		// Making the copy is a read of the whole film at disk speed with a
		// television waiting on it, which is playback by every measure the
		// priority order recognises — so thumbnails and tag reading stand
		// down for it. Nothing else marks it here: the production is
		// detached from any request, and a cast is the one route where no
		// delivery is in flight meanwhile to hold the gate instead.
		//
		// Deferred inside a closure rather than released in a line of its
		// own: a panic anywhere under remux.File is recovered by net/http
		// and the handler simply ends, and a stream count that is never
		// given back reads as "something is playing" for the life of the
		// process — thumbnails collapsed to one job, enrichment paused and
		// every hover sheet refused, for ever. Every other site marking the
		// gate defers; this is not the one to be clever in.
		err := func() error {
			defer s.lib.StartStream()()
			_, err := s.remux.File(ctx, it, audio, kind)
			return err
		}()
		if err == nil {
			path = "remux/" + url.PathEscape(it.ID) + "?a=" + strconv.Itoa(audioTrack(audio)) + remuxQuery(kind)
			mimeType = remuxMime(it, kind)
		} else {
			// Nothing is lost but the choice: the file itself still plays,
			// with whichever soundtrack the set picks out of it. The
			// commonest reason is size — a copy has to fit in the scratch
			// space, and a 25 GB release does not fit in 16 GB — and it is
			// worth saying out loud, since what the viewer sees otherwise is
			// a film in the wrong language and a menu insisting otherwise.
			note = "The television plays this film's own soundtrack: the one you chose cannot be copied out"
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
			return "", "", note, errCannotPlay{name: d.Name, typ: mimeType}
		}
	}
	base := s.localBase(d)
	if base == "" {
		return "", "", note, errNoAddress{}
	}
	// The query, where there is one, goes after the path the token covers.
	head, query, _ := strings.Cut(path, "?")
	if query != "" {
		query = "?" + query
	}
	return s.mediaURL(base, head) + query, mimeType, note, nil
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

// castSourceError answers a castSource failure: a set that cannot play the
// container is the caller's problem (422), no local address is this
// server's (503).
func castSourceError(w http.ResponseWriter, err error) {
	var noAddr errNoAddress
	if errors.As(err, &noAddr) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, err.Error(), http.StatusUnprocessableEntity)
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

// noReceiverAudio is the soundtracks a television is not to be handed
// without converting them: the cinema formats, which carry a licence fee per
// decoder and which set makers have been dropping rather than paying. The
// list is the browser's own (NO_BROWSER_AUDIO in playback.ts) — codecs with
// no decoder in any browser — and it holds for a set for the same reason.
//
// **Why this is decided from the codec and not from the set**: a renderer
// says which *containers* it takes and nothing about what is inside them.
// Probed on a television here, 43 of its 69 sink entries carry a DLNA
// profile naming an exact codec combination — and every one of those is a
// legacy profile it will never be sent, while `video/x-matroska`, which is
// what this server actually hands over, is listed as a bare `*`. The set is
// saying "send me any Matroska and I will try", which is the truth and is no
// help at all. Its audio-only sinks are the one hint it gives, and they are
// for playing a music file rather than a film — so they are taken as an
// exception rather than as the rule: a set that does list the codec is
// handed the film untouched.
//
// The asymmetry with the picture is deliberate. A set that cannot decode the
// video fails **visibly** — a black screen, and the viewer knows to do
// something else — which is why castAliases leaves that judgement to the
// set. A soundtrack it cannot decode fails **silently**: the film plays, and
// there is no error, no message and no menu on the television to put it
// right. That is the same argument the player already makes for the browser,
// where a codec it cannot decode "produces no error, only silence".
var noReceiverAudio = map[string]bool{"dts": true, "dca": true, "truehd": true, "mlp": true}

// castSoundKind says whether this film's soundtrack has to be converted
// before a set is given it, and answers the kind of copy that does it.
//
// dtsSinks are the names a set that really does decode DTS lists among its
// audio sinks; where it says so, nothing is converted.
var dtsSinks = []string{"audio/vnd.dts", "audio/vnd.dts.hd", "audio/x-dts"}

func castSoundKind(it library.Item, accepts func(string) bool) (remuxKind, bool) {
	if !noReceiverAudio[strings.ToLower(it.ACodec)] {
		return remuxCopy, false
	}
	for _, name := range dtsSinks {
		if accepts(name) {
			return remuxCopy, false // it says it can; take it at its word
		}
	}
	// The picture is copied through and only the sound is re-encoded, so the
	// question is whether this film can take a sound fix at all — the same
	// question the player asks before it orders one, and the same answer.
	// Pointedly not `remuxable`, which asks about the soundtrack too and
	// would refuse every film this exists for, the soundtrack being exactly
	// what is wrong with them.
	if !soundFixable(it) {
		return remuxCopy, false
	}
	return remuxSound, true
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

// remuxQuery asks the endpoint for the same kind of copy again — and says
// that a television is what will fetch it.
//
// `tv=1` turns off one refusal and nothing else. `handleRemux` answers 404
// where the picture reorders further than it declares, which is the honest
// answer to a *browser*: copying would not help it, and the player acts on
// that by converting instead. A set is not a browser. It fetches the file
// and decodes it with the generosity VLC has, so the copy plays there
// perfectly — and the 404 reached it as "716 Resource not found", a cast
// that simply failed with the film sitting ready on disk. The rule was
// already documented as not being a television's ("re-encoding a film for a
// player that was never going to drop a frame would be paying the whole
// cost for nothing"); what was missing was any way for the handler to know
// which of the two had come knocking.
//
// It authorises nothing — the same posture as the internal-read marker. A
// browser that sent it would be handed a copy that may stutter on its own
// screen, which is a self-inflicted wound and not a way past anything.
func remuxQuery(kind remuxKind) string {
	q := "&tv=1"
	if kind == remuxTrack {
		q += "&mode=track"
	}
	return q
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

// castTaken reports whether another request has taken the set out from under
// this one, and tells this one so when it has. Every step of a cast asks it,
// which is the point: a superseded request must say nothing further to the
// set, and must not report the silence that follows as a fault of the set's.
// It is not one — the film the viewer actually asked for last is the one now
// playing, and the page's own generation guard discards this answer anyway.
func (s *Server) castTaken(ctx context.Context, w http.ResponseWriter, r *http.Request, d *dlna.Renderer, it library.Item) bool {
	if !superseded(r.Context(), ctx) {
		return false
	}
	s.log.Debug("cast superseded by a later request", "renderer", d.Name, "item", it.Name)
	http.Error(w, "another request took "+d.Name, http.StatusConflict)
	return true
}
