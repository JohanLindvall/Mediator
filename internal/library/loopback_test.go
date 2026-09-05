package library

import (
	"net"
	"strings"
	"testing"
)

// Only an address loopback can actually reach becomes a base URL. Guessing
// 127.0.0.1 for a listener bound to one specific interface would send every
// internal read to whatever else happens to answer on that port.
func TestLoopbackAddr(t *testing.T) {
	for _, c := range []struct {
		name string
		addr net.Addr
		want string
	}{
		{"every interface", &net.TCPAddr{IP: net.IPv4zero, Port: 8080}, "http://127.0.0.1:8080"},
		{"every interface, v6", &net.TCPAddr{IP: net.IPv6unspecified, Port: 8080}, "http://127.0.0.1:8080"},
		{"no address at all", &net.TCPAddr{Port: 8080}, "http://127.0.0.1:8080"},
		{"loopback", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 45123}, "http://127.0.0.1:45123"},
		{"loopback, v6", &net.TCPAddr{IP: net.IPv6loopback, Port: 45123}, "http://[::1]:45123"},
		{"one LAN interface", &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 8080}, ""},
		{"not tcp", &net.UnixAddr{Name: "/tmp/s", Net: "unix"}, ""},
		{"unbound", &net.TCPAddr{IP: net.IPv4zero}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := LoopbackAddr(c.addr); got != c.want {
				t.Fatalf("LoopbackAddr(%v) = %q, want %q", c.addr, got, c.want)
			}
		})
	}
}

// With no loopback address every caller has to fall back to the pipe, and the
// way they learn that is an empty URL.
func TestLoopbackURLNeedsABase(t *testing.T) {
	t.Cleanup(func() { SetLoopback("") })
	it := Item{ID: "abc123"}

	SetLoopback("")
	if got := LoopbackURL(it); got != "" {
		t.Fatalf("LoopbackURL without a base = %q, want empty", got)
	}
	SetLoopback("http://127.0.0.1:8080")
	if got := LoopbackURL(it); got != "http://127.0.0.1:8080/api/stream/abc123" {
		t.Fatalf("LoopbackURL = %q", got)
	}
	if got := LoopbackURL(Item{}); got != "" {
		t.Fatalf("LoopbackURL of an item with no id = %q, want empty", got)
	}
}

// The marker has to be unguessable, and it has to be the same for the whole
// process or the server would not recognise its own reads.
func TestInternalTokenIsStableAndOpaque(t *testing.T) {
	tok := InternalToken()
	if len(tok) < 16 {
		t.Fatalf("token %q is too short to be unguessable", tok)
	}
	if tok != InternalToken() {
		t.Fatal("the token changed between calls")
	}
	hdr := LoopbackHeaderArg()
	if !strings.HasPrefix(hdr, InternalHeader+": "+tok) || !strings.HasSuffix(hdr, "\r\n") {
		t.Fatalf("header argument %q is not a CRLF-terminated header line", hdr)
	}
}
