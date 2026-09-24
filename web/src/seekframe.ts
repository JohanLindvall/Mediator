/**
 * The frame under the pointer on the seek bar: where the preview goes, how
 * big it is, and how its frames are fetched without anything waiting on them.
 *
 * The server takes the frame at the exact moment asked for (seekframe.go),
 * and that is not free: measured on a loaded machine, half a second for a
 * 1080p film and three or four for 4K, most of it the decode from the
 * keyframe before the moment. So nothing here waits. The box, its position
 * and the time under it follow the pointer on every move; the picture is the
 * best one to hand — the exact frame when it is cached, else the last one
 * that arrived, dimmed until the exact one does — and at most one frame is
 * ever being fetched. Moving keeps that one: when it lands the latest
 * position is asked for next, so a drag shows pictures at whatever pace the
 * server can make them. Resting is what gives it up: a request for somewhere
 * the pointer has left is dropped once the pointer settles elsewhere, which
 * on the server kills its ffmpeg, and the moment it settled on is asked for
 * at once.
 *
 * DOM-free and without constructor parameter properties, so the tests can
 * load it; the timers and the fetch are handed in.
 */

/** How long the pointer rests before a request for elsewhere is dropped. */
export const SETTLE_MS = 120;

/** How many frames are kept for going back over the same stretch of the bar. */
export const FRAME_CACHE = 32;

/** The preview's size on screen, and the width of the frame to ask for. */
export interface PreviewBox {
  /** The box, in CSS pixels, as it stands on screen (turned, if the film is). */
  boxW: number;
  boxH: number;
  /** The frame's own width in device pixels, before any turn: what to ask for. */
  frameW: number;
}

/**
 * A suitable size for the preview: a fifth of the player's width for a film
 * that is wider than tall, between 160 and 320 CSS pixels — a thumbnail on a
 * television-sized window and still a picture on a phone — and for a picture
 * taller than wide a quarter of its height, between 120 and 240, so a
 * portrait clip is bounded by its height as it is on screen. The frame is
 * asked for at the display's density, capped at two, and rounded up to a
 * multiple of 32 so that nearby sizes share their frames.
 *
 * `aspect` is the picture's width over its height as shown, and `quarterTurns`
 * the turn the player applies: an odd one stands the picture on its side, so
 * the box takes the other shape and the frame is asked for by its height.
 */
export function previewBox(o: {
  playerW: number;
  playerH: number;
  aspect: number;
  quarterTurns: number;
  dpr: number;
}): PreviewBox {
  const aspect = o.aspect > 0 && Number.isFinite(o.aspect) ? o.aspect : 16 / 9;
  const turned = Math.abs(o.quarterTurns) % 2 === 1;
  const shown = turned ? 1 / aspect : aspect;
  let boxW: number;
  let boxH: number;
  if (shown >= 1) {
    boxW = within(o.playerW * 0.2, 160, 320);
    boxH = boxW / shown;
  } else {
    boxH = within(o.playerH * 0.25, 120, 240);
    boxW = boxH * shown;
  }
  boxW = Math.round(boxW);
  boxH = Math.round(boxH);
  const along = turned ? boxH : boxW;
  const dpr = within(o.dpr || 1, 1, 2);
  const frameW = within(Math.ceil((along * dpr) / 32) * 32, 96, 960);
  return { boxW, boxH, frameW };
}

/**
 * Where the box's left edge goes, relative to the bar: centred on the
 * pointer, and held inside the bar's ends so a moment near either end is not
 * shown half off the screen.
 */
export function previewLeft(x: number, boxW: number, barW: number): number {
  if (boxW >= barW) return 0;
  return within(x - boxW / 2, 0, barW - boxW);
}

/** The moment a point on the bar stands for, to the millisecond. */
export function momentAt(x: number, barW: number, durationS: number): number {
  if (barW <= 0 || durationS <= 0) return 0;
  return Math.round(within(x / barW, 0, 1) * durationS * 1000);
}

function within(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v));
}

/** What the frames are fetched with and shown through. */
export interface SeekFramesOpts {
  /** The frame at a moment (ms), or null where there is none to be had. */
  fetch: (ms: number, signal: AbortSignal) => Promise<Blob | null>;
  /**
   * Put a frame up: the frame of moment `ms`, and exact says that is the
   * moment under the pointer as it is handed over. A view that puts it up
   * later — after decoding it, say — asks again with `ms`.
   */
  show: (url: string, exact: boolean, ms: number) => void;
  /** The moment under the pointer has no frame yet: what is up is older. */
  waiting: () => void;
  setTimeout?: (fn: () => void, ms: number) => number;
  clearTimeout?: (id: number) => void;
  makeURL?: (b: Blob) => string;
  dropURL?: (url: string) => void;
}

/**
 * The frames of one film, fetched one at a time for wherever the pointer is.
 * `at` is the pointer moving, `stop` it leaving the bar, `reset` the film
 * changing or the player closing.
 */
export class SeekFrames {
  private readonly opts: SeekFramesOpts;
  private readonly setT: (fn: () => void, ms: number) => number;
  private readonly clearT: (id: number) => void;
  private readonly makeURL: (b: Blob) => string;
  private readonly dropURL: (url: string) => void;
  /** The moment under the pointer, or null while it is off the bar. */
  private wanted: number | null = null;
  /** The one request in flight, if any. */
  private inflight: { ms: number; ctl: AbortController } | null = null;
  /** The moment of the frame that is up, or null. */
  private shown: number | null = null;
  /**
   * The URL of the frame that is up, where it has left the cache: it cannot
   * be released while it is on screen, so it is released when it is replaced.
   */
  private orphan: string | null = null;
  private settle = 0;
  /** Frames already made, least recently used first. */
  private readonly cache = new Map<number, string>();

  constructor(opts: SeekFramesOpts) {
    this.opts = opts;
    this.setT = opts.setTimeout ?? ((fn, ms) => window.setTimeout(fn, ms));
    this.clearT = opts.clearTimeout ?? ((id) => window.clearTimeout(id));
    this.makeURL = opts.makeURL ?? ((b) => URL.createObjectURL(b));
    this.dropURL = opts.dropURL ?? ((u) => URL.revokeObjectURL(u));
  }

  /** The pointer is at a moment: show what is known at once, fetch what is not. */
  at(ms: number): void {
    this.wanted = ms;
    this.clearT(this.settle);
    const hit = this.cache.get(ms);
    if (hit !== undefined) {
      this.touch(ms, hit);
      this.put(ms, hit);
      return;
    }
    this.opts.waiting();
    if (!this.inflight) {
      this.request(ms);
      return;
    }
    if (this.inflight.ms === ms) return;
    // Something else is being made. Moving keeps it — it lands soon and the
    // latest moment is asked for then — and resting gives it up.
    this.settle = this.setT(() => this.settled(), SETTLE_MS);
  }

  /**
   * The pointer left the bar: nothing is wanted, nothing is made for it, and
   * the view takes its picture down, so none is up.
   */
  stop(): void {
    this.wanted = null;
    this.clearT(this.settle);
    this.inflight?.ctl.abort();
    this.inflight = null;
    this.shown = null;
  }

  /** Another film, or none: drop everything, the frames included. */
  reset(): void {
    this.stop();
    for (const url of this.cache.values()) this.dropURL(url);
    this.cache.clear();
    if (this.orphan) this.dropURL(this.orphan);
    this.orphan = null;
    this.shown = null;
  }

  private settled(): void {
    const want = this.wanted;
    if (want === null || this.cache.has(want)) return;
    if (this.inflight && this.inflight.ms !== want) {
      this.inflight.ctl.abort();
      this.inflight = null;
    }
    if (!this.inflight) this.request(want);
  }

  private request(ms: number): void {
    const ctl = new AbortController();
    const mine = { ms, ctl };
    this.inflight = mine;
    this.opts
      .fetch(ms, ctl.signal)
      .then((blob) => {
        if (ctl.signal.aborted) return;
        if (blob) {
          const url = this.makeURL(blob);
          this.remember(ms, url);
          // The exact frame is shown as exact; any other is still the
          // freshest picture there is, unless the exact one is already up.
          if (this.wanted !== null && this.shown !== this.wanted) this.put(ms, url);
        }
      })
      .catch(() => {
        // A dropped request or a failed one: neither is anything to show, and
        // a preview has no business reporting a fault of its own.
      })
      .finally(() => {
        if (this.inflight !== mine) return;
        this.inflight = null;
        const want = this.wanted;
        if (want !== null && want !== ms && !this.cache.has(want)) this.request(want);
      });
  }

  private put(ms: number, url: string): void {
    this.shown = ms;
    this.opts.show(url, ms === this.wanted, ms);
    if (this.orphan && this.orphan !== url) {
      this.dropURL(this.orphan);
      this.orphan = null;
    }
  }

  private remember(ms: number, url: string): void {
    const old = this.cache.get(ms);
    if (old !== undefined && old !== url) this.dropURL(old);
    this.cache.delete(ms);
    this.cache.set(ms, url);
    while (this.cache.size > FRAME_CACHE) {
      const [first, firstURL] = this.cache.entries().next().value as [number, string];
      this.cache.delete(first);
      // The picture that is up keeps its URL until it is replaced.
      if (first === this.shown) this.orphan = firstURL;
      else this.dropURL(firstURL);
    }
  }

  private touch(ms: number, url: string): void {
    this.cache.delete(ms);
    this.cache.set(ms, url);
  }
}
