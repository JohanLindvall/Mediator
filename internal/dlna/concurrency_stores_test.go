package dlna

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A read whose caller has gone must end at once rather than sitting out the
// whole search window. Discover is called from handlers, whose context
// carries no deadline at all, so the only thing that could shorten the
// receive loop was a deadline it never had: a viewer who navigated away left
// a socket and a goroutine per interface running, and the search mutex held
// against the next client, for an answer nobody was left to read.
//
// Driven against a loopback socket rather than through search itself, which
// on a host with no up, multicast, non-loopback IPv4 interface — a plausible
// shape for a build machine — starts no goroutines at all and would pass
// having tested nothing.
func TestAReadEndsWhenNobodyIsWaiting(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("no loopback UDP socket here: %v", err)
	}
	defer conn.Close()
	// The window a search would really be given, so a read that ends because
	// of the deadline rather than because of the cancellation cannot pass.
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer endReadsWhenDone(ctx, conn)()

	read := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_, _, _ = conn.ReadFromUDP(make([]byte, 2048))
		read <- time.Since(start)
	}()

	time.Sleep(20 * time.Millisecond) // let the read reach the kernel
	cancel()
	select {
	case d := <-read:
		if d > 5*time.Second {
			t.Errorf("the read took %v to notice its caller had gone", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read never ended; it is waiting out the whole window for nobody")
	}
}

// What the network answers with decides how many datagrams arrive, and until
// this that decided how many goroutines this process started: one per
// location, all of them alive at once behind a gate that only bounded the
// fetches. The pool is what keeps the cost of a noisy network constant.
func TestDescribeAllSpendsAFixedAmountOfItself(t *testing.T) {
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	var inFlight, peak, served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			was := peak.Load()
			if n <= was || peak.CompareAndSwap(was, n) {
				break
			}
		}
		served.Add(1)
		<-release
		inFlight.Add(-1)
		// One television answering on many interfaces, which is what the
		// folding below is about: one UDN, so one renderer.
		fmt.Fprint(w, `<root><device><UDN>uuid:one-set</UDN>`+
			`<friendlyName>A Television</friendlyName><serviceList><service>`+
			`<serviceType>`+avTransport+`</serviceType>`+
			`<controlURL>/ctl</controlURL></service></serviceList></device></root>`)
	}))
	defer srv.Close()
	// Registered last so it runs first: the handlers are parked on this
	// channel and srv.Close waits for them, so releasing them has to come
	// before the close. The other order made a failing assertion hang until
	// the go test timeout rather than say which assertion went.
	defer releaseAll()

	const many = 200
	locs := map[string]bool{}
	for i := range many {
		locs[fmt.Sprintf("%s/desc/%d.xml", srv.URL, i)] = true
	}

	before := runtime.NumGoroutine()
	done := make(chan []*Renderer, 1)
	go func() { done <- describeAll(context.Background(), locs) }()

	// Wait until the pool is full, then count what this process is spending.
	deadline := time.Now().Add(10 * time.Second)
	for served.Load() < describeAtOnce && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if served.Load() < describeAtOnce {
		t.Fatalf("only %d of %d fetches started", served.Load(), describeAtOnce)
	}
	// A worker apiece plus the handful the HTTP client keeps per connection
	// in flight — nowhere near one per location, which is what this is for.
	if spent := runtime.NumGoroutine() - before; spent > many/2 {
		t.Errorf("describing %d locations started %d goroutines; the pool is %d wide", many, spent, describeAtOnce)
	}
	if p := peak.Load(); p > describeAtOnce {
		t.Errorf("%d fetches were in flight at once, want at most %d", p, describeAtOnce)
	}

	releaseAll()
	found := <-done
	if served.Load() != many {
		t.Errorf("described %d of %d locations", served.Load(), many)
	}
	if len(found) != 1 {
		t.Errorf("found %d renderers; one television answering many times is one television", len(found))
	}
}
