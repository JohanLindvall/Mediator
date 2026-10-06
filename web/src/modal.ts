// SPDX-License-Identifier: MIT

/** Keyboard focus follows the top dialog and returns to its opener. */
const dialogs: HTMLElement[] = [];

/** Let form controls and focused buttons handle their own activation keys. */
export function nativeControlKey(ev: KeyboardEvent): boolean {
  if (!(ev.target instanceof HTMLElement) || ev.key === 'Escape') return false;
  if (ev.target.closest('input, select, textarea, [contenteditable="true"]')) return true;
  return (ev.key === ' ' || ev.key === 'Enter') && !!ev.target.closest('button, a[href]');
}

export function modalFocus(root: HTMLElement, label: string, initial: HTMLElement = root): () => void {
  const before = document.activeElement;
  root.tabIndex = -1;
  if (!root.hasAttribute('role')) root.setAttribute('role', 'dialog');
  root.setAttribute('aria-modal', 'true');
  if (!root.hasAttribute('aria-labelledby')) root.setAttribute('aria-label', label);
  dialogs.push(root);
  initial.focus({ preventScroll: true });
  const onKey = (ev: KeyboardEvent): void => {
    if (ev.key !== 'Tab' || dialogs[dialogs.length - 1] !== root) return;
    const targets = [...root.querySelectorAll<HTMLElement>(
      'button, input, select, textarea, a[href], [tabindex]',
    )].filter((el) => el.tabIndex >= 0 && !el.matches(':disabled') && el.getClientRects().length > 0);
    const index = targets.indexOf(document.activeElement as HTMLElement);
    if (targets.length === 0 || index < 0 || ev.shiftKey && index === 0 || !ev.shiftKey && index === targets.length - 1) {
      ev.preventDefault();
      (targets[ev.shiftKey ? targets.length - 1 : 0] ?? root).focus({ preventScroll: true });
    }
  };
  document.addEventListener('keydown', onKey, true);
  let closed = false;
  return () => {
    if (closed) return;
    closed = true;
    document.removeEventListener('keydown', onKey, true);
    const index = dialogs.lastIndexOf(root);
    const wasTop = index === dialogs.length - 1;
    if (index >= 0) dialogs.splice(index, 1);
    if (wasTop && before instanceof HTMLElement && before.isConnected) before.focus({ preventScroll: true });
  };
}
