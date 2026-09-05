/**
 * Swipe and drag recognition shared by the overlays, so a gesture means the
 * same thing whichever viewer is on screen.
 *
 * Two shapes, because the viewers need two. `watchSwipes` reports a flick
 * once it is over — enough for "go to the next one". `watchDrag` reports the
 * whole gesture as it happens, which is what lets a viewer move the picture
 * with the finger and show where it is going before letting go of it.
 */

/** Below this the finger was tapping, not swiping. */
export const MIN_PX = 60;
/** A swipe must be this much longer along its axis than across it. */
export const RATIO = 1.5;
/** Movement before a drag commits to an axis and starts following. */
const SLOP_PX = 10;

export type SwipeDir = 'left' | 'right' | 'up' | 'down';

export interface Swipe {
  dir: SwipeDir;
  /** Travel, signed, in CSS pixels — the viewer's own thresholds may differ. */
  dx: number;
  dy: number;
}

/** Whether a touch begins inside furniture that owns its own gestures. */
function owned(target: EventTarget | null, ignore?: string): boolean {
  return !!(ignore && (target as HTMLElement | null)?.closest?.(ignore));
}

/** Classify a finished gesture, or null if it was not one. Pure, and pinned by swipe.test.ts. */
export function classify(dx: number, dy: number): Swipe | null {
  if (Math.abs(dx) > MIN_PX && Math.abs(dx) > Math.abs(dy) * RATIO) {
    return { dir: dx > 0 ? 'right' : 'left', dx, dy };
  }
  if (Math.abs(dy) > MIN_PX && Math.abs(dy) > Math.abs(dx) * RATIO) {
    return { dir: dy > 0 ? 'down' : 'up', dx, dy };
  }
  return null;
}

/**
 * Report swipes on `el`.
 *
 * `ignore` is a selector for the viewer's own interactive furniture: a finger
 * dragging the seek bar is scrubbing, not swiping, and the gesture has to be
 * disowned where it starts — by the time it ends it looks like any other
 * travel.
 *
 * The handler returns whether it acted, and that is what suppresses the tap
 * the browser would otherwise synthesise from the gesture — which in these
 * viewers lands on a video that toggles playback, or on a backdrop that
 * closes the overlay.
 */
export function watchSwipes(
  el: HTMLElement,
  handle: (s: Swipe) => boolean,
  ignore?: string,
): void {
  let x = 0;
  let y = 0;
  let live = false;

  el.addEventListener(
    'touchstart',
    (ev) => {
      const t = ev.touches[0];
      live = !!t && ev.touches.length === 1 && !owned(ev.target, ignore);
      if (t) {
        x = t.clientX;
        y = t.clientY;
      }
    },
    { passive: true },
  );

  // Deliberately not passive: preventDefault on a recognised swipe is the
  // only way to cancel the click that follows it.
  el.addEventListener('touchend', (ev) => {
    const wasLive = live;
    live = false;
    if (!wasLive) return;
    // A drag on the same element has already dealt with this gesture and
    // said so by cancelling the default. Acting on it again would step
    // twice for one movement of the finger.
    if (ev.defaultPrevented) return;
    const t = ev.changedTouches[0];
    if (!t) return;
    const s = classify(t.clientX - x, t.clientY - y);
    if (s && handle(s)) ev.preventDefault();
  });
}

export interface DragOpts {
  /** The axis the viewer moves along; travel across it is not a drag. */
  axis: 'x' | 'y';
  /** Selector for furniture whose touches belong to it, not to the drag. */
  ignore?: string;
  /**
   * A drag is starting. Return false to refuse it — there may be nothing on
   * either side to move to, and a picture that follows the finger and then
   * springs back says there is.
   */
  begin(): boolean;
  /** Live travel along the axis, in pixels, signed. */
  move(offset: number): void;
  /**
   * Released. `dir` is -1 towards what precedes, +1 towards what follows, and
   * 0 when the gesture did not carry far enough to be either.
   */
  end(dir: -1 | 0 | 1, offset: number): void;
}
/**
 * Follow a drag along one axis, reporting it as it happens.
 *
 * The gesture is claimed only once it has travelled far enough to show which
 * way it is going, so a tap still reaches what is underneath and a drag
 * across the axis is left to whatever else wants it. From the moment it is
 * claimed the browser is told to keep out of it, because a viewer moving with
 * the finger and a page scrolling with the same finger cannot both be right.
 */
export function watchDrag(el: HTMLElement, o: DragOpts): void {
  let startX = 0;
  let startY = 0;
  let active = false; // a touch that might become a drag
  let claimed = false; // ... and now has
  let offset = 0;

  const reset = (): void => {
    active = false;
    claimed = false;
    offset = 0;
  };

  el.addEventListener(
    'touchstart',
    (ev) => {
      const t = ev.touches[0];
      if (!t || ev.touches.length !== 1 || owned(ev.target, o.ignore)) {
        reset();
        return;
      }
      active = true;
      claimed = false;
      startX = t.clientX;
      startY = t.clientY;
      offset = 0;
    },
    { passive: true },
  );

  el.addEventListener(
    'touchmove',
    (ev) => {
      if (!active) return;
      const t = ev.touches[0];
      if (!t) return;
      const dx = t.clientX - startX;
      const dy = t.clientY - startY;
      const along = o.axis === 'x' ? dx : dy;
      const across = o.axis === 'x' ? dy : dx;
      if (!claimed) {
        if (Math.abs(along) < SLOP_PX && Math.abs(across) < SLOP_PX) return;
        // The first decisive movement settles it: along the axis this is
        // ours, across it this was never going to be.
        if (Math.abs(along) <= Math.abs(across) || !o.begin()) {
          active = false;
          return;
        }
        claimed = true;
      }
      ev.preventDefault();
      offset = along;
      o.move(offset);
    },
    { passive: false },
  );

  const finish = (ev: TouchEvent): void => {
    if (!claimed) {
      reset();
      return;
    }
    // Past the flick threshold it goes; short of it, it springs back.
    const dir = Math.abs(offset) > MIN_PX ? ((offset < 0 ? 1 : -1) as -1 | 1) : 0;
    o.end(dir, offset);
    // The gesture moved the viewer, so the tap the browser would synthesise
    // from it must not also toggle playback.
    ev.preventDefault();
    reset();
  };
  el.addEventListener('touchend', finish);
  el.addEventListener('touchcancel', (ev) => {
    if (claimed) o.end(0, offset);
    reset();
    void ev;
  });
}

/** What a viewer hands the deck about the things on either side of the one on screen. */
export interface Neighbour<T> {
  item: T;
  index: number;
}

export interface DeckOpts<T> {
  /** The overlay, which wears `sliding` while a drag is under way. */
  root: HTMLElement;
  /** The moving layer, and the two slides that carry what lies on either side. */
  layer: HTMLElement;
  prev: HTMLImageElement;
  next: HTMLImageElement;
  axis: 'x' | 'y';
  /** Furniture whose touches belong to it, not to the drag (see watchDrag). */
  ignore?: string;
  /** How far the layer travels to leave the stage: the stage's size along the axis. */
  extent: () => number;
  /**
   * Freeze what is on screen into the current slide, or refuse the drag —
   * a picture enlarged for panning, a player with nothing to step through.
   */
  capture: () => boolean;
  /** Find what lies one step that way, or null when nothing does. */
  neighbour: (dir: -1 | 1) => Promise<Neighbour<T> | null>;
  /** The preview a neighbour's slide shows. */
  preview: (item: T) => string;
  /**
   * The neighbour the drag committed to has arrived. `early` viewers render
   * it under the layer while the layer still covers it, which gives a
   * picture its 220 ms to load; the others wait for the layer to finish
   * its travel, since a frame of the file being left is what the drag was
   * meant to move away from.
   */
  arrive: (n: Neighbour<T>) => void;
  arriveEarly?: boolean;
  /** After the layer is down: open what arrived, where a viewer has more to do. */
  open?: (n: Neighbour<T>) => void;
  /**
   * The gesture asked to move but the search for what is on that side had
   * not answered yet — a fast drag over a listing that needed fetching. Go
   * the ordinary way rather than swallowing it.
   */
  fallback: (dir: -1 | 1) => void;
  closed: () => boolean;
}

/** How long the layer takes to run the rest of the way on release. */
const SLIDE_MS = 220;

/**
 * A drag that carries the picture with the finger and shows what is coming.
 *
 * The player moves up and down between films, the picture viewer sideways
 * between pictures, and both used to spell out the same deck line for line
 * — the neighbours resolved while the finger is still down so the release
 * is instant, the damped travel where nothing lies that way, the layer run
 * the rest of the way before the file changes, the release taken to the
 * neighbour that was previewed rather than searched for again. One deck,
 * parametrised by what differs.
 */
export class SlideDeck<T> {
  private readonly o: DeckOpts<T>;
  private dragging = false;
  private before: Neighbour<T> | null = null;
  private after: Neighbour<T> | null = null;

  // Not a parameter property: node's type stripping refuses those, and this
  // module is loaded by the tests.
  constructor(o: DeckOpts<T>) {
    this.o = o;
    watchDrag(o.root, {
      axis: o.axis,
      ignore: o.ignore,
      begin: () => this.begin(),
      move: (offset) => this.move(offset),
      end: (dir) => this.end(dir),
    });
  }

  private place(px: number): void {
    this.o.layer.style.transform = this.o.axis === 'x' ? `translateX(${px}px)` : `translateY(${px}px)`;
  }

  /** A drag is starting: freeze what is on screen and find what is on either side. */
  private begin(): boolean {
    if (this.dragging || !this.o.capture()) return false;
    this.before = null;
    this.after = null;
    this.o.prev.removeAttribute('src');
    this.o.next.removeAttribute('src');
    // Both searches may be walking pages; whichever answers is used, and one
    // that answers after the viewer has closed is dropped.
    void this.o.neighbour(-1).then((n) => {
      if (this.o.closed() || !n) return;
      this.before = n;
      this.o.prev.src = this.o.preview(n.item);
    });
    void this.o.neighbour(1).then((n) => {
      if (this.o.closed() || !n) return;
      this.after = n;
      this.o.next.src = this.o.preview(n.item);
    });
    this.dragging = true;
    this.o.root.classList.add('sliding');
    this.o.layer.style.transition = 'none';
    this.place(0);
    return true;
  }

  /** Follow the finger; damped where nothing lies that way, so the end of the run can be felt. */
  private move(offset: number): void {
    if (!this.dragging) return;
    const wanted = offset < 0 ? this.after : this.before;
    this.place(wanted ? offset : offset * 0.25);
  }

  /** Released: run the layer the rest of the way, then change the file. */
  private end(dir: -1 | 0 | 1): void {
    if (!this.dragging) return;
    const wanted = dir === 1 ? this.after : dir === -1 ? this.before : null;
    this.o.layer.style.transition = `transform ${SLIDE_MS}ms ease-out`;
    if (!wanted) {
      this.place(0);
      window.setTimeout(() => this.finish(), SLIDE_MS);
      if (dir !== 0) this.o.fallback(dir);
      return;
    }
    const extent = this.o.extent();
    this.place(dir === 1 ? -extent : extent);
    if (this.o.arriveEarly) this.o.arrive(wanted);
    window.setTimeout(() => {
      if (!this.o.arriveEarly) this.o.arrive(wanted);
      this.finish();
      this.o.open?.(wanted);
    }, SLIDE_MS);
  }

  private finish(): void {
    this.dragging = false;
    this.o.root.classList.remove('sliding');
    this.o.layer.style.transition = 'none';
    this.place(0);
  }
}
