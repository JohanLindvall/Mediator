package library

import "time"

// A file's modification time is what the file says, and a file can say
// anything: a handful here claim to have been written in 2097, stamped by
// whatever copied them there. Shown as it is — that is what the file says —
// but not believed where it orders things: newest first, those four sat at
// the head of the whole library and would have for seventy years, and a
// release or a show holding one would have been the newest of its kind.
//
// So a time that is later than now is not a time. The listing sorts it after
// every real one whichever way it runs, the rule the grouped views already
// follow for a key they do not have (orderBy), and a collection's modified
// time — the newest of its members' — is taken from the real ones only, a
// collection with none having none.

// mtimeSlack is how far ahead of this machine's clock a file's time may be
// and still be taken for one: clocks disagree — a network share's, a
// camera's — by minutes or hours, and a day covers that without letting in
// anything that is plainly wrong.
const mtimeSlack = 24 * time.Hour

// knownTime says whether a modification time, in unix milliseconds, is one a
// file can really have.
func knownTime(ms, now int64) bool {
	return ms <= now+mtimeSlack.Milliseconds()
}

// nowMillis is the clock the rule is judged against, a variable only so a
// test can hold it still.
var nowMillis = func() int64 { return time.Now().UnixMilli() }
