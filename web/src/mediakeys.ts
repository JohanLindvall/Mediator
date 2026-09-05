/**
 * The keyboard's media keys, and the desktop's own transport controls.
 *
 * Both the bottom bar and the video player want them, and only one set of
 * handlers exists per page — so they are claimed rather than registered. The
 * player takes them while it is open and hands them back on the way out,
 * which leaves the bar in charge again with whatever it was holding.
 */

export interface MediaKeys {
  play(): void;
  pause(): void;
  /** Stop is treated as a pause that keeps its place, so it can resume. */
  stop?(): void;
  previous?(): void;
  next?(): void;
}

/** Innermost last: the top of the stack is whoever is in front. */
const claims: MediaKeys[] = [];

function set(name: MediaSessionAction, fn: (() => void) | undefined): void {
  try {
    navigator.mediaSession.setActionHandler(name, fn ?? null);
  } catch {
    // An action this browser does not know is one it will never send.
  }
}

function apply(): void {
  if (!('mediaSession' in navigator)) return;
  const top = claims[claims.length - 1];
  // Cleared rather than left behind when nobody claims one: a key that still
  // reached the bottom bar from over an open film would be answered by
  // something the viewer cannot see.
  set('play', top && (() => top.play()));
  set('pause', top && (() => top.pause()));
  set('stop', top && (() => (top.stop ?? top.pause)()));
  set('previoustrack', top?.previous && (() => top.previous?.()));
  set('nexttrack', top?.next && (() => top.next?.()));
}

/** Take the keys. The returned function gives them back. */
export function claimMediaKeys(keys: MediaKeys): () => void {
  claims.push(keys);
  apply();
  return () => {
    const i = claims.lastIndexOf(keys);
    if (i >= 0) claims.splice(i, 1);
    apply();
  };
}

/**
 * Say which way round it is. The desktop's controls go on offering whatever
 * they last showed otherwise — a pause button for something that has
 * stopped, which then sends a pause that changes nothing.
 */
export function setPlaybackState(playing: boolean): void {
  if (!('mediaSession' in navigator)) return;
  navigator.mediaSession.playbackState = playing ? 'playing' : 'paused';
}
