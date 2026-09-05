/**
 * What counts as a swipe. The thresholds decide whether a finger stepped
 * between files or merely wandered, so they are pinned here.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { classify, MIN_PX, RATIO } from './swipe.ts';

test('a swipe is long enough along its axis and clearly along it', () => {
  assert.equal(classify(MIN_PX + 1, 0)?.dir, 'right');
  assert.equal(classify(-(MIN_PX + 1), 0)?.dir, 'left');
  assert.equal(classify(0, MIN_PX + 1)?.dir, 'down');
  assert.equal(classify(10, -(MIN_PX + 1))?.dir, 'up');
});

test('a tap, a short travel or a diagonal is not a swipe', () => {
  assert.equal(classify(0, 0), null);
  assert.equal(classify(MIN_PX, 0), null, 'exactly the threshold is not past it');
  // Along the axis by less than RATIO times the travel across it.
  assert.equal(classify(100, 100 / RATIO), null);
  assert.equal(classify(100, 100 / RATIO - 1)?.dir, 'right');
});

test('the travel is reported signed, for a viewer with thresholds of its own', () => {
  assert.deepEqual(classify(0, 90), { dir: 'down', dx: 0, dy: 90 });
});
