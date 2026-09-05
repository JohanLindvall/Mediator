package library

// One file reachable by several paths — a hard link, or a symlink beside its
// target — should appear once in the library, not once per path. Paths are
// therefore keyed by the identity of the file they resolve to, and only the
// first path to claim a file is indexed.

// fileKey identifies a file on disk: inode number plus the device it lives
// on, since inode numbers are only unique within a filesystem.
type fileKey struct {
	dev uint64
	ino uint64
}

func (k fileKey) valid() bool { return k != fileKey{} }

// claim decides whether path may represent the file identified by key.
// Caller must hold l.mu.
//
// The winner is the first path seen, except that a real file always beats a
// symlink: whichever order the walk happens to reach them in, the library
// ends up pointing at the file itself.
func (l *Library) claim(key fileKey, path string, symlink bool) bool {
	if !key.valid() {
		return true // unknown identity: treat every path as its own file
	}
	held, ok := l.byInode[key]
	if !ok || held == path {
		return true
	}
	prev, indexed := l.byPath[held]
	if !indexed {
		return true // the holder is gone; this path takes over
	}
	if prev.symlink && !symlink {
		// Replace the symlink with the real file: its claim goes with it,
		// and the caller's upsert makes the new one.
		l.dropItem(prev)
		return true
	}
	return false
}

// releaseInode drops an item's claim on its file. Caller must hold l.mu.
func (l *Library) releaseInode(it *Item) {
	if it.ino.valid() && l.byInode[it.ino] == it.Path {
		delete(l.byInode, it.ino)
	}
}
