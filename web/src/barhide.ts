/**
 * When the top bar may leave the screen, and when it must come back.
 *
 * On a phone the bar is tall — a mixed library wears eleven chips — and the
 * whole of it is idle while the viewer is travelling down the grid. So it
 * slides away once a downward gesture has committed, and returns on the
 * first upward one: everything is still where it was, one flick away, which
 * is what separates this from hiding controls behind an edge.
 *
 * This module is only the decision; main.ts wires it to the scroller and a
 * class. Pure on purpose — the thresholds below are the behaviour, and a
 * wrong one is a bar that flickers on every touch, so they are pinned by
 * tests rather than by hope.
 */

export interface BarScroll {
  /** Where the last event left the scroller. */
  y: number;
  /** Downward travel accumulated since the last upward move. */
  down: number;
  hidden: boolean;
}

export const barShown: BarScroll = { y: 0, down: 0, hidden: false };

/**
 * How far a downward scroll has to travel before it means "browsing, not
 * adjusting" — a thumb resting on a screen drifts a few pixels.
 */
const HIDE_AFTER = 48;

/** An upward move smaller than this is drift, not a request for the bar. */
const SHOW_AFTER = 8;

/** Near the top the bar is simply there; nothing at 0 can be scrolled up. */
const AT_TOP = 8;

/**
 * A move larger than a finger makes in one frame is a programmatic jump —
 * an overlay putting the scroll position back on close — and says nothing
 * about what the viewer wants next.
 */
const JUMP = 400;

/** Fold one scroll position into the state. */
export function barStep(s: BarScroll, y: number): BarScroll {
  const dy = y - s.y;
  // The top outranks the jump guard: switching views resets the scroll in
  // one assignment, and a hidden bar at the top would be stuck — there is
  // no further up to scroll to reveal it.
  if (y <= AT_TOP) return { y, down: 0, hidden: false };
  if (Math.abs(dy) > JUMP) return { y, down: 0, hidden: s.hidden };
  if (dy > 0) {
    const down = s.down + dy;
    return { y, down, hidden: s.hidden || down >= HIDE_AFTER };
  }
  if (dy < -SHOW_AFTER) return { y, down: 0, hidden: false };
  return { y, down: s.down, hidden: s.hidden };
}
