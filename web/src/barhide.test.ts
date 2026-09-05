import { test } from 'node:test';
import assert from 'node:assert/strict';

import { barShown, barStep, type BarScroll } from './barhide.ts';

const run = (ys: number[], from: BarScroll = barShown): BarScroll =>
  ys.reduce((s, y) => barStep(s, y), from);

test('bar: a committed downward scroll hides it', () => {
  assert.equal(run([20, 40, 80]).hidden, true);
});

test('bar: drift does not', () => {
  // A thumb resting on the screen moves a few pixels; the bar must not
  // flicker for that.
  assert.equal(run([20, 24, 28]).hidden, false);
});

test('bar: the first real upward move brings it back', () => {
  const hidden = run([20, 100, 200]);
  assert.equal(hidden.hidden, true);
  assert.equal(barStep(hidden, 150).hidden, false);
});

test('bar: at the top it is simply there', () => {
  const hidden = run([20, 100, 200]);
  assert.equal(barStep(hidden, 0).hidden, false);
});

test('bar: downward travel accumulates across events', () => {
  // Six slow events of 10px are one gesture of 60.
  assert.equal(run([20, 30, 40, 50, 60, 70]).hidden, true);
});

test('bar: an upward move resets the accumulator', () => {
  // Down 30, up, then down 30 again: two half-gestures are not one whole.
  assert.equal(run([20, 50, 30, 60]).hidden, false);
});

test('bar: a jump to the top reveals it — nothing can scroll up from there', () => {
  // Switching views resets the scroll in one assignment. The jump guard
  // must not swallow that: a hidden bar at the top would be stuck.
  const hidden = run([20, 300, 600]);
  assert.equal(hidden.hidden, true);
  assert.equal(barStep(hidden, 0).hidden, false);
});

test('bar: a programmatic jump decides nothing', () => {
  // An overlay closing puts the scroll position back in one assignment;
  // that is not the viewer asking for anything.
  assert.equal(barStep(barShown, 2000).hidden, false);
  const hidden = run([20, 100, 200]);
  assert.equal(barStep(hidden, 1800).hidden, true, 'nor does it reveal');
});
