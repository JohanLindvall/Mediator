// SPDX-License-Identifier: MIT

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
  ahead,
  appendToOrder,
  freshForRadio,
  freshFrom,
  namesNothing,
  nextPosition,
  pickRadio,
  placeFirst,
  recentArtists,
  resumable,
  songKey,
  songTitle,
  runsHours,
  shuffleInPlace,
  windowRows,
  wrappedAround,
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

test('the loop going round is told apart from playing and from seeking', () => {
  // The closing stretch of a five-minute track jumping to its opening: the loop.
  assert.equal(wrappedAround(299.8, 0.05, 300), true);
  // Timeupdates a second apart, as a throttled page may see them.
  assert.equal(wrappedAround(299, 0.4, 300), true);
  // A seek the bar made moves `last` with it, so its seeking event jumps nowhere.
  assert.equal(wrappedAround(0, 0, 300), false);
  assert.equal(wrappedAround(120, 120, 300), false);
  // Playing on: forward, or a clock a few milliseconds behind itself.
  assert.equal(wrappedAround(10, 10.25, 300), false);
  assert.equal(wrappedAround(0.04, 0.03, 300), false);
  assert.equal(wrappedAround(150.2, 150.19, 300), false);
  // Back into the opening from the middle is not the end going round.
  assert.equal(wrappedAround(150, 1, 300), false);
  // Back from the closing stretch to somewhere past the opening is not either.
  assert.equal(wrappedAround(299, 40, 300), false);
  // A track of two seconds goes round from wherever its clock last was.
  assert.equal(wrappedAround(1.9, 0.02, 2), true);
  // With no length known, the jump back into the opening is enough.
  assert.equal(wrappedAround(212, 0.1, NaN), true);
  assert.equal(wrappedAround(212, 0.1, Infinity), true);
  assert.equal(wrappedAround(212, 30, NaN), false);
  // Nothing known about where it was says nothing.
  assert.equal(wrappedAround(NaN, 0.1, 300), false);
  // A jitter in the first moments says nothing either, length known or not.
  assert.equal(wrappedAround(0.15, 0.05, NaN), false);
  assert.equal(wrappedAround(0.3, 0.25, NaN), false);
  // A track shorter than a second still goes round.
  assert.equal(wrappedAround(0.38, 0.01, 0.4), true);
});

test('one song is one key, whatever file and whatever take it is in', () => {
  const key = (artist: string, title: string) => songKey({ id: '', artist, title });
  const song = key('Gorse Beacon', 'Signal Fires');
  // The same song on the album, on a compilation and on a live record —
  // tagged alike, or as the take it is.
  for (const title of [
    'signal fires',
    'Signal Fires (Live)',
    'Signal Fires (Live at the Harbour, 1999)',
    'Signal Fires [Demo 1994]',
    'Signal Fires - 2011 Remaster',
    'Signal Fires (Remastered 2017)',
    'Signal Fires (Instrumental)',
    'Signal Fires (Tern Signal cover)',
    'Signal Fires (Bonus Track)',
    'Signal Fires (Re-Recorded)',
    'Signal Fires (Demo Version - Remaster 2017)',
    'Signal Fires (1994)',
    'Signal Fires.',
    'Signal Fires (feat. Sixth Quay)',
  ]) {
    assert.equal(key('Gorse Beacon', title), song, title);
  }
  // Accents and punctuation are spelling, not a different song.
  assert.equal(key('Gorse Beacon', 'Fjärdljus'), key('Gorse Beacon', 'Fjardljus'));
  assert.equal(key('Gorse Beacon', 'Vs. The Tide'), key('Gorse Beacon', 'Vs The Tide'));
  assert.equal(key('Gorse Beacon', "Harbour's Edge (Demo Version)"), key('Gorse Beacon', 'Harbours Edge'));
  assert.equal(key('Gorse Beacon', 'Don’t Wake the Tide'), key('Gorse Beacon', "Don't Wake the Tide"));
  assert.equal(key('Gorse Beacon', 'Salt & Iron'), key('Gorse Beacon', 'Salt and Iron'));
  // A guest is still their song.
  assert.equal(key('Gorse Beacon feat. Sixth Quay', 'Signal Fires'), song);
  // Somebody else's song of the same name is another song.
  assert.notEqual(key('Tern Signal', 'Signal Fires'), song);
  // And a bracket that names another piece of music keeps it apart.
  for (const title of ['Signal Fires (Part II)', 'Signal Fires (Reprise)', 'Signal Fires (Intro)', 'Signal Fires - Demonic Shore']) {
    assert.notEqual(key('Gorse Beacon', title), song, title);
  }
});

test('a title that names nothing is no song', () => {
  for (const title of ['Untitled', '[untitled]', '(Untitled Track)', 'Track 3', 'track01', 'Unknown', 'untitled #2']) {
    assert.ok(namesNothing(title), title);
    assert.equal(songKey({ id: '', artist: 'Gorse Beacon', title }), '', title);
  }
  for (const title of ['Untitled Harbour Air', 'Tracks in Snow', 'The Unknown Shore']) {
    assert.ok(!namesNothing(title), title);
  }
  // Nothing at all has no key, and neither has a name that is only a number:
  // two different songs called "01" are not one song.
  assert.equal(songKey({ id: '' }), '');
  assert.equal(songKey({ id: '', name: '01.mp3' }), '');
  assert.equal(songTitle('(Live)'), '');
});

test('a file nothing tagged is the song its name says', () => {
  // The title the row is drawn with: no number, no extension, no performer.
  assert.equal(
    songKey({ id: '', name: 'GORSE BEACON - 03.Signal Fires_320.mp3', performer: 'Gorse Beacon' }),
    songKey({ id: '', artist: 'Gorse Beacon', title: 'Signal Fires' }),
  );
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
    // The same song, in four other files: an album, a bootleg, a live record
    // and a demo. Different ids, one song.
    { id: '3', artist: 'Gorse Beacon', title: 'Signal Fires' },
    { id: '4', artist: 'gorse beacon', title: 'SIGNAL FIRES' },
    { id: '6', artist: 'Gorse Beacon', title: 'Signal Fires (Live at the Pier)' },
    { id: '7', artist: 'Gorse Beacon', title: 'Signal Fires [Demo]' },
    { id: '1', artist: 'Gorse Beacon', title: 'Signal Fires' },
    { id: '5', artist: 'Gorse Beacon', title: 'First Breath' },
  ];
  assert.deepEqual(
    freshForRadio(batch, queued).map((t) => t.id),
    ['5'],
  );
});

test('radio keeps one copy of a song it has not heard, and every file that names no song', () => {
  const batch = [
    { id: '1', artist: 'Gorse Beacon', title: 'First Breath' },
    { id: '2', artist: 'Gorse Beacon', title: 'First Breath (Remastered)' },
    { id: '3' },
    { id: '4' },
    { id: '5', artist: 'Gorse Beacon', title: '[untitled]' },
    { id: '6', artist: 'Gorse Beacon', title: '[untitled]' },
  ];
  assert.deepEqual(
    freshForRadio(batch, []).map((t) => t.id),
    ['1', '3', '4', '5', '6'],
  );
});

test('artist radio draws undamped: a guest credit is no reason to be drawn', () => {
  // One performer's catalogue, the farthest track crediting a guest besides.
  // Damped, every track that names them alone falls by a third per draw and
  // the guest's does not, so it is drawn far more than its place says.
  const pool = [
    ...Array.from({ length: 9 }, (_, i) => ({ id: `${i}`, artist: 'Gorse Beacon' })),
    { id: 'guest', artist: 'Gorse Beacon feat. Sixth Quay' },
  ];
  const drawn = (damp?: number) => {
    const rand = seeded(11);
    let n = 0;
    for (let run = 0; run < 400; run++) {
      if (pickRadio(pool, 5, rand, [], damp).some((t) => t.id === 'guest')) n++;
    }
    return n;
  };
  const undamped = drawn(1);
  const damped = drawn();
  assert.ok(undamped * 2 < damped, `undamped ${undamped}, damped ${damped} in 400`);
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

test('ahead: how many follow the one playing', () => {
  assert.equal(ahead(5, 0), 4);
  assert.equal(ahead(5, 4), 0);
  assert.equal(ahead(1, 0), 0);
  assert.equal(ahead(0, -1), 0);
});

test('recentArtists reads backwards through the order, not the queue tail', () => {
  const queue = [{ artist: 'A' }, { artist: 'B' }, { artist: 'C' }, { artist: 'D' }];
  const order = [3, 1, 0, 2]; // playing D, then B, then A, then C
  // At position 2 (A), the recently played are A, B, D — not the queue's tail.
  assert.deepEqual(recentArtists(order, queue, 2, 3), ['A', 'B', 'D']);
  assert.deepEqual(recentArtists(order, queue, 0, 5), ['D']); // clamped at the start
  assert.deepEqual(recentArtists([0], [{}], 0, 3), ['']); // no performer: empty, not a gap
});

test('freshFrom reads the sets and does not add the whole pool to them', () => {
  const ids = new Set(['a']);
  const heard = new Set([songKey({ id: '', artist: 'Gorse Beacon', title: 'Signal Fires' })]);
  const pool = [
    { id: 'a', artist: 'x', title: 'y' }, // already queued by id
    { id: 'b', artist: 'Gorse Beacon', title: 'Signal Fires' }, // queued recording
    { id: 'c', artist: 'Gorse Beacon', title: 'Other' }, // fresh
    { id: 'd', artist: 'gorse beacon', title: 'OTHER' }, // same recording as c, within batch
    { id: 'e' }, // untagged, always fresh
  ];
  assert.deepEqual(
    freshFrom(pool, ids, heard).map((t) => t.id),
    ['c', 'e'],
  );
  // The caller's sets are untouched: only what is actually queued is added,
  // by the caller, as it appends.
  assert.deepEqual([...ids], ['a']);
  assert.equal(heard.size, 1);
});

// The queue's time column is one width all the way down, and that width is
// hours only where a track needs them.
test('the time column is wide enough for hours only where a track runs one', () => {
  assert.equal(runsHours([]), false);
  // Ten minutes and more is one digit wider, and still not hours.
  assert.equal(runsHours([{ duration: 272_000 }, { duration: 765_000 }, {}]), false);
  assert.equal(runsHours([{ duration: 272_000 }, { duration: 3_600_000 }]), true);
  // Only what was added since the last asking is read.
  assert.equal(runsHours([{ duration: 3_600_000 }, { duration: 1_000 }], 1), false);
});
