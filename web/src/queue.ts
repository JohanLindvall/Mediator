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
