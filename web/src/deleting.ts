/**
 * What deleting a thing from the disk is, as the page asks for it and says
 * it: which request a card makes, what the offer is called, and how the
 * confirmation and its outcome are worded. The deciding of what goes is the
 * server's (library/delete.go); this is only the asking and the telling, and
 * it is pure so the tests can load it.
 */

import { formatBytes } from './format.ts';
import type { DeletePlanResponse, DeleteRequest, DeleteResult } from './types.gen';

/** Anything a card can show, as far as deleting it is concerned. */
interface Card {
  id?: string;
  kind?: string;
  source?: string;
  tracks?: number;
  seasons?: unknown[];
  season?: number;
  name?: string;
  albums?: number;
  artists?: number;
}

/**
 * The deletion a card stands for: a file by its id, a release by its id, a
 * show by its name, and a season by its show's name and its number — or
 * nothing, for a performer or a genre, which are ways of looking at releases
 * and not things on the disk.
 */
export function deleteRequestFor(card: Card, show = ''): DeleteRequest | null {
  if (typeof card.season === 'number' && !Array.isArray(card.seasons)) {
    return show ? { kind: 'season', id: show, season: card.season } : null;
  }
  if (Array.isArray(card.seasons) && card.name) return { kind: 'series', id: card.name };
  if (typeof card.source === 'string' && typeof card.tracks === 'number' && card.id) {
    return { kind: 'album', id: card.id };
  }
  if (typeof card.kind === 'string' && card.id && card.albums === undefined && card.artists === undefined) {
    return { kind: 'item', id: card.id };
  }
  return null;
}

/** What the offer is called on a menu. */
export function deleteLabel(req: DeleteRequest): string {
  switch (req.kind) {
    case 'album':
      return 'Delete release…';
    case 'series':
      return 'Delete show…';
    case 'season':
      return 'Delete season…';
    default:
      return 'Delete file…';
  }
}

function counted(n: number, noun: string): string {
  return `${n.toLocaleString()} ${noun}${n === 1 ? '' : 's'}`;
}

/**
 * The size of a deletion, as the confirmation says it: how many files and
 * folders, and how much of the disk.
 */
export function deleteSummary(p: Pick<DeletePlanResponse, 'files' | 'folders' | 'bytes'>): string {
  const parts = [counted(p.files, 'file')];
  if (p.folders > 0) parts.push(counted(p.folders, 'folder'));
  return `${parts.join(' and ')} · ${formatBytes(p.bytes)}`;
}

/** What a deletion did, for the toast after it. */
export function deleteDoneText(r: DeleteResult): string {
  const gone = r.files + r.folders;
  if (gone === 0 && (r.kept?.length ?? 0) > 0) return 'Nothing was deleted: it changed since it was shown';
  const what = [r.files > 0 ? counted(r.files, 'file') : '', r.folders > 0 ? counted(r.folders, 'folder') : '']
    .filter(Boolean)
    .join(' and ');
  const kept = r.kept?.length ? ` · ${counted(r.kept.length, 'thing')} left in place` : '';
  return `Deleted ${what || 'nothing'} (${formatBytes(r.bytes)})${kept}`;
}
