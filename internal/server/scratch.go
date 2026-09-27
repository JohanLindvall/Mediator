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
	"math"
	"os"
	"path/filepath"
	"regexp"
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

var sizePattern = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmgt]?)(b|ib)?$`)

// ParseSize reads a size written the way an operator writes one: a number,
// optionally followed by K, M, G or T, which are binary multiples because
// that is what a disk of media is measured in. An empty string, or "off",
// means no limit.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "off") || s == "0" {
		return 0, nil
	}
	parts := sizePattern.FindStringSubmatch(s)
	if parts == nil || parts[2] == "" && strings.EqualFold(parts[3], "ib") {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	mult := int64(1)
	switch strings.ToLower(parts[2]) {
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	case "t":
		mult = 1 << 40
	}
	n, err := strconv.ParseFloat(parts[1], 64)
	bytes := n * float64(mult)
	if err != nil || math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes < 0 || bytes >= float64(math.MaxInt64) || n > 0 && bytes < 1 {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	return int64(bytes), nil
}
