// Package blob is the single-file store (bbolt) for everything the server
// keeps: generated thumbnails and probed metadata, the mirrored index, and
// the two things that are the owner's rather than the file's — flags and
// playback positions. One file means one thing to back up, and one thing to
// delete for a clean slate.
//
// Derived entries carry the source file's (mtime, size) and are ignored once
// the file changed, so a modified file simply overwrites its stale entries.
// The flags and positions buckets are the exception: they hold what the owner
// did rather than what was read from a file, so they have no (mtime, size)
// stamp and Prune leaves them alone.
package blob

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

type DB struct {
	db *bolt.DB
}

var (
	thumbBucket = []byte("thumbs")
	metaBucket  = []byte("meta")
	itemBucket  = []byte("items")
	flagBucket  = []byte("flags")
	posBucket   = []byte("positions")
	infoBucket  = []byte("info")
	cropBucket  = []byte("crops")
	linkBucket  = []byte("links")
	featBucket  = []byte("features")
)

// epochKey names the value that identifies this store to clients. See Epoch.
var epochKey = []byte("epoch")

// thumbHeaderLen prefixes each derived value — a thumbnail, a detected
// border — with the source file's mtime (8) and size (8), which is what
// makes a stale entry ignorable once the file has changed.
const thumbHeaderLen = 16

// stamp prefixes data with the source file it was derived from.
func stamp(mtime, size int64, data []byte) []byte {
	v := make([]byte, thumbHeaderLen, thumbHeaderLen+len(data))
	binary.BigEndian.PutUint64(v, uint64(mtime))
	binary.BigEndian.PutUint64(v[8:], uint64(size))
	return append(v, data...)
}

// unstamp returns the data behind a stamp that matches the file as it is
// now, or nil for a value written for a file that has since changed. The
// copy is deliberate: bolt's buffers die with the transaction.
func unstamp(v []byte, mtime, size int64) []byte {
	if len(v) < thumbHeaderLen ||
		int64(binary.BigEndian.Uint64(v)) != mtime ||
		int64(binary.BigEndian.Uint64(v[8:])) != size {
		return nil
	}
	return bytes.Clone(v[thumbHeaderLen:])
}

// Open opens (creating if needed) the blob database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// Timeout so a second instance pointed at the same file fails fast
	// instead of blocking forever on the file lock.
	db, err := bolt.Open(path, 0o644, &bolt.Options{Timeout: time.Second})
	if err != nil {
		if errors.Is(err, berrors.ErrTimeout) {
			// The bare "timeout" from the lock says nothing about what to
			// do; the cause is always another instance holding the file.
			return nil, fmt.Errorf(
				"%s is locked by another instance — stop it, or start this one with a different -db path (or -db off)",
				path)
		}
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{
			thumbBucket, metaBucket, itemBucket, flagBucket, posBucket, infoBucket, cropBucket,
			linkBucket, featBucket,
		} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	out := &DB{db: db}
	if err := out.initEpoch(); err != nil {
		db.Close()
		return nil, err
	}
	return out, nil
}

// initEpoch mints this store's identity the first time it is opened, and
// leaves it alone ever after.
//
// It exists for the browser. Thumbnails are served immutable for a year, and
// the only thing versioning their URLs is the source file's mtime — so a
// deleted database changes nothing a browser can see, and it keeps showing
// images the server has forgotten. Including the epoch in those URLs ties
// them to the store that produced them: delete the database and every URL
// changes, so browsers refetch; keep it and they stay cached, which is what
// protects the connection budget playback needs.
func (s *DB) initEpoch() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(infoBucket)
		if v := b.Get(epochKey); len(v) > 0 {
			return nil
		}
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return err
		}
		return b.Put(epochKey, []byte(hex.EncodeToString(raw[:])))
	})
}

// Epoch identifies this store. It is stable for the life of the file and
// different for a file created later. Empty only if the read fails.
func (s *DB) Epoch() string {
	var out string
	_ = s.db.View(func(tx *bolt.Tx) error {
		out = string(tx.Bucket(infoBucket).Get(epochKey))
		return nil
	})
	return out
}

// signKeyName names the secret that signs stream links. See SignKey.
var signKeyName = []byte("signkey")

// SignKey returns the secret this store signs stream links with, minting it
// the first time it is asked for.
//
// It lives here so that a link outlives a restart: a signed URL handed to a
// television, or written into a playlist, is worth nothing if the key behind
// it is gone by the time anything fetches it. Deleting the database
// invalidates every outstanding link, which is the same promise the epoch
// makes about thumbnails and is exactly what deleting it should mean.
func (s *DB) SignKey() ([]byte, error) {
	var key []byte
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(infoBucket)
		if v := b.Get(signKeyName); len(v) >= 32 {
			key = append([]byte(nil), v...)
			return nil
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		key = raw
		return b.Put(signKeyName, raw)
	})
	return key, err
}

// --- configuration ------------------------------------------------------------
//
// The directories to scan live here rather than beside the database, for the
// same reason playback positions do: one file to keep, one file to delete,
// and nothing left behind by either. With no database there is nowhere to put
// them, so a change lasts the run and the startup arguments are what comes
// back — which is the honest behaviour for a server told not to write things
// down.

var rootsKey = []byte("roots")

// Roots returns the stored scan directories, or nil when none were ever set.
func (s *DB) Roots() []string {
	var out []string
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(infoBucket).Get(rootsKey)
		if len(v) == 0 {
			return nil
		}
		return json.Unmarshal(v, &out)
	})
	return out
}

// SetRoots stores the scan directories.
func (s *DB) SetRoots(roots []string) error {
	v, err := json.Marshal(roots)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(infoBucket).Put(rootsKey, v)
	})
}

// --- playback positions -------------------------------------------------------
//
// Values are opaque here: the shape belongs to the package that owns them,
// and keeping it out of the store means the two can change independently.

// GetPositions returns every stored position, keyed by item id.
func (s *DB) GetPositions() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(posBucket).ForEach(func(k, v []byte) error {
			out[string(k)] = bytes.Clone(v)
			return nil
		})
	})
	return out, err
}

// PutPositions writes the given positions and removes the given ids in one
// transaction. Both together, because a flush is one consistent picture of
// what changed since the last one — and one transaction is also what keeps a
// debounced flush cheap.
func (s *DB) PutPositions(put map[string][]byte, remove []string) error {
	if len(put) == 0 && len(remove) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(posBucket)
		for id, v := range put {
			if err := b.Put([]byte(id), v); err != nil {
				return err
			}
		}
		for _, id := range remove {
			if err := b.Delete([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *DB) Close() error { return s.db.Close() }

// --- thumbnails ---------------------------------------------------------------

func thumbKey(id string, width int) []byte {
	return fmt.Appendf(nil, "%s|%d", id, width)
}

// GetThumb returns the cached thumbnail for (id, width), or false if there is
// none matching the source file's current mtime and size.
func (s *DB) GetThumb(id string, mtime, size int64, width int) ([]byte, bool) {
	var data []byte
	_ = s.db.View(func(tx *bolt.Tx) error {
		data = unstamp(tx.Bucket(thumbBucket).Get(thumbKey(id, width)), mtime, size)
		return nil
	})
	return data, data != nil
}

// PutThumb stores a thumbnail, replacing any previous entry for (id, width).
// Concurrent puts are coalesced into shared transactions (bolt.DB.Batch).
func (s *DB) PutThumb(id string, mtime, size int64, width int, jpeg []byte) error {
	v := stamp(mtime, size, jpeg)
	return s.db.Batch(func(tx *bolt.Tx) error {
		return tx.Bucket(thumbBucket).Put(thumbKey(id, width), v)
	})
}

// --- metadata -----------------------------------------------------------------

// Meta is the persisted enrichment result for one item.
type Meta struct {
	MTime    int64  `json:"mtime"`
	Size     int64  `json:"size"`
	Duration int64  `json:"duration,omitempty"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Genre    string `json:"genre,omitempty"`
	Track    int    `json:"track,omitempty"`
	Year     int    `json:"year,omitempty"`
	VCodec   string `json:"vcodec,omitempty"`
	ACodec   string `json:"acodec,omitempty"`
	// The picture's shape, for video and for stills alike. Written down for
	// the same reason the codecs are: it is read once and wanted every time
	// the file is listed, and the alternative is opening every file in the
	// library again after every restart.
	Width  int     `json:"w,omitempty"`
	Height int     `json:"h,omitempty"`
	FPS    float64 `json:"fps,omitempty"`
	// Shape records *which reading* of the file's shape this is, which the
	// fields above cannot say: plenty of files have no picture to measure,
	// and a record written before there was anywhere to put one looks exactly
	// like a file nobody could measure. Without it every start would read
	// every file in the library again to find out what it already knew.
	//
	// A number rather than a flag, so that learning to read something new
	// from the same header — the frame rate was the first — reaches the files
	// already read. The library owns what the current one means.
	Shape int `json:"shape,omitempty"`
}

// GetMeta returns the stored metadata for id, or false if there is none
// matching the source file's current mtime and size.
func (s *DB) GetMeta(id string, mtime, size int64) (Meta, bool) {
	var m Meta
	ok := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(metaBucket).Get([]byte(id))
		if v == nil || json.Unmarshal(v, &m) != nil {
			return nil
		}
		ok = m.MTime == mtime && m.Size == size
		return nil
	})
	if !ok {
		return Meta{}, false
	}
	return m, true
}

// PutMeta stores metadata for id, stamped with the file's mtime and size.
func (s *DB) PutMeta(id string, mtime, size int64, m Meta) error {
	m.MTime, m.Size = mtime, size
	v, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.db.Batch(func(tx *bolt.Tx) error {
		return tx.Bucket(metaBucket).Put([]byte(id), v)
	})
}

// PutMetas stores many metadata records in one transaction. Enrichment
// writes through this: a commit-plus-fsync per track caps a cold library
// at a few hundred items a second, while one transaction per flush makes
// the file reads the limit again.
func (s *DB) PutMetas(metas map[string]Meta) error {
	if len(metas) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(metaBucket)
		for id, m := range metas {
			if err := putJSON(b, id, m); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- the index itself -----------------------------------------------------------

// Item is one indexed file as persisted between runs, so a restart can show
// the library before the filesystem walk has finished. It is a cache: the
// scan reconciles every record against what is actually on disk.
type Item struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// PathBytes carries Path when it is not valid UTF-8, which plenty of
	// real file names are not. encoding/json replaces every such byte with
	// U+FFFD without saying so, and a path that has been through that names
	// a file which does not exist: the warm start would restore an item that
	// cannot be opened, under an id that hashes to something else again.
	// JSON encodes a []byte as base64 and gives it back unchanged, so the
	// bytes survive. Written only when they have to be, and never read by
	// anything but the two helpers below.
	PathBytes []byte `json:"pathb,omitempty"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	MTime     int64  `json:"mtime"`
	// FirstSeen is when the index first saw the path; it survives here so
	// that a restart does not re-date the whole library as newly added.
	FirstSeen int64  `json:"added,omitempty"`
	Duration  int64  `json:"duration,omitempty"`
	Title     string `json:"title,omitempty"`
	Artist    string `json:"artist,omitempty"`
	Album     string `json:"album,omitempty"`
	Genre     string `json:"genre,omitempty"`
	Track     int    `json:"track,omitempty"`
	Year      int    `json:"year,omitempty"`
	VCodec    string `json:"vcodec,omitempty"`
	ACodec    string `json:"acodec,omitempty"`
	// Enriched records that the file has already been examined for
	// metadata, so files that simply have none are not re-read every run.
	Enriched bool `json:"enriched,omitempty"`
	// Shape is the same reading marker as Meta.Shape, mirrored here because
	// Enriched cannot stand for it: the whole library was examined for tags
	// before there was anywhere to keep a size, so without this a warm start
	// would restore every one of those items as finished and never read one
	// again.
	Shape int `json:"shape,omitempty"`
}

// Items returns every persisted index record.
func (s *DB) Items() ([]Item, error) {
	var out []Item
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(itemBucket)
		// Not presized from b.Stats(): that walks every page of the bucket
		// to count keys, which read the whole bucket twice on a warm start.
		return b.ForEach(func(_, v []byte) error {
			var it Item
			if json.Unmarshal(v, &it) == nil {
				it.restorePath()
				if it.Path != "" {
					out = append(out, it)
				}
			}
			return nil // a corrupt record is simply re-scanned
		})
	})
	return out, err
}

// encodePath returns the record with its path preserved as bytes when it is
// not valid UTF-8, and unchanged otherwise (which is nearly every path).
func (it Item) encodePath() Item {
	if !utf8.ValidString(it.Path) {
		it.PathBytes = []byte(it.Path)
	}
	return it
}

// restorePath puts the bytes back, for a record that needed them.
func (it *Item) restorePath() {
	if len(it.PathBytes) > 0 {
		it.Path = string(it.PathBytes)
		it.PathBytes = nil
	}
}

// SaveItems writes the given records and deletes the given ids in one
// transaction.
func (s *DB) SaveItems(put []Item, remove []string) error {
	if len(put) == 0 && len(remove) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(itemBucket)
		for _, it := range put {
			if err := putJSON(b, it.ID, it.encodePath()); err != nil {
				return err
			}
		}
		for _, id := range remove {
			if err := b.Delete([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- detected borders ---------------------------------------------------------
//
// Where the picture is inside a file's frame, which costs several seconds of
// ffmpeg to find and never changes for a file that has not changed. Stamped
// with mtime and size like the thumbnails, so a file rewritten under the
// same name is measured again rather than cropped by yesterday's answer.

// GetCrop returns what was stored for this file, if anything was.
func (s *DB) GetCrop(id string, mtime, size int64) ([]byte, bool) {
	var data []byte
	_ = s.db.View(func(tx *bolt.Tx) error {
		data = unstamp(tx.Bucket(cropBucket).Get([]byte(id)), mtime, size)
		return nil
	})
	return data, data != nil
}

// PutCrop remembers it. An empty answer is worth storing too: "there are no
// borders here" is exactly as expensive to find out as the other kind.
func (s *DB) PutCrop(id string, mtime, size int64, data []byte) error {
	v := stamp(mtime, size, data)
	// Batched like the thumbnails: the player asks per film opened, and a
	// burst of them serialising an fsync each is what Batch is for.
	return s.db.Batch(func(tx *bolt.Tx) error {
		return tx.Bucket(cropBucket).Put([]byte(id), v)
	})
}

// --- shortlinks ------------------------------------------------------------
//
// A shortlink is a name for somewhere in the app: a performer, a genre, a
// programme, a search, or one particular film, photograph or track. What is
// stored is the app's own state — the same string the address bar carries —
// so this layer never has to understand a view to be able to point at one,
// and a view invented later needs nothing here.
//
// They are kept both ways round. The code finds the target, which is what a
// visitor needs; and the target finds the code, so that asking twice for a
// link to the same thing gives the same link back rather than filling the
// database with synonyms for one place. Neither expires: a link that has
// been sent to somebody is worth more than the hundred bytes it occupies,
// and deleting the database invalidates every one of them, which is the same
// promise the signing key and the thumbnail epoch make.
//
// **Every key begins with the hostname the link was made on**, and that is
// what stops one crossing between the faces of a library. One server answers
// under several names — a face of music, a face of films, the whole thing —
// and until this was so, a link made on one of them resolved on all of them:
// the hostname in the address was decoration, and a code sent to somebody for
// one face opened another if they changed the name in front of it. Scoped by
// host, a code means something only where it was minted; anywhere else it
// reads as a code nobody made, which is what it is.

// The two directions live in one bucket under prefixes, since they are
// written in one transaction and are meaningless apart. The host is a key
// component rather than part of the code, so a code cannot be made to answer
// for a host it was not minted on by any amount of guessing.
const (
	linkByCode   = "c"
	linkByTarget = "t"
)

// linkKey builds one. NUL separates the host from what follows because
// neither a hostname nor a stored target can contain one — a target is
// checked to be printable ASCII before it is ever stored — so no host and
// code can be made to spell another pair's key.
func linkKey(kind, host, rest string) []byte {
	return []byte(kind + host + "\x00" + rest)
}

// Link returns where this code points on this host, if anywhere.
func (s *DB) Link(host, code string) (string, bool) {
	var target string
	found := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(linkBucket).Get(linkKey(linkByCode, host, code)); v != nil {
			target, found = string(v), true
		}
		return nil
	})
	return target, found
}

// LinkFor returns the code already minted on this host for this target.
func (s *DB) LinkFor(host, target string) (string, bool) {
	var code string
	found := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(linkBucket).Get(linkKey(linkByTarget, host, target)); v != nil {
			code, found = string(v), true
		}
		return nil
	})
	return code, found
}

// PutLink records a code and its target, in one transaction so that a reader
// can never find one direction without the other.
func (s *DB) PutLink(host, code, target string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(linkBucket)
		if err := b.Put(linkKey(linkByCode, host, code), []byte(target)); err != nil {
			return err
		}
		return b.Put(linkKey(linkByTarget, host, target), []byte(code))
	})
}

// --- item flags -----------------------------------------------------------

// Flags is what the owner recorded about one item. It is the only thing in
// this database that cannot be regenerated from the media, so it is keyed by
// item ID alone — hiding a file says nothing about its bytes, and the record
// has to outlive both a re-encode and the file itself.
type Flags struct {
	Hidden    bool `json:"hidden,omitempty"`
	Favourite bool `json:"favourite,omitempty"`
	// Rotation is quarter turns clockwise, 0-3. A camera held sideways is a
	// property of the file rather than of whoever is watching it, so the
	// correction is remembered here and applies wherever it is opened.
	Rotation int `json:"rotation,omitempty"`
	// NoCrop keeps the black borders this file carries. They are trimmed by
	// default where they are found, and this is how a viewer says not to —
	// a film framed at 2.39:1 is meant to have them.
	NoCrop bool `json:"nocrop,omitempty"`
}

// GetFlags returns the flags stored for id, or false if it has none.
func (s *DB) GetFlags(id string) (Flags, bool) {
	var f Flags
	ok := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(flagBucket).Get([]byte(id))
		ok = v != nil && json.Unmarshal(v, &f) == nil
		return nil
	})
	if !ok {
		return Flags{}, false
	}
	return f, true
}

// AllFlags returns every stored record, keyed by item ID. The library reads
// them in one go at startup: they are consulted on every listing.
func (s *DB) AllFlags() (map[string]Flags, error) {
	out := make(map[string]Flags)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(flagBucket).ForEach(func(k, v []byte) error {
			var f Flags
			if json.Unmarshal(v, &f) == nil {
				out[string(k)] = f
			}
			return nil // a corrupt record just loses that one judgement
		})
	})
	return out, err
}

// SaveFlags writes many records in one transaction — culling a multi-selection
// must not cost a commit and an fsync per file. An entry whose flags are all
// false is deleted rather than stored: there is nothing left to remember, and
// the bucket stays as small as the set the owner actually marked.
func (s *DB) SaveFlags(set map[string]Flags) error {
	if len(set) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(flagBucket)
		for id, f := range set {
			if f == (Flags{}) {
				if err := b.Delete([]byte(id)); err != nil {
					return err
				}
				continue
			}
			if err := putJSON(b, id, f); err != nil {
				return err
			}
		}
		return nil
	})
}

// putJSON stores one record as JSON. A record that will not marshal fails
// the write rather than being skipped in silence: three writers used to
// `continue` past it, and a value that cannot be written down is a fault
// the caller logs, not one to discover at the next restart.
func putJSON(b *bolt.Bucket, id string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("record %s: %w", id, err)
	}
	return b.Put([]byte(id), data)
}

// Prune deletes everything cached for items that no longer exist: index
// records, metadata and thumbnails (whose keys are "<id>|<width>"). It
// reports how many keys were removed. Call it only after a full successful
// scan — with a partial view of the library it would throw away good data.
//
// Flags are deliberately not in the list: they are the owner's, not a cache,
// and a file that comes back (a disk remounted, a rename undone) must find
// them again.
func (s *DB) Prune(live map[string]struct{}) (int, error) {
	n := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{itemBucket, metaBucket, thumbBucket, featBucket} {
			b := tx.Bucket(name)
			var stale [][]byte
			err := b.ForEach(func(k, _ []byte) error {
				id, _, _ := bytes.Cut(k, []byte("|")) // thumbs are id|width
				if _, ok := live[string(id)]; !ok {
					stale = append(stale, bytes.Clone(k))
				}
				return nil
			})
			if err != nil {
				return err
			}
			for _, k := range stale {
				if err := b.Delete(k); err != nil {
					return err
				}
				n++
			}
		}
		return nil
	})
	return n, err
}

// --- audio features -----------------------------------------------------------

// PutFeatures stores how a track sounds, stamped with the file it was read
// from and the version of the recipe: a stamp that no longer matches, or an
// older recipe, means the track is read again.
func (s *DB) PutFeatures(id string, mtime, size int64, version int, vec []float32) error {
	body := make([]byte, 4+4*len(vec))
	binary.BigEndian.PutUint32(body, uint32(version))
	for i, x := range vec {
		binary.LittleEndian.PutUint32(body[4+4*i:], math.Float32bits(x))
	}
	v := stamp(mtime, size, body)
	return s.db.Batch(func(tx *bolt.Tx) error {
		return tx.Bucket(featBucket).Put([]byte(id), v)
	})
}

// EachFeatures walks every stored vector.
func (s *DB) EachFeatures(fn func(id string, mtime, size int64, version int, vec []float32)) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(featBucket).ForEach(func(k, v []byte) error {
			if len(v) < thumbHeaderLen+4 {
				return nil
			}
			mtime := int64(binary.BigEndian.Uint64(v))
			size := int64(binary.BigEndian.Uint64(v[8:]))
			body := v[thumbHeaderLen:]
			version := int(binary.BigEndian.Uint32(body))
			vec := make([]float32, (len(body)-4)/4)
			for i := range vec {
				vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(body[4+4*i:]))
			}
			fn(string(k), mtime, size, version, vec)
			return nil
		})
	})
}
