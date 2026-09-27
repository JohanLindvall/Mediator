package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Keep identity and full timestamps: replacing a file must invalidate its
// confirmation even if its size and rounded modification time are unchanged.
type plannedEntry struct {
	name string // relative to the folder, "." for the folder itself
	info fs.FileInfo
}

func plannedFile(path string, info fs.FileInfo, inFolder bool) PlannedFile {
	return PlannedFile{Path: path, Size: info.Size(), MTime: info.ModTime().UnixMilli(), InFolder: inFolder, info: info}
}

var errDeleteChanged = errors.New("changed since it was shown; ask for a new deletion plan")

func samePlannedFile(before, now fs.FileInfo) bool {
	return before != nil && os.SameFile(before, now) && before.Mode() == now.Mode() &&
		(before.IsDir() || before.Size() == now.Size() && before.ModTime().Equal(now.ModTime()))
}

// Snapshot every entry, including sidecars and empty directories. A new
// sidecar is as much an unconfirmed arrival as a new film.
func (p *planner) snapshotTree(dir string) ([]plannedEntry, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var entries []plannedEntry
	err = fs.WalkDir(r.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if len(entries) >= folderScanLimit || !d.IsDir() && !d.Type().IsRegular() {
			return errDeleteChanged
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if !d.IsDir() && !p.files[path] && !p.l.leftover(path, p.items) {
			return errDeleteChanged
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, plannedEntry{filepath.FromSlash(name), info})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cannot plan folder: %w", err)
	}
	return entries, nil
}

// DeleteNow removes only confirmed entries. Open directory handles constrain
// all operations; Remove (never RemoveAll) leaves concurrent arrivals alone.
func (l *Library) DeleteNow(p DeletePlan) DeleteOutcome {
	var out DeleteOutcome
	keep := func(path string, err error) {
		if !errors.Is(err, fs.ErrNotExist) {
			out.Kept = append(out.Kept, KeptPath{path, err.Error()})
		}
	}
	for _, f := range p.Files {
		if f.InFolder {
			continue
		}
		if !l.UnderRoots(f.Path) {
			keep(f.Path, errDeleteChanged)
			continue
		}
		r, err := os.OpenRoot(filepath.Dir(f.Path))
		if err != nil {
			keep(f.Path, err)
			continue
		}
		err = removePlanned(r, filepath.Base(f.Path), f.info)
		r.Close()
		if err != nil {
			keep(f.Path, err)
			continue
		}
		out.Files++
		out.Bytes += f.Size
		l.Remove(f.Path)
	}
	for _, dir := range p.Folders {
		if err := l.deleteTree(dir, p.trees[dir], &out); err != nil {
			keep(dir, err)
		}
	}
	return out
}

func removePlanned(r *os.Root, name string, before fs.FileInfo) error {
	now, err := r.Lstat(name)
	if err != nil {
		return err
	}
	if !samePlannedFile(before, now) {
		return errDeleteChanged
	}
	return r.Remove(name)
}

func (l *Library) deleteTree(dir string, entries []plannedEntry, out *DeleteOutcome) error {
	if len(entries) == 0 || !l.UnderRoots(dir) {
		return errDeleteChanged
	}
	for _, root := range l.Roots() {
		if pathUnder(root, dir) {
			return errDeleteChanged // roots may have changed after confirmation
		}
	}
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer parent.Close()
	base := filepath.Base(dir)
	now, err := parent.Lstat(base)
	if err != nil {
		return err
	}
	if !samePlannedFile(entries[0].info, now) {
		return errDeleteChanged
	}
	r, err := parent.OpenRoot(base)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := checkDeleteTree(r, entries); err != nil {
		return err
	}
	var removed []string
	defer func() {
		for _, path := range removed {
			l.Remove(path)
		}
	}()
	// Reverse walk order removes children before parents. Recheck identity
	// at each removal, since the disk can change after the walk.
	for i := len(entries) - 1; i > 0; i-- {
		e := entries[i]
		if err := removePlanned(r, e.name, e.info); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		if !e.info.IsDir() {
			out.Files++
			out.Bytes += e.info.Size()
		}
		removed = append(removed, filepath.Join(dir, e.name))
	}
	// Close first for platforms that do not allow removing an open folder.
	if err := r.Close(); err != nil {
		return err
	}
	if err := removePlanned(parent, base, entries[0].info); err != nil {
		return err
	}
	out.Folders++
	removed = []string{dir} // reconcile a completed subtree in a single pass
	return nil
}

func checkDeleteTree(r *os.Root, entries []plannedEntry) error {
	want := make(map[string]fs.FileInfo, len(entries))
	for _, e := range entries {
		want[e.name] = e.info
	}
	seen := 0
	err := fs.WalkDir(r.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !samePlannedFile(want[filepath.FromSlash(name)], info) {
			return errDeleteChanged
		}
		seen++
		return nil
	})
	if err == nil && seen != len(want) {
		return errDeleteChanged
	}
	return err
}
