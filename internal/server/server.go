// Package server exposes the library over HTTP: JSON APIs, range-request media
// streaming, thumbnails, server-sent events and the embedded frontend.
package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/state"
)

func init() {
	// Types the stdlib mime table tends to miss — or gets wrong. `.webm` is
	// the second kind and is worth the note: Go's *built-in* table calls it
	// `audio/webm`, which is what a WebM holding only sound is, and not what
	// one arriving from a video site is. The system's own tables say
	// `video/webm` and are overruled, so this has to be stated here or a
	// television is told a film is a soundtrack and declines it by name.
	for ext, typ := range map[string]string{
		".webm": "video/webm",
		".mkv":  "video/x-matroska", ".m4v": "video/mp4", ".mov": "video/quicktime",
		".avi": "video/x-msvideo", ".flac": "audio/flac", ".m4a": "audio/mp4",
		".opus": "audio/ogg", ".oga": "audio/ogg", ".m3u": "audio/x-mpegurl",
		".m3u8": "audio/x-mpegurl", ".avif": "image/avif",
		// A DVD title is an MPEG-2 program stream, whatever it is called.
		".vob": "video/mpeg",
		".ts":  "video/mp2t", ".mts": "video/mp2t", ".m2ts": "video/mp2t",
		".divx": "video/x-msvideo", ".f4v": "video/mp4", ".ogv": "video/ogg",
		".rm": "application/vnd.rn-realmedia", ".rmvb": "application/vnd.rn-realmedia",
		".jfif": "image/jpeg",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

// Server wires the library, state store and thumbnailer into an http.Handler.
type Server struct {
	lib    *library.Library
	st     *state.Store
	thumbs *Thumbnailer
	remux  *Remuxer
	hls    *HLS
	log    *slog.Logger
	// sign mints and checks the links that carry their own permission, for
	// the things that fetch media without a browser behind them (sign.go).
	sign *signer
	// Applying a change of scanned directories belongs to main, which is the
	// only place holding the watcher and the scanner together. nil leaves the
	// set fixed for the run.
	setRoots     SetRootsFunc
	prefsPersist bool
	access       bool
	mux          *http.ServeMux
	transSem     chan struct{} // caps concurrent live transcodes
	// Casting to a television that is not a browser (cast.go): what has
	// been found on the network, and the port a set can fetch from us on.
	cast casting
	port int
	// links mints and resolves the short names for somewhere in the app.
	links *links
	// reorder remembers which pictures cannot be handed to a browser by
	// copying, however playable their codec is (reorder.go).
	reorder reorderCache
	// embsubs caches the subtitles extracted from inside video files
	// (embsubs.go): the read is the whole container, and the player asks
	// again with a new ?shift= on every seek.
	embsubs embSubs
}

// New builds the server. dist is the built frontend to serve at /.
func New(lib *library.Library, st *state.Store, thumbs *Thumbnailer, remux *Remuxer, hls *HLS, keys KeyStore, dist fs.FS, log *slog.Logger) *Server {
	s := &Server{
		lib: lib, st: st, thumbs: thumbs, remux: remux, hls: hls, log: log,
		sign: newSigner(keys, log),
		mux:  http.NewServeMux(), transSem: make(chan struct{}, 2),
	}
	if ls, ok := keys.(LinkStore); ok {
		s.links = newLinks(ls, log)
	} else {
		s.links = newLinks(nil, log)
	}
	s.mux.HandleFunc("GET /api/info", s.handleInfo)
	s.mux.HandleFunc("GET /api/library", s.handleList)
	s.mux.HandleFunc("GET /api/playlist.m3u", s.handlePlaylist)
	s.mux.HandleFunc("GET /api/albums", s.handleAlbums)
	s.mux.HandleFunc("GET /api/albums/{id}", s.handleAlbum)
	s.mux.HandleFunc("GET /api/albums/{id}/zip", s.handleAlbumZip)
	s.mux.HandleFunc("GET /api/tracks", s.handleTracks)
	s.mux.HandleFunc("GET /api/artists", s.handleArtists)
	s.mux.HandleFunc("GET /api/genres", s.handleGenres)
	s.mux.HandleFunc("GET /api/series", s.handleSeries)
	s.mux.HandleFunc("GET /api/stream/{id}", s.handleStream)
	s.mux.HandleFunc("GET /api/signed/{token}/{rest...}", s.handleSigned)
	s.mux.HandleFunc("GET /api/remux/{id}", s.handleRemux)
	s.mux.HandleFunc("GET /api/transcode/{id}", s.handleTranscode)
	s.mux.HandleFunc("GET /api/convert/{id}", s.handleConvertProgress)
	s.mux.HandleFunc("GET /api/hls/{id}/index.m3u8", s.handleHLSStart)
	s.mux.HandleFunc("GET /api/hls/{id}/{sid}/{file}", s.handleHLSFile)
	s.mux.HandleFunc("GET /api/keyframe/{id}", s.handleKeyframe)
	s.mux.HandleFunc("GET /api/crop/{id}", s.handleCrop)
	s.mux.HandleFunc("GET /api/item/{id}", s.handleItem)
	s.mux.HandleFunc("GET /api/subs/{id}", s.handleSubs)
	s.mux.HandleFunc("GET /api/subs/{id}/{index}", s.handleSubFile)
	s.mux.HandleFunc("GET /api/thumb/{id}", s.handleThumb)
	s.mux.HandleFunc("GET /api/sprite/{id}", s.handleSprite)
	s.mux.HandleFunc("PUT /api/flags", s.handleFlagsBatch)
	s.mux.HandleFunc("PUT /api/flags/{id}", s.handleFlagsPut)
	s.mux.HandleFunc("GET /api/state", s.handleStateAll)
	s.mux.HandleFunc("GET /api/state/{id}", s.handleStateGet)
	s.mux.HandleFunc("PUT /api/state/{id}", s.handleStatePut)
	s.mux.HandleFunc("POST /api/plays/{id}", s.handlePlay)
	s.mux.HandleFunc("POST /api/like/{id}", s.handleLike)
	s.mux.HandleFunc("DELETE /api/state/{id}", s.handleStateDelete)
	s.mux.HandleFunc("GET /api/prefs", s.handlePrefs)
	s.mux.HandleFunc("PUT /api/prefs", s.handlePrefsPut)
	s.mux.HandleFunc("GET /api/renderers", s.handleRenderers)
	s.mux.HandleFunc("GET /api/renderers/{rid}", s.handleCastStatus)
	s.mux.HandleFunc("POST /api/renderers/{rid}/play/{id}", s.handleCast)
	s.mux.HandleFunc("POST /api/renderers/{rid}/next/{id}", s.handleCastNext)
	s.mux.HandleFunc("POST /api/renderers/{rid}/control", s.handleCastControl)
	s.mux.HandleFunc("POST /api/links", s.handleLinkCreate)
	s.mux.HandleFunc("GET /s/{code}", s.handleLink)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.Handle("/", spaHandler(dist))
	return s
}

// AllowRootChanges lets the preferences endpoint change what is indexed.
// persisted says whether the change survives a restart, which the dialog
// reports rather than letting a setting look permanent when it is not.
func (s *Server) AllowRootChanges(fn SetRootsFunc, persisted bool) {
	s.setRoots = fn
	s.prefsPersist = persisted
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler {
	if s.access {
		return logged(s.mux, s.log)
	}
	return s.mux
}

// LogRequests records every request and what it was answered with, at debug
// level. Off by default — it is a line per request and playback makes
// hundreds — and the way to see what a player that will not play is actually
// doing, which on a phone there is no other way to find out.
func (s *Server) LogRequests() { s.access = true }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Default to no-store, but leave a handler-set policy alone — the
	// album/artist endpoints use no-cache + ETag so browsers revalidate
	// instead of re-downloading.
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	_ = json.NewEncoder(w).Encode(v)
}

// listQuery reads the selection every listing shares. The m3u export takes
// the same parameters, so an exported playlist is what the grid is showing
// and not a second interpretation of the same words.
func listQuery(q url.Values, c content, paths library.PathFilter) library.Query {
	// The shuffle seed belongs to the client and must stay put for the whole
	// query: a fresh seed per page would deal a new hand each time.
	seed, _ := strconv.ParseInt(q.Get("seed"), 10, 64)
	return library.Query{
		Kind:   library.Kind(q.Get("kind")),
		Watch:  watchFilter(q.Get("watch")),
		Played: q.Get("played") == "1",
		Series: q.Get("series"),
		Season: seasonNumber(q.Get("season")),
		// What this caller may draw from at all. A kind it asked for but is
		// not allowed simply matches nothing, which is the same answer as an
		// empty library and needs no special case.
		Kinds:          c.kinds(),
		Paths:          paths,
		Search:         q.Get("q"),
		Sort:           q.Get("sort"),
		Seed:           seed,
		Desc:           q.Get("order") != "asc",
		ShowHidden:     q.Get("hidden"),
		FavouritesOnly: q.Get("fav") == "1",
	}
}

// handleInfo answers what the client needs before it asks for anything else.
// Never cached: the epoch's whole job is to be noticed when it changes.
// FindHardware looks for a video engine now rather than when the first film
// needs one. It is a five-frame encode, and doing it at startup means the
// answer is on screen before anybody asks and no viewer waits for it in the
// middle of a conversion.
func (s *Server) FindHardware() {
	hw.find(s.thumbs.FFmpegPath(), s.log)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	token, expires, _ := s.sign.mint(time.Now())
	writeJSON(w, InfoResponse{
		ThumbEpoch:    s.thumbs.StoreEpoch(),
		Content:       contentOf(r).names(),
		StreamToken:   token,
		StreamExpires: expires,
		Confined:      pathsOf(r).Restricted(),
		Build:         buildOf(),
		Capabilities:  s.capabilities(),
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	c := contentOf(r)
	lq := listQuery(q, c, pathsOf(r))
	lq.Offset, lq.Limit = offset, limit
	res := s.lib.List(lq)
	// The chips count what this caller may see, not what the library holds.
	res.Counts = c.mask(res.Counts)
	if res.Matching != nil {
		m := c.mask(*res.Matching)
		res.Matching = &m
	}
	// This page is what the grid is about to show, so its metadata is worth
	// reading before the rest of the library's. Not worth waiting for.
	s.lib.EnrichSoon(itemIDs(res.Items))
	writeJSON(w, res)
}

const (
	// maxPlaylistEntries caps an export: a playlist is a selection handed to
	// a player, not a second copy of the index, and players do not enjoy
	// tens of thousands of lines.
	maxPlaylistEntries = 5000
	// listPage is the largest page List will serve.
	listPage = 500
)

// collect pages through List for up to max items. The paging costs nothing
// beyond the copies: the library caches the filtered, sorted result per
// (query, version), so only the first call does the work.
func (s *Server) collect(q library.Query, max int) []library.Item {
	var out []library.Item
	for len(out) < max {
		q.Offset, q.Limit = len(out), min(listPage, max-len(out))
		res := s.lib.List(q)
		out = append(out, res.Items...)
		if len(res.Items) == 0 || len(out) >= res.Total {
			break
		}
	}
	return out
}

// handlePlaylist exports the current selection as an m3u, taking the same
// query parameters as /api/library. The entries are stream URLs by item ID,
// absolute because the file is played outside the browser, where there is no
// page for a relative path to resolve against.
func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	items := s.collect(listQuery(r.URL.Query(), contentOf(r), pathsOf(r)), maxPlaylistEntries)
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="playlist.m3u"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, buildM3U(items, requestBase(r)))
}

// requestBase reconstructs the origin the client reached us on, so the
// exported links work from wherever that was — a LAN address, a host name,
// or a reverse proxy terminating TLS in front of us.
func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// A header is whatever the proxy — or a client pretending to be one —
	// chose to send, so only the two values that mean anything are taken.
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	// The host the visitor asked for, not the upstream address a proxy
	// rewrote it to: the same reading the shortlinks are scoped by, since an
	// exported playlist behind nginx without proxy_set_header Host named the
	// backend and played nowhere.
	return scheme + "://" + linkHost(r)
}

// buildM3U renders the export. #EXTINF carries whole seconds, -1 when the
// length is unknown, and the title runs to the end of the line — so anything
// in a tag that would start a new one is flattened first, or a stray line
// would be read back as another entry.
func buildM3U(items []library.Item, base string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, it := range items {
		secs := -1
		if it.Duration > 0 {
			secs = int(it.Duration / 1000)
		}
		fmt.Fprintf(&b, "#EXTINF:%d,%s\n%s/api/stream/%s\n",
			secs, m3uTitle(it), base, url.PathEscape(it.ID))
	}
	return b.String()
}

// m3uTitle is what the player shows for an entry: the tagged title with its
// artist in front, falling back to the file name for everything untagged.
func m3uTitle(it library.Item) string {
	title := it.Title
	if title == "" {
		title = it.Name
	}
	if it.Artist != "" {
		title = it.Artist + " - " + title
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, title))
}

// priorityWait bounds how long a request will wait for metadata it asked to
// be read first. Anything unfinished continues in the background and
// reaches the page over the change stream.
const priorityWait = 1500 * time.Millisecond

func itemIDs(items []library.Item) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

// handleItem returns one item, reading its metadata first if that has not
// happened yet. The player asks for this when it opens something, so that
// it knows the duration and which codecs the file actually holds.
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.item(r, id); !ok {
		http.NotFound(w, r)
		return
	}
	// One budget for the three looks that follow — the tags, the codecs and
	// the frame order — and the request's own context: the player asks for
	// this three lines after playback starts, so an unbounded look here
	// competes with the stream it is meant to serve, and three budgets in
	// series on a slow disk were three times the wait before the player
	// learnt anything. Running out of time is not fatal — the item answers
	// with what was learnt, and the next open looks again.
	ctx, cancel := context.WithTimeout(r.Context(), priorityWait)
	defer cancel()
	s.lib.EnrichNow(ctx, []string{id})
	// Opening is the moment codecs matter (can the browser play this?);
	// eager enrichment skips that probe for the whole library's sake.
	s.lib.EnsureCodecs(ctx, id)
	it, ok := s.item(r, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The one fault native playback cannot see for itself: a stream that
	// reorders its frames further than it declares plays correctly only
	// where something re-encodes it, and a browser handed the file trusts
	// the declaration exactly as it trusts a copy of it (reorder.go). The
	// look is once per film per process, like the probe above; running out
	// of time is not a verdict and is not cached.
	it.Reencode = s.mustReencode(ctx, it)
	writeJSON(w, it)
}

// probed is a video with its open-time probe done: the codecs, the
// soundtracks and the embedded captions come from it, and every handler
// that serves an opening asks the same way. The probe honours the request's
// context and runs at most once per film per process (EnsureCodecs), so an
// item may come back exactly as it went in. Anything but a video is handed
// back untouched.
func (s *Server) probed(ctx context.Context, it library.Item) library.Item {
	if it.Kind != library.KindVideo {
		return it
	}
	s.lib.EnsureCodecs(ctx, it.ID)
	if fresh, ok := s.lib.Get(it.ID); ok {
		return fresh
	}
	return it
}

// versionTag lets the browser revalidate the album/artist lists for free:
// their bodies depend only on the library version and the request URL, so
// an unchanged version means an unchanged body and a 304 replaces shipping
// hundreds of kilobytes of JSON that the client already has.
func (s *Server) versionTag(w http.ResponseWriter, r *http.Request) (int64, bool) {
	version := s.lib.Version()
	// The restriction is part of the tag, and the tag is what a browser
	// revalidates against. Two faces of one library are at the same version
	// and hold different answers, so a version alone would let a client that
	// changed faces — or a cache in between serving both — keep the answer
	// it had. Hashed rather than spelled out: a header can be long, and an
	// ETag is echoed back on every request.
	tag := fmt.Sprintf(`W/"v%d-%s"`, version, faceTag(r))
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-cache")
	// And said out loud, so a shared cache keeps the answers apart rather
	// than handing one face's to another.
	w.Header().Set("Vary", ContentHeader+", "+PathsHeader)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return version, true
	}
	return version, false
}

// faceTag is a short, stable hash of the restrictions a request arrived
// with — which face it is, and which part of the library it may see.
func faceTag(r *http.Request) string {
	sum := sha1.Sum([]byte(strings.Join(contentOf(r).names(), ",") + "\x00" + pathsOf(r).Key()))
	return hex.EncodeToString(sum[:])[:8]
}

// seasonNumber reads a season from a query parameter. Anything that is not a
// plain small number is no season at all, which shows the whole series.
func seasonNumber(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 99 {
		return 0
	}
	return n
}

// audioTrack reads a requested soundtrack. Anything that is not a plain,
// small number is the first one, which is what a file with a single
// soundtrack has anyway.
func audioTrack(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxAudioTracks {
		return 0
	}
	return n
}

// audioMap turns one into an ffmpeg stream specifier.
func audioMap(v string) string {
	return audioMapN(audioTrack(v))
}

// audioMapN is the same specifier for a track already parsed.
func audioMapN(n int) string {
	return fmt.Sprintf("0:a:%d?", n)
}

// maxAudioTracks bounds what a caller may ask for. Films ship a handful of
// soundtracks; this is only here so a made-up number cannot become a made-up
// ffmpeg argument.
const maxAudioTracks = 63

// watchFilter reads the watch parameter, admitting only what the listing
// knows how to answer — an unknown value narrows nothing rather than
// narrowing to nothing.
func watchFilter(v string) string {
	switch v {
	case "started", "done":
		return v
	}
	return ""
}

// matchingCounts answers what the chips should show for a search made from
// one of the grouped views, which fetch no items and so get no counts with
// their results. Nil while nothing is being searched for, where the totals
// the client already has are the same numbers.
func matchingCounts(lib *library.Library, q url.Values, c content, paths library.PathFilter) *library.Counts {
	// No watch narrowing, deliberately: each chip says what clicking it
	// would show, and a chip click leaves the watch views — the counting
	// pass never filtered on it either, so a Watch here only fragmented the
	// cache.
	cq := library.CountQuery{
		Search: q.Get("q"), Artist: q.Get("artist"), Genre: q.Get("genre"),
		Paths: paths, Kinds: c.kinds(),
	}
	if cq == (library.CountQuery{}) {
		return nil // nothing narrowed: the totals the client has are these
	}
	m := c.mask(lib.CountsFor(cq))
	return &m
}

// groupedView is what every grouped view starts from: the version for the
// response and its tag, the face, the query and the caller's paths — the
// four handlers used to spell these out apiece. done says the browser's copy
// was still good and a 304 has already been answered.
type groupedView struct {
	version int64
	q       url.Values
	c       content
	paths   library.PathFilter
}

func (s *Server) groupedView(w http.ResponseWriter, r *http.Request) (groupedView, bool) {
	version, done := s.versionTag(w, r)
	if done {
		return groupedView{}, true
	}
	return groupedView{version: version, q: r.URL.Query(), c: contentOf(r), paths: pathsOf(r)}, false
}

// matching is what the chips show over the ordinary listing.
func (v groupedView) matching(lib *library.Library) *library.Counts {
	return matchingCounts(lib, v.q, v.c, v.paths)
}

// listOrNear is a grouped view's two answers under one rule. A face that
// has no such view gets an empty list — empty and not nil, since a nil slice
// marshals to null and the client's source counts what it is given. Asked
// for what sounds like one thing (near), the listing is the resemblance
// order and the chips count what is in front of the viewer, the library's
// own totals saying nothing about it; otherwise the ordinary listing under
// the ordinary chips.
func listOrNear[T any](shown bool, near string, similar, ordinary func() []*T,
	counts func([]*T) *library.Counts, matching *library.Counts) ([]*T, *library.Counts) {
	if !shown {
		return []*T{}, matching
	}
	var list []*T
	if near != "" {
		list = similar()
		matching = counts(list)
	} else {
		list = ordinary()
	}
	if list == nil {
		list = []*T{}
	}
	return list, matching
}

// handleAlbums lists the releases. Albums, artists and genres are music: a
// caller that is not shown music is not shown the collections it is grouped
// into either.
func (s *Server) handleAlbums(w http.ResponseWriter, r *http.Request) {
	v, done := s.groupedView(w, r)
	if done {
		return
	}
	albums, matching := listOrNear(v.c.music, v.q.Get("near"),
		func() []*library.Album { return s.lib.SimilarAlbums(v.q.Get("near"), albumQuery(v.q, v.paths)) },
		func() []*library.Album { return s.lib.SearchAlbums(albumQuery(v.q, v.paths)) },
		countsOfAlbums, v.matching(s.lib))
	writeJSON(w, AlbumsResponse{Albums: albums, Version: v.version, Matching: matching})
}

// handleSeries lists the shows. Television is video, so a face that shows no
// video gets an empty list — the mirror of albums being music's own.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	v, done := s.groupedView(w, r)
	if done {
		return
	}
	series, matching := listOrNear(v.c.allows(library.KindVideo), "", nil,
		func() []*library.Series {
			return s.lib.SearchSeries(v.q.Get("q"), v.q.Get("sort"), v.q.Get("order") != "asc", v.paths)
		},
		nil, v.matching(s.lib))
	writeJSON(w, SeriesResponse{Series: series, Version: v.version, Matching: matching})
}

// handleGenres lists the genres, which are grouped from albums exactly as
// artists are — and are music's own.
func (s *Server) handleGenres(w http.ResponseWriter, r *http.Request) {
	v, done := s.groupedView(w, r)
	if done {
		return
	}
	genres, matching := listOrNear(v.c.music, "", nil,
		func() []*library.Genre {
			return s.lib.SearchGenres(v.q.Get("q"), v.q.Get("sort"), v.q.Get("order") != "asc", v.paths)
		},
		nil, v.matching(s.lib))
	writeJSON(w, GenresResponse{Genres: genres, Version: v.version, Matching: matching})
}

func (s *Server) handleArtists(w http.ResponseWriter, r *http.Request) {
	v, done := s.groupedView(w, r)
	if done {
		return
	}
	desc := v.q.Get("order") != "asc"
	artists, matching := listOrNear(v.c.music, v.q.Get("near"),
		func() []*library.Artist { return s.lib.SimilarArtists(v.q.Get("near"), v.q.Get("q"), desc, v.paths) },
		func() []*library.Artist { return s.lib.SearchArtists(v.q.Get("q"), v.q.Get("sort"), desc, v.paths) },
		countsOfArtists, v.matching(s.lib))
	writeJSON(w, ArtistsResponse{Artists: artists, Version: v.version, Matching: matching})
}

func (s *Server) handleAlbum(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	album, tracks, ok := s.albumFor(r, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The user is looking at this album now, so read its tags ahead of the
	// background sweep and answer with them rather than with filenames.
	// Track order, album name, artist and total time all derive from the
	// tags, so the album itself is re-read afterwards.
	ctx, cancel := context.WithTimeout(r.Context(), priorityWait)
	defer cancel()
	if s.lib.EnrichNow(ctx, itemIDs(tracks)) {
		if a, t, ok := s.albumFor(r, id); ok {
			album, tracks = a, t
		}
	}
	writeJSON(w, AlbumDetailResponse{Album: album, Tracks: tracks})
}

// albumFor resolves a release for this caller: none on a face without music,
// and only the tracks under the caller's paths — a release is kept for one
// allowed track (AllowedAlbums), and its others may lie outside. A release
// with no track left is no release to this caller, exactly as an item is
// nothing to a caller that may not see it (Server.item).
func (s *Server) albumFor(r *http.Request, id string) (*library.Album, []library.Item, bool) {
	album, tracks, ok := s.lib.AlbumByID(id)
	if !ok || !contentOf(r).music {
		return nil, nil, false
	}
	if paths := pathsOf(r); paths.Restricted() {
		kept := tracks[:0:0]
		for _, it := range tracks {
			if paths.AllowsItem(it) {
				kept = append(kept, it)
			}
		}
		if len(kept) == 0 {
			return nil, nil, false
		}
		tracks = kept
	}
	return album, tracks, true
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	s.serveStream(w, r, r.PathValue("id"))
}

// serveStream is the streaming itself, shared with the signed path.
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, id string) {
	it, ok := s.item(r, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := library.OpenItem(it)
	if err != nil {
		s.log.Warn("stream open failed", "path", it.Rel, "err", err)
		// Known and unreadable is not the same answer as unknown, and the
		// player asks exactly this question when a film will not start: a
		// 404 is "no such item", while this is a disk that has stopped
		// answering for a file the index still lists. Said as 503 — the
		// mount may well come back — so the player can say what happened
		// rather than send the file through conversions that all fail at
		// this same open and then blame the format.
		http.Error(w, "file unavailable: "+openFault(err), http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	if r.Header.Get(library.InternalHeader) == library.InternalToken() {
		// This process reading its own content: the thumbnailer and the
		// metadata probe point ffmpeg and ffprobe at this endpoint because
		// it is the only seekable view of an archived member. Counting that
		// as playback would make thumbnail generation throttle itself
		// against its own fetch and pause tag enrichment for as long as
		// tiles are being made.
		//
		// The marker is a per-process random token, so it cannot be guessed;
		// and it grants nothing. All it does is skip the flag above and
		// accept the ceiling below, which is a restriction, not a privilege
		// — the same bytes are served either way.
		// ... except where the reader has said it needs the whole member. A
		// conversion of a two-hour film reads every byte of it, and the
		// ceiling turned that into a truncated file that opens and stops.
		if r.Header.Get(library.InternalWholeHeader) != library.InternalToken() {
			w = &cappedWriter{ResponseWriter: w, limit: internalStreamCap}
		}
	} else {
		// Mark playback active for the whole response: thumbnailing and tag
		// enrichment throttle themselves while media is being served.
		defer s.lib.StartStream()()
	}
	if r.URL.Query().Get("dl") == "1" {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", it.Name))
		// A DVD title's clock restarts at every join, which a player takes
		// for a file five minutes long that cannot be seeked. Straighten it
		// on the way out — the file is the same length and every range
		// still answers, so nothing else about this response changes.
		//
		// Only for the download, deliberately. What this server plays it
		// seeks by byte position from the disc's own table and times from
		// the IFO, so it needs none of this; and the correction is a
		// per-sector rewrite that nothing in that path should have to
		// depend on. A file handed to some other player has neither.
		f = library.FixTimestamps(it, f)
	}
	// ServeContent handles Range requests, If-Modified-Since and content type
	// — and swallows whatever went wrong if the copy stops early, which for a
	// transfer that dies half way through is the one thing worth knowing.
	// A reader that remembers its last error tells the two cases apart: our
	// read failed, or the far end went away.
	rr := &recordingReader{File: f}
	http.ServeContent(w, r, it.Name, time.UnixMilli(it.ModTime), rr)
	if err := rr.err; err != nil && !errors.Is(err, io.EOF) && r.Context().Err() == nil {
		s.log.Warn("stream read failed part way through",
			"path", it.Rel, "read", rr.n, "size", it.Size, "err", err)
	}
}

// openFault says, in the viewer's words, why a file the index lists could
// not be opened. The classes are the ones a disk actually produces: gone
// (moved or deleted since the walk), not ours to read, a disk that has
// stopped answering, and — the one a remount after a crash leaves behind —
// a directory whose on-disk structure is damaged, which XFS reports as
// "structure needs cleaning" and answers with until it is repaired. Anything
// else is said as it came, without the path, which the viewer already has.
func openFault(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "it is no longer where the library found it"
	case errors.Is(err, fs.ErrPermission):
		return "the server is not allowed to read it"
	case errors.Is(err, syscall.EIO):
		return "the disk it is on is not answering"
	case errors.Is(err, syscall.EUCLEAN):
		return "the filesystem it is on is damaged and needs repair"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// recordingReader keeps the last error a read produced, which ServeContent
// does not report: a transfer that stops half way is either our read failing
// or the far end letting go, and those want different fixes.
type recordingReader struct {
	library.File
	n   int64
	err error
}

func (r *recordingReader) Read(p []byte) (int, error) {
	n, err := r.File.Read(p)
	r.n += int64(n)
	if err != nil {
		r.err = err
	}
	return n, err
}

func (r *recordingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.File.ReadAt(p, off)
	r.n += int64(n)
	if err != nil {
		r.err = err
	}
	return n, err
}

// internalStreamCap bounds what ONE internal response may hand back. It is a
// per-response ceiling and nothing accumulates across them, so it does not by
// itself stop a runaway reader working through a member range by range —
// what bounds the total is the item's own time budget (archiveThumbTimeout,
// and ffprobe's shorter one), plus -rw_timeout on the ffmpeg side. What the
// ceiling does catch is the single pathological response: measured, a whole
// frame extraction reads 9.3-28.7 MiB spread over about six ranges, so one
// response wanting more than this is not doing the job it was started for.
// A var so tests can shrink it.
var internalStreamCap int64 = 64 << 20

// errStreamCapped stops ServeContent's copy once the ceiling is reached. It
// never reaches the client — the response is already in flight — which is
// exactly right: an internal reader that hits this has read more than the
// work needed and is being cut off, not told about it.
var errStreamCapped = errors.New("internal stream ceiling reached")

// cappedWriter is an http.ResponseWriter that stops writing after limit
// bytes of body.
type cappedWriter struct {
	http.ResponseWriter
	n     int64
	limit int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	room := c.limit - c.n
	if room <= 0 {
		return 0, errStreamCapped
	}
	if int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	if err == nil && c.n >= c.limit {
		err = errStreamCapped
	}
	return n, err
}

// handleConvertProgress says how far a conversion of this item has reached,
// so a wait can show something other than a spinner. Both converters are
// asked, since which one is running depends on the file and the browser.
func (s *Server) handleConvertProgress(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.remux != nil {
		if p, active := s.remux.Progress(it.ID); active {
			writeJSON(w, ConvertProgress{Active: true, Percent: int(p * 100), Kind: "rewrap"})
			return
		}
	}
	if s.hls != nil {
		if p, active := s.hls.Progress(it.ID, it.Duration); active {
			writeJSON(w, ConvertProgress{Active: true, Percent: int(p * 100), Kind: "convert"})
			return
		}
	}
	writeJSON(w, ConvertProgress{})
}

// handleRemux serves the item copied into an MP4, for the two faults a copy
// can fix: a container the browser will not open, and — with ?mode=audio — a
// soundtrack it has no decoder for beside a picture it decodes perfectly
// well (see remux.go).
//
// Unlike the live conversion this is an ordinary file response: ranges, a
// length, and seeking the browser does itself. That is what makes it work on
// iOS, which will not play a media URL that cannot answer a range request.
//
// A 404 is the honest answer for "rewrapping would not help", and it is also
// the one the player acts on: it falls through to the converter.
func (s *Server) handleRemux(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	// The decision is about the streams, so they have to be known, and this
	// is exactly the moment EnsureCodecs exists for: the player is asking.
	it = s.probed(r.Context(), it)
	// ?mode=audio is the other copy this endpoint makes: the picture through
	// untouched and the soundtrack converted beside it, for the film whose
	// only fault is a soundtrack the browser has no decoder for. It is a file
	// for the same reason the plain rewrap is — ranges, a length, seeking —
	// and that matters more here than anywhere, since the live conversion's
	// answer to a reconnect is to start again from the beginning.
	kind := remuxCopy
	switch r.URL.Query().Get("mode") {
	case "audio":
		kind = remuxSound
	case "track":
		// The same streams with one soundtrack, in the container they came
		// in. For a dubbed download that is the whole of what is wanted: the
		// choice cannot be said to a television or to any browser but Safari,
		// so it is made by handing over a file holding only that language.
		kind = remuxTrack
	}
	// A copy is faithful, and for some streams that is exactly the problem:
	// one that reorders further than it declares plays correctly only where
	// something re-encodes it. 404 is the honest answer — "copying would not
	// help" — and it is the one the player already acts on.
	if s.mustReencode(r.Context(), it) {
		http.NotFound(w, r)
		return
	}
	path, err := s.remux.File(r.Context(), it, r.URL.Query().Get("a"), kind)
	if err != nil {
		if !errors.Is(err, ErrNoRemux) && r.Context().Err() == nil {
			s.log.Warn("remux failed", "path", it.Rel, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	// Held for the length of the response, so the budget cannot prune the
	// file out from under it.
	defer s.remux.Hold(path)()
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// This is playback: thumbnailing and enrichment yield to it.
	defer s.lib.StartStream()()
	// Scratch, and evictable, so nothing downstream should hold on to it.
	w.Header().Set("Cache-Control", "no-store")
	// Not always an MP4 now: a soundtrack copy keeps the container it was made
	// from, and a television deciding from the name alone has to be told the
	// truth about it.
	w.Header().Set("Content-Type", remuxMime(it, kind))
	// The name is only what ServeContent sniffs a type from, and the type is
	// already set; the modification time is the source's, so a client that
	// revalidates is answered about the file it actually asked for.
	http.ServeContent(w, r, it.ID+remuxExt(it, kind), time.UnixMilli(it.ModTime), f)
}

// handleTranscode live-remuxes a video the browser cannot decode (e.g. old
// MPEG-4 Part 2, HEVC, or E-AC3 audio) into a fragmented MP4 on stdout. No
// ranges: seeking is done by reopening with ?t=<seconds>. The client falls
// back to this endpoint automatically when a stream plays black or silent.
//
// ?mode=audio keeps the video stream as-is and only re-encodes the audio —
// the common case of a perfectly playable picture with an undecodable
// soundtrack, where re-encoding 4K video would be both pointless and far
// too slow to keep up with playback.
func (s *Server) handleTranscode(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	ffmpeg := s.thumbs.FFmpegPath()
	if ffmpeg == "" {
		http.Error(w, "transcoding unavailable (no ffmpeg)", http.StatusNotImplemented)
		return
	}
	start, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if start < 0 || start > 1e7 {
		start = 0
	}

	// At most two live transcodes; further viewers wait their turn.
	select {
	case s.transSem <- struct{}{}:
		defer func() { <-s.transSem }()
	case <-r.Context().Done():
		return
	}
	defer s.lib.StartStream()()

	// Copying the video means ffmpeg can only start on a keyframe, and it
	// picks the last one at or before this time. The seek must stay exactly
	// as asked: /api/keyframe measured where this very seek lands, and the
	// client has arranged its timeline around that answer.
	copyVideo := r.URL.Query().Get("mode") == "audio"
	// Which is a copy of the picture, so it carries the same fault as the
	// rewrap: a stream that reorders further than it declares has to be
	// re-encoded whatever the client asked for. The client cannot know this
	// — it reads a stalled picture, not a header — so the decision is made
	// here, where the header has been read.
	if copyVideo && s.mustReencode(r.Context(), it) {
		copyVideo = false
	}

	// The same plan the segmented converter runs (convert.go); only the
	// delivery is this endpoint's own: fragmented MP4 down the response.
	plan, err := planConversion(ffmpeg, it, start, copyVideo, r.URL.Query().Get("a"), s.log)
	if err != nil {
		// Known and unopenable, not unknown: the same answer the stream
		// gives, with the same reason, so the player says what happened
		// rather than blaming the format.
		http.Error(w, "file unavailable: "+openFault(err), http.StatusServiceUnavailable)
		return
	}
	defer plan.close()
	args := append(plan.args,
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "none")

	cmd := exec.CommandContext(r.Context(), ffmpeg, args...)
	if plan.stdin != nil {
		cmd.Stdin = plan.stdin
	}
	out := &flushWriter{w: w}
	cmd.Stdout = out
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil && r.Context().Err() == nil {
		s.log.Warn("transcode failed", "path", it.Rel,
			"err", err, "ffmpeg", strings.TrimSpace(errBuf.String()))
		// Nothing went out, so there is still a status to give: a run that
		// could not so much as open its input used to answer an empty 200,
		// which in the access log is indistinguishable from success.
		if out.n == 0 {
			http.Error(w, "conversion failed", http.StatusServiceUnavailable)
		}
	}
}

// sheetWorthMaking answers the one question handleSprite has to decide: may
// this request pay for a scrub sheet that does not exist yet?
//
// A sheet already stored is always served, costing nothing but a read from
// the database. A request that is not a hover — the player opening a film,
// where the seek bar wants the sheet anyway — always goes ahead. What is
// refused is the speculative case: making one under a pointer while the disk
// is busy carrying playback.
func sheetWorthMaking(hover, stored, streaming bool) bool {
	return stored || !hover || !streaming
}

// flushWriter pushes each ffmpeg write straight to the client so playback
// can start while transcoding continues, and counts what went out — while
// that is nothing, the response still has a status to give.
type flushWriter struct {
	w http.ResponseWriter
	n int64
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	f.n += int64(n)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// handleSubs lists a video's subtitles: the sidecar files found next to it,
// and the text streams carried inside it, as one list under one numbering.
func (s *Server) handleSubs(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The embedded tracks come from the probe that runs when a video is
	// opened; this listing is part of that opening, so it is the moment the
	// probe exists for — and without it a film asked about cold would deny
	// the captions it carries.
	it = s.probed(r.Context(), it)
	subs := s.lib.Subtitles(it)
	if subs == nil {
		subs = []library.Subtitle{}
	}
	writeJSON(w, SubtitlesResponse{Subs: subs})
}

// subtitleData reads one of an item's subtitles by its combined index: the
// bytes of a sidecar as they are on disk, or an embedded stream already
// extracted to WebVTT. The name that comes back is what the converters key
// their decoding on — a sidecar's own, or a .vtt name for the extraction,
// which has nothing left to decode.
func (s *Server) subtitleData(ctx context.Context, it library.Item, index int) (data []byte, name string, err error) {
	if path, ok := s.lib.SubtitlePath(it, index); ok {
		data, err = os.ReadFile(path)
		return data, path, err
	}
	if stream, ok := s.lib.EmbeddedSubStream(it, index); ok {
		data, err = s.extractEmbSub(ctx, it, stream)
		return data, "embedded.vtt", err
	}
	return nil, "", fmt.Errorf("no subtitle %d", index)
}

// handleSubFile serves one subtitle converted to WebVTT, the only format
// <track> accepts.
func (s *Server) handleSubFile(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it = s.probed(r.Context(), it)
	// Resolved through the index, so a subtitle outside the roots is not
	// reachable even if something odd is sitting in the directory list.
	// Past the sidecars the index names a stream inside the file itself,
	// which is served extracted (embsubs.go) through the same conversions
	// and the same ?shift= as a sidecar — one list, one numbering, and
	// nothing downstream knows which kind it picked.
	data, path, err := s.subtitleData(r.Context(), it, index)
	if err != nil {
		if r.Context().Err() == nil {
			s.log.Debug("subtitle unavailable", "path", it.Rel, "index", index, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	// A television reads the sidecar itself and wants SubRip; the browser
	// takes only WebVTT. Same file, same list, one parameter apart.
	if r.URL.Query().Get("format") == "srt" {
		srt, err := ToSRT(path, data)
		if err != nil {
			s.log.Debug("subtitle conversion failed", "path", path, "err", err)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(srt)
		return
	}
	vtt, err := ToVTT(path, data)
	if err != nil {
		s.log.Debug("subtitle conversion failed", "path", path, "err", err)
		http.NotFound(w, r)
		return
	}
	// ?shift= rebases the cues onto a transcoded stream, whose clock starts at
	// the keyframe it was opened at rather than at the start of the film.
	if shift, err := strconv.ParseFloat(r.URL.Query().Get("shift"), 64); err == nil && shift > 0 {
		vtt = shiftVTT(vtt, shift)
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "subtitles.vtt", time.UnixMilli(it.ModTime), bytes.NewReader(vtt))
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	data, err := s.thumbs.Get(r.Context(), it, width)
	if err != nil {
		if !errors.Is(err, ErrNoThumb) {
			s.log.Debug("thumb failed", "path", it.Path, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	serveImmutableJPEG(w, r, it, data)
}

// serveImmutableJPEG hands out a picture made once and kept: a year in the
// browser's cache, since the URL it was fetched by carries the file's mtime
// and the database's epoch and changes when either does.
func serveImmutableJPEG(w http.ResponseWriter, r *http.Request, it library.Item, data []byte) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "", time.UnixMilli(it.ModTime), bytes.NewReader(data))
}

// handleSprite serves a video's scrub sheet. The layout is a fixed
// convention (see Thumbnailer.Sprite), so the client derives every frame's
// place and timestamp from the item's duration and needs nothing else from
// here. A video with no sheet — too short, unknown duration, or no ffmpeg —
// is a 404, which is exactly how the client should treat it.
func (s *Server) handleSprite(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// A hover says so, and that changes what it is allowed to cost — but
	// what it costs is not what it is stored in. This used to refuse every
	// archived item, on the strength of a 4K release measured at 2.9 s and
	// 164 MiB; a DVD title is archived by the same definition and measures
	// 1.2-1.4 s over six titles, MPEG-2 keyframes being dense enough that a
	// seek lands almost at once. What the reads have in common is the disk,
	// and the disk is what playback needs.
	//
	// So the rule is the app's own priority order rather than a guess at the
	// container: a hover is the most speculative work here, and it stands
	// down while anything is playing. It is paid once — the sheet is stored
	// and served immutable afterwards — and a refusal is not remembered by
	// the client, so the next hover asks again. A sheet already made is
	// served either way, costing nothing but the read from the database.
	if !sheetWorthMaking(r.URL.Query().Get("hover") == "1", s.thumbs.HasSprite(it), s.lib.Streaming()) {
		http.NotFound(w, r)
		return
	}
	data, err := s.thumbs.Sprite(r.Context(), it)
	if err != nil {
		if !errors.Is(err, ErrNoThumb) {
			s.log.Debug("sprite failed", "path", it.Path, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	serveImmutableJPEG(w, r, it, data)
}

// maxFlagBody bounds a flag request. A cull of a whole multi-selection is one
// request, so the batch form has to fit thousands of ids; nothing more.
const maxFlagBody = 1 << 20

// handleFlagsPut records the owner's judgement about one item. A field left
// out of the body is left as it is, so hiding something does not silently
// clear whether it is a favourite.
func (s *Server) handleFlagsPut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.item(r, id); !ok {
		http.NotFound(w, r)
		return
	}
	body, ok := decodeFlagUpdate(w, r)
	if !ok {
		return
	}
	writeJSON(w, FlagsResponse{Flags: s.lib.SetFlags([]string{id}, body.Hidden, body.Favourite, body.NoCrop, body.Rotation)})
}

// handleFlagsBatch applies one change to many items at once: culling a
// selection is a single request, a single write and a single change event.
// Ids the library does not know are skipped, and the response says which
// items were actually judged.
func (s *Server) handleFlagsBatch(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeFlagUpdate(w, r)
	if !ok {
		return
	}
	if len(body.IDs) == 0 {
		http.Error(w, "no ids", http.StatusBadRequest)
		return
	}
	writeJSON(w, FlagsResponse{Flags: s.lib.SetFlags(body.IDs, body.Hidden, body.Favourite, body.NoCrop, body.Rotation)})
}

func decodeFlagUpdate(w http.ResponseWriter, r *http.Request) (FlagUpdate, bool) {
	var body FlagUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFlagBody)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return FlagUpdate{}, false
	}
	return body, true
}

// handleStateAll hands the page every position, count and verdict it may
// know about. Filtered like every by-id route: a restricted face was being
// told how far the films it cannot see had been watched, and how often.
func (s *Server) handleStateAll(w http.ResponseWriter, r *http.Request) {
	all := s.st.All()
	c, paths := contentOf(r), pathsOf(r)
	if !c.unrestricted() || paths.Restricted() {
		for id := range all {
			it, ok := s.lib.Get(id)
			// A position can outlive its file; one for a file the library
			// no longer holds says nothing about anything this caller is
			// kept from, and the client prunes it itself.
			if ok && (!c.allows(it.Kind) || !paths.AllowsItem(it)) {
				delete(all, id)
			}
		}
	}
	writeJSON(w, PositionsResponse{Positions: all})
}

// handleStateGet answers one item's record: the zero record for a file
// nothing has been saved about, which is what "unwatched" is, and nothing
// at all for an item this caller may not see.
func (s *Server) handleStateGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.item(r, id); !ok {
		http.NotFound(w, r)
		return
	}
	p, _ := s.st.Get(id)
	writeJSON(w, p)
}

func (s *Server) handleStatePut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.item(r, id); !ok {
		http.NotFound(w, r)
		return
	}
	var body PositionUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if body.Time < 0 || body.Duration < 0 {
		http.Error(w, "bad values", http.StatusBadRequest)
		return
	}
	s.st.Set(id, body.Time, body.Duration)
	// The listing filters on how far things have been watched, so it is told
	// as the position moves rather than being made to ask the store.
	s.lib.SetWatch(id, library.Watch{Pos: body.Time, Len: body.Duration})
	w.WriteHeader(http.StatusNoContent)
}

// handlePlay records that something was played.
//
// A POST rather than a counter the server keeps for itself, because only the
// client knows what a play is: a file opened and abandoned after two seconds
// is not one, and the response that carries the bytes cannot tell the
// difference — a seek opens another. The rule lives with the players
// (`PLAY_AFTER`), which is also where the same floor already decides whether
// something counts as started.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.item(r, id); !ok {
		http.NotFound(w, r)
		return
	}
	n := s.st.Play(id)
	// The listings sort and filter on this, so the library is told rather
	// than made to ask the store — the same arrangement as the positions
	// above, and for the same reason.
	s.lib.SetPlays(id, n)
	writeJSON(w, PlayResponse{Plays: n})
}

func (s *Server) handleStateDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// A position can outlive its file, so clearing one the library no longer
	// knows is not an error (state_test pins that). An item the library does
	// know is resolved like every other by-id route, so a restricted face
	// cannot clear positions for things it is not shown.
	if _, known := s.lib.Get(id); known {
		if _, ok := s.item(r, id); !ok {
			http.NotFound(w, r)
			return
		}
	}
	s.st.Delete(id)
	s.lib.SetWatch(id, library.Watch{})
	w.WriteHeader(http.StatusNoContent)
}

// handleEvents streams library change notifications as server-sent events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	ch, cancel := s.lib.Subscribe()
	defer cancel()

	c := contentOf(r)
	paths := pathsOf(r)
	restricted := c.kinds() != 0 || paths.Restricted()
	send := func(ev library.Event) bool {
		// The same numbers the listing carries, counted the same way. A
		// restricted face cannot take the running totals: masking them
		// afterwards cannot fix the three that run across kinds, and a
		// stream that answered for the whole library while the listing
		// answered for this face is a chip that flickers between two
		// libraries every time anything changes.
		if restricted {
			ev.Counts = s.lib.CountsFor(library.CountQuery{Kinds: c.kinds(), Paths: paths})
		}
		ev.Counts = c.mask(ev.Counts)
		data, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "event: change\ndata: %s\n\n", data); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	// Initial event so the client can sync to the current version immediately.
	if !send(library.Event{Version: s.lib.Version(), Counts: s.lib.Counts()}) { //nolint:staticcheck // masked in send
		return
	}

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !send(ev) {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
