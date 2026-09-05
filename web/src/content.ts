/**
 * What a restricted face may show.
 *
 * A proxy in front of the server can give one hostname to the music and
 * another to the videos (`X-Media-Content`), and the server answers
 * accordingly whatever the page asks. This is only the page's side of it:
 * which chips are worth offering, and which view to fall back to when a
 * link arrives for one that is not there.
 */

/** The views the page has, as they appear in the URL hash. */
export type ViewMode =
  | 'all'
  | 'video'
  | 'image'
  | 'audio'
  | 'started'
  | 'watched'
  | 'popular'
  | 'albums'
  | 'artists'
  | 'genres'
  | 'audiobooks'
  | 'series';

/**
 * Whether a view is worth offering.
 *
 * With nothing withheld, all of them are. With one class of media, the chip
 * for that class would only repeat "All" — the whole library *is* that class
 * — so it is left out, and what remains is All plus whatever grouped views
 * the class has: albums and artists for music, nothing of the sort for
 * pictures or films. Restricted to two classes the chips come back, because
 * now they choose between something.
 */
export function modeShown(mode: ViewMode, content: string[] | null): boolean {
  if (!content || content.length === 0) return true;
  const has = (c: string): boolean => content.includes(c);
  switch (mode) {
    case 'all':
      return true;
    case 'albums':
    case 'artists':
    case 'genres':
    case 'audiobooks':
      return has('music');
    case 'video':
      return has('videos') && content.length > 1;
    case 'series':
      // Television is video, so this belongs wherever films do — and it
      // says something All does not, so it stays even when films are all
      // there is.
      return has('videos');
    case 'popular':
      // Anything can be played, so this belongs on every face — unlike the
      // two watch views, which list films.
      return true;
    case 'started':
    case 'watched':
      // These list films, so they belong wherever films do — and unlike the
      // Videos chip they say something All does not, so they stay even when
      // films are all there is.
      return has('videos');
    case 'image':
      return has('images') && content.length > 1;
    case 'audio':
      return has('music') && content.length > 1;
  }
}

/**
 * Where a face opens when the URL says nothing.
 *
 * A library that is only music is a library of *performers*: the file
 * listing there is a thousand tracks in whatever order they were written to
 * disk, which is the least useful thing to be shown first. Everywhere else
 * the listing is right — a mixed library has no one shelf to open on, and a
 * face of films has no grouping to open into.
 *
 * Only the default: a link that names a view still opens that view, and the
 * chips are unchanged.
 */
export function defaultMode(content: string[] | null): ViewMode {
  return content?.length === 1 && content[0] === 'music' ? 'artists' : 'all';
}

/** The view to open when the one asked for is not on offer here. */
export function fallbackMode(mode: ViewMode, content: string[] | null): ViewMode {
  return modeShown(mode, content) ? mode : 'all';
}

/** What a view's "queue all" asks the server for: how its tracks are gathered. */
export type QueueSource = 'albums' | 'artists' | 'genres' | 'audiobooks' | 'items';

/**
 * Where "queue all" is offered, and what it queues.
 *
 * The three grouped views of music queue themselves — every release listed,
 * every release of every performer listed, every release in every genre
 * listed. The music chip queues its tracks. The listings that mix kinds
 * queue only on a face that shows nothing but music, where they are track
 * listings too; elsewhere a button over a grid of films that queued the odd
 * track among them would be a puzzle, not a control.
 */
export function queueSource(mode: ViewMode, content: string[] | null): QueueSource | null {
  switch (mode) {
    case 'albums':
    case 'artists':
    case 'genres':
    case 'audiobooks':
      return mode;
    case 'audio':
      return 'items';
    case 'all':
    case 'popular':
      return content?.length === 1 && content[0] === 'music' ? 'items' : null;
    default:
      return null;
  }
}
