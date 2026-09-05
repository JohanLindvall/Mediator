/** Minimal toast notifications. */

export function showToast(message: string, ms = 2400): void {
  const host = document.getElementById('toasts');
  if (!host) return;
  const el = document.createElement('div');
  el.className = 'toast';
  el.textContent = message;
  host.appendChild(el);
  requestAnimationFrame(() => el.classList.add('show'));
  window.setTimeout(() => {
    el.classList.remove('show');
    window.setTimeout(() => el.remove(), 300);
  }, ms);
}
