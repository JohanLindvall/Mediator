/**
 * Deleting from the disk, asked and confirmed.
 *
 * The server works out what would go and the owner is shown exactly that —
 * every folder that goes whole, every file that goes on its own, how many and
 * how much, and anything not asked for that shares a container with what was
 * — before anything is removed. Confirming sends back the plan's token, and
 * the server removes that and nothing else.
 *
 * The dialog takes the keyboard while it is up, so the player or the grid
 * behind it does not act on a key meant for it — Escape closing the player
 * rather than the question was the obvious way to get that wrong — and Cancel
 * has the focus: a stray Enter answers no.
 */

import { confirmDelete, planDelete, type DeleteRequest, type DeleteResult } from './api';
import { deleteDoneText, deleteSummary } from './deleting';
import { esc } from './format';
import { icons } from './icons';
import { showToast } from './toast';

/**
 * Ask the server what deleting this would remove, show it, and delete it if
 * the owner says so. Resolves with what was done, or null if nothing was.
 */
export async function deleteWithConfirmation(req: DeleteRequest): Promise<DeleteResult | null> {
  let plan;
  try {
    plan = await planDelete(req);
  } catch (err) {
    showToast(`Cannot delete this: ${(err as Error).message}`, 4000);
    return null;
  }
  const root = document.createElement('div');
  root.className = 'confirm-overlay';
  root.innerHTML = `
    <div class="confirm" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-what">
      <div class="confirm-head">${icons.trash}<h2 id="confirm-title">Delete permanently?</h2></div>
      <div class="confirm-what" id="confirm-what">
        <strong>${esc(plan.title)}</strong>
        <span>${esc(deleteSummary(plan))}</span>
      </div>
      <ul class="confirm-paths">${plan.paths.map((p) => `<li>${esc(p)}</li>`).join('')}${
        plan.more ? `<li class="confirm-more">and ${plan.more.toLocaleString()} more</li>` : ''
      }</ul>
      ${
        plan.others?.length
          ? `<p class="confirm-others">Also removed, being in the same archive or on the same disc: ${esc(plan.others.join(', '))}</p>`
          : ''
      }
      <p class="confirm-warn">This cannot be undone.</p>
      <p class="confirm-error" hidden></p>
      <div class="confirm-actions">
        <button class="btn" data-cancel>Cancel</button>
        <button class="btn danger" data-delete>${icons.trash}<span>Delete</span></button>
      </div>
    </div>`;
  const host = document.getElementById('overlays') ?? document.body;
  host.appendChild(root);
  const cancelBtn = root.querySelector<HTMLButtonElement>('[data-cancel]')!;
  const deleteBtn = root.querySelector<HTMLButtonElement>('[data-delete]')!;
  const errorEl = root.querySelector<HTMLElement>('.confirm-error')!;
  const before = document.activeElement as HTMLElement | null;
  cancelBtn.focus();

  return new Promise((resolve) => {
    let busy = false;
    const finish = (result: DeleteResult | null): void => {
      window.removeEventListener('keydown', onKey, true);
      root.remove();
      before?.focus?.();
      resolve(result);
    };
    const onKey = (ev: KeyboardEvent): void => {
      // Everything stops here while the question is up; only Tab and the
      // buttons' own Enter and Space move on, and they need no help.
      ev.stopImmediatePropagation();
      if (ev.key === 'Escape' && !busy) {
        ev.preventDefault();
        finish(null);
      }
    };
    window.addEventListener('keydown', onKey, true);
    cancelBtn.addEventListener('click', () => {
      if (!busy) finish(null);
    });
    root.addEventListener('click', (ev) => {
      if (ev.target === root && !busy) finish(null);
    });
    deleteBtn.addEventListener('click', () => {
      if (busy) return;
      busy = true;
      deleteBtn.disabled = true;
      cancelBtn.disabled = true;
      errorEl.hidden = true;
      confirmDelete(plan.token)
        .then((result) => {
          showToast(deleteDoneText(result), 4000);
          finish(result);
        })
        .catch((err: Error) => {
          busy = false;
          cancelBtn.disabled = false;
          errorEl.textContent = err.message;
          errorEl.hidden = false;
        });
    });
  });
}
