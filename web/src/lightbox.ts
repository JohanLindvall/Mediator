/**
 * Image lightbox: fit-to-screen viewing with keyboard/swipe navigation
 * between the images of the current query, neighbor preloading and download.
 */
import { streamUrl, type Item } from './api';
import { holdScroll, releaseScroll } from './scrollhold';
import { esc, formatBytes, formatDateFull } from './format';
import { icons } from './icons';
import { shareItem } from './links';
import { findKind, type ItemSource } from './sources';
import { SlideDeck, watchSwipes } from './swipe';
import { holdThumbs, releaseThumbs } from './thumbs';

export function openLightbox(src: ItemSource, index: number): void {
  new Lightbox(src, index);
}

class Lightbox {
  private root: HTMLElement;
  private img: HTMLImageElement;
  private caption: HTMLElement;
  private meta: HTMLElement;
  private dlLink: HTMLAnchorElement;
  private fsBtn: HTMLButtonElement;
  private index: number;
  private closed = false;
  // The layers a drag moves: what is on screen, and what sits on either side
  // of it. Without them a swipe was a blank stage until the next picture had
  // been fetched and decoded.
  private slideCur!: HTMLImageElement;
  /** What is on screen, for the things that ask about it after it is drawn. */
  private shown: Item | null = null;
  private nav = 0; // navigation token to drop stale loads
  /** Drops a step whose search was overtaken by a later one. */
  private stepGen = 0;
  private rotation = 0; // quarter turns, viewing aid only — never persisted

  /**
   * Apply the current rotation. A quarter-turned image swaps its width and
   * height constraints, so the layout box is capped against the stage's
   * opposite axis and the rotated bounding box still fits the screen.
   */
  private applyRotation(): void {
    const odd = this.rotation % 180 !== 0;
    const stage = this.img.parentElement!;
    this.img.style.maxWidth = odd ? `${stage.clientHeight}px` : '100%';
    this.img.style.maxHeight = odd ? `${stage.clientWidth}px` : '100%';
    this.img.style.transform = this.rotation ? `rotate(${this.rotation}deg)` : '';
  }

  private rotate(quarter: 1 | -1): void {
    this.rotation = (this.rotation + quarter * 90 + 360) % 360;
    this.applyRotation();
  }

  private onResize = (): void => this.applyRotation();

  constructor(private src: ItemSource, index: number) {
    holdThumbs(); // the grid is covered: full-size images get the connections
    this.index = index;
    this.root = document.createElement('div');
    this.root.className = 'overlay lightbox';
    this.root.innerHTML = `
      <div class="lb-stage">
        <img alt="" draggable="false">
        <div class="lb-spinner"><div class="spinner"></div></div>
      </div>
      <div class="lb-slides" data-slides aria-hidden="true">
        <img class="lb-slide" data-cur alt="">
        <img class="lb-slide lb-slide-prev" data-prev alt="">
        <img class="lb-slide lb-slide-next" data-next alt="">
      </div>
      <div class="lb-top vo-fade">
        <div class="lb-cap">
          <div class="lb-name"></div>
          <div class="lb-meta"></div>
        </div>
        <button class="icon-btn" data-rccw aria-label="Rotate left (Shift+R)">${icons.rotateCcw}</button>
        <button class="icon-btn" data-rcw aria-label="Rotate right (R)">${icons.rotateCw}</button>
        <button class="icon-btn" data-share aria-label="Copy a link to this picture">${icons.link}</button>
        <a class="icon-btn" data-dl download title="Download">${icons.download}</a>
        <button class="icon-btn" data-fs aria-label="Fullscreen (F)">${icons.maximize}</button>
        <button class="icon-btn" data-close aria-label="Close (Esc)">${icons.close}</button>
      </div>
      <button class="lb-arrow lb-prev icon-btn" aria-label="Previous image">${icons.chevronLeft}</button>
      <button class="lb-arrow lb-next icon-btn" aria-label="Next image">${icons.chevronRight}</button>`;

    this.img = this.root.querySelector('img') as HTMLImageElement;
    this.caption = this.root.querySelector('.lb-name') as HTMLElement;
    this.meta = this.root.querySelector('.lb-meta') as HTMLElement;
    this.dlLink = this.root.querySelector('[data-dl]') as HTMLAnchorElement;
    this.slideCur = this.root.querySelector('[data-cur]') as HTMLImageElement;

    document.getElementById('overlays')!.appendChild(this.root);
    document.body.classList.add('no-scroll', 'viewing');
    holdScroll();

    this.fsBtn = this.root.querySelector('[data-fs]') as HTMLButtonElement;
    this.root.querySelector('[data-close]')!.addEventListener('click', () => this.close());
    this.fsBtn.addEventListener('click', () => this.toggleFullscreen());
    document.addEventListener('fullscreenchange', this.onFsChange);
    this.root.querySelector('[data-share]')!.addEventListener('click', () => {
      if (this.shown) void shareItem(this.shown.id, this.shown.name);
    });
    this.root.querySelector('[data-rcw]')!.addEventListener('click', () => this.rotate(1));
    this.root.querySelector('[data-rccw]')!.addEventListener('click', () => this.rotate(-1));
    window.addEventListener('resize', this.onResize);
    this.root.querySelector('.lb-prev')!.addEventListener('click', () => void this.step(-1));
    this.root.querySelector('.lb-next')!.addEventListener('click', () => void this.step(1));
    this.root.addEventListener('click', (ev) => {
      if (ev.target === this.root || (ev.target as HTMLElement).classList.contains('lb-stage')) this.close();
    });
    this.img.addEventListener('dblclick', () => this.root.classList.toggle('zoomed'));
    document.addEventListener('keydown', this.onKey, true);
    // A zoomed picture is panned by dragging it, so while it is enlarged a
    // drag across the stage moves the picture rather than stepping off it.
    // Sideways moves between pictures and carries them with the finger; a
    // downward flick still dismisses, which watchDrag leaves alone because it
    // only claims the axis it was given.
    // The drag between pictures; the deck is shared with the player.
    new SlideDeck<Item>({
      root: this.root,
      layer: this.root.querySelector('[data-slides]') as HTMLElement,
      prev: this.root.querySelector('[data-prev]') as HTMLImageElement,
      next: this.root.querySelector('[data-next]') as HTMLImageElement,
      axis: 'x',
      ignore: '.zoomed .lb-stage, .lb-top, .lb-arrow',
      extent: () => this.root.clientWidth,
      // Refused while the picture is enlarged, where a sideways drag is
      // panning it; otherwise what is on screen goes into the moving layer.
      capture: () => {
        if (this.root.classList.contains('zoomed')) return false;
        this.slideCur.src = this.img.currentSrc || this.img.src;
        return true;
      },
      neighbour: (dir) => this.findImage(this.index + dir, dir),
      preview: (item) => streamUrl(item.id),
      // Rendered underneath while the layer still covers it, which gives
      // the picture its moment to load — and rendered from the picture that
      // was previewed rather than from a second search, which could land
      // elsewhere if the listing changed while the finger was down.
      arrive: (n) => {
        this.index = n.index;
        this.render(n.item);
      },
      arriveEarly: true,
      fallback: (dir) => void this.step(dir),
      closed: () => this.closed,
    });
    watchSwipes(
      this.root,
      (sw) => {
        // A dismissing flick travels further than a stepping one.
        if (sw.dir === 'down' && Math.abs(sw.dy) >= 80) {
          this.close();
          return true;
        }
        // A flick fast enough to arrive with no movement in between never
        // became a drag; this is the same gesture recognised the other way.
        if (sw.dir === 'left' || sw.dir === 'right') {
          void this.step(sw.dir === 'right' ? -1 : 1);
          return true;
        }
        return false;
      },
      '.zoomed .lb-stage',
    );

    void this.show(this.index);
    requestAnimationFrame(() => this.root.classList.add('open'));
  }

  /**
   * Fill the screen with the picture. The overlay is what goes fullscreen,
   * not the image: its chrome — arrows, caption, rotate — has to come along,
   * and the stage is what centers and scales the picture.
   */
  private toggleFullscreen(): void {
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => {});
    else void this.root.requestFullscreen?.().catch(() => {});
  }

  private onFsChange = (): void => {
    this.fsBtn.innerHTML = document.fullscreenElement ? icons.minimize : icons.maximize;
    // The stage just changed size, and a quarter-turned picture is sized
    // against its axes.
    requestAnimationFrame(() => this.applyRotation());
  };

  private onKey = (ev: KeyboardEvent): void => {
    switch (ev.key) {
      case 'Escape':
        if (document.fullscreenElement) break; // the browser leaves fullscreen first
        this.close();
        break;
      case 'f':
        this.toggleFullscreen();
        break;
      case 'ArrowLeft':
        void this.step(-1);
        break;
      case 'ArrowRight':
      case ' ':
        void this.step(1);
        break;
      case 'r':
        this.rotate(1);
        break;
      case 'R':
        this.rotate(-1);
        break;
      default:
        return;
    }
    ev.preventDefault();
    ev.stopPropagation();
  };

  /** Find the nearest image scanning from `from` in direction `dir`. */
  private findImage(from: number, dir: 1 | -1): Promise<{ item: Item; index: number } | null> {
    return findKind(this.src, from, dir, 'image');
  }

  private async step(dir: 1 | -1): Promise<void> {
    const gen = ++this.stepGen;
    const found = await this.findImage(this.index + dir, dir);
    // Two quick steps used to resolve out of order and the earlier answer
    // overwrote the later one; a step overtaken by another is dropped.
    if (!found || this.closed || gen !== this.stepGen) return;
    this.index = found.index;
    this.render(found.item);
  }

  private async show(index: number): Promise<void> {
    const it = await this.src.item(index);
    if (this.closed) return;
    if (it && it.kind === 'image') {
      this.render(it);
      return;
    }
    const found = (await this.findImage(index + 1, 1)) ?? (await this.findImage(index - 1, -1));
    if (found && !this.closed) {
      this.index = found.index;
      this.render(found.item);
    } else {
      this.close();
    }
  }

  private render(it: Item): void {
    this.shown = it;
    const nav = ++this.nav;
    this.root.classList.add('loading');
    this.root.classList.remove('zoomed');
    // Rotation is a per-picture correction, not a viewing mode.
    this.rotation = 0;
    this.applyRotation();
    const url = streamUrl(it.id);
    this.img.src = url;
    this.img.alt = it.name;
    const done = (): void => {
      if (nav === this.nav) this.root.classList.remove('loading');
    };
    this.img.onload = done;
    this.img.onerror = done;
    this.caption.innerHTML = esc(it.name);
    this.meta.textContent = `${formatDateFull(it.mtime)} · ${formatBytes(it.size)}`;
    this.dlLink.href = streamUrl(it.id, true);
    void this.preload(1);
    void this.preload(-1);
  }

  private async preload(dir: 1 | -1): Promise<void> {
    const found = await this.findImage(this.index + dir, dir);
    if (found && !this.closed) new Image().src = streamUrl(found.item.id);
  }

  private close(): void {
    if (this.closed) return;
    this.closed = true;
    releaseThumbs();
    document.removeEventListener('keydown', this.onKey, true);
    document.removeEventListener('fullscreenchange', this.onFsChange);
    window.removeEventListener('resize', this.onResize);
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => {});
    document.body.classList.remove('no-scroll', 'viewing');
    releaseScroll();
    this.root.classList.remove('open');
    window.setTimeout(() => this.root.remove(), 200);
  }
}
