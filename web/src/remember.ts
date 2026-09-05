/**
 * localStorage, wrapped once.
 *
 * A private window (Safari above all) throws on every write, and some
 * browsers throw even on read when site data is blocked — inside a click
 * handler that is a control that half-works. Every module that remembers
 * something small goes through here, so none of them can forget the
 * try/catch: the memory is a convenience, and losing it must cost nothing
 * but the memory.
 */

export function recall(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function remember(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Nothing to remember it with; whatever was set still holds for now.
  }
}

export function forget(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // Nothing was going to be read back anyway.
  }
}
