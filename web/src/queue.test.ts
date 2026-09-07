/**
 * Where an album added to the queue ends up. "At the end" is the whole
 * promise of the button, and with shuffle on it is the promise most easily
 * broken — a shuffle over the whole order would deal the new tracks in among
 * the ones already waiting.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  appendToOrder,
  freshForRadio,
  nextPosition,
  pickRadio,
  placeFirst,
  recordingKey,
  resumable,
  shuffleInPlace,
  windowRows,
} from './queue.ts';

test('added tracks follow everything already queued, in the order they came', () => {
  const order = [2, 0, 1];
  const at = appendToOrder(order, 3, 3, false);
  assert.deepEqual(order, [2, 0, 1, 3, 4, 5]);
  assert.equal(at, 3);
});

test('with shuffle on they are shuffled among themselves, still at the end', () => {
  const order = [2, 0, 1];
  let n = 0;
  const rand = () => [0.9, 0.1, 0.5][n++ % 3]!;
  const at = appendToOrder(order, 3, 4, true, rand);
  assert.equal(at, 3);
  assert.deepEqual(order.slice(0, 3), [2, 0, 1], 'what was queued keeps its order');
  assert.deepEqual([...order.slice(3)].sort(), [3, 4, 5, 6], 'every new track is there once');
  assert.notDeepEqual(order.slice(3), [3, 4, 5, 6], 'and not in album order');
});

test('an empty order starts at the beginning', () => {
  const order: number[] = [];
  assert.equal(appendToOrder(order, 0, 2, false), 0);
  assert.deepEqual(order, [0, 1]);
});

test('a shuffle is a permutation', () => {
  const arr = [0, 1, 2, 3, 4, 5, 6, 7];
  shuffleInPlace(arr);
  assert.deepEqual([...arr].sort((a, b) => a - b), [0, 1, 2, 3, 4, 5, 6, 7]);
});

test('what follows: the next, the first again with repeat, nothing at the end', () => {
  assert.equal(nextPosition(0, 3, false, false), 1);
  assert.equal(nextPosition(2, 3, false, false), null);
  assert.equal(nextPosition(2, 3, true, false), 0);
  // With shuffle the next order is dealt fresh, so nothing is knowable yet.
  assert.equal(nextPosition(2, 3, true, true), null);
  assert.equal(nextPosition(0, 0, true, false), null);
});

test('the track started from plays first whatever the shuffle dealt', () => {
  const order = [2, 0, 3, 1];
  placeFirst(order, 3);
  assert.deepEqual(order, [3, 2, 0, 1]);
  placeFirst(order, 3);
  assert.deepEqual(order, [3, 2, 0, 1], 'already first: untouched');
  placeFirst(order, 9);
  assert.deepEqual(order, [3, 2, 0, 1], 'not in the order: untouched');
});

test('the window of rows worth drawing is what is in view and a margin', () => {
  assert.deepEqual(windowRows(0, 380, 38, 1000, 8), { first: 0, last: 18 });
  assert.deepEqual(windowRows(3800, 380, 38, 1000, 8), { first: 92, last: 118 });
  assert.deepEqual(windowRows(37900, 380, 38, 1000, 8), { first: 989, last: 1000 }, 'clamped to the list');
  assert.deepEqual(windowRows(0, 380, 38, 5, 8), { first: 0, last: 5 });
});

test('resumable: loaded, not failed, not played out, and not parked on the final end', () => {
  const ok = { loaded: true, failed: false, exhausted: false, ended: false, atLast: false, repeat: false };
  assert.ok(resumable(ok));
  assert.ok(!resumable({ ...ok, loaded: false }));
  assert.ok(!resumable({ ...ok, failed: true }));
  assert.ok(!resumable({ ...ok, exhausted: true }));
  assert.ok(resumable({ ...ok, ended: true }), 'a boundary with more to come');
  assert.ok(!resumable({ ...ok, ended: true, atLast: true }), 'the final boundary restarts from zero');
  assert.ok(resumable({ ...ok, ended: true, atLast: true, repeat: true }), 'unless repeat wraps it');
});

test('one recording is one key, whatever file it is in', () => {
  // The same song on the album, on a compilation and on a live record.
  assert.equal(
    recordingKey({ artist: 'Gorse Beacon', title: 'Signal Fires' }),
    recordingKey({ artist: 'gorse beacon', title: 'signal fires' }),
  );
  assert.notEqual(
    recordingKey({ artist: 'Gorse Beacon', title: 'Signal Fires' }),
    recordingKey({ artist: 'Tern Signal', title: 'Signal Fires' }),
  );
  // Nothing tagged has no key: the file name is not a title, and two
  // different songs called "01" are not one recording.
  assert.equal(recordingKey({}), '');
  assert.equal(recordingKey({ artist: 'Gorse Beacon' }), '');
});

test('radio draws the nearest likeliest, and never the same track twice', () => {
  const pool = ['a', 'b', 'c', 'd', 'e'];
  // The bottom of every weight range is the nearest still in hand.
  assert.deepEqual(pickRadio(pool, 3, () => 0), ['a', 'b', 'c']);
  // The top of it is the farthest.
  assert.deepEqual(
    pickRadio(pool, 3, () => 0.999999),
    ['e', 'd', 'c'],
  );
  // Asked for more than there is, it gives what there is, each once.
  const all = pickRadio(pool, 99, () => 0.5);
  assert.equal(all.length, pool.length);
  assert.equal(new Set(all).size, pool.length);
  assert.deepEqual(pickRadio([], 5), []);
});

test('radio is a different evening every time, in the same neighbourhood', () => {
  const pool = Array.from({ length: 50 }, (_, i) => i);
  const rand = seeded();
  const runs = Array.from({ length: 8 }, () => pickRadio(pool, 10, rand));
  for (const run of runs) {
    assert.equal(run.length, 10);
    assert.equal(new Set(run).size, 10);
  }
  const distinct = new Set(runs.map((r) => r.join(',')));
  assert.equal(distinct.size, runs.length, 'two evenings drew the same ten tracks in the same order');
  // Still the neighbourhood: over eight draws the nearer half is picked
  // more often than the farther one.
  const near = runs.flat().filter((i) => i < 25).length;
  assert.ok(near > runs.flat().length / 2, `the draw wandered: ${near} of ${runs.flat().length} from the near half`);
});

test('radio never brings back a song the queue already holds', () => {
  const live = { id: '1', artist: 'Gorse Beacon', title: 'Signal Fires' };
  const queued = [live, { id: '2', artist: 'Tern Signal', title: 'Low Water' }];
  const batch = [
    // The same recording, in three other files: an album, a bootleg, a live
    // record. Different ids, one song.
    { id: '3', artist: 'Gorse Beacon', title: 'Signal Fires' },
    { id: '4', artist: 'gorse beacon', title: 'SIGNAL FIRES' },
    { id: '1', artist: 'Gorse Beacon', title: 'Signal Fires' },
    { id: '5', artist: 'Gorse Beacon', title: 'First Breath' },
  ];
  assert.deepEqual(
    freshForRadio(batch, queued).map((t) => t.id),
    ['5'],
  );
});

test('radio keeps one copy of a song it has not heard, and every untagged file', () => {
  const batch = [
    { id: '1', artist: 'Gorse Beacon', title: 'First Breath' },
    { id: '2', artist: 'Gorse Beacon', title: 'First Breath' },
    { id: '3' },
    { id: '4' },
  ];
  assert.deepEqual(
    freshForRadio(batch, []).map((t) => t.id),
    ['1', '3', '4'],
  );
});

/** A little deterministic generator, so a failure can be read back. */
function seeded(seed = 1): () => number {
  let s = seed;
  return () => {
    s = (s * 1103515245 + 12345) % 2147483648;
    return s / 2147483648;
  };
}

test('radio does not play five songs by one band out of ten', () => {
  // What a resemblance answer really looks like: the seed's own band takes
  // the near half of it, the rest of the neighbourhood the far half.
  const pool = [
    ...Array.from({ length: 25 }, (_, i) => ({ id: `a${i}`, artist: 'Gorse Beacon' })),
    ...Array.from({ length: 25 }, (_, i) => ({ id: `b${i}`, artist: `Band ${i % 8}` })),
  ];
  const rand = seeded();
  let worst = 0;
  for (let run = 0; run < 20; run++) {
    const batch = pickRadio(pool, 10, rand);
    const own = batch.filter((t) => t.artist === 'Gorse Beacon').length;
    worst = Math.max(worst, own);
    // Nothing twice, whatever the weights did.
    assert.equal(new Set(batch.map((t) => t.id)).size, batch.length);
  }
  assert.ok(worst <= 4, `one batch held ${worst} songs by the seed's own band`);
});

test('a band already in the queue starts damped', () => {
  const pool = [
    { id: 'a', artist: 'Gorse Beacon' },
    { id: 'b', artist: 'Gorse Beacon' },
    { id: 'c', artist: 'Tern Signal' },
    { id: 'd', artist: 'Sixth Quay' },
  ];
  const lately = Array.from({ length: 6 }, () => 'gorse beacon');
  const rand = seeded(7);
  let own = 0;
  for (let run = 0; run < 20; run++) {
    own += pickRadio(pool, 2, rand, lately).filter((t) => t.artist === 'Gorse Beacon').length;
  }
  // Two of four in the pool are theirs and they lead it, so an undamped
  // draw would take one nearly every time.
  assert.ok(own < 10, `the band lately played was drawn ${own} times in 40`);
});

test('a neighbourhood that really is one band still fills the batch', () => {
  const pool = Array.from({ length: 12 }, (_, i) => ({ id: `${i}`, artist: 'Gorse Beacon' }));
  const batch = pickRadio(pool, 10, seeded(3), ['gorse beacon', 'gorse beacon']);
  assert.equal(batch.length, 10);
  assert.equal(new Set(batch.map((t) => t.id)).size, 10);
});
