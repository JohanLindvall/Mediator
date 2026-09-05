/**
 * What the tab says: the film in front of everything, else the track the
 * bar holds, else the app.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { nowPlayingTitle } from './nowplaying.ts';

test('the player wins, then the bar, then the app', () => {
  assert.equal(nowPlayingTitle('A Film', 'A Track', 'Mediator'), 'A Film — Mediator');
  assert.equal(nowPlayingTitle(null, 'A Track', 'Mediator'), 'A Track — Mediator');
  assert.equal(nowPlayingTitle(null, null, 'Mediator'), 'Mediator');
});
