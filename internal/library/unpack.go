package library

// Packed members unpacked into the scratch space, for playback.
//
// A film inside an archive, deflated or compressed by rar, is unpacked once
// into the working space the rewrapper and the segmented converter share —
// the same directory tree, the same budget (-tmp, -tmp-max) — and served from
// there as an ordinary file, with every range a browser asks for answered at
// once. It is the rewrap's arrangement and kept to the rewrap's rules,
// because it is the same problem: one copy per member, keyed by what the
// member is, made by whoever asks first and waited for by the rest; made
// beside its real name and moved into place, so an interrupted run leaves
// nothing a later one would trust; checked against the archive's own
// checksum before it is believed; counted against the budget before it is
// written rather than after; the least recently wanted freed first; and kept
// across a restart, keyed by identity, so a film unpacked last night is not
// unpacked again this morning. The unpacking belongs to no request — the
// browser that asked first may have gone by the time it is done — and is
// stopped at shutdown.
//
// A copy is not pruned within unpackKeepFor of being last asked for, the
// rewrapper's rule and for its reason: a player works through a film in range
// requests minutes apart, every one of which opens the copy afresh, so one
// pruned between two of them is a film unpacked all over again in the middle
// of being watched.

import (
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/flate"
	"github.com/nwaples/rardecode/v2"
)

// ScratchSpace is the working space the converters share — server.Scratch,
// seen from here: a corner of its own, and one budget for all of them.
type ScratchSpace interface {
	Sub(name string) (string, error)
	Report(owner string, bytes int64)
	Excess() int64
	Limit() int64
}

// ErrNoUnpack says no copy of a member can be made: there is no scratch space,
// the member is larger than the whole budget, or it is not packed at all. The
// caller reads it as it is packed instead.
var ErrNoUnpack = errors.New("no unpacked copy can be made")

// ErrDamagedMember is a member whose bytes do not unpack to what its archive
// says they are: a stream that breaks off, a checksum that does not match,
// fewer bytes than were promised. Measured on these disks: two films split
// into pieces by a file host, each a single deflated member, broke off 137 MB
// and 620 MB in — and Python's own zip reader stops at the same bytes. A
// verdict on the file, unlike a read that failed, so it is remembered for the
// run by the member's identity: asked again, it would unpack the same six
// hundred megabytes to fail at the same place.
var ErrDamagedMember = errors.New("the archive it is in is damaged")

// damage says whether an unpacking failed on the bytes themselves rather than
// on reading them, and is the error to give for it.
func damage(err error) (error, bool) {
	var corrupt flate.CorruptInputError
	switch {
	case errors.As(err, &corrupt), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, errUnpackedWrong):
	case slices.ContainsFunc(rarDamage, func(e error) bool { return errors.Is(err, e) }):
	default:
		return err, false
	}
	return fmt.Errorf("%w (%w)", ErrDamagedMember, err), true
}

// rarDamage is what the rar decoder says of bytes that are not what they
// should be — as against a set it cannot open at all, encrypted or solid,
// which the parse has already told apart.
var rarDamage = []error{
	rardecode.ErrBadFileChecksum, rardecode.ErrShortFile, rardecode.ErrUnexpectedArcEnd,
	rardecode.ErrDecoderOutOfData, rardecode.ErrCorruptBlockHeader, rardecode.ErrCorruptFileHeader,
	rardecode.ErrBadHeaderCRC, rardecode.ErrCorruptPPM, rardecode.ErrInvalidFileBlock,
	rardecode.ErrHuffDecodeFailed, rardecode.ErrInvalidLengthTable, rardecode.ErrCorruptDecodeHeader,
	rardecode.ErrInvalidFilter, rardecode.ErrInvalidVMInstruction,
}

// errUnpackedWrong is an unpacking that came to the wrong size or checksum.
var errUnpackedWrong = errors.New("unpacked to something other than the archive says")

const (
	// unpackSub is the corner of the scratch space the copies live in.
	unpackSub = "unpack"
	// unpackOwner is the name the copies are reported to the budget under.
	unpackOwner = "unpack"
	// unpackTimeout bounds one unpacking: generous, since a rar-compressed
	// film of several gigabytes unpacks at tens of megabytes a second.
	unpackTimeout = 30 * time.Minute
	// unpackAtOnce is how many unpackings run at a time. Each is a core and
	// a disk's worth of writing, beside whatever is being watched.
	unpackAtOnce = 2
)

// unpackKeepFor protects a copy from pruning after it was last asked for — the
// rewrapper's remuxKeepFor. A variable only so a test can do without it.
var unpackKeepFor = 5 * time.Minute

// unpackFrom is the size from which a packed member being streamed is served
// from an unpacked copy. Below it, unpacking from the top for every range a
// browser asks for costs a tenth of a second or so, and a copy would be a
// write to the disk for nothing. A variable only so a test need not make a
// member of thirty-two megabytes to reach the other side of it.
var unpackFrom int64 = 32 << 20

type unpacker struct {
	mu      sync.Mutex
	space   ScratchSpace
	dir     string
	log     *slog.Logger
	entries map[string]*unpackEntry
	damaged map[string]error // members that do not unpack, by identity, for the run
	total   int64            // bytes held by finished copies
	pending int64            // bytes being written
	seq     int64
	closed  bool
	slots   chan struct{}
}

type unpackEntry struct {
	path   string
	size   int64
	done   chan struct{}
	err    error
	used   int64     // order of the last ask, for pruning least recently wanted first
	last   time.Time // when it was last asked for (unpackKeepFor)
	cancel context.CancelFunc
}

// unpacks is the process's one unpacker, as the scratch space is one.
var unpacks = newUnpacker()

func newUnpacker() *unpacker {
	return &unpacker{entries: map[string]*unpackEntry{}, damaged: map[string]error{}, slots: make(chan struct{}, unpackAtOnce)}
}

// SetScratch gives the unpacker its corner of the scratch space and takes in
// the copies an earlier run left there. Called once, at startup; until then
// nothing is unpacked to the disk and playback reads packed members as they
// are.
func SetScratch(space ScratchSpace, log *slog.Logger) error {
	dir, err := space.Sub(unpackSub)
	if err != nil {
		return err
	}
	u := unpacks
	u.mu.Lock()
	defer u.mu.Unlock()
	u.space, u.dir, u.log, u.closed = space, dir, log, false
	u.adoptLocked()
	return nil
}

// CloseScratch stops the unpackings in flight; what has finished stays on the
// disk for the next run to take in.
func CloseScratch() {
	u := unpacks
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	for _, e := range u.entries {
		if e.cancel != nil {
			e.cancel()
		}
	}
}

// unpackName is a finished copy's file name: what the member is, hashed, and
// the member's own extension.
var unpackName = regexp.MustCompile(`^([0-9a-f]{40})(\.[a-z0-9]{1,8})?$`)

// unpackKey is a member's identity: where it is, and the time and size it was
// indexed with — the container changing is the member changing.
func unpackKey(it Item) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s\x00%d\x00%d", it.Path, it.ModTime, it.Size)))
	return hex.EncodeToString(h[:])
}

// unpackExt is an extension a copy may carry: the member's own, where it is
// a plain one.
var unpackExt = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)

func unpackFile(it Item) string {
	ext := strings.ToLower(filepath.Ext(it.stored.name))
	if !unpackExt.MatchString(ext) {
		ext = ""
	}
	return unpackKey(it) + ext
}

// adoptLocked takes in what an earlier run left: finished copies are counted
// and offered, oldest least wanted; half-written ones are removed.
func (u *unpacker) adoptLocked() {
	ents, err := os.ReadDir(u.dir)
	if err != nil {
		return
	}
	type found struct {
		key, path string
		size      int64
		mod       time.Time
	}
	var fs []found
	for _, d := range ents {
		path := filepath.Join(u.dir, d.Name())
		if strings.HasSuffix(d.Name(), ".part") {
			_ = os.Remove(path)
			continue
		}
		m := unpackName.FindStringSubmatch(d.Name())
		if m == nil || !d.Type().IsRegular() {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		fs = append(fs, found{m[1], path, info.Size(), info.ModTime()})
	}
	slices.SortFunc(fs, func(a, b found) int { return a.mod.Compare(b.mod) })
	for _, f := range fs {
		if _, ok := u.entries[f.key]; ok {
			continue
		}
		done := make(chan struct{})
		close(done)
		u.seq++
		u.entries[f.key] = &unpackEntry{path: f.path, size: f.size, done: done, used: u.seq, last: f.mod}
		u.total += f.size
	}
	u.space.Report(unpackOwner, u.total)
}

// ready is a finished copy of the member, where there is one.
func (u *unpacker) ready(it Item) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.entries[unpackKey(it)]
	if !ok {
		return "", false
	}
	select {
	case <-e.done:
	default:
		return "", false
	}
	if e.err != nil {
		return "", false
	}
	u.seq++
	e.used, e.last = u.seq, time.Now()
	return e.path, true
}

// Unpacked is the path of a copy of a packed member in the scratch space,
// unpacking it first where there is none. The unpacking belongs to no
// caller: it goes on for whoever asks next if this one gives up waiting.
func Unpacked(ctx context.Context, it Item) (string, error) {
	return unpacks.unpacked(ctx, it)
}

func (u *unpacker) unpacked(ctx context.Context, it Item) (string, error) {
	if !it.packed() {
		return "", ErrNoUnpack
	}
	u.mu.Lock()
	if u.space == nil || u.closed {
		u.mu.Unlock()
		return "", ErrNoUnpack
	}
	// Larger than the whole budget: it would evict everything else and still
	// not fit, so it is read as it is packed.
	limit := u.space.Limit()
	if limit > 0 && it.Size > limit {
		u.mu.Unlock()
		return "", ErrNoUnpack
	}
	key := unpackKey(it)
	if err, ok := u.damaged[key]; ok {
		u.mu.Unlock()
		return "", err
	}
	if e, ok := u.entries[key]; ok {
		u.seq++
		e.used, e.last = u.seq, time.Now()
		u.mu.Unlock()
		return u.wait(ctx, e)
	}
	// Room is made before writing, counting what is being written already.
	if limit > 0 {
		u.pruneLocked(u.total + u.pending + it.Size - limit)
	}
	u.seq++
	e := &unpackEntry{path: filepath.Join(u.dir, unpackFile(it)), size: it.Size, used: u.seq, last: time.Now(), done: make(chan struct{})}
	// Stoppable from the moment it is published, as a rewrap is: CloseScratch
	// cancels what it finds, and an entry without a cancel would be missed.
	work, stop := context.WithTimeout(context.Background(), unpackTimeout)
	e.cancel = stop
	u.entries[key] = e
	u.pending += it.Size
	u.mu.Unlock()
	go u.produce(work, stop, it, key, e)
	return u.wait(ctx, e)
}

func (u *unpacker) wait(ctx context.Context, e *unpackEntry) (string, error) {
	select {
	case <-e.done:
		if e.err != nil {
			return "", e.err
		}
		return e.path, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// produce unpacks one member into place and settles the books before anybody
// is told it is there.
func (u *unpacker) produce(ctx context.Context, stop context.CancelFunc, it Item, key string, e *unpackEntry) {
	defer stop()
	part := e.path + ".part"
	select {
	case u.slots <- struct{}{}:
		e.err = unpackTo(ctx, it, part)
		<-u.slots
	case <-ctx.Done():
		e.err = ctx.Err()
	}
	if e.err == nil {
		e.err = os.Rename(part, e.path)
	}
	if e.err != nil {
		_ = os.Remove(part)
	}
	wrote := e.err == nil
	u.mu.Lock()
	u.pending -= it.Size
	closed := u.closed
	if wrote && !closed {
		u.total += e.size
		e.last = time.Now()
		u.space.Report(unpackOwner, u.total)
		u.pruneLocked(u.space.Excess())
	} else {
		// Not remembered: a later ask deserves a fresh attempt, and a copy
		// that landed as the process was closing is left on the disk for the
		// next run to take in rather than handed out by this one. Bytes that
		// do not unpack are the one exception, being a property of the file.
		delete(u.entries, key)
		if wrote && closed {
			e.err = ErrNoUnpack
		}
		if err, bad := damage(e.err); bad {
			e.err = err
			u.damaged[key] = err
		}
	}
	u.mu.Unlock()
	if e.err != nil && !errors.Is(e.err, context.Canceled) && u.log != nil {
		u.log.Warn("could not unpack an archive member", "path", it.Rel, "err", e.err)
	}
	close(e.done)
}

// unpackTo writes a packed member's content to path, checking it came to the
// size the archive says and, where the archive records one, its checksum.
func unpackTo(ctx context.Context, it Item, path string) error {
	src, err := openUnpacking(it)
	if err != nil {
		return err
	}
	defer src.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	sum := crc32.NewIEEE()
	n, err := io.Copy(io.MultiWriter(f, sum), ctxReader{ctx, src})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != it.stored.size {
		return fmt.Errorf("%w: %d bytes where it says %d", errUnpackedWrong, n, it.stored.size)
	}
	if p := it.stored.pack; p.format == packZip && sum.Sum32() != p.crc {
		return fmt.Errorf("%w: checksum %08x where it says %08x", errUnpackedWrong, sum.Sum32(), p.crc)
	}
	return nil
}

// pruneLocked frees at least need bytes of finished copies, least recently
// wanted first, never one asked for within unpackKeepFor — being over a
// budget the operator chose is the lesser wrong. Called with the lock.
func (u *unpacker) pruneLocked(need int64) {
	if need <= 0 {
		return
	}
	type aged struct {
		key  string
		used int64
	}
	now := time.Now()
	var order []aged
	for k, e := range u.entries {
		select {
		case <-e.done:
			if e.err == nil && now.Sub(e.last) >= unpackKeepFor {
				order = append(order, aged{k, e.used})
			}
		default:
		}
	}
	slices.SortFunc(order, func(a, b aged) int { return cmp.Compare(a.used, b.used) })
	for _, a := range order {
		if need <= 0 {
			break
		}
		e := u.entries[a.key]
		delete(u.entries, a.key)
		u.total -= e.size
		need -= e.size
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) && u.log != nil {
			u.log.Debug("scratch: could not remove", "path", e.path, "err", err)
		}
	}
	u.space.Report(unpackOwner, u.total)
	if need > 0 && u.log != nil {
		u.log.Info("scratch budget exceeded; every unpacked copy held is in use",
			"over", need, "held", len(u.entries))
	}
}

// OpenForPlayback opens an item to be streamed: a packed member of any size
// from an unpacked copy in the scratch space, made if there is none, and
// everything else as OpenItem opens it. Where no copy can be made — no
// scratch space, a member larger than the budget — the member is streamed as
// it is packed, which plays and seeks slowly rather than not at all.
func OpenForPlayback(ctx context.Context, it Item) (File, error) {
	if it.packed() && it.Size >= unpackFrom {
		p, err := Unpacked(ctx, it)
		switch {
		case err == nil:
			if f, err := os.Open(p); err == nil {
				return f, nil
			}
		case errors.Is(err, ErrNoUnpack):
		default:
			return nil, err
		}
	}
	return OpenItem(it)
}
