package server

// The working space the converters share.
//
// A rewrap writes a copy of the file it is rewrapping, and a segmented
// conversion writes the whole of what it has converted so far. Both are
// scratch — reproducible in seconds, and deliberately not in the database,
// which is meant to be the one thing worth keeping — but both are also
// measured in gigabytes, so where they go and how much of it they may use
// are things an operator has to be able to say.
//
// One budget covers both, because the disk is one disk. Each converter
// reports what it is holding and asks whether the two of them are over; each
// then frees its own least recently wanted, which is the only thing either of
// them knows how to do.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Scratch is where converted files go, and how much of it there may be.
type Scratch struct {
	dir   string
	limit int64

	mu   sync.Mutex
	used map[string]int64
}

// NewScratch describes the working space. limit is the total both converters
// may hold between them; zero or less leaves them unbounded.
//
// The directory is a fixed place rather than a fresh one per run, because
// what is in it outlives the run: converting a film again after a restart is
// minutes of work to produce a file that was already sitting there. An
// operator who named one gets that one; otherwise it is a named directory
// under the system's temp, which is still the same one next time.
func NewScratch(dir string, limit int64) *Scratch {
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "media-scratch")
	}
	return &Scratch{dir: dir, limit: limit, used: map[string]int64{}}
}

// Limit is the total budget, or zero when there is none.
func (s *Scratch) Limit() int64 { return s.limit }

// Dir is the base directory.
func (s *Scratch) Dir() string { return s.dir }

// Sub is one converter's own corner of the working space, made if it is not
// there. Named rather than random, so what it holds can be found again after
// a restart.
func (s *Scratch) Sub(name string) (string, error) {
	dir := filepath.Join(s.dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Temp makes a directory inside one converter's corner for a single piece of
// work — an HLS session, which is many files and wants them apart.
func (s *Scratch) Temp(sub, prefix string) (string, error) {
	dir, err := s.Sub(sub)
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, prefix)
}

// Report records what one converter is holding.
func (s *Scratch) Report(owner string, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used[owner] = bytes
}

// Excess is how far over the budget the converters are between them, or zero
// when they are within it.
func (s *Scratch) Excess() int64 {
	if s.limit <= 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, n := range s.used {
		total += n
	}
	if total <= s.limit {
		return 0
	}
	return total - s.limit
}

// ParseSize reads a size written the way an operator writes one: a number,
// optionally followed by K, M, G or T, which are binary multiples because
// that is what a disk of media is measured in. An empty string, or "off",
// means no limit.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "off") || s == "0" {
		return 0, nil
	}
	mult := int64(1)
	// "8G" and "8GiB" and "8GB" all mean the same thing here; anything past
	// the letter is a unit spelt out, and spelling is not information.
	trimmed := strings.TrimRight(s, "bBiI")
	if trimmed == "" {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	switch last := trimmed[len(trimmed)-1]; last {
	case 'k', 'K':
		mult = 1 << 10
	case 'm', 'M':
		mult = 1 << 20
	case 'g', 'G':
		mult = 1 << 30
	case 't', 'T':
		mult = 1 << 40
	default:
		trimmed += " " // keep the number whole below
	}
	num := strings.TrimSpace(strings.TrimRight(trimmed, "kKmMgGtT "))
	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	return int64(n * float64(mult)), nil
}
