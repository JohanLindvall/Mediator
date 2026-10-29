// SPDX-License-Identifier: MIT

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { renderCollectionCell, type CollectionCard } from './collectioncell.ts';

// Model the DOM operation that loses decoded artwork: assigning innerHTML
// replaces every child. The renderer runs unchanged, with a thumbnail loader
// that records which image it was handed instead of making network requests.
class Element {
  dataset: { key?: string } = {};
  className = '';
  textContent = '';
  title = '';
  writes = 0;
  private html = '';
  children = new Map<string, Element>();

  get innerHTML(): string { return this.html; }
  set innerHTML(html: string) {
    this.html = html;
    this.writes++;
    this.children = new Map();
    for (const match of html.matchAll(/<(\w+)(?: class="([^"]+)")?/g)) {
      const child = new Element();
      if (!this.children.has(match[1])) this.children.set(match[1], child);
      for (const cls of (match[2] ?? '').split(' ').filter(Boolean)) {
        if (!this.children.has(`.${cls}`)) this.children.set(`.${cls}`, child);
      }
    }
  }

  querySelector(selector: string): Element | null { return this.children.get(selector) ?? null; }
  removeAttribute(name: string): void { if (name === 'title') this.title = ''; }
}

const card: CollectionCard = {
  cls: '', icon: '', badge: '2 ♪', title: 'Invented Release',
  sub: 'Invented Performer', subTitle: 'Invented Performer',
  play: 'Play this album', mtime: 1,
};

function surface() {
  const cell = new Element();
  const loads: { img: Element | null; src: string | null }[] = [];
  return {
    cell, loads,
    draw(card: CollectionCard, src: string | null = '/cover?v=1') {
      renderCollectionCell(cell as unknown as HTMLElement, card, src, () => {
        loads.push({ img: cell.querySelector('img'), src });
      });
    },
  };
}

test('play counts update without replacing the cover or restarting its load', () => {
  const { cell, loads, draw } = surface();
  draw(card);
  const img = cell.querySelector('img');
  const title = cell.querySelector('.title');
  const sub = cell.querySelector('.sub')!;
  for (const n of [1, 2]) {
    const text = `Invented Performer · ${n} play${n === 1 ? '' : 's'}`;
    draw({ ...card, sub: text, subTitle: text });
    assert.equal(sub.innerHTML, text, 'the count still changes');
    assert.equal(sub.title, text, 'the hover agrees');
    assert.equal(cell.querySelector('img'), img, 'decoded artwork stays mounted');
    assert.equal(cell.querySelector('.title'), title, 'the title stays mounted too');
  }
  assert.equal(cell.writes, 1);
  assert.equal(loads.length, 1, 'an in-flight cover is not restarted either');
});

test('updated facts and tooltips keep the cover, and unchanged links are untouched', () => {
  const { cell, loads, draw } = surface();
  const linked = { ...card, sub: '<span data-artist="A &amp; B">A &amp; B</span>', subTitle: 'A & B' };
  draw(linked);
  const sub = cell.querySelector('.sub')!;
  const img = cell.querySelector('img');
  draw({ ...linked, title: 'Invented Renamed Release', badge: '3 ♪', tip: 'FLAC · 3 tracks' });
  assert.equal(cell.querySelector('.title')!.textContent, 'Invented Renamed Release');
  assert.match(cell.querySelector('.title')!.title, /FLAC/);
  assert.equal(cell.querySelector('.badge')!.textContent, '3 ♪');
  assert.equal(cell.title, 'FLAC · 3 tracks');
  assert.equal(sub.writes, 1, 'unchanged links survive the refresh');
  assert.equal(cell.querySelector('img'), img);
  assert.equal(loads.length, 1);

  draw(linked);
  assert.equal(cell.title, '', 'a removed tooltip does not linger');
  assert.equal(cell.querySelector('.title')!.title, card.title);
});

test('a changed cover or a recycled cell still loads fresh artwork', () => {
  const { cell, loads, draw } = surface();
  draw(card, null);
  draw(card);
  const first = cell.querySelector('img');
  draw(card, '/cover?v=2');
  assert.notEqual(cell.querySelector('img'), first, 'a new file version replaces the image');
  const second = cell.querySelector('img');
  // scrubCell clears the key when the virtual grid recycles this node.
  delete cell.dataset.key;
  cell.innerHTML = '';
  draw(card, '/cover?v=2');
  assert.notEqual(cell.querySelector('img'), second);
  assert.deepEqual(loads.map(({ src }) => src), [null, '/cover?v=1', '/cover?v=2', '/cover?v=2']);
});
