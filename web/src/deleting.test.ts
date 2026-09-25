import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { deleteDoneText, deleteLabel, deleteRequestFor, deleteSummary } from './deleting.ts';

// A card is a file, a release, a show or a season — and a performer or a
// genre is a way of looking at releases, which there is nothing on the disk
// to delete.
test('a card asks to delete what it is', () => {
  assert.deepEqual(deleteRequestFor({ id: 'f1', kind: 'video', name: 'a.mkv' }), { kind: 'item', id: 'f1' });
  assert.deepEqual(deleteRequestFor({ id: 'f2', kind: 'audio', name: 'a.mp3' }), { kind: 'item', id: 'f2' });
  assert.deepEqual(deleteRequestFor({ id: 'd1', source: 'dir', tracks: 9, name: 'Saltings' }), { kind: 'album', id: 'd1' });
  assert.deepEqual(deleteRequestFor({ id: 't1', name: 'Harbour Lights', seasons: [] }), {
    kind: 'series',
    id: 'Harbour Lights',
  });
  assert.deepEqual(deleteRequestFor({ season: 2 }, 'Harbour Lights'), { kind: 'season', id: 'Harbour Lights', season: 2 });
  // A season with no show named is nothing to ask about.
  assert.equal(deleteRequestFor({ season: 2 }), null);
  assert.equal(deleteRequestFor({ id: 'a1', name: 'Lee Shore', albums: 3, tracks: 30 }), null);
  assert.equal(deleteRequestFor({ id: 'g1', name: 'Ambient', artists: 4, albums: 9 }), null);
});

test('the offer says what goes', () => {
  assert.equal(deleteLabel({ kind: 'item', id: 'x' }), 'Delete file…');
  assert.equal(deleteLabel({ kind: 'album', id: 'x' }), 'Delete release…');
  assert.equal(deleteLabel({ kind: 'series', id: 'x' }), 'Delete show…');
  assert.equal(deleteLabel({ kind: 'season', id: 'x', season: 1 }), 'Delete season…');
});

test('the confirmation and the outcome are said in words', () => {
  assert.equal(deleteSummary({ files: 1, folders: 0, bytes: 1_500_000_000 }), '1 file · 1.5 GB');
  assert.equal(deleteSummary({ files: 14, folders: 1, bytes: 412_000_000 }), '14 files and 1 folder · 412 MB');
  assert.equal(deleteDoneText({ files: 1, folders: 0, bytes: 1_500_000_000 }), 'Deleted 1 file (1.5 GB)');
  assert.equal(deleteDoneText({ files: 0, folders: 1, bytes: 412_000_000 }), 'Deleted 1 folder (412 MB)');
  assert.equal(
    deleteDoneText({ files: 2, folders: 0, bytes: 3000, kept: ['a: changed since it was shown'] }),
    'Deleted 2 files (3.0 KB) · 1 thing left in place',
  );
  assert.equal(
    deleteDoneText({ files: 0, folders: 0, bytes: 0, kept: ['a: changed since it was shown'] }),
    'Nothing was deleted: it changed since it was shown',
  );
});
