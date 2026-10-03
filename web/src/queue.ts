/**
 * The arithmetic of the play order, kept out of the player so it can be
 * tested: the order is a list of queue indices, and what "after everything
 * already queued" means is a question about that list and nothing else.
 */
import { trackTitle } from './format.ts';

/** Fisher–Yates, in place; `rand` is injectable so a test can pin the result. */
export function shuffleInPlace(arr: number[], rand: () => number = Math.random): void {
  for (let i = arr.length - 1; i > 0; i--) {
    const j = Math.floor(rand() * (i + 1));
    [arr[i], arr[j]] = [arr[j]!, arr[i]!];
  }
}

/**
 * Put `count` new queue entries, numbered from `first`, at the end of the
 * order — in the order they came, or shuffled among themselves when the
 * player is shuffling, since "at the end" then means after everything else
 * and in no particular order, never mixed in among what was already there.
 * Returns the position in the order where the new entries begin, which is
 * where a player that had run out of queue goes next.
 */
export function appendToOrder(
  order: number[],
  first: number,
  count: number,
  shuffle: boolean,
  rand: () => number = Math.random,
): number {
  const at = order.length;
  const idx = Array.from({ length: count }, (_, i) => first + i);
  if (shuffle) shuffleInPlace(idx, rand);
  // Pushed one by one: a spread of a hundred thousand arguments is more
  // than a call stack takes, and a queue of the whole library is that big.
  for (const i of idx) order.push(i);
  return at;
}

/**
 * Where the order goes after `pos`: the next position, the first again with
 * repeat on, or null at the end — and null too with shuffle and repeat
 * together, since then the next order is dealt fresh and which track comes
 * up is not knowable yet.
 */
export function nextPosition(pos: number, length: number, repeat: boolean, shuffle: boolean): number | null {
  if (pos + 1 < length) return pos + 1;
  if (repeat && length > 0 && !shuffle) return 0;
  return null;
}

/**
 * Move `first` to the front of the order when it is in it: the track the
 * listener started from plays first whatever the shuffle dealt.
 */
export function placeFirst(order: number[], first: number): void {
  const at = order.indexOf(first);
  if (at > 0) {
    order.splice(at, 1);
    order.unshift(first);
  }
}

/**
 * The rows of a windowed list worth drawing: those in view plus `margin`
 * either side for the scroll, clamped to the list.
 */
export function windowRows(
  scrollTop: number,
  height: number,
  rowHeight: number,
  total: number,
  margin: number,
): { first: number; last: number } {
  const first = Math.max(0, Math.floor(scrollTop / rowHeight) - margin);
  const last = Math.min(total, Math.ceil((scrollTop + height) / rowHeight) + margin);
  return { first, last };
}

/**
 * Whether a queue's time column has to be wide enough for hours.
 *
 * The column keeps one width all the way down, or the performer beside it
 * steps left on every track of ten minutes or more, the one extra digit
 * pushing it over. That width is "59:59" unless a track in the queue runs an
 * hour, and then "9:59:59". Asked of the whole queue rather than of the rows
 * in view, so scrolling never moves the column — and of only what was added
 * since it was last asked (`from`), since a queue grows a batch at a time and
 * can hold the whole library.
 */
export function runsHours(tracks: readonly { duration?: number }[], from = 0): boolean {
  for (let i = from; i < tracks.length; i++) {
    if ((tracks[i]?.duration ?? 0) >= 3_600_000) return true;
  }
  return false;
}

/**
 * Whether pressing play would continue rather than start over. Nothing
 * loaded, a failed element and a queue that has played out are not
 * continuable; nor is the final boundary with nothing after it, where
 * `play()` on an ended element restarts it from zero — unless repeat is on,
 * because then the next step wraps. AudioPlayer.canResume says why each
 * of these is what it is.
 */
export function resumable(s: {
  loaded: boolean;
  failed: boolean;
  exhausted: boolean;
  ended: boolean;
  atLast: boolean;
  repeat: boolean;
}): boolean {
  if (!s.loaded || s.failed || s.exhausted) return false;
  return !(s.ended && s.atLast && !s.repeat);
}

/**
 * The sounding deck is never allowed to end: it loops, and the jump back to
 * its start is where the bar moves on. iOS gives up a page's audio session
 * the moment an element plays to its natural end with nothing else playing
 * (WebKit, since iOS 17), and a locked phone cannot get it back — the next
 * track's `play()` then waits, silent, until the app is opened. Seen in the
 * server's log from a phone in a car: an album's second track ended with the
 * page in the background, and its third was not asked for until nearly seven
 * minutes later, after the page had been brought back to the front.
 * A loop never ends, so the session is never given up, and the next track is
 * started while the last one still holds it.
 *
 * This says whether the deck has just gone round. Every seek the bar makes
 * moves `last` with it, so a jump backwards that nobody here asked for, into
 * the opening seconds and from the closing stretch of the track, is the loop
 * and nothing else. Ordinary playback only ever moves forward, and a clock
 * reading a few milliseconds behind itself is not a jump: the jump has to be
 * half a second, or half of where the clock was for a track shorter than a
 * second, and never from the first fifth of a second, where a jitter would
 * otherwise read as a wrap before the length is even known. Where
 * the length is unknown the closing stretch cannot be asked about, and the
 * jump alone decides: a loop left undetected would play the same track for
 * ever, which is worse than moving on at a seek nobody routed through here.
 */
export const WRAP_START_S = 3;
export const WRAP_BACK_S = 0.5;
export const WRAP_FROM_S = 0.2;
export const WRAP_END_S = 5;
export const WRAP_END_FRACTION = 0.05;

export function wrappedAround(last: number, now: number, duration: number): boolean {
  if (!Number.isFinite(last) || !Number.isFinite(now)) return false;
  if (!(last > WRAP_FROM_S) || !(now < WRAP_START_S) || !(last - now > Math.min(WRAP_BACK_S, last / 2))) return false;
  if (!Number.isFinite(duration) || duration <= 0) return true;
  return last >= duration - Math.max(WRAP_END_S, duration * WRAP_END_FRACTION);
}

/**
 * What a ripper or a release writes where a track has no name: "Untitled",
 * "[untitled]", "Track 3", "track01". Such a title names no song, so it
 * makes no recording and no song of one — a performer's untitled pieces are
 * as many different songs as there are of them. The server asks the same
 * question with the same pattern (placeholderTitle in similar.go).
 */
const PLACEHOLDER = /^[[({]?\s*(?:untitled(?: track)?|no title|unknown(?: title)?|unnamed|track)\s*[#.\-_]?\s*\d*\s*[\])}]?$/i;

export function namesNothing(title: string): boolean {
  return title.length <= 32 && PLACEHOLDER.test(title.trim());
}

/**
 * The words that say a bracket, or a tail after a dash, names a version of a
 * song rather than a song: "(Live at …)", "[Demo 1994]", "- 2011 Remaster",
 * "(… cover)", "(feat. …)". Read off this library's own titles — every
 * word inside a bracket or after a dash, counted — rather than guessed, and
 * kept to those: "(Part II)", "(Reprise)", "(Intro)" are different pieces
 * of music and say so, and a word that merely *begins* like one of these is
 * not one ("Demonic" is no demo). The few prefixes are compounds that only
 * ever mean one thing.
 */
const VERSION_WORDS = new Set(
  `live demo demos remaster remastered remastering remix remixed mix mixes edit edited version versions
  ver feat featuring ft bonus acoustic akustisk instrumental mono stereo single rerecorded rerecording
  rerecord take takes alternate alternative alt rehearsal rehearsals bootleg unplugged session sessions
  cover extended explicit clean outtake outtakes unreleased early rough unmixed acapella cappella
  karaoke original orchestral radio preproduction vinyl master mastered recording recorded rec studio
  deluxe anniversary expanded edition commentary raw rare rarity ep mcd lp cd split`.split(/\s+/),
);
const VERSION_PREFIXES = ['remaster', 'remix', 'rerecord', 'preprod'];

/**
 * Lower case, with the accents off and the apostrophes out: "Fjärdljus" and
 * "Fjardljus" are one title, and so are "Harbour's Edge" and "Harbours
 * Edge" — an apostrophe joins a word, and read as a space it made a second
 * song out of a possessive spelt two ways, which a real station queued twice.
 */
function folded(s: string): string {
  return s
    .normalize('NFKD')
    .replace(/\p{M}/gu, '')
    .replace(/['’‘`´]/g, '')
    .toLowerCase();
}

/** Letters and digits, one space between runs: what a title is made of. */
function wordsOf(s: string): string[] {
  return s.match(/[\p{L}\p{N}]+/gu) ?? [];
}

/** Whether a bracket's contents, or a dashed tail, name a version. */
function namesAVersion(text: string): boolean {
  const t = folded(text).replace(/\./g, ' ');
  // A year alone, or a span of them, dates a take: "(1994)", "(1991-92)".
  if (/^\s*\d{4}(\s*[-/]\s*\d{2,4})?\s*$/.test(t)) return true;
  // Hyphens both ways: "re-recorded" is one word, "porch-session" two.
  const words = [...wordsOf(t.replace(/-/g, ' ')), ...wordsOf(t.replace(/-/g, ''))];
  if (words.length === 0) return true;
  return words.some((w) => VERSION_WORDS.has(w) || VERSION_PREFIXES.some((p) => w.startsWith(p)));
}

const BRACKETED = /\s*[([{]([^()[\]{}]*)[)\]}]/g;
const DASHED = /\s+[-–—]\s+([^-–—]+)$/;

/**
 * A title as a song: what names a version taken off, the accents and the
 * punctuation folded away — "Low Tide (Live at the Pier)", "Low Tide -
 * 2011 Remaster" and "Low Tide." are one song. Empty where nothing that
 * names one is left.
 */
export function songTitle(title: string): string {
  let t = title;
  for (;;) {
    const before = t;
    t = t.replace(BRACKETED, (whole: string, inner: string) => (namesAVersion(inner) ? '' : whole));
    const tail = DASHED.exec(t);
    if (tail && namesAVersion(tail[1]!)) t = t.slice(0, tail.index);
    if (t === before) break;
  }
  const song = wordsOf(folded(t).replace(/&/g, ' and ')).join(' ');
  return /\p{L}/u.test(song) ? song : '';
}

/**
 * Where a credit to a guest begins in an artist tag: "feat.", "ft." or
 * "featuring" after a space, or a bracket opening on any of them. An
 * undotted "feat" outside a bracket is left, being as likely the end of a
 * band's own name. The server takes guests off the same way (station.go).
 */
const GUESTS = /\s+(?:feat\.|ft\.|featuring\s|[([]\s*(?:feat|ft|featuring)\b)/i;

/**
 * A song, whichever file and whichever version of it this is: what radio
 * remembers, so that it never plays one song twice.
 *
 * The server folds the copies of one *recording* out of every answer
 * (RecordingKey: the performer and the title as the tags spell them), and it
 * keeps a performance that says it is one — "… (Live)" is another title.
 * That is right for a queue somebody built: a live record queued is meant to
 * be heard. It is wrong for radio, which chooses on the listener's behalf:
 * the song back as a live take, a demo or a remaster is the same song again,
 * and a performer's catalogue is full of them — measured over this library,
 * 28,158 tagged tracks are 15,820 recordings and 14,938 songs. Radio for one
 * performer is where that shows. And the fold has to be here as well as
 * there, applied to what is already queued, since a copy one batch left
 * there would otherwise be matched by a different copy in the next.
 *
 * A file with no title is keyed by the title its name gives it
 * (`trackTitle`), where the server's key refuses to: a file name is not a
 * title to fold a queue on, but for radio the worst a wrong fold costs is a
 * song left for another day, where a missed one is the same song twice. A
 * name that leaves no letters ("01") is still no key, nor is a placeholder.
 */
export function songKey(t: RadioTrack): string {
  const title = t.title || (t.name ? trackTitle({ name: t.name, artist: t.artist, performer: t.performer }) : '');
  if (!title || namesNothing(title)) return '';
  const song = songTitle(title);
  if (!song) return '';
  const who = t.artist || t.performer || '';
  const at = who.search(GUESTS);
  return `${wordsOf(folded(at < 0 ? who : who.slice(0, at))).join(' ')}\u0000${song}`;
}

/**
 * How much a performer's weight falls for each of their tracks already
 * drawn or lately queued.
 *
 * Every track a performer records shares a voice, a producer and a decade,
 * so a resemblance answer about one of their songs is mostly their own
 * catalogue — measured on a radio batch of ten, five were the seed's band.
 * That is the right answer to "what sounds like this" and the wrong answer
 * to "what shall I play next".
 *
 * A third at each step, rather than a quota: the second track by a
 * performer is a third as likely as it was, the third a ninth, so they fade
 * instead of being cut off. A neighbourhood that really is one band — a
 * performer nobody else in the library resembles — still fills the batch,
 * since every weight falls together and nothing is ever zero.
 */
const ARTIST_DAMP = 1 / 3;

/** A performer's name as a key, or "" for a track that names nobody. */
function artistKey(t: { artist?: string }): string {
  return (t.artist ?? '').toLowerCase();
}

/**
 * Draw what radio plays next from the tracks that sound like the one
 * playing: `want` of them, none twice, the nearest likeliest, and not five
 * songs by one band.
 *
 * Taking the nearest few outright is what a resemblance answer is for, and
 * it makes a poor radio: the answer is the same every time it is asked, so
 * the same handful comes back, and their neighbours are that same handful
 * again. The weights are linear in the position — the nearest is as many
 * times likelier than the farthest as there are tracks to draw from — so
 * what plays still sounds like the seed, without sounding like it in the
 * same order every evening.
 *
 * `recent` is the performers already in the queue, which is what keeps one
 * band out of batch after batch: their tracks start damped rather than
 * being damped only once this batch has drawn them. A track that names
 * nobody is never damped — the unnamed are not one performer. Artist radio
 * passes a `damp` of 1: its pool is one performer by design, and damping
 * the tracks that name them while sparing one that names a guest besides
 * would favour the guest's.
 */
export function pickRadio<T extends { artist?: string }>(
  pool: T[],
  want: number,
  rand: () => number = Math.random,
  recent: readonly string[] = [],
  damp: number = ARTIST_DAMP,
): T[] {
  const left = pool.slice();
  const out: T[] = [];
  const drawn = new Map<string, number>();
  const count = (key: string): void => {
    if (key !== '') drawn.set(key, (drawn.get(key) ?? 0) + 1);
  };
  for (const name of recent) count(name.toLowerCase());
  while (out.length < want && left.length > 0) {
    const n = left.length;
    let total = 0;
    const weights = left.map((t, i) => {
      const w = (n - i) * damp ** (drawn.get(artistKey(t)) ?? 0);
      total += w;
      return w;
    });
    let r = rand() * total;
    let i = 0;
    for (; i < n - 1; i++) {
      r -= weights[i]!;
      if (r < 0) break;
    }
    const picked = left[i]!;
    left.splice(i, 1);
    out.push(picked);
    count(artistKey(picked));
  }
  return out;
}


/** The little of a track this module needs to tell one from another. */
export interface RadioTrack {
  id: string;
  artist?: string;
  title?: string;
  /** The file's name, which titles a track nothing tagged (songKey). */
  name?: string;
  /** Who its release is by, where the file names nobody (Item.performer). */
  performer?: string;
}

/**
 * What of a batch of similar tracks is worth queueing: not what is queued
 * already, by file or by song (songKey).
 *
 * The second half of that is the one that matters. Radio asks again every
 * few tracks, and each answer is drawn from the same neighbourhood as the
 * last — so the song that has just played comes back at once, in a different
 * file. Measured on a real library: one song is there nine times over, on
 * three live records, four bootlegs and two albums, all tagged alike, and
 * four of them arrived in one batch. And in another version: the live take
 * of a song is its nearest neighbour after its own copies.
 *
 * The queue is the whole memory, played part included, so this holds for
 * every later batch and not only the next.
 */
export function freshForRadio<T extends RadioTrack>(pool: T[], queued: RadioTrack[]): T[] {
  const ids = new Set<string>();
  const heard = new Set<string>();
  for (const t of queued) {
    ids.add(t.id);
    const key = songKey(t);
    if (key !== '') heard.add(key);
  }
  // Within the batch too (freshFrom keeps adding as it goes): the server
  // deduplicates one answer, but a page open across a restart may hold a
  // copy from before it did.
  return freshFrom(pool, ids, heard);
}

/**
 * How many tracks follow the one playing in the order — what radio tops up
 * against. Spelled once here rather than as `order.length - 1 - pos` in two
 * places that could drift.
 */
export function ahead(orderLength: number, orderPos: number): number {
  return orderLength - 1 - orderPos;
}

/**
 * The performers of the tracks lately played: the one on now and the `n`
 * before it, read backwards through the order.
 *
 * Radio damps a performer already in earshot, and "in earshot" is what has
 * played, not the tail of a shuffled queue — with the whole library shuffled
 * in, `queue.slice(-n)` is an arbitrary handful of library-order entries, so
 * the damping was fed noise. This follows the order the player is actually
 * walking.
 */
export function recentArtists(
  order: number[],
  queue: { artist?: string }[],
  orderPos: number,
  n: number,
): string[] {
  const out: string[] = [];
  for (let p = orderPos; p >= 0 && out.length < n; p--) {
    const t = queue[order[p]!];
    if (t) out.push(t.artist ?? '');
  }
  return out;
}

/**
 * The fold of `freshForRadio` when the caller already holds the queue's ids
 * and song keys, so a top-up need not walk a queue that may be the
 * whole library. The two sets are **read, not written**: only the tracks a
 * top-up actually keeps go into the queue, and the caller adds those as it
 * appends them — folding the whole pool in here would bar every track it
 * looked at and did not take. Duplicates *within* the batch are still
 * dropped, against a set local to this call.
 */
export function freshFrom<T extends RadioTrack>(
  pool: T[],
  ids: ReadonlySet<string>,
  heard: ReadonlySet<string>,
): T[] {
  const batchIds = new Set<string>();
  const batchKeys = new Set<string>();
  return pool.filter((t) => {
    if (ids.has(t.id) || batchIds.has(t.id)) return false;
    const key = songKey(t);
    if (key !== '' && (heard.has(key) || batchKeys.has(key))) return false;
    batchIds.add(t.id);
    if (key !== '') batchKeys.add(key);
    return true;
  });
}
