/**
 * What each view can be sorted on, and the key it opens on.
 *
 * A file listing sorts by what a file is; a release listing sorts by what the
 * tags say, which is the point of grouping them in the first place. The
 * select is refilled whenever the view changes, and a key that does not
 * survive the change falls back to the one that view opens on rather than
 * being sent to a server that would quietly ignore it.
 *
 * Pure, and tested: the key a view opens on is the first thing a viewer sees
 * of it, and it has been wrong — a performer's releases opened by name for as
 * long as the fallback was simply the first row of the table below.
 */
import type { ViewMode } from './content';

/** The narrowing a view stands in, which changes what it lists. */
export interface Narrowing {
  artist?: string;
  series?: string;
  /** Listing what sounds like one thing: the order is the resemblance. */
  near?: string;
}

export function sortOptions(mode: ViewMode, where: Narrowing = {}): Array<[string, string]> {
  // What sounds like one thing is listed nearest first, and nothing else
  // would be an order worth the name.
  if (where.near && (mode === 'albums' || mode === 'artists')) return [['similarity', 'Similarity']];
  switch (mode) {
    case 'albums':
    case 'audiobooks':
      return [
        ['name', 'Name'],
        ['popular', 'Popular'],
        ['artist', 'Artist'],
        ['year', 'Year'],
        ['genre', 'Genre'],
        ['tracks', 'Tracks'],
        ['duration', 'Length'],
        ['mtime', 'Modified'],
        ['size', 'Size'],
      ];
    case 'artists':
      return [
        ['name', 'Name'],
        ['popular', 'Popular'],
        ['albums', 'Albums'],
        ['tracks', 'Tracks'],
        ['duration', 'Length'],
        ['mtime', 'Modified'],
        ['size', 'Size'],
      ];
    case 'series':
      // Inside a show nothing is sorted by the viewer: a season is watched
      // in order, and that order is the point.
      if (where.series) return [['episode', 'Episode']];
      return [
        ['name', 'Name'],
        ['popular', 'Popular'],
        ['episodes', 'Episodes'],
        ['seasons', 'Seasons'],
        ['duration', 'Length'],
        ['mtime', 'Modified'],
        ['size', 'Size'],
      ];
    case 'genres':
      return [
        ['name', 'Name'],
        ['popular', 'Popular'],
        ['artists', 'Artists'],
        ['albums', 'Albums'],
        ['tracks', 'Tracks'],
        ['duration', 'Length'],
        ['mtime', 'Modified'],
        ['size', 'Size'],
      ];
    case 'video':
      // Two facts only a film has, and two orders rather than one: the
      // biggest picture and the heaviest file are different questions, and a
      // release that answers one rarely answers the other.
      return [
        ['mtime', 'Modified'],
        ['name', 'Name'],
        ['popular', 'Popular'],
        ['size', 'Size'],
        ['added', 'Added'],
        ['duration', 'Length'],
        ['pixels', 'Resolution'],
        ['bitrate', 'Bitrate'],
      ];
    default:
      return [
        ['mtime', 'Modified'],
        ['name', 'Name'],
        ['popular', 'Popular'],
        ['size', 'Size'],
        ['added', 'Added'],
        ['duration', 'Length'],
      ];
  }
}

/**
 * The key a view opens on.
 *
 * The first of its own options, with two exceptions that are one judgement:
 * a view whose subject is an order opens in that order. The popularity
 * listing opens on popularity — one that opened by date would be a joke at the
 * viewer's expense — and a performer's releases open by year, since a
 * discography is read in the order it was made and by name it is one shelf
 * shuffled. A genre's releases are many performers' and stay by name.
 *
 * The direction is the viewer's own and is left alone: newest first under
 * the default, oldest first for a viewer who has turned it round.
 */
export function openingSort(mode: ViewMode, where: Narrowing = {}): string {
  if (mode === 'popular') return 'popular';
  if (mode === 'albums' && where.artist) return 'year';
  return sortOptions(mode, where)[0]![0];
}
