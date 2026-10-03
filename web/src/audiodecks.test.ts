import assert from 'node:assert/strict';
import { test } from 'node:test';
import { AudioDecks } from './audiodecks.ts';

function deferred() {
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

/**
 * Permission belongs to a successfully started element. A play on an empty
 * element fails; calling pause afterwards cannot turn that into permission.
 * The visibility flag models the locked phone without relying on timers.
 */
function setup(reuse: boolean) {
  const env = { gesture: true, hidden: false, calls: [] as string[] };
  class Media {
    source: string | null = null;
    started = false;
    paused = true;
    currentTime = 0;
    loop = false;
    preload = '';
    pending: ReturnType<typeof deferred> | null = null;
    name: string;
    constructor(name: string) { this.name = name; }
    get src() { return this.source ?? ''; }
    set src(url: string) {
      env.calls.push(`${this.name} src ${url}`);
      this.source = url;
      this.currentTime = 0;
      this.paused = true;
    }
    play() {
      env.calls.push(`${this.name} play ${env.hidden ? 'hidden' : 'visible'}`);
      if (!this.source) return Promise.reject(new DOMException('No source', 'NotSupportedError'));
      if (!env.gesture && !this.started) return Promise.reject(new DOMException('Tap required', 'NotAllowedError'));
      this.started = true;
      this.paused = false;
      return this.pending?.promise ?? Promise.resolve();
    }
    pause() { env.calls.push(`${this.name} pause`); this.paused = true; }
    load() { env.calls.push(`${this.name} load`); this.currentTime = 0; this.paused = true; }
    getAttribute() { return this.source; }
    removeAttribute() { env.calls.push(`${this.name} clear`); this.source = null; }
  }
  const a = new Media('a');
  const b = new Media('b');
  const decks = new AudioDecks([a as unknown as HTMLAudioElement, b as unknown as HTMLAudioElement], reuse);
  return { env, a, b, decks };
}

for (const hidden of [false, true]) {
  test(`iOS queue reuses the tapped element with the page ${hidden ? 'hidden' : 'visible'}`, async () => {
    const { decks, a, b, env } = setup(true);
    const first = decks.load('/track-one', true);
    // Playback must already have been requested while the tap is active.
    assert.equal(a.started, true);
    env.gesture = false;
    await first;
    env.hidden = hidden;
    for (const url of ['/track-two', '/track-three', '/track-four']) {
      a.currentTime = 0.1; // the current track has just looped
      decks.preload(url);
      env.calls.length = 0;
      await decks.load(url, true);
      assert.equal(decks.current, a);
      assert.equal(a.paused, false);
      assert.equal(a.loop, true);
      assert.deepEqual(env.calls, [`a src ${url}`, `a play ${hidden ? 'hidden' : 'visible'}`]);
    }
    assert.equal(b.started, false);
    assert.equal(b.source, null);
  });
}

test('iOS preload and resume never start or load the spare element', async () => {
  const { decks, a, b, env } = setup(true);
  await decks.load('/track-one', true);
  env.calls.length = 0;
  decks.preload('/track-two');
  assert.deepEqual(env.calls, []);
  env.gesture = false;
  env.hidden = true;
  a.currentTime = 42;
  decks.pause();
  assert.equal(a.paused, true);
  await decks.play();
  assert.equal(a.currentTime, 42);
  assert.equal(a.paused, false);
  assert.deepEqual(env.calls, ['a pause', 'a play hidden']);
  assert.equal(b.started, false);
});

test('a repeated track restarts without replacing its source', async () => {
  const { decks, a, env } = setup(true);
  await decks.load('/track-one', true);
  a.currentTime = 42;
  env.gesture = false;
  env.calls.length = 0;
  await decks.load('/track-one', true);
  assert.equal(a.currentTime, 0);
  assert.deepEqual(env.calls, ['a play visible']);
});

test('a paused load stays silent and can be resumed on the same element', async () => {
  const { decks, a, env } = setup(true);
  await decks.load('/track-one', true);
  env.gesture = false;
  await decks.load('/track-two', false);
  assert.equal(a.paused, true);
  await decks.play();
  assert.equal(a.src, '/track-two');
  assert.equal(a.paused, false);
});

test('desktop handoff uses its preload without requesting the file again', async () => {
  const { decks, a, b, env } = setup(false);
  await decks.load('/track-one', true);
  assert.equal(decks.current, b);
  decks.preload('/track-two');
  assert.equal(b.paused, false);
  assert.equal(a.paused, true);
  env.calls.length = 0;
  decks.preload('/track-two');
  assert.deepEqual(env.calls, []);
  await decks.load('/track-two', true);
  assert.equal(decks.current, a);
  assert.equal(a.paused, false);
  assert.equal(b.paused, true);
  assert.equal(b.source, null);
  assert.deepEqual(env.calls, ['a play visible', 'b pause', 'b clear', 'b load']);
});

test('discarding a preload does not stop the current track', async () => {
  const { decks, a, b, env } = setup(false);
  await decks.load('/track-one', true);
  decks.preload('/track-two');
  env.calls.length = 0;
  decks.clear(decks.idle);
  assert.equal(b.paused, false);
  assert.equal(a.source, null);
  assert.deepEqual(env.calls, ['a clear', 'a load']);
});

test('a refused current play remains observable', async () => {
  const { decks, env } = setup(true);
  env.gesture = false;
  env.hidden = true;
  await assert.rejects(decks.load('/track-one', true), { name: 'NotAllowedError' });
});

for (const action of ['skip', 'pause', 'close'] as const) {
  test(`late play rejection after ${action} is ignored without restarting playback`, async () => {
    const { decks, a, env } = setup(true);
    const pending = deferred();
    a.pending = pending;
    const first = decks.load('/track-one', true);
    a.pending = null;
    if (action === 'skip') await decks.load('/track-two', true);
    else if (action === 'pause') decks.pause();
    else decks.stop(decks.current);
    env.calls.length = 0;
    pending.reject(new DOMException('Superseded', 'NotAllowedError'));
    await first;
    assert.deepEqual(env.calls, []);
    assert.equal(a.paused, action !== 'skip');
  });
}

test('closing releases both sources and a new tap can start another queue', async () => {
  const { decks, a, b } = setup(false);
  await decks.load('/track-one', true);
  decks.preload('/track-two');
  for (const element of decks.elements) decks.stop(element);
  assert.equal(a.source, null);
  assert.equal(b.source, null);
  assert.equal(a.paused, true);
  assert.equal(b.paused, true);
  await decks.load('/track-three', true);
  assert.equal(decks.current.src, '/track-three');
  assert.equal(decks.current.paused, false);
});
