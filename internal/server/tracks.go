package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// similarBatch is how many tracks "more like this" hands over unasked: a
// radio's worth, not a listing's. similarMax is the most that may be asked
// for: the nearest are kept by a bounded selection that is cheap only while
// the bound is small, and nobody wants ten thousand neighbours.
const (
	similarBatch = 20
	similarMax   = 200
)

// maxQueue is the most tracks one answer carries: a million, which is not
// a limit anybody meets — the whole library goes into the queue and is
// shuffled there. The bar's own queue takes the same (QUEUE_CAP in
// audio.ts), and the two have to agree or the page would fetch what it then
// throws away.
const maxQueue = 1 << 20

// albumQuery reads the album listing's parameters — for the listing, and
// for the queue that flattens it. One reading, or the two would drift.
func albumQuery(q url.Values, paths library.PathFilter) library.AlbumQuery {
	return library.AlbumQuery{
		Search: q.Get("q"),
		Artist: q.Get("artist"),
		Genre:  q.Get("genre"),
		Sort:   q.Get("sort"),
		Desc:   q.Get("order") != "asc",
		// The audiobooks shelf, or the records: never both.
		Audiobooks: q.Get("audiobooks") == "1",
		// The one grouped view that used to leave this out: a caller
		// confined to part of the library was handed every release in it,
		// under chips that counted only the ones it may see.
		Paths: paths,
	}
}

// handleTracks answers the tracks behind a view, in the order a queue plays
// them: every release listed, every release of every performer listed, every
// release in every genre listed, or the tracks of a listing. It is what
// "queue all" asks — the grid holds tiles and the queue needs tracks, and
// asking release by release would be a request per tile. Music's own, like
// the collections it flattens, so a face without music is answered with
// nothing; a confined caller is handed only what it may see.
func (s *Server) handleTracks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	of := q.Get("of")
	if of != "albums" && of != "artists" && of != "genres" && of != "items" && of != "similar" {
		http.Error(w, "of must be albums, artists, genres, items or similar", http.StatusBadRequest)
		return
	}
	c := contentOf(r)
	paths := pathsOf(r)
	desc := q.Get("order") != "asc"
	// One more than the cap is asked for, which is how the cut is known
	// without counting the whole view.
	tracks := []library.Item{}
	if c.music {
		switch of {
		case "albums":
			tracks = s.lib.TracksOf(s.lib.SearchAlbums(albumQuery(q, paths)), paths, maxQueue+1)
		case "artists":
			artists := s.lib.SearchArtists(q.Get("q"), q.Get("sort"), desc, paths)
			tracks = s.lib.TracksOf(s.lib.ReleasesOf(artists, paths), paths, maxQueue+1)
		case "genres":
			genres := s.lib.SearchGenres(q.Get("q"), q.Get("sort"), desc, paths)
			tracks = s.lib.TracksOf(s.lib.ReleasesIn(genres, paths), paths, maxQueue+1)
		case "items":
			lq := listQuery(q, c, paths)
			// The queue plays music: a mixed listing gives up its films.
			lq.Kind = library.KindAudio
			tracks = s.collect(lq, maxQueue+1)
		case "similar":
			// The tracks that sound most like one, for "more like this" and
			// for radio: the seed has to be something this caller may see.
			n, _ := strconv.Atoi(q.Get("n"))
			if n <= 0 {
				n = similarBatch
			}
			if _, ok := s.item(r, q.Get("id")); ok {
				tracks = s.lib.Similar(q.Get("id"), min(n, similarMax), c.kinds(), paths)
			}
		}
	}
	if tracks == nil {
		tracks = []library.Item{} // an empty answer is a list, never null
	}
	// The cut, where the view held more than a queue takes. A similar
	// answer is bounded by its own cap and is never cut.
	truncated := len(tracks) > maxQueue
	if truncated {
		tracks = tracks[:maxQueue]
	}
	// What is about to be listened to is worth reading tags for ahead of the
	// rest of the library; the queue panel shows titles as they land. The
	// first thousand: a queue of the whole library is not about to be
	// listened to all at once, and the sweep reaches the rest in its turn.
	s.lib.EnrichSoon(itemIDs(tracks[:min(len(tracks), 1000)]))
	writeJSON(w, TracksResponse{Tracks: tracks, Truncated: truncated})
}

// countsOfAlbums is what the chips say over a listing of releases the
// library's totals cannot describe — the ones that sound like one: the
// listing's own size, its performers and its tracks.
func countsOfAlbums(albums []*library.Album) *library.Counts {
	var c library.Counts
	performers := map[string]bool{}
	for _, a := range albums {
		c.Albums++
		c.Audio += a.Tracks
		if a.Artist != "" {
			performers[strings.ToLower(a.Artist)] = true
		}
	}
	c.Artists = len(performers)
	c.Total = c.Audio
	return &c
}

// countsOfArtists is the same over a listing of performers.
func countsOfArtists(artists []*library.Artist) *library.Counts {
	var c library.Counts
	for _, ar := range artists {
		c.Artists++
		c.Albums += ar.Albums
		c.Audio += ar.Tracks
	}
	c.Total = c.Audio
	return &c
}
