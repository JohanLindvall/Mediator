/**
 * The key a view opens on, pinned: it is the first thing a viewer sees of a
 * view, and a performer's releases opened by name for as long as the rule
 * was "the first row of the table".
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import type { ViewMode } from './content';
import { openingSort, sortOptions } from './sorts.ts';

const modes: ViewMode[] = [
  'all', 'video', 'image', 'audio', 'started', 'watched', 'popular', 'albums', 'artists', 'genres', 'audiobooks', 'series',
];

test("a performer's releases open by year, a genre's by name", () => {
  assert.equal(openingSort('albums', { artist: 'Harbour Lights' }), 'year');
  assert.equal(openingSort('albums'), 'name');
  assert.equal(openingSort('albums', { artist: '' }), 'name');
});

test('the popularity listing opens on popularity', () => {
  assert.equal(openingSort('popular'), 'popular');
});

test('every other view opens on the first thing it offers', () => {
  for (const mode of modes) {
    if (mode === 'popular') continue;
    assert.equal(openingSort(mode), sortOptions(mode)[0]![0], mode);
  }
});

test('a view never opens on a key it does not offer', () => {
  for (const mode of modes) {
    for (const where of [{}, { artist: 'Harbour Lights' }, { series: 'Harbour Lights' }, { near: 'abc' }]) {
      const key = openingSort(mode, where);
      assert.ok(
        sortOptions(mode, where).some(([v]) => v === key),
        `${mode} opens on ${key}, which it does not offer`,
      );
    }
  }
});

test('inside a show there is one order, and it is the episodes', () => {
  assert.deepEqual(sortOptions('series', { series: 'Harbour Lights' }), [['episode', 'Episode']]);
  assert.equal(openingSort('series', { series: 'Harbour Lights' }), 'episode');
});

test('what sounds like one thing is ordered by resemblance alone', () => {
  assert.deepEqual(sortOptions('albums', { near: 'abc' }), [['similarity', 'Similarity']]);
  assert.deepEqual(sortOptions('artists', { near: 'Harbour Lights' }), [['similarity', 'Similarity']]);
  assert.equal(openingSort('albums', { near: 'abc' }), 'similarity');
  // The audiobook shelf sorts like the records.
  assert.deepEqual(sortOptions('audiobooks'), sortOptions('albums'));
});
