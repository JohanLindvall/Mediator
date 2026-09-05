package server

// The directories to index, changeable while the server runs.
//
// Changing them is not a matter of writing a list down: the index has to be
// walked against the new set, the filesystem watches have to move with it,
// and what belonged to a directory that is gone has to leave the index. All
// of that is one operation, so the server hands the whole change to a single
// callback that main owns — the only place that has the watcher and the
// scanner in the same scope.
//
// There is no authentication in front of any of this, which is worth stating
// plainly: anyone who can reach the port can point the library at any
// directory the server process can read, and then browse and stream it. That
// is the deliberate posture for a personal server on a network its owner
// trusts, and it is the same posture the rest of the API already has.
//
// A server that does not want it says so at startup with -lock, which leaves
// the callback below unset. The endpoint then reports what is indexed and
// refuses every change — the dialog hides its controls to match, but that is
// a courtesy and this is the guarantee.
//
// A caller **confined to part of the library** (`X-Allowed-Paths`) is refused
// outright, both the reading and the writing. Changing the roots is obvious
// enough — it is the one call that could hand somebody the whole disk — but
// reading them matters just as much: the list names the directories the
// library is rooted at, and a caller allowed one branch of that tree has no
// business learning what the others are called. Everything else this server
// tells such a caller is already filtered to what they may see, and this
// would have been the one place that was not.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// maxRoots bounds the list. A media server watches a handful of trees; a list
// beyond this is a mistake or a wedge, and either way each entry costs a full
// walk and a watch on every directory beneath it.
const maxRoots = 64

// SetRootsFunc applies a new set of directories: record them, move the
// watches, rescan. It returns the set that ended up in force.
type SetRootsFunc func(roots []string) ([]string, error)

func (s *Server) handlePrefs(w http.ResponseWriter, r *http.Request) {
	if pathsOf(r).Restricted() {
		http.Error(w, "this view is confined to part of the library", http.StatusForbidden)
		return
	}
	writeJSON(w, s.prefs())
}

// prefs describes the current configuration and what may be done to it.
func (s *Server) prefs() PrefsResponse {
	return PrefsResponse{
		Roots:     s.lib.Roots(),
		Persisted: s.prefsPersist,
		Editable:  s.setRoots != nil,
	}
}

func (s *Server) handlePrefsPut(w http.ResponseWriter, r *http.Request) {
	if pathsOf(r).Restricted() {
		// Asked before the lock, because a confined caller may not even
		// learn whether this server would otherwise have allowed it.
		http.Error(w, "this view is confined to part of the library", http.StatusForbidden)
		return
	}
	if s.setRoots == nil {
		// Started with -lock. Refusing here is the whole guarantee: the
		// dialog hides its controls to match, but that is a courtesy and
		// this is the check.
		http.Error(w, "this server was started with locked directories", http.StatusForbidden)
		return
	}
	var up PrefsUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&up); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	clean, err := validateRoots(up.Roots)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	applied, err := s.setRoots(clean)
	if err != nil {
		s.log.Warn("could not change the scanned directories", "err", err)
		http.Error(w, "could not change the directories", http.StatusInternalServerError)
		return
	}
	p := s.prefs()
	p.Roots = applied
	writeJSON(w, p)
}

// validateRoots turns what was asked for into a set that can be scanned, or
// says why it cannot be.
//
// Every entry has to be a directory that exists and can be listed. Refusing
// here rather than at the walk is the difference between a dialog that
// reports a typo and a library that silently indexes nothing.
func validateRoots(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, errors.New("at least one directory is needed")
	}
	if len(in) > maxRoots {
		return nil, fmt.Errorf("at most %d directories", maxRoots)
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: not a usable path", raw)
		}
		abs = filepath.Clean(abs)
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: cannot be read", raw)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s: not a directory", raw)
		}
		if f, err := os.Open(abs); err != nil {
			return nil, fmt.Errorf("%s: cannot be listed", raw)
		} else {
			f.Close()
		}
		if _, dup := seen[abs]; dup {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	// A directory inside another one on the list is walked twice and watched
	// twice, and every file under it is then a duplicate of itself. The
	// enclosing one already covers it.
	return dropNested(out), nil
}

// dropNested removes entries that lie inside another entry.
func dropNested(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		nested := false
		for _, other := range roots {
			if other != r && isUnder(r, other) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, r)
		}
	}
	return out
}

// isUnder reports whether path lies inside dir — strictly inside: unlike
// library's pathUnder (the containment test everything in the index shares),
// a directory is not under itself here, because dropNested must keep exactly
// one copy of a duplicated entry rather than dropping both.
func isUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) &&
		rel != "." && !hasDotDotPrefix(rel)
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.' &&
		(len(rel) == 2 || rel[2] == filepath.Separator)
}
