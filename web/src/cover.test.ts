/**
 * When the bar may keep the sleeve it has up, and when it must not.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { sameRelease } from './cover.ts';

test('tracks in one directory are one release, whatever the tags say', () => {
  assert.ok(
    sameRelease({ path: 'music/Harbour Lights/Tides/01.flac' }, { path: 'music/Harbour Lights/Tides/02.flac' }),
  );
  // A compilation: every track names its own performer, one sleeve for all.
  assert.ok(
    sameRelease(
      { path: 'music/Various/Sampler/01.mp3', album: 'Sampler', artist: 'Harbour Lights' },
      { path: 'music/Various/Sampler/02.mp3', album: 'Sampler', artist: 'Sixth Quay' },
    ),
  );
});

test('a release split over discs is one release', () => {
  assert.ok(
    sameRelease(
      { path: 'music/Harbour Lights/Anthology/CD1/07.flac', album: 'Anthology', artist: 'Harbour Lights' },
      { path: 'music/Harbour Lights/Anthology/CD2/01.flac', album: 'Anthology', artist: 'Harbour Lights' },
    ),
  );
});

test('two releases that share a title are told apart by their performers', () => {
  assert.ok(
    !sameRelease(
      { path: 'music/Harbour Lights/Live/01.mp3', album: 'Live', artist: 'Harbour Lights' },
      { path: 'music/Sixth Quay/Live/01.mp3', album: 'Live', artist: 'Sixth Quay' },
    ),
  );
});

test('different directories and no tag to agree on is a change of sleeve', () => {
  assert.ok(!sameRelease({ path: 'a/01.mp3' }, { path: 'b/01.mp3' }));
  assert.ok(!sameRelease({ path: 'a/01.mp3', album: 'Tides' }, { path: 'b/01.mp3' }));
});

test('an archived member is filed by the archive it is in', () => {
  // The member's path is the archive's own with the member after a NUL.
  const nul = String.fromCharCode(0);
  const inSet = (set: string, member: string) => ({ path: `music/${set}${nul}Tides/${member}` });
  assert.ok(sameRelease(inSet('set.rar', '01.mp3'), inSet('set.rar', '02.mp3')));
  assert.ok(!sameRelease(inSet('set.rar', '01.mp3'), inSet('other.rar', '01.mp3')));
});
