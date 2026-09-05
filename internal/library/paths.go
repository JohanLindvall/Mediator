package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Restricting a caller to part of the library.
//
// The other face of the same idea as the content classes: a request can be
// told it may only see what lives under certain directories, and then that
// is all it sees — not in a listing, not in a count, not by asking for one
// thing by id, and not through the collections. The two restrictions compose;
// a request carrying both sees the intersection, which falls out of each
// being applied where it belongs rather than either knowing about the other.
//
// The match is on the **absolute path on disk**, not the display path. The
// display path starts at a root's own base name, so two roots can produce the
// same one and a restriction written against it would let through files the
// operator never named. The absolute path is what the person configuring the
// proxy is thinking of, and it is unambiguous.
//
// An archived member's path is the archive's own with the member appended
// after a NUL, so it is under the same directories the archive is and needs
// no special case.

// PathFilter is a set of directories a caller may see, in a form a cache key
// can hold: the canonical string is what is compared, and the prefixes are
// derived from it.
//
// The zero value allows everything, which is what an absent header means.
type PathFilter struct {
	// key is the canonical form: cleaned, deduplicated, sorted, newline
	// separated. It is the whole value as far as equality is concerned, so a
	// query carrying it can be cached and compared like any other field.
	key string
}

// ParsePaths reads a header value: paths separated by commas, or by newlines
// for the caller who has one with a comma in it.
//
// Anything relative is dropped rather than resolved. A relative path here
// would be resolved against this process's working directory, which is not
// something the person writing the proxy configuration can see, and quietly
// allowing the wrong directory is the one failure this must not have.
func ParsePaths(h string) PathFilter {
	fields := strings.FieldsFunc(h, func(r rune) bool { return r == ',' || r == '\n' })
	seen := map[string]struct{}{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		p := strings.TrimSpace(f)
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return PathFilter{}
	}
	slices.Sort(out)
	return PathFilter{key: strings.Join(out, "\n")}
}

// Restricted reports whether this filter narrows anything at all.
func (f PathFilter) Restricted() bool { return f.key != "" }

// Key is the canonical form, for a cache key or a log line.
func (f PathFilter) Key() string { return f.key }

// prefixes is the parsed form. Split per pass rather than held, because the
// filter has to be comparable and a slice is not.
func (f PathFilter) prefixes() []string {
	if f.key == "" {
		return nil
	}
	return strings.Split(f.key, "\n")
}

// Allows reports whether a path is under one of the directories.
//
// Component-aware: "/srv/media/rock" is under "/srv/media" and "/srv/mediax"
// is not, which a plain string prefix would get wrong — and getting it wrong
// here hands over a directory nobody allowed. A path equal to a directory is
// under it, so naming a file exactly allows that file.
func (f PathFilter) Allows(path string) bool {
	if f.key == "" {
		return true
	}
	for _, p := range f.prefixes() {
		if pathUnder(path, p) {
			return true
		}
	}
	return false
}

// allower returns a function that answers the same question with the
// prefixes already split, for passes that ask it thousands of times.
func (f PathFilter) allower() func(string) bool {
	if f.key == "" {
		return func(string) bool { return true }
	}
	prefixes := f.prefixes()
	return func(path string) bool {
		for _, p := range prefixes {
			if pathUnder(path, p) {
				return true
			}
		}
		return false
	}
}

// pathUnder is the package's one answer to "is path dir, or inside it" —
// UnderRoots, underAny and the exclude walk all route through it, so the
// component-awareness above cannot quietly diverge between them.
func pathUnder(path, dir string) bool {
	if path == dir {
		return true
	}
	if !strings.HasPrefix(path, dir) {
		return false
	}
	rest := path[len(dir):]
	return rest != "" && (rest[0] == os.PathSeparator || strings.HasSuffix(dir, string(os.PathSeparator)))
}

// AllowsItem reports whether a caller under this filter may see an item.
func (f PathFilter) AllowsItem(it Item) bool { return f.Allows(it.Path) }
