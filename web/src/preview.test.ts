import { test } from 'node:test';
import assert from 'node:assert/strict';

import { holdsItem, itemCellKey } from './cells.ts';

// The grid recycles cells, and a preview takes the better part of a second
// to arrive: the dwell, then the fetch. What arrives has to be checked
// against what the cell holds *now*, or a film's frames are drawn over
// whatever the tile has since become — which is how hovering an album came
// to play a film over the sleeve.
test('preview: a cell still holding its item is previewed', () => {
  assert.equal(holdsItem('abc123:1700000000:4096', 'abc123'), true);
});

test('preview: a recycled cell is not', () => {
  // An album card, where a film tile used to be.
  assert.equal(holdsItem('def456:1700000000:0::At The Gates', 'abc123'), false);
  // A skeleton, and a cell that has not been rendered at all.
  assert.equal(holdsItem('sk', 'abc123'), false);
  assert.equal(holdsItem(undefined, 'abc123'), false);
});

test('preview: an id that is a prefix of another is not a match', () => {
  // Ids are fixed-width hashes, but the separator is what makes that true
  // here rather than by luck.
  assert.equal(holdsItem('abc1234:1700000000:4096', 'abc123'), false);
});

test('preview: the key a cell is drawn under starts with the item', () => {
  const item = {
    id: 'abc123',
    name: 'film.mkv',
    path: 'x/film.mkv',
    kind: 'video' as const,
    size: 4096,
    mtime: 1700000000,
    added: 1700000000,
  };
  assert.equal(holdsItem(itemCellKey(item), item.id), true);
  // A track carries its tags in the key, since they arrive after the file
  // does — and it still begins with the id.
  const track = { ...item, kind: 'audio' as const, artist: 'At The Gates', title: 'Suicide Nation' };
  assert.equal(holdsItem(itemCellKey(track), track.id), true);
  assert.notEqual(itemCellKey(track), itemCellKey({ ...track, title: 'Blinded by Fear' }));
});
