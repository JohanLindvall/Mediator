/**
 * Which soundtrack a viewer wants, remembered.
 *
 * Two things are kept, both in this browser. The choice made for one film,
 * so that coming back to it starts where it left off in every sense; and the
 * language of the last choice made anywhere, which is the "smart" part —
 * pick Swedish once and the next release carrying Swedish starts in it,
 * without a preferences page to visit or a setting to find.
 *
 * Neither is worth a round trip or a row in the database: getting it wrong
 * costs one click, and a viewer on another device has their own ideas about
 * what language they want anyway.
 */

import type { Track } from './playback';
// Imported with its extension so the test runner's module loader can follow
// it: this module is one of the node-tested ones, and node resolves exactly
// what is written.
import { recall, remember } from './remember.ts';

const FILE_KEY = 'media.audioTrack:';
const LANG_KEY = 'media.audioLang';

export function rememberedTrack(id: string): number | undefined {
  const v = recall(FILE_KEY + id);
  return v === null ? undefined : Number(v);
}

export function preferredLang(): string {
  return recall(LANG_KEY) ?? '';
}

/** Record a choice: this film's, and the language for every other film. */
export function rememberTrack(id: string, track: number, tracks: readonly Track[]): void {
  remember(FILE_KEY + id, String(track));
  const lang = tracks.find((t) => t.index === track)?.lang;
  if (lang) remember(LANG_KEY, lang.toLowerCase());
}
