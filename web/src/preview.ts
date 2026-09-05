/**
 * The moving preview under the pointer.
 *
 * Hovering a film shows what is in it rather than one frame of it, and the
 * cheapest honest way to do that is the scrub sheet the seek bar already
 * uses: ten frames taken across the film, in one image. Stepping through
 * them is a background-position change per frame — no video element, no
 * stream, no decoder, and nothing that could compete with playback for the
 * handful of connections a browser gives an origin.
 *
 * Three things keep it from becoming expensive.
 *
 * The sheet is asked for only after the pointer has **stayed** (`DWELL_MS`).
 * Crossing a grid on the way somewhere is not a request for ten previews,
 * and a sheet that does not exist yet costs the server ten seeks to make.
 *
 * Only **one** is ever fetched at a time, and the current one is what the
 * fetch is for: a pointer that has moved on before the sheet arrives leaves
 * it to be shown by whoever asks next, since it is cached from then on.
 *
 * And it is only for what can have one: a video long enough for the sheet to
 * exist, on a device that has a pointer at all. A phone has no hover, and
 * wiring one to touch would put a preview in the way of the tap.
 */
import { spriteUrl, type Item } from './api';
import { holdsItem } from './cells';
import { SPRITE } from './types.gen';

/** How long the pointer must rest on a tile before the sheet is asked for. */
const DWELL_MS = 350;

/** How long each frame is held. Ten frames at this rate is a five-second
 * loop of the whole film, which is what a preview is for — not motion. */
const FRAME_MS = 500;

/** Sheets already fetched, so a second hover starts moving at once. */
const sheets = new Map<string, HTMLImageElement>();

/**
 * How many are kept. A sheet is a few hundred kilobytes decoded, and a long
 * scroll would otherwise hold every tile the pointer ever crossed.
 */
const SHEET_CAP = 40;

let fetching = false;
let current: Preview | null = null;

/** Whether this device points at things rather than touching them. */
export function canHover(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(hover: hover)').matches;
}

/** Whether there is a sheet to be had for this item. */
export function previewable(item: Item): boolean {
  return item.kind === 'video' && (item.duration ?? 0) >= SPRITE.minDurationMs;
}

class Preview {
  private timer = 0;
  private frame = 0;
  private el: HTMLElement | null = null;
  /** One frame's drawn size, and where the first one starts, in CSS px. */
  private frameW = 0;
  private frameH = 0;
  private offX = 0;
  private offY = 0;

  constructor(
    private readonly cell: HTMLElement,
    private readonly item: Item,
  ) {}

  /** Wait for the pointer to settle, then show whatever we can get. */
  start(): void {
    this.timer = window.setTimeout(() => void this.begin(), DWELL_MS);
  }

  private async begin(): Promise<void> {
    const sheet = sheets.get(this.item.id) ?? (await this.load());
    if (current !== this || !sheet) return;
    // The cell may have been recycled while this was being fetched, and
    // mounting into it then would draw a film over whatever it now holds.
    if (!stillHolds(this.cell, this.item)) return;
    this.show(sheet);
  }

  private async load(): Promise<HTMLImageElement | null> {
    // One at a time, and never for a tile the pointer has already left:
    // making a sheet is ten seeks on the server's disk, which is the disk
    // playback is coming off.
    if (fetching || current !== this) return null;
    fetching = true;
    try {
      const img = new Image();
      img.decoding = 'async';
      const src = spriteUrl(this.item.id, this.item.mtime, true);
      const ok = await new Promise<boolean>((done) => {
        img.onload = () => done(true);
        img.onerror = () => done(false);
        img.src = src;
      });
      if (!ok) return null;
      sheets.set(this.item.id, img);
      // Oldest out first: insertion order is what a Map iterates in.
      for (const id of sheets.keys()) {
        if (sheets.size <= SHEET_CAP) break;
        sheets.delete(id);
      }
      return img;
    } finally {
      fetching = false;
    }
  }

  private show(sheet: HTMLImageElement): void {
    const thumb = this.cell.querySelector<HTMLElement>('.thumb');
    if (!thumb) return;
    // **The frame keeps its own shape.** Sizing the background as a
    // percentage of the tile forces every frame into the tile's proportions,
    // which is invisible on a wide film and grotesque on a clip shot on a
    // phone: a portrait frame was being stretched to a landscape box. The
    // sheet's natural size says what shape the frames really are — it is one
    // image of cols by rows of them — so the frame is fitted inside the tile
    // at that shape and the rest of the tile is left black. Bars, not a
    // distortion, and not a crop either: a preview is for seeing what is in
    // the film.
    const box = thumb.getBoundingClientRect();
    const fw = sheet.naturalWidth / SPRITE.cols;
    const fh = sheet.naturalHeight / SPRITE.rows;
    if (box.width === 0 || box.height === 0 || fw === 0 || fh === 0) return;
    const scale = Math.min(box.width / fw, box.height / fh);
    this.frameW = fw * scale;
    this.frameH = fh * scale;
    this.offX = (box.width - this.frameW) / 2;
    this.offY = (box.height - this.frameH) / 2;

    const el = document.createElement('div');
    el.className = 'thumb-preview';
    el.style.backgroundImage = `url("${sheet.src}")`;
    // In pixels rather than percentages: a percentage is of the box, and the
    // whole point here is that the frame is not the shape of the box.
    el.style.backgroundSize = `${this.frameW * SPRITE.cols}px ${this.frameH * SPRITE.rows}px`;
    // Under the play badge and the corner marks, over the still: the badge
    // is what says the tile can be opened, and it must not disappear at the
    // moment the pointer is on it.
    thumb.insertBefore(el, thumb.querySelector('.badge'));
    this.el = el;
    this.paint();
    this.timer = window.setInterval(() => {
      this.frame = (this.frame + 1) % SPRITE.frames;
      this.paint();
    }, FRAME_MS);
  }

  private paint(): void {
    if (!this.el) return;
    const col = this.frame % SPRITE.cols;
    const row = Math.floor(this.frame / SPRITE.cols);
    // Step by whole frames from where the fitted picture begins.
    const x = this.offX - col * this.frameW;
    const y = this.offY - row * this.frameH;
    this.el.style.backgroundPosition = `${x}px ${y}px`;
    if (this.frame === 0) this.el.classList.add('on'); // once, on the first frame
  }

  /** Put the still back. */
  stop(): void {
    window.clearTimeout(this.timer);
    window.clearInterval(this.timer);
    this.timer = 0;
    this.el?.remove();
    this.el = null;
  }
}

/**
 * Wire a cell to preview this item while the pointer rests on it.
 *
 * Assigned as handler properties, deliberately: the grid pools recycled
 * cells, and its recycle() clears handler properties along with everything
 * else — so a tile that stops being a film cannot carry a film's listener
 * into its next life as an album card, which is exactly what happened when
 * unwiring was left to each renderer to remember.
 */
export function watchPreview(cell: HTMLElement, item: Item): void {
  cell.onpointerenter = (ev): void => {
    // A phone has no hover, and wiring this to touch would put a preview in
    // the way of the tap.
    if (ev.pointerType === 'touch') return;
    previewEnter(cell, item);
  };
  cell.onpointerleave = (): void => previewLeave();
}

/**
 * Whether the cell still holds the item a preview was started for.
 *
 * The cell's key begins with the item's id (see renderItemCell), so this is
 * what a recycled cell fails. It is asked twice — before the sheet is
 * fetched, and again before it is mounted — because the dwell and the fetch
 * are together the better part of a second, and a scroll inside that window
 * would otherwise put a film's frames on top of whatever the tile has since
 * become.
 */
function stillHolds(cell: HTMLElement, item: Item): boolean {
  return holdsItem(cell.dataset.key, item.id);
}

/**
 * Show a preview on this cell while the pointer is inside it.
 *
 * Called on entering rather than read once per cell, because the grid
 * recycles cells: the item a cell holds is not the item it held a moment
 * ago, and a listener that closed over the old one would preview the wrong
 * film.
 */
export function previewEnter(cell: HTMLElement, item: Item): void {
  if (!canHover() || !previewable(item) || !stillHolds(cell, item)) return;
  previewLeave();
  current = new Preview(cell, item);
  current.start();
}

/** Stop whatever is playing, wherever it is. */
export function previewLeave(): void {
  current?.stop();
  current = null;
}
