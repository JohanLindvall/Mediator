import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { deafStep, DEAF_AFTER, type DeafState } from './playback.ts';

const fresh = (): DeafState => ({ heard: false, silent: 0, deaf: false });

// Playing, and nothing arriving: after long enough that no real passage of
// music could account for it, the panel says so.
test('a playing film with no audio reaching the graph is reported', () => {
  let s = fresh();
  for (let t = 0; t < DEAF_AFTER - 1; t++) s = deafStep(s, { heard: false, sounding: true, dt: 1 });
  assert.equal(s.deaf, false, 'reported before it could be sure');
  s = deafStep(s, { heard: false, sounding: true, dt: 1 });
  assert.equal(s.deaf, true);
});

// A paused film reads exactly like a broken audio path, and the difference is
// the whole question: waiting on it would accuse every browser of everything.
test('a pause never earns the report, however long', () => {
  let s = fresh();
  for (let t = 0; t < DEAF_AFTER * 5; t++) s = deafStep(s, { heard: false, sounding: false, dt: 1 });
  assert.equal(s.deaf, false);
  assert.equal(s.silent, 0);
});

// One bar is proof the graph works. A film with a long silence in the middle
// of it must not go on to accuse the browser that has been drawing it.
test('once something has been heard it is never reported', () => {
  let s = deafStep(fresh(), { heard: true, sounding: true, dt: 1 });
  for (let t = 0; t < DEAF_AFTER * 5; t++) s = deafStep(s, { heard: false, sounding: true, dt: 1 });
  assert.equal(s.deaf, false);
  assert.equal(s.heard, true);
});

// And it is taken back rather than left standing: nothing is claimed that
// turned out to be untrue.
test('sound arriving late withdraws the report', () => {
  let s = fresh();
  for (let t = 0; t < DEAF_AFTER; t++) s = deafStep(s, { heard: false, sounding: true, dt: 1 });
  assert.equal(s.deaf, true);
  s = deafStep(s, { heard: true, sounding: true, dt: 1 });
  assert.equal(s.deaf, false);
  assert.equal(s.heard, true);
});

// The clock is the frame clock, so a slow screen reaches the same conclusion
// at the same moment as a fast one rather than several times later — which is
// what counting frames would give.
test('the wait is in seconds, not in frames', () => {
  const untilDeaf = (dt: number): number => {
    let s = fresh();
    let t = 0;
    while (!s.deaf && t < DEAF_AFTER * 4) {
      s = deafStep(s, { heard: false, sounding: true, dt });
      t += dt;
    }
    return t;
  };
  // 120 Hz, 60 Hz, 30 Hz, and a browser throttling a background tab: each
  // lands within the frame that carries it.
  for (const dt of [1 / 120, 1 / 60, 1 / 30, 0.1]) {
    const t = untilDeaf(dt);
    assert.ok(Math.abs(t - DEAF_AFTER) <= dt + 1e-6, `at dt=${dt} it took ${t}s`);
  }
});
