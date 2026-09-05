package server

// Subtitle renditions for the segmented conversion.
//
// AirPlay hands a receiver a URL and nothing else — no caption field, no
// metadata document — so the only way subtitles reach one is to be part of
// what the URL describes. HLS has exactly that shape: a master playlist
// names the media and, beside it, subtitle renditions the player's own menu
// offers. It is also the Apple-sanctioned way generally, so the same
// renditions give Safari one subtitle menu that works inline, in the native
// fullscreen and on the receiver alike — three places the page's <track>
// elements reach unevenly or not at all.
//
// The conversion itself is untouched. Sessions and segments know nothing of
// subtitles; the master is composed per request from the film's own listing
// (sidecars and embedded tracks, one numbering), and the viewer's choice
// picks which rendition is marked DEFAULT. Each rendition is a one-entry
// playlist naming a single WebVTT file for the whole run — cues rebased onto
// the session's clock, which starts at the seek, by the same arithmetic the
// <track> path has always used.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// masterPlaylist names the media playlist and the subtitle renditions, all
// relative to the master's own URL so they resolve under the session path —
// signed prefix and all — exactly as segments always have.
func masterPlaylist(sid string, it library.Item, subs []library.Subtitle, chosen string) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n")
	def := -1
	if n, err := strconv.Atoi(chosen); err == nil && n >= 0 && n < len(subs) {
		def = n
	}
	seen := map[string]int{}
	for i, sub := range subs {
		// NAME must be unique within the group or players collapse them.
		base := strings.ReplaceAll(sub.Label, `"`, "'")
		if base == "" {
			base = fmt.Sprintf("Track %d", i+1)
		}
		name := base
		// Counted under the name as it will be written, or three tracks
		// labelled alike came out as one name twice and the player folded
		// them into one entry.
		if n := seen[base]; n > 0 {
			name = fmt.Sprintf("%s %d", base, n+1)
		}
		seen[base]++
		// DEFAULT and AUTOSELECT only on the viewer's own choice: with
		// AUTOSELECT on everything, a player picks by system language and
		// subtitles appear that nobody asked for — the menu is the offer.
		flags := "DEFAULT=NO,AUTOSELECT=NO"
		if i == def {
			flags = "DEFAULT=YES,AUTOSELECT=YES"
		}
		lang := ""
		if sub.Lang != "" {
			lang = fmt.Sprintf(`LANGUAGE="%s",`, strings.ReplaceAll(sub.Lang, `"`, ""))
		}
		fmt.Fprintf(&b,
			"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"text\",NAME=\"%s\",%s%s,URI=\"%s/sub%d.m3u8\"\n",
			name, lang, flags, sid, i)
	}
	// BANDWIDTH is required by the specification; the file's own average is
	// the honest figure where the length is known.
	bw := int64(8_000_000)
	if it.Duration > 0 {
		if v := it.Size * 8 * 1000 / it.Duration; v > 200_000 {
			bw = v
		}
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,SUBTITLES=\"text\"\n%s/media.m3u8\n", bw, sid)
	return []byte(b.String())
}

var hlsSubPattern = regexp.MustCompile(`^sub([0-9]{1,3})\.(m3u8|vtt)$`)

func hlsSubName(name string) bool { return hlsSubPattern.MatchString(name) }

// handleHLSChild serves the master's children: the media playlist, and the
// subtitle renditions.
func (s *Server) handleHLSChild(w http.ResponseWriter, r *http.Request, sess *hlsSession, name string) {
	defer s.lib.StartStream()()
	w.Header().Set("Cache-Control", "no-store")

	if name == "media.m3u8" {
		// The same body the start endpoint serves when there is no master —
		// settled, but not qualified: served from inside the session path,
		// its bare segment names already resolve to the right place.
		body, err := os.ReadFile(filepath.Join(sess.dir, "index.m3u8"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		http.ServeContent(w, r, name, time.Now(), strings.NewReader(string(settledPlaylist(body))))
		return
	}

	m := hlsSubPattern.FindStringSubmatch(name)
	index, _ := strconv.Atoi(m[1])
	it, start, ok := s.hlsSessionItem(r, sess)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if m[2] == "m3u8" {
		// One entry for the whole run: subtitles are kilobytes, and cutting
		// them into four-second pieces would be a thousand requests for
		// nothing. The duration is what remains of the film past the seek.
		left := 3600.0
		if it.Duration > 0 {
			left = max(float64(it.Duration)/1000-start, 1)
		}
		body := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n"+
			"#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:%.3f,\nsub%d.vtt\n#EXT-X-ENDLIST\n",
			int(left)+1, left, index)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		http.ServeContent(w, r, name, time.Now(), strings.NewReader(body))
		return
	}

	data, srcName, err := s.subtitleData(r.Context(), it, index)
	if err != nil {
		if r.Context().Err() == nil {
			s.log.Debug("hls subtitle unavailable", "path", it.Rel, "index", index, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	vtt, err := ToVTT(srcName, data)
	if err != nil {
		s.log.Debug("hls subtitle conversion failed", "path", it.Rel, "err", err)
		http.NotFound(w, r)
		return
	}
	// Onto this session's clock, which starts at the seek — the same
	// arithmetic the ?shift= on the <track> path has always done.
	vtt = shiftVTT(vtt, start)
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeContent(w, r, name, time.Now(), strings.NewReader(string(vtt)))
}

// hlsSessionItem is the film a session converts and where it started.
//
// A session made this run carries a snapshot. One adopted from a previous
// run's disk has only its key — id, identity and start are all in it, which
// is what lets an old conversion still serve its captions.
func (s *Server) hlsSessionItem(r *http.Request, sess *hlsSession) (library.Item, float64, bool) {
	it, start := sess.item, sess.start
	if it.ID == "" {
		parts := strings.Split(sess.key, "|")
		if len(parts) < 4 {
			return it, 0, false
		}
		got, ok := s.lib.Get(parts[0])
		if !ok {
			return it, 0, false
		}
		it = got
		start, _ = strconv.ParseFloat(parts[3], 64)
	}
	// The embedded tracks come from the probe; a session resumed after a
	// restart reaches here before anything else has opened the film.
	return s.probed(r.Context(), it), start, true
}
