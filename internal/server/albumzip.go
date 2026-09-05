package server

// Downloading a release as one file.
//
// What a release *is* on disk is a directory: the tracks, the sleeve art, the
// nfo, sometimes a scans folder. Handing back only the indexed audio would
// hand back less than what is there, so a directory album is zipped whole and
// the archive is named after the directory, which is the name the release
// already has.
//
// A playlist is the other case and is not a directory at all — it is a
// selection that can reach across several — so for one of those the entries
// themselves are zipped, resolved through the index exactly as the m3u export
// resolves them. That is what keeps a playlist from naming a path outside the
// configured roots, and it has to stay that way.

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/JohanLindvall/Mediator/internal/library"
)

const (
	// zipMaxEntries bounds one archive. A release directory holds tens of
	// files; anything wanting thousands is not a release and the request is
	// answered with what fits rather than walking a whole tree.
	zipMaxEntries = 5000
	// zipMaxDepth bounds the walk below the release directory. Artwork and
	// scans live one or two levels down; nothing legitimate is deeper, and a
	// bounded walk cannot be led somewhere unexpected by a nested tree.
	zipMaxDepth = 3
)

// handleAlbumZip streams the album as a zip download.
//
// The entries are stored, not deflated: audio, pictures and video are already
// compressed, so deflating them spends CPU on a fraction of a percent — and
// this runs while the same disk is serving playback.
func (s *Server) handleAlbumZip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	album, tracks, ok := s.albumFor(r, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// A confined caller gets the tracks it may see and nothing beside them:
	// the whole directory is more than it was allowed.
	name, files, err := s.zipContents(album, tracks, pathsOf(r).Restricted())
	if err != nil {
		s.log.Warn("album zip", "album", album.Name, "err", err)
		http.Error(w, "nothing to download", http.StatusNotFound)
		return
	}
	if len(files) == 0 {
		http.Error(w, "nothing to download", http.StatusNotFound)
		return
	}

	// Reading a release off the disk is the same competition for it that
	// playback is, so it announces itself the same way.
	defer s.lib.StartStream()()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	// No length: the size is only known once every entry has been written,
	// and buffering a release in memory to find it out is not worth a
	// progress bar.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", name+".zip"))

	zw := zip.NewWriter(w)
	for _, f := range files {
		if r.Context().Err() != nil {
			break // the client left; stop reading the disk for it
		}
		if err := writeZipEntry(zw, f); err != nil {
			// The response is already in flight, so there is no status left
			// to change: log it and stop. A truncated archive announces
			// itself when it is opened.
			s.log.Warn("album zip entry", "path", f.name, "err", err)
			break
		}
	}
	if err := zw.Close(); err != nil && r.Context().Err() == nil {
		s.log.Warn("album zip close", "album", album.Name, "err", err)
	}
}

// zipFile is one entry: where to read it and what to call it inside.
type zipFile struct {
	path string // on disk, or "" for an archived item read through the index
	name string // the path inside the zip
	item library.Item
}

// zipContents decides what goes into the archive and what it is called.
// tracksOnly takes the track list even for a directory album: for a caller
// confined to part of the library, and for a release whose tracks are
// members of an archive set, which have no directory to walk — the path
// of a member is the set's own with the member after a NUL, and walking
// its directory zipped the volumes instead of the music.
func (s *Server) zipContents(album *library.Album, tracks []library.Item, tracksOnly bool) (string, []zipFile, error) {
	if album.Source == "dir" && !tracksOnly && !(len(tracks) > 0 && tracks[0].Archived()) {
		if dir := albumDir(tracks); dir != "" {
			// The paths that reach here came from the index and so are under
			// the roots already; this keeps that true if anything ever hands
			// over one that was not.
			if abs, err := filepath.Abs(dir); err != nil || !s.lib.UnderRoots(abs) {
				return "", nil, errors.New("album directory is outside the configured roots")
			}
			files, err := dirEntries(dir)
			return filepath.Base(dir), files, err
		}
	}
	// A playlist, or a directory that could not be determined: take the
	// tracks the index resolved, which is the only set guaranteed to be ours.
	files := make([]zipFile, 0, len(tracks))
	seen := map[string]int{}
	for _, it := range tracks {
		name := it.Name
		// Two directories can contribute the same file name to one playlist;
		// a zip may hold duplicate names but no one wants to unpack them.
		if n := seen[name]; n > 0 {
			ext := filepath.Ext(name)
			name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n+1, ext)
		}
		seen[it.Name]++
		files = append(files, zipFile{path: it.Path, name: name, item: it})
	}
	return safeName(album.Name), files, nil
}

// albumDir is the directory a directory-album's tracks live in, or "" when
// they do not agree on one.
func albumDir(tracks []library.Item) string {
	dir := ""
	for _, it := range tracks {
		d := filepath.Dir(it.Path)
		if dir == "" {
			dir = d
			continue
		}
		if d != dir {
			return ""
		}
	}
	return dir
}

// dirEntries collects the regular files under dir, artwork subfolders and all.
//
// Symlinks are skipped rather than followed: a link is the one thing in a
// release directory that can point somewhere else entirely, and a download
// has no reason to leave the tree it was asked for.
func dirEntries(dir string) ([]zipFile, error) {
	var files []zipFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner is skipped, not fatal
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if d.IsDir() {
			if rel != "." && depth >= zipMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if len(files) >= zipMaxEntries {
			return filepath.SkipAll
		}
		files = append(files, zipFile{path: p, name: filepath.ToSlash(rel)})
		return nil
	})
	return files, err
}

// writeZipEntry copies one file into the archive.
func writeZipEntry(zw *zip.Writer, f zipFile) error {
	var src io.ReadCloser
	var err error
	if f.path != "" && !f.item.Archived() {
		src, err = os.Open(f.path)
	} else {
		// A member of an archive set has no path to open; the index knows how
		// to read it.
		src, err = library.OpenItem(f.item)
	}
	if err != nil {
		return err
	}
	defer src.Close()

	hdr := &zip.FileHeader{Name: path.Clean(f.name), Method: zip.Store}
	if fi, err := os.Stat(f.path); err == nil {
		hdr.Modified = fi.ModTime()
	}
	dst, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}

// safeName keeps a download's filename to something every platform will
// accept, since it comes from a tag rather than from a path.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" {
		return "album"
	}
	return s
}
