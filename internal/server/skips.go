package server

// Where the credits are, so they can be skipped.
//
// Television has an opening and a closing that are the same every episode,
// and nobody watching a season wants either more than once. Nothing in the
// file says where they are, so the owner does: for a season, for the whole
// show, or for one episode — the narrowest that says anything wins
// (effectiveMarks, on the client). The player then offers a button while the
// intro plays and another while the credits roll, and the second one goes on
// to the next episode with its intro skipped as well, since that is what
// skipping the credits of a season means.
//
// This is the owner's data, like the flags and the positions: it lives in
// the blob database beside them, is held in memory for the run, and lasts the
// run with -db off. Keyed by what it applies to rather than by file, so a
// season's marks survive an episode being replaced and reach an episode that
// arrives later.

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// SkipStore is the part of the blob database the skip marks need. nil is
// allowed and means there is no database, as `-db off` asks for.
type SkipStore interface {
	AllSkips() (map[string]blob.Skip, error)
	PutSkip(key string, m blob.Skip) error
}

// skipStore holds the marks for the run and writes them through.
type skipStore struct {
	mu  sync.Mutex
	set map[string]blob.Skip
	db  SkipStore
	log *slog.Logger
}

func newSkipStore(db SkipStore, log *slog.Logger) *skipStore {
	s := &skipStore{set: map[string]blob.Skip{}, db: db, log: log}
	if db == nil || isNil(db) {
		s.db = nil
		return s
	}
	all, err := db.AllSkips()
	if err != nil {
		log.Warn("skip marks: could not read the database", "err", err)
		return s
	}
	s.set = all
	return s
}

func (s *skipStore) get(key string) (blob.Skip, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.set[key]
	return m, ok
}

// put records one scope's marks. Held across the write, as the flags are,
// so the database is written in the order the values were settled in.
func (s *skipStore) put(key string, m blob.Skip) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m == (blob.Skip{}) {
		delete(s.set, key)
	} else {
		s.set[key] = m
	}
	if s.db == nil {
		return nil
	}
	return s.db.PutSkip(key, m)
}

// skipKeys names the scopes an item belongs to. The episode's own is always
// there; the season's and the show's only where the path said which show it
// is (series.go), and the season's only where it said which season.
func skipKeys(it library.Item) (episode, season, series string) {
	episode = "e|" + it.ID
	if it.Series == "" {
		return episode, "", ""
	}
	k := library.SeriesKey(it.Series)
	series = "t|" + k
	if it.Season > 0 {
		season = "s|" + k + "|" + strconv.Itoa(it.Season)
	}
	return episode, season, series
}

// skipMaxSeconds bounds a mark: a day, which no episode reaches.
const skipMaxSeconds = 86400

// normalise makes the marks consistent: an intro that does not end after it
// starts is no intro, and every figure is a finite number of seconds.
func normaliseSkip(u SkipUpdate) (blob.Skip, bool) {
	for _, v := range []float64{u.IntroStart, u.IntroEnd, u.Outro} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > skipMaxSeconds {
			return blob.Skip{}, false
		}
	}
	m := blob.Skip{IntroStart: u.IntroStart, IntroEnd: u.IntroEnd, Outro: u.Outro}
	if m.IntroEnd <= m.IntroStart {
		m.IntroStart, m.IntroEnd = 0, 0
	}
	return m, true
}

func marksOf(m blob.Skip) *SkipMarks {
	return &SkipMarks{IntroStart: m.IntroStart, IntroEnd: m.IntroEnd, Outro: m.Outro}
}

// skipResponse is everything marked at any scope this item belongs to.
func (s *Server) skipResponse(it library.Item) SkipResponse {
	var resp SkipResponse
	e, se, t := skipKeys(it)
	if m, ok := s.skips.get(e); ok {
		resp.Episode = marksOf(m)
	}
	if se != "" {
		if m, ok := s.skips.get(se); ok {
			resp.Season = marksOf(m)
		}
	}
	if t != "" {
		if m, ok := s.skips.get(t); ok {
			resp.Series = marksOf(m)
		}
	}
	return resp
}

// handleSkipGet answers what is marked for a video, at every scope it
// belongs to. Through the face, like every by-id route.
func (s *Server) handleSkipGet(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, s.skipResponse(it))
}

// maxSkipBody bounds the request: four short fields.
const maxSkipBody = 1 << 10

// handleSkipPut records the marks for one scope of a video and answers with
// everything marked for it afterwards.
func (s *Server) handleSkipPut(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(r, r.PathValue("id"))
	if !ok || it.Kind != library.KindVideo {
		http.NotFound(w, r)
		return
	}
	var u SkipUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSkipBody)).Decode(&u); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	e, se, t := skipKeys(it)
	var key string
	switch u.Scope {
	case "episode":
		key = e
	case "season":
		key = se
	case "series":
		key = t
	}
	if key == "" {
		// A scope this video does not have: a film that is nobody's
		// episode has no season and no show to mark.
		http.Error(w, "no such scope for this video", http.StatusBadRequest)
		return
	}
	m, ok := normaliseSkip(u)
	if !ok {
		http.Error(w, "marks must be seconds, from nought to a day", http.StatusBadRequest)
		return
	}
	if err := s.skips.put(key, m); err != nil {
		// Held in memory regardless, so the season is skipped for the run;
		// what is lost is the restart, and that is said.
		s.log.Warn("skip marks: could not write the database", "key", key, "err", err)
	}
	writeJSON(w, s.skipResponse(it))
}
