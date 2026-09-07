/**
 * The arithmetic of the play order, kept out of the player so it can be
 * tested: the order is a list of queue indices, and what "after everything
 * already queued" means is a question about that list and nothing else.
 */

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
 * What makes two tracks the same recording: the performer and the title, as
 * the tags spell them. The server folds the copies of one recording out of a
 * resemblance answer; this is the same rule applied to what is already in
 * the queue, since a copy one batch left there would otherwise be matched by
 * a different copy in the next.
 *
 * An untagged file has no key and is never folded — its title is unknown,
 * and the file name is not one.
 */
export function recordingKey(t: { artist?: string; title?: string }): string {
  if (!t.title) return '';
  return `${(t.artist ?? '').toLowerCase()}\u0000${t.title.toLowerCase()}`;
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
 * nobody is never damped — the unnamed are not one performer.
 */
export function pickRadio<T extends { artist?: string }>(
  pool: T[],
  want: number,
  rand: () => number = Math.random,
  recent: readonly string[] = [],
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
      const w = (n - i) * ARTIST_DAMP ** (drawn.get(artistKey(t)) ?? 0);
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
}

/**
 * What of a batch of similar tracks is worth queueing: not what is queued
 * already, by file or by recording.
 *
 * The second half of that is the one that matters. Radio asks again every
 * few tracks, and each answer is drawn from the same neighbourhood as the
 * last — so the song that has just played comes back at once, in a different
 * file. Measured on a real library: one song is there nine times over, on
 * three live records, four bootlegs and two albums, all tagged alike, and
 * four of them arrived in one batch.
 *
 * The queue is the whole memory, played part included, so this holds for
 * every later batch and not only the next.
 */
export function freshForRadio<T extends RadioTrack>(pool: T[], queued: RadioTrack[]): T[] {
  const ids = new Set<string>();
  const heard = new Set<string>();
  for (const t of queued) {
    ids.add(t.id);
    const key = recordingKey(t);
    if (key !== '') heard.add(key);
  }
  return pool.filter((t) => {
    if (ids.has(t.id)) return false;
    const key = recordingKey(t);
    if (key !== '' && heard.has(key)) return false;
    // Within the batch too: the answer is deduplicated by the server, but a
    // page open across a restart may hold one from before it was.
    ids.add(t.id);
    if (key !== '') heard.add(key);
    return true;
  });
}
