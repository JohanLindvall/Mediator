import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { FRAME_CACHE, SETTLE_MS, SeekFrames, momentAt, previewBox, previewLeft } from './seekframe.ts';

// A suitable size: a fifth of a wide player, within 160 to 320, and a portrait
// picture bounded by its height; asked for at the display's density.
test('the preview is sized to the player and the picture', () => {
  const wide = { playerW: 1920, playerH: 1080, aspect: 16 / 9, quarterTurns: 0, dpr: 1 };
  assert.deepEqual(previewBox(wide), { boxW: 320, boxH: 180, frameW: 320 });
  // Sharp on a dense screen, and no sharper than twice over.
  assert.equal(previewBox({ ...wide, dpr: 2 }).frameW, 640);
  assert.equal(previewBox({ ...wide, dpr: 3 }).frameW, 640);
  // A phone keeps a picture worth looking at.
  assert.deepEqual(previewBox({ ...wide, playerW: 390, playerH: 844 }), { boxW: 160, boxH: 90, frameW: 160 });
  // Portrait is bounded by the height, as it stands on screen.
  const tall = previewBox({ ...wide, aspect: 9 / 16 });
  assert.deepEqual(tall, { boxW: 135, boxH: 240, frameW: 160 });
  // A film the player has turned on its side stands the box on its side
  // too, and the frame is asked for by the side that was its width.
  assert.deepEqual(previewBox({ ...wide, quarterTurns: 1 }), { boxW: 135, boxH: 240, frameW: 256 });
  assert.deepEqual(previewBox({ ...wide, quarterTurns: 2 }), previewBox(wide));
  // A picture nobody has measured is taken as wide.
  assert.deepEqual(previewBox({ ...wide, aspect: 0 }), previewBox(wide));
  assert.deepEqual(previewBox({ ...wide, aspect: NaN }), previewBox(wide));
  // Never a frame too small to be worth the request.
  assert.equal(previewBox({ ...wide, aspect: 0.2 }).frameW, 96);
});

test('the preview stands over the pointer, inside the bar', () => {
  assert.equal(previewLeft(500, 320, 1000), 340);
  assert.equal(previewLeft(20, 320, 1000), 0);
  assert.equal(previewLeft(990, 320, 1000), 680);
  assert.equal(previewLeft(100, 320, 200), 0);
});

test('a point on the bar is a moment to the millisecond', () => {
  assert.equal(momentAt(250, 1000, 7200), 1_800_000);
  assert.equal(momentAt(1, 997, 5423.5), 5440);
  assert.equal(momentAt(-5, 1000, 100), 0);
  assert.equal(momentAt(2000, 1000, 100), 100_000);
  assert.equal(momentAt(10, 0, 100), 0);
  assert.equal(momentAt(10, 100, 0), 0);
});

/** A fetch whose answers are given by hand, and a clock that is run by hand. */
function rig() {
  const calls: { ms: number; signal: AbortSignal; resolve: (b: Blob | null) => void }[] = [];
  const shown: { url: string; exact: boolean }[] = [];
  const dropped: string[] = [];
  let waits = 0;
  let timers: { fn: () => void; id: number }[] = [];
  let nextId = 1;
  let made = 0;
  const frames = new SeekFrames({
    fetch: (ms, signal) =>
      new Promise((resolve, reject) => {
        calls.push({ ms, signal, resolve });
        signal.addEventListener('abort', () => reject(new Error('aborted')));
      }),
    show: (url, exact) => shown.push({ url, exact }),
    waiting: () => waits++,
    setTimeout: (fn) => {
      const id = nextId++;
      timers.push({ fn, id });
      return id;
    },
    clearTimeout: (id) => {
      timers = timers.filter((t) => t.id !== id);
    },
    makeURL: () => `frame:${++made}`,
    dropURL: (u) => dropped.push(u),
  });
  const settle = (): void => {
    const due = timers;
    timers = [];
    for (const t of due) t.fn();
  };
  return { frames, calls, shown, dropped, waits: () => waits, settle };
}

const blob = new Blob(['jpeg']);
const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

test('a frame is fetched and shown as exact when it is still the moment', async () => {
  const r = rig();
  r.frames.at(1000);
  assert.equal(r.waits(), 1);
  assert.deepEqual(r.calls.map((c) => c.ms), [1000]);
  r.calls[0]!.resolve(blob);
  await tick();
  assert.deepEqual(r.shown, [{ url: 'frame:1', exact: true }]);
  // Back to the same moment: shown at once, nothing fetched.
  r.frames.at(2000);
  r.frames.at(1000);
  assert.deepEqual(r.shown.at(-1), { url: 'frame:1', exact: true });
});

test('moving keeps one request, and the latest moment is asked for next', async () => {
  const r = rig();
  r.frames.at(1000);
  r.frames.at(2000);
  r.frames.at(3000);
  assert.deepEqual(r.calls.map((c) => c.ms), [1000], 'one request in flight, never a queue');
  r.calls[0]!.resolve(blob);
  await tick();
  // The picture that arrived is the freshest there is, and says it is not
  // the moment under the pointer.
  assert.deepEqual(r.shown, [{ url: 'frame:1', exact: false }]);
  assert.deepEqual(r.calls.map((c) => c.ms), [1000, 3000], 'skips what the pointer passed over');
  r.calls[1]!.resolve(blob);
  await tick();
  assert.deepEqual(r.shown.at(-1), { url: 'frame:2', exact: true });
});

test('resting drops a request for elsewhere and asks for where it rests', async () => {
  const r = rig();
  r.frames.at(1000);
  r.frames.at(5000);
  assert.equal(r.calls[0]!.signal.aborted, false, 'moving does not give it up');
  r.settle();
  assert.equal(r.calls[0]!.signal.aborted, true);
  assert.deepEqual(r.calls.map((c) => c.ms), [1000, 5000]);
  // The dropped one answering late shows nothing and asks for nothing.
  r.calls[0]!.resolve(blob);
  await tick();
  assert.deepEqual(r.shown, []);
  assert.equal(r.calls.length, 2);
  r.calls[1]!.resolve(blob);
  await tick();
  assert.deepEqual(r.shown, [{ url: 'frame:1', exact: true }]);
  assert.ok(SETTLE_MS > 0 && SETTLE_MS < 500);
});

test('leaving the bar stops what is being made, and another film drops the rest', async () => {
  const r = rig();
  r.frames.at(1000);
  r.calls[0]!.resolve(blob);
  await tick();
  r.frames.at(2000);
  r.frames.stop();
  assert.equal(r.calls[1]!.signal.aborted, true);
  r.calls[1]!.resolve(blob);
  await tick();
  assert.equal(r.calls.length, 2, 'nothing asked for after the pointer left');
  r.frames.reset();
  assert.deepEqual(r.dropped, ['frame:1']);
});

test('the cache is bounded, and every frame is released in the end', async () => {
  const r = rig();
  for (let i = 0; i <= FRAME_CACHE; i++) {
    r.frames.at(i * 1000);
    r.calls.at(-1)!.resolve(blob);
    await tick();
  }
  // One past the bound: the least recently used frame is released, and it is
  // not the one on screen, which is always the latest.
  assert.deepEqual(r.dropped, ['frame:1']);
  assert.equal(r.shown.at(-1)!.url, `frame:${FRAME_CACHE + 1}`);
  // Going back to a frame makes it the most recent, so the next one past the
  // bound takes another.
  r.frames.at(1000);
  assert.deepEqual(r.shown.at(-1), { url: 'frame:2', exact: true });
  r.frames.at(999_000);
  r.calls.at(-1)!.resolve(blob);
  await tick();
  assert.deepEqual(r.dropped, ['frame:1', 'frame:3']);
  r.frames.reset();
  const all = new Set(r.dropped);
  for (let i = 1; i <= FRAME_CACHE + 2; i++) assert.ok(all.has(`frame:${i}`), `frame:${i} released`);
  assert.equal(r.dropped.length, FRAME_CACHE + 2, 'each released once');
});
