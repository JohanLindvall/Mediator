/**
 * Data sources: windowed, cached access to the (potentially huge) library.
 * The grid only ever asks for the visible index range; pages are fetched on
 * demand and cached per query generation.
 */
import {
  listAlbums,
  listArtists,
  listGenres,
  listMedia,
  listSeries,
  type Album,
  type Artist,
  type Counts,
  type Genre,
  type Item,
  type Series,
} from './api';

export const PAGE_SIZE = 200;

// The item source and the walk of a kind live in query.ts, where the test
// runner can reach them; re-exported so the viewers keep one import.
export { findKind } from './query';
export type { ItemSource } from './query';

// The query's shape and the hold-over decision live in query.ts, where the
// test runner can reach them; re-exported so callers keep one import.
export type { QueryState } from './query';
import { listFilters, sameSubject, type QueryState } from './query';

/**
 * How many pages stay cached per query. Scrolling an enormous library end
 * to end would otherwise pin every item in memory; pages far from the one
 * just fetched are cheap to refetch if the user comes back.
 */
const MAX_CACHED_PAGES = 64;

export class LibrarySource {
  private pages = new Map<number, Item[]>();
  /**
   * The previous pages, served while a live-update refetch is in flight.
   * Without this every change event blanked the grid to skeletons for a
   * beat; with it, cells keep their content and only re-render where the
   * refetched data actually differs.
   */
  private stale = new Map<number, Item[]>();
  /** The fetch in flight per page, shared: a page the grid is already
   * fetching is not fetched again for a viewer stepping into it. */
  private inflight = new Map<number, Promise<void>>();
  private gen = 0;
  private query: QueryState = { kind: '', q: '', sort: 'mtime', desc: true };

  /** -1 while the first page of a query is loading. */
  total = -1;
  counts: Counts | null = null;
  /** What the chips show while a search is on; null when there is none. */
  matching: Counts | null = null;
  version = 0;
  onUpdate: () => void = () => {};
  onError: (err: Error) => void = () => {};

  /**
   * A new query. The rows it will bring are different rows, but blanking the
   * grid to say so flashes the whole screen on every keystroke of a search —
   * and the answer is usually one page and a few milliseconds away. So the
   * outgoing rows stay on screen, and the count that sizes the grid around
   * them stays with them, until the first page of the new query lands. What
   * the two queries have in common then keeps its cell; the rest is built.
   *
   * The one thing not carried over is a total of -1, which means "nothing
   * has ever arrived": a first load has no rows to hold and should show
   * skeletons, because there the wait is real.
   */
  setQuery(q: QueryState): void {
    const before = this.query;
    this.query = { ...q };
    this.gen++;
    // A query that matched nothing has no rows to hold on to, and its total
    // of zero would refuse the very fetch meant to replace it.
    if (this.total === 0) this.total = -1;
    if (sameSubject(before, this.query)) {
      this.holdOver();
    } else {
      // A different subject altogether: pictures where films were. Holding
      // those up for the moment it takes to answer is not the listing
      // settling, it is the wrong listing.
      this.pages.clear();
      this.stale.clear();
      this.inflight.clear();
      this.total = -1;
      this.onUpdate(); // and the grid has to be told, or it keeps drawing them
    }
    this.fetchPage(0);
  }

  /**
   * The library changed (SSE): refetch, but keep serving what is on screen
   * until the fresh pages land — the rows are still the right rows, at
   * most slightly outdated for a moment.
   */
  invalidate(): void {
    this.gen++;
    this.holdOver();
    this.fetchPage(0);
  }

  /** Move the loaded pages aside to be served while the refetch is in flight. */
  private holdOver(): void {
    for (const [p, items] of this.pages) {
      this.stale.delete(p); // re-insert so the freshest copies trim last
      this.stale.set(p, items);
    }
    // Stale pages for ranges never revisited would otherwise pile up
    // across change events; insertion order makes the oldest trim first.
    for (const p of this.stale.keys()) {
      if (this.stale.size <= MAX_CACHED_PAGES) break;
      this.stale.delete(p);
    }
    this.pages.clear();
    this.inflight.clear();
  }

  get(i: number): Item | undefined {
    if (i < 0) return undefined;
    const p = Math.floor(i / PAGE_SIZE);
    return (this.pages.get(p) ?? this.stale.get(p))?.[i % PAGE_SIZE];
  }

  /** Ensure all pages covering [a, b] are loaded or loading (fresh, not stale). */
  need(a: number, b: number): void {
    if (this.total === 0) return;
    const last = this.total > 0 ? Math.min(b, this.total - 1) : b;
    for (let p = Math.max(0, Math.floor(a / PAGE_SIZE)); p <= Math.floor(last / PAGE_SIZE); p++) {
      if (!this.pages.has(p)) this.fetchPage(p);
    }
  }

  /**
   * Await a single item, for the viewers stepping to a neighbour. The page
   * is fetched through the same door the grid uses, so a swipe into a page
   * the grid is already fetching waits for that fetch rather than starting
   * a second one.
   */
  async item(i: number): Promise<Item | undefined> {
    if (i < 0 || (this.total >= 0 && i >= this.total)) return undefined;
    const have = this.get(i);
    if (have) return have;
    await this.fetchPage(Math.floor(i / PAGE_SIZE));
    return this.get(i);
  }

  private params() {
    return listFilters(this.query);
  }

  /** Fetch a page unless it is here or on its way; the promise is the way. */
  private fetchPage(p: number): Promise<void> {
    if (this.pages.has(p)) return Promise.resolve();
    const going = this.inflight.get(p);
    if (going) return going;
    if (this.total >= 0 && p * PAGE_SIZE >= this.total) return Promise.resolve();
    const gen = this.gen;
    const run = listMedia({ ...this.params(), offset: p * PAGE_SIZE, limit: PAGE_SIZE })
      .then((res) => {
        if (gen !== this.gen) return;
        this.absorb(p, res.items, res.total, res.counts, res.version, res.matching ?? null);
      })
      .catch((err: Error) => {
        if (gen === this.gen) this.onError(err);
      })
      .finally(() => {
        if (this.inflight.get(p) === run) this.inflight.delete(p);
      });
    this.inflight.set(p, run);
    return run;
  }

  private absorb(
    page: number,
    items: Item[],
    total: number,
    counts: Counts,
    version: number,
    matching: Counts | null,
  ): void {
    this.pages.set(page, items);
    this.stale.delete(page);
    // Evict the page farthest from the one just fetched once the cache is
    // full, so scrolling an enormous library never pins every item.
    while (this.pages.size > MAX_CACHED_PAGES) {
      let far = page;
      let dist = -1;
      for (const p of this.pages.keys()) {
        const d = Math.abs(p - page);
        if (d > dist) {
          dist = d;
          far = p;
        }
      }
      this.pages.delete(far);
    }
    this.total = total;
    this.counts = counts;
    this.matching = matching;
    this.version = version;
    this.onUpdate();
  }
}

/**
 * One grouped view, fetched whole: albums, artists, genres or shows. None of
 * them approaches the size of the file listing — a library of two hundred
 * thousand files groups into a few thousand releases — so a single request
 * answers, and the grid asks for ranges of the array rather than paging.
 *
 * The generation counter is what makes rapid queries safe: only the answer
 * to the latest question is kept, however the requests come back. A face
 * that cannot see the view is answered with an empty list, not an error;
 * an error is a failure and is said (onError) rather than drawn as "no
 * matches", which it is not — the rows on screen, if any, stay up.
 */
export class CollectionSource<T> {
  items: T[] | null = null;
  matching: Counts | null = null;
  private gen = 0;
  private subject = '';
  onUpdate: () => void = () => {};
  onError: (err: Error) => void = () => {};

  /**
   * `subjectOf` names what a query is about — the performer, the genre, the
   * thing something is like — and a change of it drops the rows on screen at
   * once rather than leaving them up until the answer arrives: stepping from
   * one performer to another must not show the first one's releases under
   * the second one's chip. Narrowing by a word keeps them, so the search is
   * deliberately not part of a subject.
   */
  constructor(
    private readonly fetch: (q: QueryState) => Promise<{ items: T[]; matching?: Counts }>,
    private readonly subjectOf: (q: QueryState) => string = () => '',
  ) {}

  load(q: QueryState): void {
    const subject = this.subjectOf(q);
    if (subject !== this.subject) {
      this.subject = subject;
      if (this.items !== null) this.clear();
    }
    const gen = ++this.gen;
    this.fetch(q)
      .then((res) => {
        if (gen !== this.gen) return;
        this.items = res.items;
        this.matching = res.matching ?? null;
        this.onUpdate();
      })
      .catch((err: Error) => {
        if (gen === this.gen) this.onError(err);
      });
  }

  /** Drop what is shown, and say so at once. */
  protected clear(): void {
    this.items = null;
    this.matching = null;
    this.onUpdate();
  }

  get(i: number): T | undefined {
    return this.items?.[i];
  }

  count(): number {
    return this.items?.length ?? -1;
  }
}

export class AlbumsSource extends CollectionSource<Album> {
  constructor() {
    super(
      (q) =>
        listAlbums({
          q: q.q || undefined,
          artist: q.artist || undefined,
          genre: q.genre || undefined,
          near: q.near || undefined,
          audiobooks: q.audiobooks,
          sort: q.sort,
          order: q.desc ? 'desc' : 'asc',
        }).then((res) => ({ items: res.albums, matching: res.matching })),
      (q) => `${q.artist ?? ''}|${q.genre ?? ''}|${q.near ?? ''}|${q.audiobooks ? 'books' : ''}`,
    );
  }
}

/**
 * The shows. Each carries its own seasons, so opening one needs no second
 * request — see the Go side for why they travel together.
 */
export class SeriesSource extends CollectionSource<Series> {
  constructor() {
    super((q) =>
      listSeries({ q: q.q || undefined, sort: q.sort, order: q.desc ? 'desc' : 'asc' }).then(
        (res) => ({ items: res.series, matching: res.matching }),
      ),
    );
  }

  /** The show of a given name, for the seasons view that opens from it. */
  find(name: string): Series | undefined {
    const key = name.toLowerCase();
    return this.items?.find((s) => s.name.toLowerCase() === key);
  }
}

export class ArtistsSource extends CollectionSource<Artist> {
  constructor() {
    super(
      (q) =>
        listArtists({
          q: q.q || undefined,
          near: q.near || undefined,
          sort: q.sort,
          order: q.desc ? 'desc' : 'asc',
        }).then((res) => ({ items: res.artists, matching: res.matching })),
      (q) => q.near ?? '',
    );
  }
}

export class GenresSource extends CollectionSource<Genre> {
  constructor() {
    super((q) =>
      listGenres({ q: q.q || undefined, sort: q.sort, order: q.desc ? 'desc' : 'asc' }).then(
        (res) => ({ items: res.genres, matching: res.matching }),
      ),
    );
  }
}
