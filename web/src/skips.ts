/**
 * Where the credits are, and what to do about them.
 *
 * The pure half of skipping a show's opening and closing: which marks apply
 * to an episode, whether the position is inside one, and where the next
 * episode should begin when the viewer skipped this one's credits. The
 * player (video.ts) owns the buttons and the seeks; everything that can be
 * wrong quietly is here, and tested.
 */
import type { SkipMarks, SkipResponse } from './types.gen';

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

/**
 * The marks that apply to an episode: its own, else its season's, else the
 * show's. The narrowest that says anything wins, so one odd episode can be
 * marked on its own under a season that is marked for all of them.
 */
export function effectiveMarks(r: SkipResponse | null | undefined): Marks | null {
  if (!r) return null;
  for (const m of [r.episode, r.season, r.series]) {
    if (m && !marksEmpty(m)) return m;
  }
  return null;
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

/**
 * Seconds out of a clock the way a person writes one: "1:32", "0:07",
 * "1:02:03", or a bare number of seconds. NaN for anything else, which the
 * form refuses rather than saving a guess.
 */
export function parseClock(text: string): number {
  const s = text.trim();
  if (s === '') return 0;
  if (!/^\d+(:\d{1,2}){0,2}(\.\d+)?$/.test(s)) return NaN;
  const parts = s.split(':').map(Number);
  let secs = 0;
  for (const p of parts) secs = secs * 60 + p;
  return secs;
}
