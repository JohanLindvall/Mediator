import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { holdsItem, itemCellKey, scrubCell, type CellSurface } from './cells.ts';
import type { Item } from './types.gen';

// The grid recycles cells across renderers and across whole views, and a
// renderer that decorates one cannot be made to remember to undo it. Both
// faults this has caught were that shape: the preview handler that survived
// recycling played a film over an album card, and the hover tooltip that
// survived it named one performer's file over another performer's release —
// a title is an attribute, so it outlives the innerHTML wipe exactly as the
// handler properties do.
test('a recycled cell keeps nothing its renderer left', () => {
  const gone: string[] = [];
  const el: CellSurface = {
    innerHTML: '<img src="thumb">',
    dataset: { key: 'abc123:1:2:0' },
    classList: { remove: (n: string) => gone.push(`class:${n}`) },
    onpointerenter: () => {},
    onpointerleave: () => {},
    removeAttribute: (n: string) => gone.push(`attr:${n}`),
  };
  scrubCell(el);
  assert.equal(el.innerHTML, '', 'content stays behind');
  assert.equal(el.dataset.key, undefined, 'the key would make the next render skip');
  assert.equal(el.onpointerenter, null, 'the preview handler survives the wipe');
  assert.equal(el.onpointerleave, null);
  assert.ok(gone.includes('attr:title'), 'the tooltip is an attribute and survives the wipe');
  assert.ok(gone.includes('class:cell-in'), 'the mount animation must not replay');
});

// The key and its reader live together so they cannot drift apart; pinned
// here beside the scrub since all three are what a cell carries.
test('the key still begins with the id its reader looks for', () => {
  const item = { id: 'abc123', mtime: 5, size: 9, kind: 'video' } as Item;
  const key = itemCellKey(item);
  assert.ok(holdsItem(key, 'abc123'));
  assert.ok(!holdsItem(key, 'abc12'));
  assert.ok(!holdsItem(undefined, 'abc123'));
});

// The shape is what the tile says on hover, and it arrives after the file
// does — a key without it would leave the tooltip empty for as long as the
// cell stayed on screen.
test('the key changes when the shape arrives', () => {
  const before = { id: 'abc', mtime: 1, size: 2, kind: 'video' } as Item;
  const after = { ...before, vcodec: 'h264', width: 1920, height: 1080 } as Item;
  assert.notEqual(itemCellKey(before), itemCellKey(after));
});
