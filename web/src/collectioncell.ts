// SPDX-License-Identifier: MIT

/** Collection cards keep their loaded cover while their facts change. */
import { esc, hoverLines } from './format.ts';
import { icons } from './icons.ts';

export interface CollectionCard {
  /** A smaller badge in the opposite corner, such as a playlist marker. */
  tag?: string;
  cls: string;
  icon: string;
  badge: string;
  title: string;
  /** The line under the title, already markup where a fact is a link. */
  sub: string;
  /** The same line in plain text, for the tooltip; defaults to sub. */
  subTitle?: string;
  /** A play badge over the cover, where the card has something to play. */
  play?: string;
  /** Details shown on hover, such as a release's path and formats. */
  tip?: string;
  coverId?: string;
  coverSrc?: string;
  mtime: number;
}

export function renderCollectionCell(
  el: HTMLElement,
  card: CollectionCard,
  coverSrc: string | null,
  wireThumb: (el: HTMLElement, src: string | null) => void,
): void {
  // Only a change to the cover or its controls rebuilds the card. Counts,
  // tags and hover text change independently: recreating the image for one
  // more play hides already decoded artwork until the loader delivers again.
  const key = JSON.stringify([card.cls, card.icon, card.tag, card.play, coverSrc]);
  if (el.dataset.key !== key) {
    el.dataset.key = key;
    el.className = `cell card album-card ${card.cls}`.trimEnd();
    const play = card.play ? `<span class="hover-play" data-play title="${esc(card.play)}">${icons.play}</span>` : '';
    el.innerHTML = `
      <div class="thumb square">
        <div class="thumb-fallback">${card.icon}</div>
        <img alt="" decoding="async" draggable="false">
        <span class="badge"></span>${card.tag ? `<span class="badge tag">${esc(card.tag)}</span>` : ''}${play}
      </div>
      <div class="meta">
        <div class="title"></div>
        <div class="sub"></div>
      </div>`;
    wireThumb(el, coverSrc);
  }

  if (card.tip) el.title = card.tip;
  else el.removeAttribute('title');
  const badge = el.querySelector<HTMLElement>('.badge')!;
  if (badge.textContent !== card.badge) badge.textContent = card.badge;
  const title = el.querySelector<HTMLElement>('.title')!;
  if (title.textContent !== card.title) title.textContent = card.title;
  title.title = hoverLines(card.title, card.tip);
  const sub = el.querySelector<HTMLElement>('.sub')!;
  // Compare the original markup, not innerHTML's normalized spelling of
  // entities, so unchanged links stay mounted through unrelated updates.
  if (sub.dataset.key !== card.sub) {
    sub.innerHTML = card.sub;
    sub.dataset.key = card.sub;
  }
  sub.title = card.subTitle ?? card.sub;
}
