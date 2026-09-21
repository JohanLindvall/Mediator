/**
 * What to do about the credits.
 *
 * The pure half of skipping a show's opening and closing: whether the
 * position is inside one, and where the next episode should begin when the
 * viewer skipped this one's credits. Where the credits *are* is the
 * server's to say — it finds them from the sound that recurs across a
 * season (library/skipdetect.go) — and the player (video.ts) owns the
 * buttons and the seeks. Everything that can be wrong quietly is here, and
 * tested.
 */
import type { SkipMarks } from './types.gen';

export type Marks = SkipMarks;

/**
 * How much of an intro's tail is not worth a button: offered for its last
 * second, the button would appear and vanish before it could be read, and
 * a skip that lands a second short of where it started is no skip.
 */
export const SKIP_MARGIN_S = 1;

export function hasIntro(m: Marks): boolean {
  return (m.introEnd ?? 0) > (m.introStart ?? 0);
}

export function hasOutro(m: Marks): boolean {
  return (m.outro ?? 0) > 0;
}

export function marksEmpty(m: Marks | null | undefined): boolean {
  return !m || (!hasIntro(m) && !hasOutro(m));
}

export type SkipOffer = 'intro' | 'credits' | null;

/**
 * Which skip the position calls for: the intro while it plays, the credits
 * once they have begun. The credits are measured from the end, since the
 * episodes of a season differ in length by seconds and the credits sit at
 * the end of each; nothing is offered until the length is known.
 */
export function skipOffer(m: Marks | null | undefined, t: number, duration: number): SkipOffer {
  if (!m) return null;
  if (hasIntro(m) && t >= (m.introStart ?? 0) && t < m.introEnd! - SKIP_MARGIN_S) return 'intro';
  if (hasOutro(m) && duration > 0 && t >= duration - m.outro! && t < duration) return 'credits';
  return null;
}

/**
 * Where an episode arrived at by skipping the previous one's credits should
 * begin: past its own intro, which is what skipping the credits of a season
 * means. Only where the start falls inside the intro — an episode already
 * watched past it resumes where it was, and one with a cold open before its
 * intro starts at the cold open and is offered the intro skip when it gets
 * there.
 */
export function startAfterIntro(m: Marks | null | undefined, startAt: number): number {
  if (!m || !hasIntro(m)) return startAt;
  if (startAt >= (m.introStart ?? 0) && startAt < m.introEnd!) return m.introEnd!;
  return startAt;
}
