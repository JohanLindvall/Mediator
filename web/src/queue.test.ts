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

import { appendToOrder, nextPosition, placeFirst, resumable, shuffleInPlace, windowRows } from './queue.ts';

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
