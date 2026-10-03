import assert from 'node:assert/strict';
import { test } from 'node:test';
import { claimMediaKeys, type MediaKeys } from './mediakeys.ts';

test('media keys preserve the owner, restore previous claims and release handlers', () => {
  const original = Object.getOwnPropertyDescriptor(navigator, 'mediaSession');
  const handlers = new Map<string, (() => void) | null>();
  Object.defineProperty(navigator, 'mediaSession', {
    configurable: true,
    value: { setActionHandler: (name: string, fn: (() => void) | null) => handlers.set(name, fn) },
  });
  const released: (() => void)[] = [];
  const calls: string[] = [];
  const bar: MediaKeys = {
    play() { assert.equal(this, bar); calls.push('bar play'); },
    pause() { assert.equal(this, bar); calls.push('bar pause'); },
  };
  const player: MediaKeys = {
    play() { assert.equal(this, player); calls.push('player play'); },
    pause() { assert.equal(this, player); calls.push('player pause'); },
    stop() { assert.equal(this, player); calls.push('player stop'); },
  };
  try {
    released.push(claimMediaKeys(bar));
    handlers.get('stop')!();
    const releasePlayer = claimMediaKeys(player);
    released.push(releasePlayer);
    handlers.get('stop')!();
    releasePlayer();
    handlers.get('play')!();
    assert.deepEqual(calls, ['bar pause', 'player stop', 'bar play']);
  } finally {
    released.reverse().forEach(release => release());
    assert.ok([...handlers.values()].every(fn => fn === null));
    if (original) Object.defineProperty(navigator, 'mediaSession', original);
    else Reflect.deleteProperty(navigator, 'mediaSession');
  }
});
