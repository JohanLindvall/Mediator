/**
 * Shortlinks: handing somebody an address for one place in the library.
 *
 * The view has always been in the URL fragment, so everything here was
 * already addressable — but a fragment naming a performer, a genre and a
 * search is a paragraph, and a paragraph is not something anybody pastes
 * into a message. The server keeps the fragment under a short name and this
 * is the half that asks it to.
 *
 * What is handed over depends on what the device can do, in the order that
 * costs the viewer least: the system's own share sheet where there is one
 * (which is what the button means on a phone), the clipboard where there is
 * not, and — where neither is allowed, which is any page served over plain
 * http to something that is not localhost — the address itself, on screen,
 * to be copied by hand. A button that silently does nothing would be worse
 * than an ugly one.
 */

import { createLink } from './api';
import { showToast } from './toast';

/**
 * Mint a link to `target` (a URL fragment) and give it to the viewer.
 * `label` is what the share sheet calls it, where there is one.
 */
export async function shareTarget(target: string, label: string): Promise<void> {
  let url: string;
  try {
    const res = await createLink(target);
    // Made absolute against the page's own address, which is the only one
    // that knows which face of the library this is — a link built on the
    // music face has to stay on it.
    url = new URL(res.path, location.href).href;
  } catch {
    showToast('Could not make a link');
    return;
  }
  if (await viaShareSheet(url, label)) return;
  if (await viaClipboard(url)) {
    showToast('Link copied');
    return;
  }
  // Nothing could take it, so show it. 8 seconds rather than the usual two:
  // this one has to be read and typed, not merely noticed.
  showToast(url, 8000);
}

async function viaShareSheet(url: string, label: string): Promise<boolean> {
  if (!navigator.share) return false;
  try {
    await navigator.share({ title: label, url });
    return true;
  } catch (err) {
    // Dismissing the sheet is an answer — "no, not now" — and quietly
    // copying instead would put something on the clipboard nobody asked
    // for. Anything else means the sheet was never shown.
    return (err as DOMException)?.name === 'AbortError';
  }
}

async function viaClipboard(url: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(url);
    return true;
  } catch {
    return legacyCopy(url);
  }
}

/**
 * The pre-clipboard-API copy, kept because the modern one needs a secure
 * context: a library opened at its address on the local network over plain
 * http has none, and that is where this app is most often opened.
 */
function legacyCopy(url: string): boolean {
  const el = document.createElement('textarea');
  el.value = url;
  el.setAttribute('readonly', '');
  el.style.position = 'fixed';
  el.style.opacity = '0';
  document.body.appendChild(el);
  try {
    el.select();
    return document.execCommand('copy');
  } catch {
    return false;
  } finally {
    el.remove();
  }
}

/**
 * The view the page is currently showing, as parameters.
 *
 * Read out of the address bar rather than passed in, because that is where
 * the view already lives — the app has written its state there all along, so
 * there is nothing to plumb through four overlays to find out what is behind
 * them.
 *
 * Anything naming a single item is dropped first. A page opened *from* a
 * shortlink still has that link's item in its address until the next thing
 * the viewer does, and building on it would produce an address naming two
 * items, of which only the first is ever read.
 */
function viewParams(): URLSearchParams {
  const p = new URLSearchParams(location.hash.replace(/^#/, ''));
  p.delete('i');
  p.delete('al');
  return p;
}

/** A link to what is on screen: a performer, a genre, a programme, a search. */
export function shareView(label: string): Promise<void> {
  // Somewhere is always better than nowhere: with nothing narrowed the
  // address is empty, and the link should still open the library.
  return shareTarget(viewParams().toString() || 'm=all', label);
}

/** A link to one film, photograph or track, on top of the view behind it. */
export function shareItem(id: string, label: string): Promise<void> {
  const p = viewParams();
  p.set('i', id);
  return shareTarget(p.toString(), label);
}

/** A link to one release. */
export function shareAlbum(albumId: string, label: string): Promise<void> {
  const p = viewParams();
  p.set('al', albumId);
  return shareTarget(p.toString(), label);
}
