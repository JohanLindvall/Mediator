/**
 * The soundtrack memory: this film's choice, and the language for the rest.
 *
 * Pure enough to pin — the storage is behind guarded helpers, so the tests
 * stand in a localStorage of their own and also prove the no-storage case
 * degrades to "no memory, no harm" rather than a throw.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { preferredLang, rememberTrack, rememberedTrack } from './audiopref.ts';
import type { Track } from './playback.ts';

/**
 * Install a stand-in localStorage; the helpers read it lazily. Node exposes
 * the real one as an accessor, so plain assignment throws — defineProperty
 * replaces accessor and value alike.
 */
function installStorage(value: unknown): void {
  Object.defineProperty(globalThis, 'localStorage', { value, configurable: true });
}

function stubStorage(): void {
  const map = new Map<string, string>();
  installStorage({
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => void map.set(k, String(v)),
    removeItem: (k: string) => void map.delete(k),
  });
}

function throwingStorage(): void {
  installStorage({
    getItem: () => {
      throw new Error('denied');
    },
    setItem: () => {
      throw new Error('denied');
    },
    removeItem: () => {
      throw new Error('denied');
    },
  });
}

const TRACKS: Track[] = [
  { index: 0, lang: 'dan', default: true },
  { index: 1, lang: 'swe' },
];

test('a choice is remembered for the film and as a language for the rest', () => {
  stubStorage();
  assert.equal(rememberedTrack('item1'), undefined);
  assert.equal(preferredLang(), '');
  rememberTrack('item1', 1, TRACKS);
  assert.equal(rememberedTrack('item1'), 1);
  assert.equal(preferredLang(), 'swe');
  // Another film knows nothing of the first's track, only its language.
  assert.equal(rememberedTrack('item2'), undefined);
});

test('a track with no language still remembers the film', () => {
  stubStorage();
  rememberTrack('item3', 1, [{ index: 0 }, { index: 1 }]);
  assert.equal(rememberedTrack('item3'), 1);
  assert.equal(preferredLang(), '');
});

test('storage that refuses is no memory, not a throw', () => {
  throwingStorage();
  assert.doesNotThrow(() => rememberTrack('item4', 1, TRACKS));
  assert.equal(rememberedTrack('item4'), undefined);
  assert.equal(preferredLang(), '');
});
