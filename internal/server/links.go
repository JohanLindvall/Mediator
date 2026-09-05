package server

// Shortlinks: a small name for somewhere in the app.
//
// Everything this library shows is already addressable — the view is in the
// URL fragment, and has been all along — but a fragment naming a performer
// with a long name, a genre and a search is a paragraph, and a paragraph is
// not something anybody pastes into a message. A shortlink is the same
// address under a name short enough to send, read aloud or write down.
//
// What is stored is the fragment itself, opaque to this file. That is the
// whole design: the server never has to understand a view in order to point
// at one, so the performer, the genre, the programme, the search and the one
// particular film, photograph or track are all the same feature, and a view
// invented later needs nothing here at all.
//
// A link belongs to the **name it was made under**. One server answers under
// several hostnames — a face of music, a face of films, the whole library —
// and a code that resolved on all of them made the hostname in the address
// decoration: a link sent to somebody for one face opened another the moment
// they changed the name in front of it. The host is therefore part of what is
// stored against a code, and a code offered to a different one reads as a
// code nobody minted, which from there is exactly what it is.
//
// There is deliberately no authentication on a shortlink, as there is none on
// anything else here: it names a place in a library that whoever can reach
// the port can already browse. Guessing one reveals nothing that visiting the
// address would not.

import (
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// LinkStore is the part of the blob database shortlinks need. nil is allowed
// and means there is no database, as `-db off` asks for.
type LinkStore interface {
	Link(host, code string) (string, bool)
	LinkFor(host, target string) (string, bool)
	PutLink(host, code, target string) error
}

const (
	// linkCodeLen is how long a minted code is. Seven characters of the
	// alphabet below is thirty billion of them, which is a great many more
	// than a person will ever make by hand.
	linkCodeLen = 7
	// linkMaxTarget bounds what may be stored under one. A fragment naming a
	// performer, a genre, a search and an item is a couple of hundred bytes;
	// this is generous and is here to stop the endpoint being used as
	// storage rather than as a name.
	linkMaxTarget = 512
	// linkAlphabet leaves out the characters that are read wrongly when a
	// link is written down or spoken: i, l, o, 0 and 1.
	linkAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
)

// linkHost is the name this request arrived under, which is what a link is
// scoped to.
//
// X-Forwarded-Host comes first because a reverse proxy that rewrites the Host
// header is the case this exists for: nginx sends the *upstream* address by
// default, so without either the original name or that header every face of a
// library looks like one host and links cross between them again — silently,
// which is the worst way for this to fail. The README says to set
// `proxy_set_header Host $host`.
//
// Lower-cased because a hostname is not case-sensitive and a link should not
// depend on how somebody typed it. The port is kept: two servers on one
// machine are two libraries.
func linkHost(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if i := strings.IndexByte(host, ','); i >= 0 {
		// A chain of proxies appends; the first is the client's own.
		host = host[:i]
	}
	if host = strings.TrimSpace(host); host == "" {
		host = r.Host
	}
	return strings.ToLower(host)
}

// links mints and resolves them.
type links struct {
	store LinkStore
	log   *slog.Logger

	// With no database there is nowhere durable to put them, so they last the
	// run — the same promise playback positions and the signing key make in
	// that mode, rather than a second file beside the one this arrangement
	// exists to avoid.
	// Both are keyed by host first (see linkHost), exactly as the database
	// is: a link belongs to the name it was made under, whether or not there
	// is anywhere to write it down.
	mu      sync.Mutex
	byCode  map[string]string
	byField map[string]string
}

// memKey is the in-memory equivalent of the database's compound key.
func memKey(host, rest string) string { return host + "\x00" + rest }

// memLinksMax bounds the in-memory maps: with no database they would grow
// for as long as the process ran, one pair per place linked. Past it they
// start over, which forgets links a run of that length made — a limit
// nobody meets, and the promise in that mode was only ever "for the run".
const memLinksMax = 10_000

func newLinks(store LinkStore, log *slog.Logger) *links {
	l := &links{log: log, byCode: map[string]string{}, byField: map[string]string{}}
	if store != nil && !isNil(store) {
		l.store = store
	}
	return l
}

// target returns where a code points on this host.
func (l *links) target(host, code string) (string, bool) {
	if l.store != nil {
		return l.store.Link(host, code)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.byCode[memKey(host, code)]
	return t, ok
}

// mint returns the code for a target, making one only if this target has not
// been asked for before. Asking twice for a link to the same place gives the
// same link back: a person clicking the button again means "give me that
// link", not "give me another name for it".
func (l *links) mint(host, target string) (string, error) {
	if code, ok := l.existing(host, target); ok {
		return code, nil
	}
	for range 8 {
		code, err := newLinkCode()
		if err != nil {
			return "", err
		}
		if _, taken := l.target(host, code); taken {
			continue
		}
		if err := l.put(host, code, target); err != nil {
			return "", err
		}
		return code, nil
	}
	return "", errLinkCollision
}

func (l *links) existing(host, target string) (string, bool) {
	if l.store != nil {
		return l.store.LinkFor(host, target)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.byField[memKey(host, target)]
	return c, ok
}

func (l *links) put(host, code, target string) error {
	if l.store != nil {
		return l.store.PutLink(host, code, target)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byCode) >= memLinksMax {
		l.byCode = map[string]string{}
		l.byField = map[string]string{}
	}
	l.byCode[memKey(host, code)] = target
	l.byField[memKey(host, target)] = code
	return nil
}

// errLinkCollision is what eight collisions in a row means, which is either
// a broken random source or a database of billions. Either way it is not a
// thing to retry forever inside a request.
var errLinkCollision = errLink("could not mint a unique code")

type errLink string

func (e errLink) Error() string { return string(e) }

func newLinkCode() (string, error) {
	raw := make([]byte, linkCodeLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, linkCodeLen)
	for i, b := range raw {
		out[i] = linkAlphabet[int(b)%len(linkAlphabet)]
	}
	return string(out), nil
}

// linkTargetOK reports whether this is something that could be a URL
// fragment. It is deliberately about the shape and not about the meaning:
// what a view is called is the frontend's business, and a target this server
// refused to store because it did not recognise a key would be a feature that
// breaks the next time the app learns a new one.
func linkTargetOK(target string) bool {
	if target == "" || len(target) > linkMaxTarget {
		return false
	}
	for _, r := range target {
		// Printable ASCII only, and not the character that would end the
		// fragment and start another.
		if r <= ' ' || r > '~' || r == '#' {
			return false
		}
	}
	return true
}

// handleLinkCreate mints a shortlink for a piece of app state.
func (s *Server) handleLinkCreate(w http.ResponseWriter, r *http.Request) {
	var req LinkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target := strings.TrimPrefix(strings.TrimSpace(req.Target), "#")
	if !linkTargetOK(target) {
		http.Error(w, "not something that can be linked to", http.StatusBadRequest)
		return
	}
	host := linkHost(r)
	code, err := s.links.mint(host, target)
	if err != nil {
		s.log.Warn("could not mint a shortlink", "err", err)
		http.Error(w, "could not make a link", http.StatusInternalServerError)
		return
	}
	// Which name it was scoped to, at the level an operator turns on when a
	// link works somewhere it should not — or nowhere at all, the proxy
	// having sent the upstream address for every face alike.
	s.log.Debug("minted a shortlink", "host", host, "code", code, "target", target)
	writeJSON(w, LinkResponse{Code: code, Path: linkPath(code)})
}

// handleLink sends a visitor to where the code points.
//
// The state is in the fragment, which never reaches a server — so this cannot
// serve the page with the view already applied, and redirects to the address
// the app reads on the way up instead. A code nobody minted is sent to the
// library rather than to an error: a link that has been mistyped, or offered
// to a hostname it was not made under, or that has outlived the database it
// was kept in, should land somewhere usable rather than on a page saying no.
func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	target, ok := s.links.target(linkHost(r), r.PathValue("code"))
	to := "/"
	if ok {
		to = "/#" + target
	}
	// Never cached: the app is what a visitor should be holding, not a
	// redirect that a browser would go on applying after the code was reused.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusFound)
}

// linkPath is where a code is answered, relative to the site root so that the
// page can make it absolute against whatever host it was itself opened on.
func linkPath(code string) string { return "/s/" + code }
