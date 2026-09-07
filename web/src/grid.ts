/**
 * Windowed virtual grid: renders only the cells intersecting the viewport
 * (plus overscan), recycling DOM nodes. Handles 10k+ items smoothly.
 *
 * Cells are reconciled by item identity, not by index. When an item is
 * inserted at the top of a newest-first listing, every other item's index
 * shifts by one — index-owned cells would all change content and the whole
 * screen would repaint. Identity-owned cells just move to their new
 * position; their DOM (and decoded thumbnails) stays intact, so only the
 * genuinely new item renders.
 */

import { scrubCell } from './cells';

export interface GridAdapter<T> {
  /** Total item count; -1 means "not yet known". */
  count(): number;
  get(i: number): T | undefined;
  /** Called with the index range about to become visible (for prefetching). */
  need(a: number, b: number): void;
  /** Stable identity of an item; the cell showing it follows it around. */
  itemKey(item: T): string;
  /** Fill a cell element for index i; item is undefined while loading. */
  render(el: HTMLElement, item: T | undefined, i: number): void;
  /** Optional: cell is unmounted or recycled — cancel its pending work. */
  release?(el: HTMLElement): void;
  /** A press on a cell: a click, or Enter/Space on a focused one. */
  onClick(i: number, ev: MouseEvent | KeyboardEvent): void;
  /** Minimum card width for the current container width. */
  minCardWidth(containerW: number): number;
  /** Cell height for a given computed card width. */
  cellHeight(cardW: number): number;
}

const OVERSCAN_ROWS = 3;

interface Mounted {
  el: HTMLElement;
  index: number;
}

export class VirtualGrid<T> {
  /** Mounted cells by item identity ("#i:<index>" for loading slots). */
  private cells = new Map<string, Mounted>();
  private pool: HTMLElement[] = [];
  private cols = 1;
  private cardW = 0;
  private rowH = 0;
  private readonly gap = 14;
  private rafPending = false;
  /** True while an update is reconciling against data that just changed. */
  private entering = false;
  private resizeObs: ResizeObserver;
  private lastRange: [number, number] = [0, -1];

  constructor(
    private scroller: HTMLElement,
    private spacer: HTMLElement,
    private plane: HTMLElement,
    private adapter: GridAdapter<T>,
  ) {
    this.scroller.addEventListener('scroll', this.onScroll, { passive: true });
    this.resizeObs = new ResizeObserver(() => this.layout());
    this.resizeObs.observe(this.scroller);
    this.plane.addEventListener('click', this.onPlaneClick);
    this.plane.addEventListener('keydown', this.onPlaneKey);
    this.layout();
  }

  destroy(): void {
    this.scroller.removeEventListener('scroll', this.onScroll);
    this.resizeObs.disconnect();
    this.plane.removeEventListener('click', this.onPlaneClick);
  }

  /**
   * Point the grid at a source of data. The same adapter with a new query
   * behind it is not a reason to tear the screen down: dropping every cell
   * repaints the whole window as skeletons and then repaints it again when
   * the answer lands, which reads as a flash on every keystroke. Only a
   * different adapter — a different kind of thing, sized and rendered
   * differently — is worth a hard reset.
   */
  setAdapter(adapter: GridAdapter<T>): void {
    if (adapter === this.adapter) {
      this.rewind();
      return;
    }
    this.adapter = adapter;
    this.hardReset();
  }

  /**
   * A new query for the same kind of thing: back to the top, keeping every
   * mounted cell. The source goes on serving the outgoing rows until the
   * new ones land, so reconciliation hands whatever the two queries have in
   * common its existing cell — DOM, decoded thumbnail and all — and only
   * what actually differs is built.
   */
  rewind(): void {
    this.scroller.scrollTop = 0;
    this.refresh();
  }

  /** Full reset: drop all cells (the view changed), scroll to top. */
  hardReset(scrollTop = true): void {
    for (const m of this.cells.values()) this.recycle(m.el);
    this.cells.clear();
    this.lastRange = [0, -1];
    this.cols = 0; // force geometry recompute (the adapter may size differently)
    if (scrollTop) this.scroller.scrollTop = 0;
    // Everything that mounts after this is new content by definition, so it
    // eases in like any other arrival rather than snapping into an empty
    // screen.
    this.entering = true;
    try {
      this.layout();
    } finally {
      this.entering = false;
    }
  }

  /** Data arrived or changed: reconcile the window against the new data. */
  refresh(): void {
    this.layout(false); // recompute geometry without updating the window yet
    // Cells mounted from here are cells the new data brought, and they fade
    // in. Ones mounted by a scroll are not: those are the same rows the
    // viewer is travelling towards, and they should simply be there.
    this.entering = true;
    try {
      this.update(true);
    } finally {
      this.entering = false;
    }
  }

  private onScroll = (): void => {
    if (this.rafPending) return;
    this.rafPending = true;
    requestAnimationFrame(() => {
      this.rafPending = false;
      this.update();
    });
  };

  private onPlaneClick = (ev: MouseEvent): void => {
    const cell = (ev.target as HTMLElement).closest<HTMLElement>('.cell');
    if (!cell || !this.plane.contains(cell)) return;
    const idx = Number(cell.dataset.idx);
    if (Number.isFinite(idx)) this.adapter.onClick(idx, ev);
  };

  /**
   * Enter or Space on a focused cell opens it as a click would. The cells
   * are focusable (obtain), so the grid can be walked with Tab and read by
   * a screen reader; without this it could be reached and not opened.
   */
  private onPlaneKey = (ev: KeyboardEvent): void => {
    if (ev.key !== 'Enter' && ev.key !== ' ') return;
    const cell = (ev.target as HTMLElement).closest<HTMLElement>('.cell');
    if (!cell || cell !== ev.target) return; // a button inside the cell handles its own keys
    const idx = Number(cell.dataset.idx);
    if (!Number.isFinite(idx)) return;
    ev.preventDefault();
    this.adapter.onClick(idx, ev);
  };

  /** Recompute geometry (columns, card size), then update visible window. */
  layout(update = true): void {
    const style = getComputedStyle(this.spacer);
    const padX = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight);
    const w = this.spacer.clientWidth - padX;
    if (w <= 0) return;
    const minCard = this.adapter.minCardWidth(w);
    const cols = Math.max(1, Math.floor((w + this.gap) / (minCard + this.gap)));
    const cardW = Math.floor((w - this.gap * (cols - 1)) / cols);
    if (cols !== this.cols || Math.abs(cardW - this.cardW) > 1) {
      this.cols = cols;
      this.cardW = cardW;
      this.rowH = this.adapter.cellHeight(cardW);
      // Geometry changed: force re-position of every mounted cell.
      for (const m of this.cells.values()) this.position(m.el, m.index);
    }
    if (update) this.update(true);
  }

  update(force = false): void {
    if (this.cols < 1) return; // geometry not computed yet (hidden container)
    const count = Math.max(0, this.adapter.count());
    const rows = Math.ceil(count / this.cols);
    const totalH = rows > 0 ? rows * this.rowH + (rows - 1) * this.gap : 0;
    this.plane.style.height = `${totalH}px`;

    const planeTop = this.plane.offsetTop;
    const viewTop = this.scroller.scrollTop - planeTop;
    const viewH = this.scroller.clientHeight;
    const rowPitch = this.rowH + this.gap;
    const firstRow = Math.max(0, Math.floor(viewTop / rowPitch) - OVERSCAN_ROWS);
    const lastRow = Math.min(rows - 1, Math.floor((viewTop + viewH) / rowPitch) + OVERSCAN_ROWS);
    const a = firstRow * this.cols;
    const b = Math.min(count - 1, (lastRow + 1) * this.cols - 1);

    if (!force && a === this.lastRange[0] && b === this.lastRange[1]) return;
    this.lastRange = [a, b];

    if (count > 0 && b >= a) {
      this.adapter.need(a, Math.min(count - 1, b + this.cols * OVERSCAN_ROWS));
    }

    // Decide which identity belongs at each visible index.
    const want = new Map<string, { index: number; item: T | undefined }>();
    for (let i = a; i <= b && i < count; i++) {
      const item = this.adapter.get(i);
      let key = item !== undefined ? this.adapter.itemKey(item) : `#i:${i}`;
      if (want.has(key)) key = `#i:${i}`; // defensive: duplicate identities
      want.set(key, { index: i, item });
    }

    // Cells whose identity left the window are recycled; the rest move to
    // their (possibly shifted) index with their DOM untouched.
    for (const [key, m] of this.cells) {
      const target = want.get(key);
      if (!target) {
        this.cells.delete(key);
        this.recycle(m.el);
      } else if (target.index !== m.index) {
        m.index = target.index;
        m.el.dataset.idx = String(target.index);
        this.position(m.el, target.index);
      }
    }

    // Mount whatever identities are new, then (re)render everything in the
    // window — renderers short-circuit when their content key is unchanged.
    for (const [key, target] of want) {
      let m = this.cells.get(key);
      if (!m) {
        m = { el: this.obtain(), index: target.index };
        m.el.dataset.idx = String(target.index);
        if (this.entering) m.el.classList.add('cell-in');
        this.cells.set(key, m);
        this.position(m.el, target.index);
      }
      this.adapter.render(m.el, target.item, target.index);
    }
  }

  private position(el: HTMLElement, i: number): void {
    const row = Math.floor(i / this.cols);
    const col = i % this.cols;
    el.style.width = `${this.cardW}px`;
    el.style.height = `${this.rowH}px`;
    el.style.transform = `translate3d(${col * (this.cardW + this.gap)}px, ${row * (this.rowH + this.gap)}px, 0)`;
  }

  private obtain(): HTMLElement {
    let el = this.pool.pop();
    if (!el) {
      el = document.createElement('article');
      el.className = 'cell';
      el.tabIndex = 0;
    }
    el.hidden = false;
    this.plane.appendChild(el);
    return el;
  }

  private recycle(el: HTMLElement): void {
    this.adapter.release?.(el);
    el.remove();
    // Everything a renderer may have left — content, key, handlers, the
    // hover tooltip — comes off in one tested place (cells.ts): a renderer
    // that decorates a cell cannot be made to remember to undo it, and both
    // faults this has caught came from exactly that.
    scrubCell(el);
    if (this.pool.length < 80) this.pool.push(el);
  }
}
