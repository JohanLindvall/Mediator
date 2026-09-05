/**
 * Keep the listing's place while something is open over it.
 *
 * The grid scrolls inside `#scroller`, and every overlay sets `no-scroll` on
 * the body, which turns that element to `overflow: hidden` for as long as it
 * is up. The browser is supposed to give the offset back afterwards, and on
 * a desktop it does — but on iOS the native fullscreen player is a different
 * presentation of the page, and coming out of it WebKit re-lays the document
 * out and brings elements into view of its own accord. The listing was then
 * at the top again, which after a long scroll is the whole way back.
 *
 * So the position is taken when an overlay opens and put back when it
 * closes, and what the browser does in between stops mattering.
 */

/** Where the listing was when the first overlay went up. */
let held: number | null = null;
/** Overlays can sit on top of one another; only the outermost one counts. */
let depth = 0;

function scroller(): HTMLElement | null {
  return document.getElementById('scroller');
}

export function holdScroll(): void {
  depth++;
  if (depth > 1) return;
  held = scroller()?.scrollTop ?? null;
}

export function releaseScroll(): void {
  if (depth === 0) return;
  depth--;
  if (depth > 0) return;
  const el = scroller();
  const top = held;
  held = null;
  if (!el || top === null || top === 0) return;

  el.scrollTop = top;
  // iOS moves the page after the event that closed the viewer, not before
  // it, so one assignment in this frame is not the last word. The re-assert
  // only fires when the listing has been sent back to the very top, which is
  // the failure and not something a viewer can have asked for; anywhere else
  // the reader has scrolled since and is left alone.
  const reassert = (): void => {
    if (el.scrollTop === 0) el.scrollTop = top;
  };
  requestAnimationFrame(reassert);
  window.setTimeout(reassert, 120);
}
