/**
 * Where "queue all" is offered, and what it asks for. A button over a grid
 * of films that queued the odd track among them would be a puzzle, so the
 * rule is pinned here rather than left to the toolbar.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { queueSource } from './content.ts';

test('the grouped views of music queue themselves', () => {
  assert.equal(queueSource('albums', null), 'albums');
  assert.equal(queueSource('artists', null), 'artists');
  assert.equal(queueSource('genres', null), 'genres');
  assert.equal(queueSource('albums', ['music']), 'albums');
  assert.equal(queueSource('audiobooks', null), 'audiobooks');
});

test('the music chip queues its tracks, whatever the face', () => {
  assert.equal(queueSource('audio', null), 'items');
  assert.equal(queueSource('audio', ['music', 'videos']), 'items');
});

test('a mixed listing queues only on a face that shows nothing but music', () => {
  assert.equal(queueSource('all', ['music']), 'items');
  assert.equal(queueSource('popular', ['music']), 'items');
  assert.equal(queueSource('all', null), null);
  assert.equal(queueSource('all', ['music', 'videos']), null);
  assert.equal(queueSource('popular', ['videos']), null);
});

test('nothing else is music to queue', () => {
  assert.equal(queueSource('video', ['music']), null);
  assert.equal(queueSource('image', null), null);
  assert.equal(queueSource('series', null), null);
  assert.equal(queueSource('started', ['music']), null);
  assert.equal(queueSource('watched', null), null);
});
