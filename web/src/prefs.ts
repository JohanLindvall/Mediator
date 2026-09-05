/**
 * Preferences: which directories the library indexes, and what is running.
 *
 * Changing the list is not a local edit — the server rewalks the disk, moves
 * its filesystem watches and drops whatever belonged to a directory that has
 * gone — so the dialog edits a copy and sends the whole list at once, and
 * nothing here pretends to have taken effect until the server says which set
 * ended up in force.
 */
import { getPrefs, serverAbout, setPrefs } from './api';
import { holdScroll, releaseScroll } from './scrollhold';
import { esc, formatDateFull } from './format';
import { icons } from './icons';
import { showToast } from './toast';

export function openPrefs(): void {
  new Prefs();
}

class Prefs {
  private root: HTMLElement;
  private roots: string[] = [];
  private persisted = true;
  private editable = true;
  private busy = false;

  constructor() {
    this.root = document.createElement('div');
    this.root.className = 'overlay sheet-overlay prefs-overlay';
    this.root.innerHTML = `
      <div class="sheet prefs">
        <button class="icon-btn sheet-close" data-close aria-label="Close">${icons.close}</button>
        <div class="prefs-head">
          <div class="sheet-kicker">Preferences</div>
          <h2 class="sheet-title">Directories to scan</h2>
          <div class="sheet-meta" data-note></div>
        </div>
        <div class="prefs-list" data-list></div>
        <form class="prefs-add" data-add>
          <input type="text" data-path placeholder="/path/to/media" spellcheck="false"
                 autocapitalize="off" autocorrect="off" aria-label="Directory to add">
          <button class="btn primary" type="submit">Add</button>
        </form>
        <div class="about" data-about></div>
      </div>`;
    document.getElementById('overlays')!.appendChild(this.root);
    document.body.classList.add('no-scroll');
    holdScroll();

    this.root.querySelector('[data-close]')!.addEventListener('click', () => this.close());
    this.root.addEventListener('click', (ev) => {
      if (ev.target === this.root) this.close();
    });
    document.addEventListener('keydown', this.onKey, true);
    (this.root.querySelector('[data-add]') as HTMLFormElement).addEventListener('submit', (ev) => {
      ev.preventDefault();
      const input = this.root.querySelector('[data-path]') as HTMLInputElement;
      const path = input.value.trim();
      if (!path) return;
      // Sent as a whole list, not as an addition: the server's answer is the
      // set that ended up in force, which is what gets rendered back.
      void this.save([...this.roots, path], () => {
        input.value = '';
      });
    });

    this.renderAbout();
    void this.reload();
    requestAnimationFrame(() => this.root.classList.add('open'));
  }

  private onKey = (ev: KeyboardEvent): void => {
    if (ev.key !== 'Escape') return;
    ev.preventDefault();
    ev.stopPropagation();
    this.close();
  };

  /**
   * What is running, and what it turned out to be able to do here.
   *
   * Both are easy to be wrong about from the outside. A binary that has been
   * rebuilt and not restarted looks exactly like one that has; and hardware
   * conversion either happens or does not, silently, with the difference
   * between a film that plays and one that stalls hanging on a driver and a
   * group membership. Neither belongs in a log nobody reads.
   *
   * Only what is worth saying is said: a capability that is simply present,
   * as they all are on an ordinary install, is one line rather than six.
   * What is *missing* gets named, because that is the line that explains
   * whatever the viewer came here wondering about.
   */
  private renderAbout(): void {
    const box = this.root.querySelector('[data-about]');
    if (!box) return;
    const info = serverAbout();
    if (!info) return;
    const b = info.build;
    const c = info.capabilities;

    const version = b.modified ? `${b.version} (modified)` : b.version;
    const built = b.time ? new Date(b.time) : null;
    const when = built && !Number.isNaN(built.valueOf()) ? formatDateFull(built.valueOf()) : '';

    // Named only when it is not there: a list of things that work is a list
    // nobody needs to read.
    const missing: string[] = [];
    if (!c.ffmpeg) missing.push('no ffmpeg — video cannot be converted or thumbnailed');
    else if (!c.ffprobe) missing.push('no ffprobe');
    if (!c.database) missing.push('no database — nothing is remembered between runs');
    if (!c.loopback) missing.push('no loopback address — archived video cannot be seeked');

    const hw = c.hardware
      ? `Converting on ${esc(c.hardware)}${c.device ? ` (${esc(c.device)})` : ''}`
      : 'Converting on the processor';

    box.innerHTML = `
      <div class="about-head">This server</div>
      <dl class="about-list">
        <dt>Version</dt><dd>${esc(version)}</dd>
        ${b.commit ? `<dt>Commit</dt><dd class="mono">${esc(b.commit.slice(0, 12))}</dd>` : ''}
        ${when ? `<dt>Built</dt><dd>${esc(when)}</dd>` : ''}
        <dt>Runtime</dt><dd>${esc(b.go)} · ${esc(b.os)}/${esc(b.arch)}</dd>
        <dt>Video</dt><dd>${hw}</dd>
      </dl>
      ${missing.length ? `<ul class="about-missing">${missing.map((m) => `<li>${esc(m)}</li>`).join('')}</ul>` : ''}`;
  }

  private async reload(): Promise<void> {
    try {
      const p = await getPrefs();
      this.roots = p.roots;
      this.persisted = p.persisted;
      this.editable = p.editable;
      this.render();
    } catch {
      showToast('Could not read the preferences');
      this.close();
    }
  }

  private async save(roots: string[], ok?: () => void): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    this.root.classList.add('busy');
    try {
      const p = await setPrefs(roots);
      this.roots = p.roots;
      this.persisted = p.persisted;
      this.editable = p.editable;
      ok?.();
      this.render();
      // The walk runs on the server after it answers, so the grid fills in
      // through the usual change events rather than anything done here.
      showToast('Scanning…', 2000);
    } catch (err) {
      showToast(err instanceof Error ? err.message : 'Could not change the directories', 4000);
    } finally {
      this.busy = false;
      this.root.classList.remove('busy');
    }
  }

  private render(): void {
    const note = this.root.querySelector('[data-note]') as HTMLElement;
    note.textContent = !this.editable
      ? 'This server was started with locked directories, so these can only be read.'
      : this.persisted
        ? 'Changes take effect straight away and are remembered.'
        : 'No database, so changes last until the server restarts.';
    // A locked server refuses every change, so the controls that would make
    // one are not offered: an input whose every use is rejected is worse
    // than no input.
    (this.root.querySelector('[data-add]') as HTMLElement).hidden = !this.editable;

    const list = this.root.querySelector('[data-list]') as HTMLElement;
    list.innerHTML =
      this.roots
        .map(
          (r) => `<div class="prefs-row">
            <span class="prefs-path"><bdi title="${esc(r)}">${esc(r)}</bdi></span>
            ${
              this.editable
                ? `<button class="icon-btn sm" data-rm="${esc(r)}"
                     aria-label="Stop scanning ${esc(r)}">${icons.close}</button>`
                : ''
            }
          </div>`,
        )
        .join('') || '<div class="prefs-empty">Nothing is being scanned.</div>';

    for (const btn of list.querySelectorAll<HTMLElement>('[data-rm]')) {
      btn.addEventListener('click', () => {
        const path = btn.dataset.rm!;
        if (this.roots.length <= 1) {
          showToast('At least one directory is needed', 3000);
          return;
        }
        void this.save(this.roots.filter((r) => r !== path));
      });
    }
  }

  private close(): void {
    document.removeEventListener('keydown', this.onKey, true);
    document.body.classList.remove('no-scroll');
    releaseScroll();
    this.root.classList.remove('open');
    window.setTimeout(() => this.root.remove(), 200);
  }
}
