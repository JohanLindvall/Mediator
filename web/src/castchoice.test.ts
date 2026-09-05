import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { castAudioChoice } from './playback.ts';

type T = { index: number; default?: boolean; lang?: string };

// Naming a track costs a copy of the film with one soundtrack in it, so it
// is named exactly when the viewer's choice differs from what the set would
// have picked anyway — the file's default.
test('the track is named only when it differs from the default', () => {
  const dubbed: T[] = [
    { index: 0, lang: 'ara' },
    { index: 1, lang: 'eng', default: true },
    { index: 2, lang: 'spa' },
  ];
  // The case the old rule dropped: track 0 chosen over a non-zero default.
  // "Send when non-zero" read this as nothing to send, and the one language
  // the viewer asked for was the one that never reached the set.
  assert.equal(castAudioChoice(dubbed, 0), 0);
  // The case the old rule paid for: the default chosen, but not track 0 —
  // a copy of the whole film that changed nothing.
  assert.equal(castAudioChoice(dubbed, 1), undefined);
  assert.equal(castAudioChoice(dubbed, 2), 2);
});

test('with no default flag the first track stands for it', () => {
  const plain: T[] = [{ index: 0 }, { index: 1 }];
  assert.equal(castAudioChoice(plain, 0), undefined);
  assert.equal(castAudioChoice(plain, 1), 1);
});

// One soundtrack is not a choice, and nothing should be said about it.
test('a single soundtrack never names a track', () => {
  assert.equal(castAudioChoice([{ index: 0 }], 0), undefined);
  assert.equal(castAudioChoice([], 0), undefined);
});
