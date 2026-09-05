/**
 * What a listing query is, and when two of them are about the same things.
 *
 * Lifted out of sources.ts so it can be tested: sources.ts itself cannot be
 * imported by the type-stripping test runner (its classes use parameter
 * properties), and whether rows are held over during a query change is
 * behaviour worth pinning — a wrong answer here is either a flashing screen
 * or the wrong listing under the chips.
 */

import type { Item, Kind } from './types.gen';

import type { ListFilters } from './api';

export interface QueryState {
  kind: Kind | '';
  /** "started" | "done", or empty for everything however far it got. */
  watch?: string;
  /** Keep only what has been played at all: the popularity listing. */
  played?: boolean;
  q: string;
  /**
   * The sort key. Which keys mean anything depends on the view — a release
   * has a year and a performer where a file has a size — so this is a plain
   * string and the view decides what it can offer.
   */
  sort: string;
  desc: boolean;
  /** Albums view only: show just this performer's albums. */
  artist?: string;
  /** Albums view only: show just the releases filed under this genre. */
  genre?: string;
  /** One show, and one season of it: what a season card opens. */
  series?: string;
  season?: number;
  /**
   * Albums and artists views: list what sounds like this one — an album id,
   * or a performer's name — nearest first, in place of the ordinary listing.
   */
  near?: string;
  /** The audiobook shelf rather than the records. */
  audiobooks?: boolean;
}

/**
 * Whether two queries are asking about the same things, so that the rows on
 * screen are worth keeping until the new ones arrive.
 *
 * Narrowing by a word, or reordering, leaves the same library underneath and
 * most of the same rows in place: holding them there is what makes a search
 * read as the listing settling rather than as the screen being rebuilt. A
 * change of kind between two particular kinds does not — no film is a
 * picture — and neither does a change of performer. To or from "everything"
 * is still the same subject, since one set contains the other.
 */
export function sameSubject(a: QueryState, b: QueryState): boolean {
  // What sounds like one thing is not the same listing as what sounds like
  // another, and the audiobook shelf is not the records.
  if ((a.near ?? '') !== (b.near ?? '') || !!a.audiobooks !== !!b.audiobooks) return false;
  if ((a.artist ?? '') !== (b.artist ?? '')) return false;
  if ((a.genre ?? '') !== (b.genre ?? '')) return false;
  if ((a.watch ?? '') !== (b.watch ?? '')) return false;
  if ((a.played ?? false) !== (b.played ?? false)) return false;
  if ((a.series ?? '') !== (b.series ?? '')) return false;
  if ((a.season ?? 0) !== (b.season ?? 0)) return false;
  return a.kind === b.kind || a.kind === '' || b.kind === '';
}

/**
 * Which set of numbers the filter chips should show.
 *
 * Three states, and the third is the one that goes wrong. With nothing
 * narrowed the library's totals are the answer. With a narrowing that has
 * been answered, the matching counts are. And while a narrowing is still
 * being answered there is no honest number at all — so the chips show none,
 * rather than the totals, which describe a library the viewer is no longer
 * looking at: clicking a genre flashed "85,118 videos" for as long as the
 * request took, then settled on nought.
 *
 * `held` is what stops that becoming a flicker of its own. A search is
 * refined — each keystroke asks about very nearly the same things, and the
 * previous hits are a good enough answer for the moment it takes — so the
 * counts are kept while only the search text changes. Stepping into or out
 * of a drill-down is not a refinement: those numbers are about somebody
 * else's releases, and showing them under a chip bar that already says whose
 * it is not would be worse than showing nothing. The same judgement
 * sameSubject makes about the rows themselves.
 */
export function countsToShow<T>(o: {
  narrowed: boolean;
  /** What the held counts answer, and what is being asked now. */
  answers: string;
  asking: string;
  held: T | null;
  totals: T | null;
}): T | null {
  if (!o.narrowed) return o.totals;
  return o.answers === o.asking ? o.held : null;
}

/**
 * What a set of counts is an answer to, which is the narrowing the *server*
 * was asked about — not everything on screen.
 *
 * Each view is fed by one source and each source sends its own narrowing: the
 * album list carries the performer and the genre, a season's episodes carry
 * the show and the season, and every other view asks by search alone. So the
 * seasons view, which fetches nothing and is derived from the show list
 * already in hand, has the same key as the show list — keying it on "a show
 * is open" would blank its chips for good, since nothing would ever arrive to
 * unblank them.
 *
 * The search is deliberately absent: see countsToShow.
 */
export function countKey(s: {
  mode: string;
  artist?: string;
  genre?: string;
  series?: string;
  season?: number;
  near?: string;
}): string {
  if (s.mode === 'albums' || s.mode === 'audiobooks') {
    return `${s.mode} ${s.artist ?? ''} ${s.genre ?? ''} ${s.near ?? ''}`;
  }
  if (s.mode === 'artists' && s.near) return `artists ${s.near}`;
  if (s.mode === 'series' && s.season) return `episodes ${s.series ?? ''} ${s.season}`;
  return 'search';
}

/**
 * The listing's own words for a query: what `/api/library` and the m3u
 * export take, and what "queue all" sends for a listing — one wording, so
 * the three cannot drift.
 */
export function listFilters(q: QueryState): ListFilters {
  return {
    kind: q.kind || undefined,
    watch: q.watch || undefined,
    played: q.played ? '1' : undefined,
    series: q.series || undefined,
    season: q.season ? String(q.season) : undefined,
    q: q.q || undefined,
    sort: q.sort,
    order: q.desc ? 'desc' : 'asc',
  };
}

/**
 * Whether anything is narrowing the view, and so whether the hits the server
 * counted apply — a search, a performer, a genre, a show, or what sounds
 * like one thing. It has to agree with countKey: a narrowing counted under
 * a key but not named here left the chips on the library's totals under a
 * "like this" chip, which is exactly the fault countsToShow exists to end.
 */
export function narrowed(s: {
  q?: string;
  artist?: string;
  genre?: string;
  series?: string;
  near?: string;
}): boolean {
  return !!(s.q || s.artist || s.genre || s.series || s.near);
}

/**
 * A positional view of the current listing, which is what the overlays step
 * through: they know the index they were opened at and nothing else about
 * where the items came from.
 */
export interface ItemSource {
  /** Resolve the item at an absolute index of the current query (may fetch). */
  item(i: number): Promise<Item | undefined>;
  total(): number;
}

/**
 * Find the nearest item of `kind` scanning from `from` in direction `dir`.
 *
 * A viewer steps between items of its own kind — a swipe in the picture
 * viewer lands on a picture, and one in the player on something playable —
 * so whatever is filed in between is passed over rather than opened in a
 * viewer that cannot show it.
 */
export async function findKind(
  src: ItemSource,
  from: number,
  dir: 1 | -1,
  kind: Kind,
): Promise<{ item: Item; index: number } | null> {
  const total = src.total();
  for (let i = from; i >= 0 && (total < 0 || i < total); i += dir) {
    const it = await src.item(i);
    if (!it) return null;
    if (it.kind === kind) return { item: it, index: i };
  }
  return null;
}
