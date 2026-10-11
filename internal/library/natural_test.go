// SPDX-License-Identifier: MIT

package library

import (
	"io"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestNamesSortNaturally(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"Part 2.mkv", "Part 10.mkv", -1},
		// A separator is a separator, whichever character spells it.
		{"Harbour_Lights_01-x.mp4", "Harbour Lights 101.mp4", -1},
		{"Part_2", "Part 2", 0},
		{"Part.2", "part - 2", 0},
		{"Part 007", "Part 7", 0},
		{"Part 007", "Part 8", -1},
		{"apple", "Banana", -1},
		{"7 Lanterns", "Amber Road", -1}, // numbers before letters, as digits are
		{"(2019) x", "2019 x", -1},       // and after punctuation
		{"a b", "a1", -1},                // a separator before a number
		{"a", "a b", -1},
		{"_abc_", "abc", 0}, // nothing at either end counts
		{"IMG_9", "IMG_20240527", -1},
		{"x 99", "x 123456789012345678901234567890", -1}, // past any integer type
		{"x 0032 a", "x 32 b", -1},                       // equal numbers, then what follows
		// Thirty-two digits are counted by the byte a space is, and it is
		// still read as a length.
		{"x 9 z", "x " + strings.Repeat("1", 32), -1},
		{"Ärger", "ärger", 0},
		{"zebra", "éclair", -1}, // otherwise by code point, as before
		{"", "a", -1},
		{"", "", 0},
	} {
		ka, kb := naturalKey(c.a), naturalKey(c.b)
		if got := strings.Compare(ka, kb); got != c.want {
			t.Errorf("%q against %q: %d, want %d (keys %q, %q)", c.a, c.b, got, c.want, ka, kb)
		}
	}
}

// naturalKeyOf builds the same key another way, from the rules: over the
// name's characters rather than its bytes, trimming its ends first.
func naturalKeyOf(s string) string {
	runes := []rune(s)
	start, end := 0, len(runes)
	for start < end && naturalSeparator(runes[start]) {
		start++
	}
	for end > start && naturalSeparator(runes[end-1]) {
		end--
	}
	var b strings.Builder
	for i := start; i < end; {
		switch r := runes[i]; {
		case naturalSeparator(r):
			for naturalSeparator(runes[i]) {
				i++
			}
			b.WriteByte(' ')
		case '0' <= r && r <= '9':
			j := i
			for j < end && '0' <= runes[j] && runes[j] <= '9' {
				j++
			}
			digits := strings.TrimLeft(string(runes[i:j]), "0")
			if digits == "" {
				digits = "0"
			}
			b.WriteByte('0')
			b.WriteByte(byte(len(digits)))
			b.WriteString(digits)
			i = j
		default:
			b.WriteRune(unicode.ToLower(r))
			i++
		}
	}
	return b.String()
}

// Over names made at random from the pieces that matter — both cases,
// every separator, digits with and without leading zeros, punctuation,
// accented letters, a byte that is not UTF-8 — the key read byte by byte is
// the key built from the rules.
func TestNaturalKeyFollowsItsRules(t *testing.T) {
	pieces := []string{"a", "A", "b", "B", "z", "ab", " ", "_", ".", "-", "  ", "\u00a0",
		"0", "1", "2", "9", "00", "10", "(", "é", "Ä", "\xff"}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		var b strings.Builder
		for range rng.IntN(9) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		s := b.String()
		if got, want := naturalKey(s), naturalKeyOf(s); got != want {
			t.Fatalf("naturalKey(%q) = %q, the rules say %q", s, got, want)
		}
	}
}

// Every grouped view orders names by one rule (orderBy).
func TestCollectionNamesCountNumbers(t *testing.T) {
	names := []string{"Vol. 10", "vol 2", "Vol_1"}
	orderBy(names, false, func(string) bool { return true }, func(a, b string) int { return 0 },
		naturalKey, func(s string) string { return s })
	if want := []string{"Vol_1", "vol 2", "Vol. 10"}; !slices.Equal(names, want) {
		t.Errorf("collection order %v, want %v", names, want)
	}
}

// A release's untagged tracks run in the order their names count.
func TestUntaggedTracksCountNumbers(t *testing.T) {
	l := New([]string{"/music"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, name := range []string{"10 Last.mp3", "2 Second.mp3", "1 First.mp3"} {
		path := "/music/Band - Demos/" + name
		l.upsert(path, KindAudio, 1000, time.Unix(1, 0), fileKey{}, false)
		l.setMeta(PathID(path), tagMeta{artist: "Band", album: "Demos"}, 1000)
	}
	albums := l.SearchAlbums(AlbumQuery{Sort: "name"})
	if len(albums) != 1 {
		t.Fatalf("built %d albums, want one", len(albums))
	}
	_, tracks, ok := l.AlbumByID(albums[0].ID)
	if !ok || len(tracks) != 3 {
		t.Fatalf("album detail = %d tracks, ok=%v", len(tracks), ok)
	}
	for i, want := range []string{"1 First.mp3", "2 Second.mp3", "10 Last.mp3"} {
		if tracks[i].Name != want {
			t.Errorf("track %d = %q, want %q", i, tracks[i].Name, want)
		}
	}
}
