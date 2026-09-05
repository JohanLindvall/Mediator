package browse

import "testing"

func TestURL(t *testing.T) {
	cases := []struct {
		listen string
		port   int
		want   string
	}{
		{":8080", 8080, "http://127.0.0.1:8080/"},
		{":0", 45001, "http://127.0.0.1:45001/"},        // random port resolved
		{"", 45002, "http://127.0.0.1:45002/"},          // unparseable: assume local
		{"0.0.0.0:0", 45003, "http://127.0.0.1:45003/"}, // wildcard is not browsable
		{"[::]:0", 45004, "http://127.0.0.1:45004/"},
		{"127.0.0.1:0", 45005, "http://127.0.0.1:45005/"},
		{"192.168.1.5:8080", 8080, "http://192.168.1.5:8080/"}, // explicit host kept
		{"[::1]:0", 45006, "http://[::1]:45006/"},              // literal ipv6 bracketed
	}
	for _, c := range cases {
		if got := URL(c.listen, c.port); got != c.want {
			t.Errorf("URL(%q, %d) = %q, want %q", c.listen, c.port, got, c.want)
		}
	}
}

func TestOpenersNonEmpty(t *testing.T) {
	if len(openers()) == 0 {
		t.Fatal("no openers configured for this platform")
	}
}
