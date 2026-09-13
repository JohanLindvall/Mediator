package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/dlna"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// A search whose caller went away says nothing about the network: the
// descriptions all fail on the dead context and what comes back is an empty
// list. Published with a fresh stamp it was the authoritative answer for the
// whole minute of the TTL — every later caller took the freshness path, an
// empty map being as non-nil as a full one — so the house had no
// televisions in it until it expired.
func TestACancelledSearchIsNotRemembered(t *testing.T) {
	s := &Server{log: testLog()}
	set := &dlna.Renderer{ID: "tv-1", Name: "Set One"}
	searches := 0
	s.cast.discover = func(ctx context.Context, _ time.Duration) []*dlna.Renderer {
		searches++
		if ctx.Err() != nil {
			// What Discover really does: the datagrams still arrive, and
			// every description fetch fails at once on the dead context.
			return nil
		}
		return []*dlna.Renderer{set}
	}

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	if got := s.renderers(dead, false); len(got) != 0 {
		t.Fatalf("a cancelled search answered %d renderers, want none", len(got))
	}

	if got := s.renderers(context.Background(), false); len(got) != 1 || got[0].ID != "tv-1" {
		t.Fatalf("the next caller got %d renderers, want the one that is on the network", len(got))
	}
	if searches != 2 {
		t.Errorf("the network was searched %d times, want 2: the cancelled answer must not have been stamped fresh", searches)
	}
}

// A datagram is lost or a description times out, and one round comes back
// without a set that is still in the room playing a film. What the picker is
// shown is what was found — but an id a client is already holding must go on
// resolving, or every transport command, status poll and handover for that
// television answers "no such renderer" until the stamp goes stale.
func TestALosingSearchKeepsASetAddressable(t *testing.T) {
	s := &Server{log: testLog()}
	one := &dlna.Renderer{ID: "tv-1", Name: "Set One"}
	two := &dlna.Renderer{ID: "tv-2", Name: "Set Two"}
	found := []*dlna.Renderer{one, two}
	s.cast.discover = func(context.Context, time.Duration) []*dlna.Renderer { return found }

	ctx := context.Background()
	if got := s.renderers(ctx, false); len(got) != 2 {
		t.Fatalf("the first search found %d renderers, want 2", len(got))
	}

	// The next round loses the second set's reply.
	found = []*dlna.Renderer{one}
	s.cast.at = time.Time{}
	if got := s.renderers(ctx, false); len(got) != 1 {
		t.Fatalf("the losing search listed %d renderers, want the 1 it actually found", len(got))
	}

	r, ok := s.renderer(ctx, "tv-2")
	if !ok {
		t.Fatal(`the set that answered a moment ago is unaddressable: every command for it would answer "no such renderer"`)
	}
	if r.ID != "tv-2" {
		t.Fatalf("looking up tv-2 answered %q", r.ID)
	}
}

// A cast is four steps and the last two are unconditional, so a second
// request landing in the middle used to have its film played and seeked by
// the first — one viewer's resume point applied to another viewer's film.
// The newest request drives the set and the older one is taken off it.
func TestASecondCastTakesTheSetFromTheFirst(t *testing.T) {
	s := &Server{log: testLog()}
	parent := context.Background()

	first, doneFirst := s.castClaim(parent, "tv-1")
	if first.Err() != nil {
		t.Fatal("the first claim should be live")
	}
	// Another set is a different sequence and must not be disturbed.
	elsewhere, doneElsewhere := s.castClaim(parent, "tv-2")
	defer doneElsewhere()

	second, doneSecond := s.castClaim(parent, "tv-1")
	if first.Err() == nil {
		t.Error("the superseded request is still driving the set; its Play and Seek would land on the new film")
	}
	if !superseded(parent, first) {
		t.Error("the superseded request would report a fault of the set's")
	}
	if second.Err() != nil || superseded(parent, second) {
		t.Error("the request that took the set is not driving it")
	}
	if elsewhere.Err() != nil {
		t.Error("a claim on one set cancelled a sequence on another")
	}

	// A superseded request releasing must not take the set from the newer
	// one: the slot belongs to whoever claimed it last.
	doneFirst()
	if second.Err() != nil {
		t.Error("the older request's release cancelled the newer one")
	}
	s.cast.mu.Lock()
	_, held := s.cast.driving["tv-1"]
	s.cast.mu.Unlock()
	if !held {
		t.Error("the older request's release cleared the newer claim; nothing would take the set from it")
	}

	doneSecond()
	s.cast.mu.Lock()
	_, still := s.cast.driving["tv-1"]
	s.cast.mu.Unlock()
	if still {
		t.Error("the set is still marked as being driven after the last request finished")
	}
}

// A client hanging up is not another request taking the set, and the two
// want opposite things said about them.
func TestAClientLeavingIsNotASupersession(t *testing.T) {
	s := &Server{log: testLog()}
	parent, cancel := context.WithCancel(context.Background())
	ctx, done := s.castClaim(parent, "tv-1")
	defer done()
	cancel()
	if superseded(parent, ctx) {
		t.Error("a request whose client left was reported as superseded")
	}
}

// What the owner did with one item is written twice — to the store, which is
// the record, and into the library, which keeps the copy the listings sort
// by — and the pair has to be one turn. Both stores lock properly on their
// own; what was missing was any order between them.
func TestOwnerWritesForOneIdAreOneTurn(t *testing.T) {
	s := &Server{}

	// An id on another stripe, so the second half of this test is about the
	// id and not about the striping.
	other := ""
	for _, candidate := range []string{"b", "c", "d", "e", "f", "g", "h"} {
		if ownerStripe(candidate) != ownerStripe("a") {
			other = candidate
			break
		}
	}
	if other == "" {
		t.Fatal("no candidate id landed on another stripe")
	}

	inside, release := make(chan struct{}), make(chan struct{})
	go func() {
		unlock := s.owning("a")
		close(inside)
		<-release
		unlock()
	}()
	<-inside

	elsewhere := make(chan struct{})
	go func() { s.owning(other)(); close(elsewhere) }()
	select {
	case <-elsewhere:
	case <-time.After(5 * time.Second):
		t.Fatal("a write for another id waited on this one")
	}

	same := make(chan struct{})
	go func() { s.owning("a")(); close(same) }()
	select {
	case <-same:
		t.Fatal("two writes for one id were inside at once: the later value can be applied first")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-same
}

// The store's count is read and then written into the library as an absolute
// number, so two plays of one track could take 1 and 2 from the store and
// apply them the other way round — leaving the tile, the Popular ordering and
// every collection total one play behind what was recorded. A cleared
// position racing a save has the same shape and leaves a film reading as
// finished.
//
// The window between the two writes is nanoseconds wide, so a stress test
// pins nothing: it goes green on the unfixed code nearly every run. What is
// pinned instead is the guarantee itself — that each of these handlers takes
// the id's turn and holds it across both writes — by holding that turn here
// and watching the handler wait for it. Unfixed, every one of them answers
// at once.
func TestTheStateHandlersTakeTheIdsTurn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01 - a song.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, srv, lib := serverUnderTest(t, dir)
	items := lib.List(library.Query{Limit: 10}).Items
	if len(items) != 1 {
		t.Fatalf("the library holds %d items, want the one track", len(items))
	}
	id := items[0].ID

	for _, c := range []struct {
		what string
		do   func() (*http.Response, error)
	}{
		{"a play", func() (*http.Response, error) {
			return http.Post(ts.URL+"/api/plays/"+id, "application/json", nil)
		}},
		{"a saved position", func() (*http.Response, error) {
			req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/state/"+id, strings.NewReader(`{"t":12,"d":100}`))
			if err != nil {
				return nil, err
			}
			return http.DefaultClient.Do(req)
		}},
		{"a cleared position", func() (*http.Response, error) {
			req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/state/"+id, nil)
			if err != nil {
				return nil, err
			}
			return http.DefaultClient.Do(req)
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			// Stand in for the other request: hold the id's turn.
			srv.ownerMu[ownerStripe(id)].Lock()
			answered := make(chan error, 1)
			go func() {
				res, err := c.do()
				if res != nil {
					res.Body.Close()
				}
				answered <- err
			}()
			select {
			case <-answered:
				srv.ownerMu[ownerStripe(id)].Unlock()
				t.Fatalf("%s wrote both stores without taking the id's turn: the store and the library can be written in opposite orders", c.what)
			case <-time.After(300 * time.Millisecond):
			}
			srv.ownerMu[ownerStripe(id)].Unlock()
			select {
			case err := <-answered:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s never finished after the turn was given back", c.what)
			}
		})
	}

	// And with the turn taken properly, the two stores end up agreeing about
	// the one thing carried between them.
	res, err := http.Post(ts.URL+"/api/plays/"+id, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var pos struct {
		Plays int `json:"p"`
	}
	got, body := getBody(t, ts.URL+"/api/state/"+id)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("the position answered %d", got.StatusCode)
	}
	if err := json.Unmarshal(body, &pos); err != nil {
		t.Fatal(err)
	}
	if lib.List(library.Query{Limit: 10}).Items[0].Plays != pos.Plays {
		t.Errorf("the library's copy says %d plays where the store says %d; the listings sort on the copy",
			lib.List(library.Query{Limit: 10}).Items[0].Plays, pos.Plays)
	}
}

// mintingStore is a LinkStore that announces every lookup and holds it there,
// which is what lets a test see whether two mints for one view were inside
// the look-then-write at the same time.
type mintingStore struct {
	mu      sync.Mutex
	byCode  map[string]string
	byField map[string]string

	entered chan struct{} // one per lookup that got in
	release chan struct{} // closed to let them all out
}

func (m *mintingStore) Link(host, code string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byCode[host+"\x00"+code]
	return t, ok
}

func (m *mintingStore) LinkFor(host, target string) (string, bool) {
	// Every lookup announces itself and waits: a mint that got in while
	// another was inside is exactly the fault, and it says so here rather
	// than being inferred from which code came back.
	m.entered <- struct{}{}
	<-m.release
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byField[host+"\x00"+target]
	return c, ok
}

func (m *mintingStore) PutLink(host, code, target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byCode[host+"\x00"+code] = target
	m.byField[host+"\x00"+target] = code
	return nil
}

// The same place gives the same link: pressing the button twice means "give
// me that link", not "give me another name for it". Minting was a look
// followed by a write with nothing spanning the two, so two presses in the
// same moment both missed, both minted, and the second overwrote the reverse
// index — two codes for one view, the first orphaned in the database and
// never handed out again.
func TestConcurrentMintsGiveOneCode(t *testing.T) {
	store := &mintingStore{
		byCode: map[string]string{}, byField: map[string]string{},
		entered: make(chan struct{}, 4), release: make(chan struct{}),
	}
	l := newLinks(store, testLog())

	const host, target = "media.example.com", "m=albums&q=harbour"
	codes := make(chan string, 2)
	mint := func() {
		code, err := l.mint(host, target)
		if err != nil {
			t.Error(err)
		}
		codes <- code
	}

	go mint()
	<-store.entered // the first mint is inside the lookup
	go mint()
	// The second must not get past the lookup while the first is in it:
	// unfixed it walks straight in, and both then mint a code of their own.
	select {
	case <-store.entered:
		close(store.release)
		t.Fatal("two mints were inside the look-then-write at once: both miss, both mint, and the second overwrites the reverse index")
	case <-time.After(300 * time.Millisecond):
	}
	close(store.release)

	first, second := <-codes, <-codes
	if first != second {
		t.Fatalf("two mints for one view gave %q and %q; the older is orphaned in the store", first, second)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.byCode) != 1 {
		t.Errorf("the store holds %d codes for one view, want 1", len(store.byCode))
	}
}

// A mint still has to answer when nothing is stored, which is the `-db off`
// path through the same lock.
func TestMintWithNoStoreIsStillOneCodePerView(t *testing.T) {
	l := newLinks(nil, testLog())
	const host, target = "media.example.com", "m=videos"
	first, err := l.mint(host, target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.mint(host, target)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.ContainsAny(first, linkAlphabet) {
		t.Fatalf("asking twice gave %q and %q", first, second)
	}
}

// The one start-over an aspect refusal earns is **this attempt's own**. The
// verdict it is seeded from is process-wide — shared with the thumbnailer
// and the segmented converter — so reading it again at the decision let a
// tile generated for the same film, a moment earlier, answer the guard for
// us: the retry that would have worked was refused because somebody else had
// already noted what we were about to note, and the viewer was told the
// conversion had failed over a film the very next request converted.
func TestStartOverWithAspect(t *testing.T) {
	const refused = "[vf#0:0 @ 0x1] Setting 'pixel_aspect' to value '-35/3'\n" +
		"Value -11.666667 for parameter 'pixel_aspect' out of range\n"
	const other = "Error while decoding stream #0:0: Invalid data found when processing input\n"
	for _, c := range []struct {
		what      string
		stderr    string
		repaired  bool
		canRepair bool
		want      bool
	}{
		{"the declaration was refused and nothing has put it right yet", refused, false, true, true},
		{"this attempt already ran with it put right", refused, true, true, false},
		{"there is no filter for this codec to be repaired with", refused, false, false, false},
		{"it died of something else entirely", other, false, true, false},
		{"it died of something else and has been repaired already", other, true, true, false},
	} {
		if got := startOverWithAspect(c.stderr, c.repaired, c.canRepair); got != c.want {
			t.Errorf("%s: startOverWithAspect = %v, want %v", c.what, got, c.want)
		}
	}
}

// pagedLibrary answers a listing out of a fixed set of rows, at whatever
// version the test says this call is at.
type pagedLibrary struct {
	calls   int
	version func(call int) int64
	rows    func(version int64) []library.Item
}

func (p *pagedLibrary) list(q library.Query) library.Result {
	p.calls++
	version := p.version(p.calls)
	rows := p.rows(version)
	start := min(q.Offset, len(rows))
	end := min(start+q.Limit, len(rows))
	return library.Result{
		Items:   append([]library.Item(nil), rows[start:end]...),
		Total:   len(rows),
		Version: version,
	}
}

func pagedRows(prefix string, n int) []library.Item {
	out := make([]library.Item, n)
	for i := range out {
		out[i] = library.Item{ID: prefix + strconv.Itoa(i)}
	}
	return out
}

// An offset names a row in one particular filtered, sorted result, and this
// library is written to constantly by design. A change between two pages
// re-sorted what the offsets referred to, so the queue came out holding one
// track twice and missing another — and, the running total being compared
// against a `Total` from a later snapshot, stopping short of the end.
func TestCollectPagesWantsOneVersionThroughout(t *testing.T) {
	const rows = 1200 // three pages of the 500 List will serve
	lib := &pagedLibrary{
		// The library moves once, under the second page of the first pass.
		version: func(call int) int64 {
			if call == 1 {
				return 1
			}
			return 2
		},
		rows: func(version int64) []library.Item {
			if version == 1 {
				return pagedRows("a", rows)
			}
			return pagedRows("b", rows)
		},
	}
	out := collectPages(lib.list, library.Query{}, 10_000)
	if len(out) != rows {
		t.Fatalf("collected %d rows, want %d", len(out), rows)
	}
	seen := map[string]bool{}
	for _, it := range out {
		if !strings.HasPrefix(it.ID, "b") {
			t.Fatalf("the collection spans two orderings (%q): rows repeat and rows at the far end are never fetched", it.ID)
		}
		if seen[it.ID] {
			t.Fatalf("%q was collected twice", it.ID)
		}
		seen[it.ID] = true
	}
}

// A library being written to continuously cannot be made to settle from up
// here: it moves under the second pass exactly as it moved under the first.
// So the second is taken as it stands — one page's worth of drift, rather
// than a third pass that buys nothing and puts hundreds more listings in
// front of every other request in the server.
func TestCollectPagesIsTakenAsItStandsAfterOneRetry(t *testing.T) {
	const rows = 1200
	lib := &pagedLibrary{
		version: func(call int) int64 { return int64(call) },
		rows:    func(int64) []library.Item { return pagedRows("a", rows) },
	}
	out := collectPages(lib.list, library.Query{}, 10_000)
	if len(out) != rows {
		t.Fatalf("a library that never settles cut the collection short at %d of %d rows", len(out), rows)
	}
	// Two pages of the first pass — the second is where the move shows —
	// and then the three of the pass that is taken as it stands.
	if lib.calls != 5 {
		t.Errorf("paged %d times, want 5: one pass, one retry, and then taken as it stands", lib.calls)
	}
}

// And a collection that fits in one page is one page: there is nothing for a
// version to move between, so nothing is ever collected twice.
func TestCollectPagesDoesNotRetakeASinglePage(t *testing.T) {
	lib := &pagedLibrary{
		version: func(call int) int64 { return int64(call) },
		rows:    func(int64) []library.Item { return pagedRows("a", 100) },
	}
	if out := collectPages(lib.list, library.Query{}, 10_000); len(out) != 100 {
		t.Fatalf("collected %d rows, want 100", len(out))
	}
	if lib.calls != 1 {
		t.Errorf("paged %d times for one page of rows", lib.calls)
	}
}

// Changing what is indexed is several mutations of global state in a row —
// the stored list, the live list, the watches, a scan — and the file's own
// comment ("all of that is one operation") was true of one caller only. Two
// PUTs interleaved could leave the database naming one set of directories
// while the index walked another, which nobody sees until the next restart
// quietly puts the other set back.
func TestPrefsChangesAreOneAtATime(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	ts, srv, _ := serverUnderTest(t, first)

	inside := make(chan struct{}, 4)
	release := make(chan struct{})
	srv.AllowRootChanges(func(roots []string) ([]string, error) {
		inside <- struct{}{}
		<-release
		return roots, nil
	}, true)

	put := func(dir string) {
		res := putJSON(t, ts.URL+"/api/prefs", `{"roots":["`+dir+`"]}`)
		res.Body.Close()
	}
	go put(first)
	<-inside // the first change is inside the callback
	go put(second)
	select {
	case <-inside:
		close(release)
		t.Fatal("two changes of the scanned directories were being applied at once: the stored list and the index can end up naming different sets")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
}

// Every step of a cast asks whether the set has been taken, and what a
// superseded request is told matters: a set that answered nothing because we
// stopped talking to it has refused nothing, and reporting it as "did not
// accept the file" blames the television for the viewer's own second press.
func TestASupersededCastIsNotReportedAsTheSetsFault(t *testing.T) {
	s := &Server{log: testLog()}
	set := &dlna.Renderer{ID: "tv-1", Name: "Set One"}
	it := library.Item{ID: "abc", Name: "a film"}

	req := httptest.NewRequest(http.MethodPost, "/api/renderers/tv-1/play/abc", nil)
	ctx, done := s.castClaim(req.Context(), set.ID)
	defer done()

	// Nothing has taken it yet.
	w := httptest.NewRecorder()
	if s.castTaken(ctx, w, req, set, it) {
		t.Fatal("a cast nobody has taken was answered as superseded")
	}

	// A second request takes the set.
	_, doneSecond := s.castClaim(context.Background(), set.ID)
	defer doneSecond()
	w = httptest.NewRecorder()
	if !s.castTaken(ctx, w, req, set, it) {
		t.Fatal("the request that lost the set went on talking to it")
	}
	if w.Code != http.StatusConflict {
		t.Errorf("a superseded cast answered %d, want %d: anything in the 5xx range blames the television",
			w.Code, http.StatusConflict)
	}

	// A client that simply left is not another request taking the set, and
	// must not be answered as one — there is nobody to answer.
	gone, cancel := context.WithCancel(context.Background())
	leaving := httptest.NewRequest(http.MethodPost, "/api/renderers/tv-2/play/abc", nil).WithContext(gone)
	own, doneOwn := s.castClaim(leaving.Context(), "tv-2")
	defer doneOwn()
	cancel()
	if s.castTaken(own, httptest.NewRecorder(), leaving, set, it) {
		t.Error("a request whose client hung up was reported as superseded")
	}
}
