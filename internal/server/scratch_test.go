package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		bad  bool
	}{
		{"8G", 8 << 30, false},
		{"8GiB", 8 << 30, false},
		{"8gb", 8 << 30, false},
		{"512M", 512 << 20, false},
		{"1T", 1 << 40, false},
		{"1024", 1024, false},
		{"1.5G", 1610612736, false},
		{"", 0, false},    // no limit
		{"off", 0, false}, // said plainly
		{"0", 0, false},
		{"lots", 0, true},
		{"-1G", 0, true},
	} {
		got, err := ParseSize(c.in)
		if c.bad {
			if err == nil {
				t.Fatalf("ParseSize(%q) = %d, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// One budget covers both converters, because the disk is one disk.
func TestScratchSharedBudget(t *testing.T) {
	s := NewScratch("", 100)
	if s.Excess() != 0 {
		t.Fatal("an empty scratch is not over its budget")
	}
	s.Report("remux", 60)
	if s.Excess() != 0 {
		t.Fatalf("60 of 100 is not over: %d", s.Excess())
	}
	s.Report("hls", 60)
	if got := s.Excess(); got != 20 {
		t.Fatalf("120 of 100 is over by %d, want 20", got)
	}
	s.Report("remux", 0)
	if s.Excess() != 0 {
		t.Fatal("freeing one converter's share should bring both within budget")
	}
}

// No limit means no limit, however much is held.
func TestScratchUnlimited(t *testing.T) {
	s := NewScratch("", 0)
	s.Report("remux", 1<<50)
	if s.Excess() != 0 {
		t.Fatal("an unlimited scratch is never over")
	}
}

// A named directory is made when it is first needed: an operator naming one
// that does not exist yet meant it to be used. And it is the same directory
// next time, which is what lets a later run find what this one converted.
func TestScratchNamedDirectory(t *testing.T) {
	base := filepath.Join(t.TempDir(), "not", "there", "yet")
	s := NewScratch(base, 0)
	if again := NewScratch(base, 0).Dir(); again != s.Dir() {
		t.Fatalf("two runs got different directories: %q and %q", s.Dir(), again)
	}
	dir, err := s.Temp("hls", "s-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, base) {
		t.Fatalf("made %q, want it under %q", dir, base)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("not a directory: %v", err)
	}
}
