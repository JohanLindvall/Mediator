/**
 * What a grid cell's key says, and how to read it back.
 *
 * The two halves are here together because they have to agree and they are
 * used in different files: `main.ts` writes the key when it renders a tile,
 * and `preview.ts` reads it to ask whether the cell it is about to draw into
 * still holds the item it was started for. The grid recycles cells, so that
 * question is a real one — and the answer depends on the key beginning with
 * the item's id, which is exactly the sort of fact that goes wrong quietly
 * when it is spelled out in two places.
 */
import type { Item } from './types.gen';

/**
 * The key an item's cell is drawn under. A cell whose key is unchanged is
 * left alone, so everything the tile *shows* has to be in here.
 *
 * mtime and size are what make a file that changed on disk — the normal case
 * for a download that finished after the grid first saw it — re-render
 * fully: the thumbnail that failed against the incomplete file is retried,
 * and the displayed size is refreshed.
 *
 * For music the tags are part of what the tile says and they arrive later
 * than the file does, enrichment reading them in the background. Without
 * them here a tile drawn before its tags were read keeps showing the file
 * name under a title the library has since learnt.
 *
 * The codecs and the picture's size arrive the same way and for the same
 * reason, and they are what the tile says on hover — so they are here too. A
 * tile drawn before the file was read would otherwise keep an empty tooltip
 * for as long as it stayed on screen.
 */
export function itemCellKey(item: Item): string {
  const tags =
    item.kind === 'audio'
      ? `:${item.title ?? ''}:${item.artist ?? ''}:${item.year ?? ''}:${item.genre ?? ''}`
      : '';
  const shape = `:${item.vcodec ?? ''}:${item.acodec ?? ''}:${item.width ?? ''}:${item.height ?? ''}`;
  return `${item.id}:${item.mtime}:${item.size}:${item.plays ?? 0}${tags}${shape}`;
}

/**
 * Whether a cell drawn under this key is still showing this item.
 *
 * Asked by the hover preview, which takes the better part of a second to
 * appear — the dwell, and then the fetch — by which time a scroll may have
 * handed the cell to something else entirely. Drawing into it then puts a
 * film's frames over whatever the tile has become, which is how hovering an
 * album came to play a film across the sleeve.
 */
export function holdsItem(key: string | undefined, id: string): boolean {
  return key !== undefined && key.startsWith(`${id}:`);
}

/**
 * The surface a renderer is allowed to decorate, and `scrubCell` is the
 * undoing of all of it — in the one place cells are reused, because a
 * renderer that sets something cannot be made to remember to clear it.
 *
 * Both faults this has caught were exactly that shape. The preview handler
 * that survived recycling left film cells playing over the album cards they
 * had become; the hover tooltip that survived it showed one performer's file
 * over another performer's release, the title being an attribute and so
 * outliving the innerHTML wipe precisely as the handler properties do.
 *
 * Typed as its own small surface rather than HTMLElement so the clearing is
 * testable where the DOM is not: the test hands in a stub and asserts what a
 * clean cell no longer carries.
 */
export interface CellSurface {
  innerHTML: string;
  dataset: { key?: string };
  classList: { remove(name: string): void };
  onpointerenter: unknown;
  onpointerleave: unknown;
  removeAttribute(name: string): void;
}

export function scrubCell(el: CellSurface): void {
  el.innerHTML = '';
  el.classList.remove('cell-in');
  delete el.dataset.key;
  // Handler properties survive both the pool and the innerHTML wipe.
  el.onpointerenter = null;
  el.onpointerleave = null;
  // So does the tooltip: an attribute, not content.
  el.removeAttribute('title');
}
