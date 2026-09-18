/**
 * What a track is, shown under the pointer.
 *
 * The queue is a list of names and running times: a row says what is
 * playing and how long it lasts, and nothing about the record it came off.
 * That is right for a list — a queue is read down the side of the screen,
 * not studied — but it leaves the questions a listener actually asks about
 * a track they did not choose unanswered: what record is this, when, and
 * what kind of thing. So the row answers them on hover.
 *
 * **It carries what the row does not already show**, which is the rule
 * every hover in this app follows and the reason this one holds artwork
 * where the grid's does not. A tile already *is* the sleeve, so a sleeve
 * under the pointer would be the same picture twice; a queue row has no
 * picture at all, and the sleeve is the fastest way to recognise a record.
 * The performer is in the row, so it is not repeated here.
 *
 * It is a card rather than a `title` attribute for one reason only: a
 * `title` cannot hold a picture. Everything else about it is deliberately
 * the cheaper thing — no fetch of its own beyond the thumbnail the store
 * has already made, one element reused for every row, and nothing at all
 * until the pointer has stayed long enough to mean it.
 */
import { thumbUrl } from './api.ts';
import { belongsTo, esc, mediaShape } from './format.ts';
import type { Item } from './types.gen.ts';

/**
 * How long the pointer has to rest before a card is built.
 *
 * Crossing a queue on the way to the close button is not a request for
 * fifteen cards, and the same reasoning — and the same number — as the
 * grid's hover preview, which is the other thing in this app that answers a
 * pointer with more than a tooltip.
 */
const DWELL_MS = 250;

/** How wide the sleeve is fetched. Twice the drawn size, for a dense screen. */
const ART_W = 160;

export class TrackCard {
  private el: HTMLElement | null = null;
  private timer = 0;
  private shown: string | null = null;

  constructor(private readonly host: HTMLElement) {}

  /**
   * Watch a list whose rows are recreated wholesale.
   *
   * The queue paints a window of rows into a spacer on every track change
   * and on every scroll, so a listener on a row would be thrown away with
   * it. One delegated pair on the container survives all of that, and knows
   * nothing about which rows happen to exist.
   */
  watch(list: HTMLElement, itemAt: (row: HTMLElement) => Item | null): void {
    list.addEventListener('pointerover', (ev) => {
      // Touch has no hover: a tap would open a card over the row it is
      // trying to play, which is the fault the grid's preview refuses the
      // same way.
      if (ev.pointerType === 'touch') return;
      const row = (ev.target as HTMLElement | null)?.closest<HTMLElement>('.q-row');
      if (!row || !list.contains(row)) return;
      const it = itemAt(row);
      if (!it || it.id === this.shown) return;
      this.arm(row, it);
    });
    list.addEventListener('pointerleave', () => this.hide());
    // A card is about the row it was opened over, and both of these move
    // the row out from under the pointer without a leave event: the window
    // repaints as the queue advances, and the list scrolls under a still
    // finger. Neither leaves the card describing something else.
    list.addEventListener('scroll', () => this.hide(), { passive: true });
  }

  /** Take the card down and forget any pointer that was still resting. */
  hide(): void {
    clearTimeout(this.timer);
    this.timer = 0;
    this.shown = null;
    this.el?.remove();
    this.el = null;
  }

  private arm(row: HTMLElement, it: Item): void {
    clearTimeout(this.timer);
    this.timer = window.setTimeout(() => {
      // The row may have gone in the meantime — the queue repaints on every
      // track change — and a card hanging off a row nothing holds any more
      // would sit wherever that row used to be.
      if (!row.isConnected) return;
      this.show(row, it);
    }, DWELL_MS);
  }

  private show(row: HTMLElement, it: Item): void {
    this.shown = it.id;
    const el = this.el ?? document.createElement('div');
    el.className = 'track-card';
    const facts = belongsTo(it);
    const shape = mediaShape(it);
    // The sleeve is the release's, which for a track is its own thumbnail:
    // the store keys those by item, and a track's is the picture beside it
    // on disk or in its tags. Left out rather than replaced by a
    // placeholder where there is none — a card is not a tile, and an empty
    // grey square says less than the words beside it.
    el.innerHTML = `
      <img class="tc-art" alt="" decoding="async" src="${esc(thumbUrl(it.id, ART_W, it.mtime))}">
      <div class="tc-body">
        <div class="tc-title">${esc(it.title || it.name)}</div>
        ${facts ? `<div class="tc-facts">${esc(facts)}</div>` : ''}
        ${shape ? `<div class="tc-shape">${esc(shape)}</div>` : ''}
      </div>`;
    const art = el.querySelector<HTMLImageElement>('.tc-art')!;
    art.onerror = () => art.remove();
    if (!this.el) this.host.append(el);
    this.el = el;
    this.place(el, row);
  }

  /**
   * Put the card beside its row and inside the window.
   *
   * Beside rather than below, because the queue stands against the right
   * edge with the listing behind it: a card under the row would cover the
   * next rows, which are the ones being read. It is clamped to the viewport
   * the same way the player's menus are — a row near the foot of a tall
   * queue would otherwise open a card off the bottom of the screen.
   */
  private place(el: HTMLElement, row: HTMLElement): void {
    const r = row.getBoundingClientRect();
    const c = el.getBoundingClientRect();
    const gap = 8;
    let left = r.left - c.width - gap;
    if (left < gap) left = Math.min(r.right + gap, window.innerWidth - c.width - gap);
    const top = Math.max(gap, Math.min(r.top, window.innerHeight - c.height - gap));
    el.style.left = `${Math.max(gap, left)}px`;
    el.style.top = `${top}px`;
  }
}
