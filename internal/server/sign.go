package server

// Signed stream links: a URL that carries its own permission.
//
// Everything that fetches media from this server through a browser arrives
// with whatever the deployment put in front — a proxy asking for HTTP Basic
// authentication, most likely. Not everything that fetches media is a
// browser. An AirPlay receiver is handed a URL and goes and gets it itself,
// with no credentials and no way to be given any; so is a casting device,
// and so is whatever an exported m3u is opened in. All of them are refused
// before the request reaches this server, which from the sofa looks like the
// television failing to play the film.
//
// A signed link answers that: the permission travels in the URL. The page
// is given a token when it loads — over the authenticated path, like
// everything else it fetches — and builds its media URLs with the token in
// them. The proxy lets that one path through unauthenticated, and the
// signature stands in place of the credentials.
//
// The token says nothing but when it stops working, which is what lets the
// page mint URLs itself rather than asking for one per file: a round trip
// before every track would be felt on a phone, and a per-file token would
// change with every request and defeat the caching the thumbnails depend on.
// The cost of that choice is the honest one to state: a leaked URL is worth
// the same as a leaked token — the streaming half of the library, until it
// expires. It is not a substitute for the proxy's authentication; it is a
// way for the things that cannot answer a password to fetch what they were
// pointed at.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	// signTTL is how long a link lasts. Long enough to start a film after
	// dinner and watch it through; short enough that a link copied out of a
	// log is worth nothing by the time anyone reads it.
	signTTL = 12 * time.Hour
	// signLen is how much of the digest travels in the link. 16 bytes is 128
	// bits of it, which is not guessable and keeps the URL readable.
	signLen = 16
)

// signer mints and checks the links.
type signer struct {
	key []byte
}

// KeyStore is the part of the blob database this needs: somewhere durable to
// keep the secret, so a link outlives a restart. nil is allowed and means
// there is no database, as `-db off` asks for.
type KeyStore interface {
	SignKey() ([]byte, error)
}

// newSigner takes the store's key, or mints one for this process when there
// is no store — with `-db off` the links then last as long as the run, which
// is the same promise everything else makes in that mode.
func newSigner(store KeyStore, log *slog.Logger) *signer {
	if store != nil && !isNil(store) {
		if key, err := store.SignKey(); err == nil && len(key) >= 32 {
			return &signer{key: key}
		} else if err != nil {
			log.Warn("could not read the link signing key", "err", err)
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// Without randomness nothing can be signed; links are then refused
		// rather than signed with something predictable.
		return &signer{}
	}
	return &signer{key: key}
}

// token is the credential itself: when it stops working, and proof that this
// server said so.
func (s *signer) token(expires int64) string {
	mac := hmac.New(sha256.New, s.key)
	fmt.Fprintf(mac, "stream\n%d", expires)
	sum := mac.Sum(nil)[:signLen]
	return strconv.FormatInt(expires, 10) + "." + base64.RawURLEncoding.EncodeToString(sum)
}

// mint issues a token for a page that has just loaded, and says when it runs
// out so the page can ask for another before it does.
func (s *signer) mint(now time.Time) (token string, expires int64, ok bool) {
	if len(s.key) == 0 {
		return "", 0, false
	}
	expires = now.Add(signTTL).Unix()
	return s.token(expires), expires, true
}

// verify reports whether this token was issued here and has not run out.
// Compared in constant time: the check is the whole of the permission, and a
// timing oracle would let it be built one byte at a time.
func (s *signer) verify(token string, now time.Time) bool {
	if len(s.key) == 0 {
		return false
	}
	exp, _, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	expires, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() > expires {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token(expires))) == 1
}

// signedPaths is what a signed link may reach: the ways media itself is
// served, and nothing else. A receiver needs the bytes, the conversion that
// makes them playable and the subtitles beside them; it has no business
// listing the library, and the token is not a login.
var signedPaths = map[string]bool{
	"stream": true, "hls": true, "remux": true, "transcode": true, "subs": true,
	// A television showing the sleeve while music plays fetches it exactly
	// as it fetches the music: one request, no credentials, no browser. A
	// thumbnail is the item's own picture and reaches nothing the stream
	// does not already reach — it still says nothing about what else is in
	// the library, which is the line this list draws.
	"thumb": true,
}

// handleSigned serves a media request whose only credential is the link it
// was handed, by verifying the token and then answering exactly as the
// unsigned path would.
//
// One route rather than a signed twin of each: a playlist names its segments
// relative to itself, so an HLS session under a signed URL resolves its
// segments under the same one and the receiver never has to be told about
// any of this. The content restriction still applies, that check being on
// the item rather than on how the request arrived.
func (s *Server) handleSigned(w http.ResponseWriter, r *http.Request) {
	if !s.sign.verify(r.PathValue("token"), time.Now()) {
		http.NotFound(w, r)
		return
	}
	rest := r.PathValue("rest")
	head, _, _ := strings.Cut(rest, "/")
	if !signedPaths[head] {
		http.NotFound(w, r)
		return
	}
	// Clone has already deep-copied the URL, so rewriting the path here
	// cannot touch the original request — which the access log reads after
	// the handler returns, and must show the signed path that arrived.
	inner := r.Clone(r.Context())
	inner.URL.Path = "/api/" + rest
	inner.RequestURI = ""
	s.mux.ServeHTTP(w, inner)
}

// isNil covers the interface holding a typed nil pointer, which is what a
// caller passing an unopened *blob.DB hands over.
func isNil(k any) bool {
	v := reflect.ValueOf(k)
	return v.Kind() == reflect.Ptr && v.IsNil()
}
