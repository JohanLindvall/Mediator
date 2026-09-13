package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
// apply them the other way round — leaving the tile, the Popular ordering
// and every collection total one play behind what was recorded.
func TestConcurrentPlaysLeaveTheTwoStoresAgreeing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01 - a song.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _, lib := serverUnderTest(t, dir)
	items := lib.List(library.Query{Limit: 10}).Items
	if len(items) != 1 {
		t.Fatalf("the library holds %d items, want the one track", len(items))
	}
	id := items[0].ID

	const plays = 40
	var wg sync.WaitGroup
	for range plays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Post(ts.URL+"/api/plays/"+id, "application/json", nil)
			if err != nil {
				return
			}
			res.Body.Close()
		}()
	}
	wg.Wait()

	var pos struct {
		Plays int `json:"p"`
	}
	res, body := getBody(t, ts.URL+"/api/state/"+id)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the position answered %d", res.StatusCode)
	}
	if err := json.Unmarshal(body, &pos); err != nil {
		t.Fatal(err)
	}
	if pos.Plays != plays {
		t.Fatalf("the store counted %d plays, want %d", pos.Plays, plays)
	}
	if got := lib.List(library.Query{Limit: 10}).Items[0].Plays; got != pos.Plays {
		t.Errorf("the library's copy says %d plays where the store says %d; the listings sort on the copy", got, pos.Plays)
	}
}

// mintingStore is a LinkStore that lets a test hold one mint inside the
// lookup while another arrives, which is the shape two presses of the Link
// button make.
type mintingStore struct {
	mu      sync.Mutex
	byCode  map[string]string
	byField map[string]string

	entered chan struct{} // the first lookup announces itself here
	release chan struct{} // and waits here
	once    sync.Once
}

func (m *mintingStore) Link(host, code string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byCode[host+"\x00"+code]
	return t, ok
}

func (m *mintingStore) LinkFor(host, target string) (string, bool) {
	m.once.Do(func() {
		close(m.entered)
		<-m.release
	})
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
		entered: make(chan struct{}), release: make(chan struct{}),
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
	// The second must not get past the lookup while the first is in it. It
	// has nowhere to announce that, so give it a moment to go wrong.
	time.Sleep(100 * time.Millisecond)
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
