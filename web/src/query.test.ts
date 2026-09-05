/**
 * When a new query keeps the rows on screen, and when it must not.
 *
 * Holding the outgoing rows while the next answer is fetched is what makes a
 * search read as the listing settling — and holding them across a change of
 * subject is the wrong listing under a chip that says whose it is not. The
 * decision is pure, so it is pinned here; a wrong answer is either a
 * flashing screen or somebody else's rows.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { findKind, listFilters, countKey, countsToShow, narrowed, sameSubject, type ItemSource, type QueryState } from './query.ts';

function q(over: Partial<QueryState> = {}): QueryState {
  return { kind: '', q: '', sort: 'mtime', desc: true, ...over };
}

test('narrowing by a word or reordering is the same subject', () => {
  assert.ok(sameSubject(q(), q({ q: 'winter' })));
  assert.ok(sameSubject(q({ q: 'winter' }), q({ q: 'winter storm' })));
  assert.ok(sameSubject(q(), q({ sort: 'name', desc: false })));
});

test('to or from everything is the same subject; between kinds is not', () => {
  assert.ok(sameSubject(q(), q({ kind: 'video' })));
  assert.ok(sameSubject(q({ kind: 'video' }), q()));
  assert.ok(!sameSubject(q({ kind: 'video' }), q({ kind: 'image' })));
});

test('a different performer, genre or show is a different subject', () => {
  assert.ok(!sameSubject(q({ artist: 'A Performer' }), q({ artist: 'Another' })));
  assert.ok(!sameSubject(q(), q({ genre: 'Black Metal' })));
  assert.ok(!sameSubject(q({ series: 'Harbour Lights' }), q({ series: 'Harbour Lights', season: 2 })));
});

test('the watch and popularity filters change the subject', () => {
  assert.ok(!sameSubject(q(), q({ watch: 'started' })));
  assert.ok(!sameSubject(q(), q({ played: true })));
  // Absent and defaulted spell the same thing.
  assert.ok(sameSubject(q({ watch: '' }), q()));
  assert.ok(sameSubject(q({ played: false }), q()));
});

// Clicking a genre flashed "85,118 videos" across the chips for as long as
// the request took, then settled on nought: the narrowing was in force
// before any answer about it had arrived, and the fallback was the library's
// own totals — numbers describing a library the viewer had just left.
test('counts: an unanswered narrowing shows no numbers rather than the wrong ones', () => {
  const totals = { total: 209857, video: 85118 };
  const hits = { total: 12, video: 0 };
  // Nothing narrowed: the totals are the answer.
  assert.deepEqual(
    countsToShow({ narrowed: false, answers: 'search', asking: 'search', held: null, totals }),
    totals,
  );
  // Narrowed, and the held counts answer something else — the moment after
  // the click.
  assert.equal(
    countsToShow({ narrowed: true, answers: 'search', asking: 'albums  Black Metal', held: totals, totals }),
    null,
  );
  // Narrowed and answered.
  assert.deepEqual(
    countsToShow({
      narrowed: true,
      answers: 'albums  Black Metal',
      asking: 'albums  Black Metal',
      held: hits,
      totals,
    }),
    hits,
  );
});

test('counts: a search keeps its last hits while the next answer is fetched', () => {
  // Refining a search asks about very nearly the same things, so the counts
  // are held: blanking them on every keystroke is the same flicker by
  // another route. The key carries no search text, which is what does it.
  const a = countKey({ mode: 'all', artist: '', genre: '' });
  const b = countKey({ mode: 'all', artist: '', genre: '' });
  assert.equal(a, b);
});

test('counts: every drill-down is keyed, not just the genre', () => {
  const base = { mode: 'albums', artist: '', genre: '', series: '', season: 0 };
  const keys = [
    countKey(base),
    countKey({ ...base, genre: 'Black Metal' }),
    countKey({ ...base, artist: 'Mayhem' }),
    countKey({ mode: 'series', series: 'Harbour Lights', season: 1 }),
    countKey({ mode: 'series', series: 'Harbour Lights', season: 2 }),
    countKey({ mode: 'series', series: 'Grey Harvest', season: 1 }),
  ];
  assert.equal(new Set(keys).size, keys.length, `keys collide: ${keys.join(' | ')}`);
});

test('counts: a view that fetches nothing keeps the key of the one that did', () => {
  // The seasons view is derived from the show list already in hand and sends
  // no query of its own. Keying it on "a show is open" would blank its chips
  // for good: nothing would ever arrive to unblank them.
  const shows = countKey({ mode: 'series', series: '', season: 0 });
  const seasons = countKey({ mode: 'series', series: 'Harbour Lights', season: 0 });
  assert.equal(seasons, shows);
});

test("the listing's words for a query: only what is set, and the direction always", () => {
  assert.deepEqual(listFilters(q()), {
    kind: undefined,
    watch: undefined,
    played: undefined,
    series: undefined,
    season: undefined,
    q: undefined,
    sort: 'mtime',
    order: 'desc',
  });
  assert.deepEqual(
    listFilters(q({ kind: 'video', watch: 'started', played: true, series: 'Harbour Lights', season: 2, q: 'storm', sort: 'name', desc: false })),
    {
      kind: 'video',
      watch: 'started',
      played: '1',
      series: 'Harbour Lights',
      season: '2',
      q: 'storm',
      sort: 'name',
      order: 'asc',
    },
  );
});

test('countKey and narrowed agree on every narrowing, "like this" included', () => {
  // Every narrowing the count key names must count as a narrowing, or the
  // chips show the library's totals under a chip that names a narrowing —
  // which is what happened to the like-this listing.
  const cases = [
    { mode: 'albums', artist: 'Harbour Lights' },
    { mode: 'albums', genre: 'Folk' },
    { mode: 'albums', near: 'abc' },
    { mode: 'artists', near: 'Harbour Lights' },
    { mode: 'series', series: 'Harbour Lights', season: 2 },
  ];
  for (const c of cases) {
    assert.ok(narrowed(c), `${JSON.stringify(c)} narrows`);
    assert.notEqual(countKey(c), 'search', `${JSON.stringify(c)} is keyed as its narrowing`);
  }
  assert.ok(!narrowed({}), 'nothing narrows an unnarrowed view');
  assert.ok(narrowed({ q: 'storm' }), 'a search narrows');
});

test('findKind steps past the other kinds and stops at the ends', async () => {
  const kinds = ['image', 'audio', 'video', 'image', 'video'] as const;
  const src: ItemSource = {
    item: (i) =>
      Promise.resolve(
        i >= 0 && i < kinds.length
          ? ({ id: `i${i}`, kind: kinds[i], name: `${i}`, path: '', size: 0, mtime: 0 } as never)
          : undefined,
      ),
    total: () => kinds.length,
  };
  assert.deepEqual((await findKind(src, 0, 1, 'video'))?.index, 2, 'the first film forwards');
  assert.deepEqual((await findKind(src, 3, 1, 'video'))?.index, 4);
  assert.deepEqual((await findKind(src, 4, -1, 'image'))?.index, 3, 'backwards, the nearest picture');
  assert.equal(await findKind(src, 5, 1, 'video'), null, 'past the end there is nothing');
  assert.equal(await findKind(src, 0, -1, 'audio'), null, 'before the start there is nothing');
  // An unknown total (nothing has arrived) walks until the source says undefined.
  const open: ItemSource = { ...src, total: () => -1 };
  assert.deepEqual((await findKind(open, 3, 1, 'video'))?.index, 4);
});
