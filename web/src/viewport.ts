/**
 * How tall the screen actually is, as a number the overlays can be sized by.
 *
 * `100dvh` is supposed to be this and on iOS it is not quite: a fixed element
 * is laid out against the layout viewport, which extends behind Safari's bars,
 * so an overlay given that height ends up with its last hundred-odd pixels
 * underneath them. That is invisible for a picture — it is letterboxed there
 * anyway — and fatal for the row of controls that sits at the bottom, which
 * is how the fullscreen button came to be off the bottom of the screen.
 *
 * `visualViewport` is the part actually being shown, bars excluded, and it
 * changes as they come and go. Published as custom properties so the sizing
 * still lives in the stylesheet, with sensible fallbacks for anything without
 * the API.
 *
 * Sizing the overlay itself to that height is the obvious move and is wrong:
 * it leaves the strip the bar sits over showing the grid behind it. The
 * backdrop covers everything; only the furniture at the bottom is lifted.
 */

/**
 * Start following the visible viewport. Safe to call more than once.
 *
 * Four numbers come out of it: how big the visible part is, and where inside
 * the layout viewport it begins. A viewer is placed on exactly that box, so
 * nothing of it is off the screen in either direction — the controls were
 * under Safari's bar when only the height was wrong, and the picture ran past
 * the right-hand edge when the width was.
 *
 * Sizing a viewer to less than the layout viewport used to leave the strip
 * around it showing the grid behind; the grid is hidden while a viewer is
 * open instead (`body.viewing`), which is both simpler and complete.
 */
export function trackViewport(): void {
  const vv = window.visualViewport;
  const apply = (): void => {
    const root = document.documentElement;
    const w = Math.round(vv ? vv.width : window.innerWidth);
    const h = Math.round(vv ? vv.height : window.innerHeight);
    if (w <= 0 || h <= 0) return;
    root.style.setProperty('--app-vw', `${w}px`);
    root.style.setProperty('--app-vh', `${h}px`);
    // Where the visible part begins inside the layout viewport. Zero most of
    // the time; not zero while the bars are sliding, or after a pinch.
    root.style.setProperty('--app-left', `${Math.round(vv ? vv.offsetLeft : 0)}px`);
    root.style.setProperty('--app-top', `${Math.round(vv ? vv.offsetTop : 0)}px`);
  };
  apply();
  if (vv) {
    vv.addEventListener('resize', apply);
    // Scrolling the visual viewport is what pinching and the bars sliding
    // away both look like from here.
    vv.addEventListener('scroll', apply);
  }
  window.addEventListener('resize', apply);
  // The measurement is wrong until the rotation has settled, and there is no
  // event for "settled" — this is the shortest wait that is reliably after it.
  window.addEventListener('orientationchange', () => window.setTimeout(apply, 250));
}
