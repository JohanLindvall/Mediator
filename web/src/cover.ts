/**
 * Whether two tracks share a sleeve — which is to say, a release.
 *
 * The bar's cover is an <img>, and an <img> keeps its picture until the next
 * one has loaded, so at a change of release the old sleeve stood under the
 * new title for as long as the thumbnail took. Within one release the
 * picture coming is the one already up, and hiding it would be a blink at
 * every boundary; so the bar asks this before deciding.
 *
 * One release is one directory — that is what a release is here — or one
 * album tag under one performer: the disc folders of a split release agree
 * on the tag and disagree on the directory. Two releases that merely share
 * a title are told apart by their performers; a compilation, whose tracks
 * name different performers, is caught by its directory.
 */
import type { Item } from './api';

type Track = Pick<Item, 'path' | 'album' | 'artist'>;

/** The directory part of a display path, whichever way its slashes lean. */
function dirOf(path: string): string {
  const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
  return i < 0 ? '' : path.slice(0, i);
}

export function sameRelease(a: Track, b: Track): boolean {
  if (dirOf(a.path) === dirOf(b.path)) return true;
  if (!a.album || a.album !== b.album) return false;
  return !a.artist || !b.artist || a.artist === b.artist;
}
