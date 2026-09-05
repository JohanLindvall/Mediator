/**
 * Album detail sheet: cover, metadata, play/shuffle/queue actions and a track list.
 */
import { albumZipUrl, getAlbum, thumbUrl, type AlbumDetailResponse, type Item } from './api';
import { holdScroll, releaseScroll } from './scrollhold';
import { esc, formatBytes, formatDuration, withoutTrackNumber } from './format';
import { icons } from './icons';
import { showToast } from './toast';
import { shareAlbum } from './links';

export interface AlbumPanelOpts {
  /** Replace the queue with `items`, tagging it with `context` — see `ownsQueue()`. */
  play(items: Item[], start: number, shuffle: boolean, context: string): void;
  /** Put `items` after everything already queued — see `AudioPlayer.enqueue`. */
  enqueue(items: Item[], context: string): void;
  /** The token the current queue was started with, null when it has none. */
  queueContext(): string | null;
  /** Id of the loaded track, null when nothing is loaded. */
  playingId(): string | null;
  /** Queue index of the loaded track, -1 when nothing is loaded. */
  queuePos(): number;
  /** True while the current track is actually sounding. */
  playing(): boolean;
  /** True when pressing play would continue rather than start something over. */
  canResume(): boolean;
  /** Pause the current track, or resume it where it stopped. */
  toggle(): void;
  /** Observe playback changes; returns an unsubscribe. */
  watch(fn: () => void): () => void;
  /**
   * Show everything by this performer, or everything filed under this genre.
   * The sheet closes onto it: a drill-down behind an open sheet is a change
   * nobody can see.
   */
  showArtist(name: string): void;
  showGenre(name: string): void;
  /** Show the releases that sound like this one. The sheet closes onto it. */
  showSimilar(id: string, name: string): void;
}

let active: AlbumPanel | null = null;

export function openAlbumPanel(albumId: string, opts: AlbumPanelOpts): void {
  active?.close();
  active = new AlbumPanel(albumId, opts);
}

/**
 * Re-fetch the open panel after a library change. Tags and durations are
 * read in the background, so a panel opened during a big scan starts out
 * showing filenames and byte sizes; this fills them in as they land.
 */
export function reloadAlbumPanel(): void {
  void active?.reload();
}

/**
 * Tag title, else the filename without its extension — and without the track
 * number either way: the list is numbered already, and reading "1  01. …"
 * down a whole record is the sort of thing nobody notices until it is gone.
 */
function trackTitle(t: Item): string {
  return withoutTrackNumber(t.title || t.name.replace(/\.[^./\\]+$/, ''));
}

class AlbumPanel {
  private root: HTMLElement;
  private tracks: Item[] = [];
  private closed = false;
  /**
   * The album this sheet shows, and — the same string, deliberately — the
   * token any queue it starts is tagged with. Ids are already unique per
   * collection (a playlist's is prefixed), so two sheets can never claim each
   * other's playback, and one closed and reopened claims its own again.
   */
  private albumId: string;
  private lastHTML = '';
  private unwatch: () => void = () => {};

  constructor(albumId: string, private opts: AlbumPanelOpts) {
    this.albumId = albumId;
    this.root = document.createElement('div');
    this.root.className = 'overlay sheet-overlay';
    this.root.innerHTML = `<div class="sheet"><div class="sheet-loading"><div class="spinner"></div></div></div>`;
    document.getElementById('overlays')!.appendChild(this.root);
    document.body.classList.add('no-scroll');
    holdScroll();
    this.root.addEventListener('click', (ev) => {
      if (ev.target === this.root) {
        this.close();
        return;
      }
      const target = ev.target as HTMLElement;
      // The overflow closes on any press outside itself.
      if (!target.closest('.sheet-more')) this.toggleMore(false);
      // The performer and the genre are offers wherever they appear; the
      // sheet closes onto the answer. Delegated once, here: attached in
      // render() it stacked a handler per re-render, and a scan re-renders
      // the sheet every few hundred files.
      const artist = target.closest<HTMLElement>('.link-artist[data-artist]');
      if (artist) {
        this.close();
        this.opts.showArtist(artist.dataset.artist ?? '');
        return;
      }
      const genre = target.closest<HTMLElement>('.link-genre[data-genre]');
      if (genre) {
        this.close();
        this.opts.showGenre(genre.dataset.genre ?? '');
      }
    });
    document.addEventListener('keydown', this.onKey, true);
    // The sheet covers the audio bar at every viewport width, so its own
    // button is the only reachable transport while it is open: follow the
    // player wherever the state changes, not just when this panel clicks.
    this.unwatch = opts.watch(() => this.syncPlayState());
    requestAnimationFrame(() => this.root.classList.add('open'));
    void this.load(albumId);
  }

  private onKey = (ev: KeyboardEvent): void => {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      ev.stopPropagation();
      this.close();
    }
  };

  private async load(albumId: string): Promise<void> {
    try {
      const detail = await getAlbum(albumId);
      if (this.closed) return;
      this.render(detail);
    } catch {
      showToast('Album not found — it may have just changed');
      this.close();
    }
  }

  /** Refetch after a library change; leave the panel alone if it fails. */
  async reload(): Promise<void> {
    if (this.closed) return;
    try {
      const detail = await getAlbum(this.albumId);
      if (!this.closed) this.render(detail);
    } catch {
      // Transient (the album list is rebuilt on every change) — keep showing
      // what we have rather than yanking the panel away mid-read.
    }
  }

  private render(detail: AlbumDetailResponse): void {
    this.tracks = detail.tracks;
    const a = detail.album;
    const cover = a.coverId
      ? `<img class="sheet-cover" src="${thumbUrl(a.coverId, 512, a.mtime)}" alt="">`
      : `<div class="sheet-cover fallback"></div>`;

    const rows = detail.tracks
      .map((t, i) => {
        // Tag track numbers when the file has them, list position otherwise.
        const num = t.track && t.track > 0 ? t.track : i + 1;
        // Per-track artist is noise on a single-artist album.
        const artist = t.artist && t.artist !== a.artist ? t.artist : '';
        const right = t.duration ? formatDuration(t.duration / 1000) : formatBytes(t.size);
        return `
          <button class="track" data-i="${i}" data-id="${esc(t.id)}">
            <span class="t-num">${num}</span>
            <span class="t-playing">${icons.volume}</span>
            <span class="t-body">
              <span class="t-title">${esc(trackTitle(t))}</span>
              <span class="t-artist">${esc(artist)}</span>
            </span>
            <span class="t-size">${right}</span>
          </button>`;
      })
      .join('');

    // Prefer what the tags say over what the filesystem says. The server
    // derives year/genre/duration from the tracks (and leaves duration 0
    // until every track is known, so no half-counted total is shown).
    // The performer and the genre are the same offer here as on the card:
    // press either and the sheet closes onto everything else that is theirs.
    const meta: string[] = [];
    if (a.year) meta.push(String(a.year));
    if (a.genre) {
      meta.push(
        `<button type="button" class="link-genre" data-genre="${esc(a.genre)}" title="Everything filed under ${esc(a.genre)}">${esc(a.genre)}</button>`,
      );
    }
    meta.push(`${a.tracks} track${a.tracks === 1 ? '' : 's'}`);
    meta.push(a.duration ? formatDuration(a.duration / 1000) : formatBytes(a.size));

    const html = `
      <button class="icon-btn sheet-close" data-close aria-label="Close">${icons.close}</button>
      <div class="sheet-head">
        ${cover}
        <div class="sheet-headtext">
          <div class="sheet-kicker">${a.spoken ? 'Audiobook' : a.source === 'm3u' ? 'Playlist' : 'Album'}</div>
          <h2 class="sheet-title">${esc(a.name)}</h2>
          <div class="sheet-sub">${
            a.artist
              ? `<button type="button" class="link-artist" data-artist="${esc(a.artist)}" title="Everything by ${esc(a.artist)}">${esc(a.artist)}</button>`
              : ''
          }</div>
          <div class="sheet-meta">${meta.join(' · ')}</div>
          <div class="sheet-actions">
            <button class="btn primary" data-playall>${icons.play}<span>Play</span></button>
            <button class="btn" data-shuffleall>${icons.shuffle}<span>Shuffle</span></button>
            <button class="btn" data-enqueue
               title="Play this release after everything already queued">${icons.queue}<span>Add to queue</span></button>
            ${
              // A reading has no sound the similarity is measured over, so
              // the server answers nothing; an offer of nothing is not one.
              a.spoken
                ? ''
                : `<button class="btn" data-similar title="Releases that sound like this one">${icons.radio}<span>Similar</span></button>`
            }
            <div class="sheet-more">
              <button class="btn" data-more aria-label="More" aria-haspopup="menu" aria-expanded="false" title="Download, link">⋯</button>
              <div class="ab-menu sheet-menu" data-moremenu role="menu" hidden>
                <a class="vo-menu-item" data-zip href="${albumZipUrl(this.albumId)}" download
                   title="Download everything in this release as one file">Download</a>
                <button class="vo-menu-item" data-share title="Copy a link to this release">Copy a link</button>
              </div>
            </div>
          </div>
        </div>
      </div>
      <div class="sheet-tracks">${rows}</div>`;

    // Enrichment fires a change event every few hundred files; only touch the
    // DOM when something actually differs, so scrolling stays undisturbed.
    if (html === this.lastHTML) return;
    this.lastHTML = html;

    const sheet = this.root.querySelector('.sheet') as HTMLElement;
    const scroll = sheet.querySelector('.sheet-tracks')?.scrollTop ?? 0;
    sheet.innerHTML = html;
    const list = sheet.querySelector('.sheet-tracks') as HTMLElement;
    list.scrollTop = scroll;

    // A sleeve that will not load gives way to the placeholder, wired here
    // rather than as an inline handler building DOM out of a string.
    sheet.querySelector<HTMLImageElement>('img.sheet-cover')?.addEventListener('error', (ev) => {
      const fallback = document.createElement('div');
      fallback.className = 'sheet-cover fallback';
      (ev.target as HTMLElement).replaceWith(fallback);
    });
    sheet.querySelector('[data-more]')!.addEventListener('click', (ev) => {
      ev.stopPropagation();
      this.toggleMore();
    });
    sheet.querySelector('[data-zip]')!.addEventListener('click', () => this.toggleMore(false));
    sheet.querySelector('[data-share]')!.addEventListener('click', () => {
      this.toggleMore(false);
      void shareAlbum(this.albumId, a.name);
    });
    sheet.querySelector('[data-close]')!.addEventListener('click', () => this.close());
    sheet.querySelector('[data-playall]')!.addEventListener('click', () => this.onPrimary());
    sheet
      .querySelector('[data-shuffleall]')!
      .addEventListener('click', () => this.startPlayback(0, true));
    sheet.querySelector('[data-enqueue]')!.addEventListener('click', () => this.enqueue());
    sheet.querySelector('[data-similar]')?.addEventListener('click', () => {
      this.close();
      this.opts.showSimilar(this.albumId, a.name);
    });
    list.addEventListener('click', (ev) => {
      const row = (ev.target as HTMLElement).closest<HTMLElement>('.track');
      if (!row) return;
      // A row is its position, never its id: a playlist may list the same file
      // twice, and an id test made both copies claim the playing one — so the
      // second copy toggled the first instead of playing itself.
      const i = Number(row.dataset.i);
      // Tapping the marked row pauses it rather than restarting it from the
      // top — the sheet hides the bar's pause button. Only when this sheet
      // owns the queue, though: the same file marked because the grid is
      // playing it belongs to a different collection, and a tap here means
      // "play the album from here", not "pause that".
      if (i === this.currentRow() && this.ownsQueue() && this.opts.canResume()) this.opts.toggle();
      else this.startPlayback(i);
    });
    // The button and the row markers live outside the diffed HTML, so a
    // rebuilt sheet has to be told the play state again.
    this.syncPlayState();
  }

  /**
   * The single place playback starts: hand the player the list *and this
   * sheet's id*, which is what makes the queue this sheet's own from then on.
   */
  private startPlayback(start: number, shuffle = false): void {
    // An album with no tracks starts nothing, and so never comes to own the
    // queue someone else is playing. Say so rather than swallowing the press:
    // a button that does nothing at all reads as broken.
    if (this.tracks.length === 0) {
      showToast('Nothing to play');
      return;
    }
    this.opts.play(this.tracks, start, shuffle, this.albumId);
  }

  /** Put the album after everything already queued; the player says what it did. */
  private enqueue(): void {
    this.opts.enqueue(this.tracks, this.albumId);
  }

  /**
   * The overflow under "⋯": the download and the link, which are wanted
   * rarely and were the third row of buttons on a phone.
   */
  private toggleMore(show?: boolean): void {
    const menu = this.root.querySelector<HTMLElement>('[data-moremenu]');
    const btn = this.root.querySelector<HTMLElement>('[data-more]');
    if (!menu || !btn) return;
    const open = show ?? menu.hidden;
    menu.hidden = !open;
    btn.setAttribute('aria-expanded', String(open));
  }

  /**
   * Whether the player is playing *this* sheet's collection.
   *
   * The player is told who filled its queue and simply hands the token back;
   * nothing here looks at what the queue contains. That is deliberate, and it
   * is the whole point of this design. Every attempt to infer ownership from
   * the queue's contents failed somewhere: membership let any queue holding
   * one of these tracks hijack the sheet, and comparing the queue against the
   * live track list broke the moment the list changed — a track number
   * arriving from background enrichment, a file appearing in the directory, a
   * rewritten playlist — because the player still holds the order it was
   * given while this sheet has already re-rendered the new one. Comparing
   * against a snapshot taken at play time had the same flaw one step later.
   * A token cannot drift: reordering, insertions, deletions, duplicate
   * entries and twin playlists with identical track sets are all answered by
   * one string comparison that no library change can disturb.
   *
   * A sheet with no tracks owns nothing — it can never have started the queue,
   * and its id must not match by coincidence of an empty comparison.
   */
  private ownsQueue(): boolean {
    return this.tracks.length > 0 && this.opts.queueContext() === this.albumId;
  }

  /**
   * The index of the row holding the loaded track, or -1 when none of these
   * rows is it.
   *
   * Deliberately independent of ownership: a track started from the grid is
   * still this file, and the reader wants to see which line of the album they
   * are hearing even though the sheet's button will start the album rather
   * than pause that queue. Identity is the id; `queuePos()` only breaks the
   * tie when the collection lists the same file twice, and only when the row
   * it points at really is that file — for a foreign queue the position
   * indexes a different list entirely.
   */
  private currentRow(): number {
    const id = this.opts.playingId();
    if (!id) return -1;
    const pos = this.opts.queuePos();
    if (pos >= 0 && this.tracks[pos]?.id === id) return pos;
    return this.tracks.findIndex((t) => t.id === id);
  }

  /**
   * The transport: pause or resume the queue this sheet started, otherwise
   * start the album from the top. Resuming requires both — ours, and actually
   * continuable: a queue that has played out or died on a decode error stays
   * loaded, but pressing play on it would replay one track or do nothing, so
   * it falls back to a "Play" that starts the album over.
   */
  private onPrimary(): void {
    if (this.ownsQueue() && this.opts.canResume()) this.opts.toggle();
    else this.startPlayback(0);
  }

  /**
   * Apply the play state by mutation, never through `render()`: the label
   * belongs nowhere near the string diffed against `lastHTML`, or every
   * pause would rebuild the whole sheet and lose the reader's place.
   */
  private syncPlayState(): void {
    if (this.closed) return;
    // The two answer different questions and are computed separately: the
    // marker asks "is this file the one loaded", the button asks "is this
    // queue mine to pause". A queue started from the grid marks its row here
    // and still leaves the button on "Play".
    const row = this.currentRow();
    const sounding = this.opts.playing();
    this.root.querySelectorAll<HTMLElement>('.track').forEach((el) => {
      // By index, for the same reason the click handler uses one: two rows of
      // a playlist can share an id, and only one of them is playing.
      const cur = Number(el.dataset.i) === row;
      el.classList.toggle('current', cur);
      // The marker says which state the row is in, not just which row it is.
      if (cur) el.querySelector('.t-playing')!.innerHTML = sounding ? icons.volume : icons.play;
    });
    // The watcher can fire while the sheet is still the loading spinner.
    const btn = this.root.querySelector<HTMLButtonElement>('[data-playall]');
    if (!btn) return;
    // "Resume" is the word that promises the press will not start over, so it
    // is shown only when the press really would continue — same condition the
    // button acts on, so the label can never mean something else than the tap.
    const resumable = this.ownsQueue() && this.opts.canResume();
    const playing = resumable && sounding;
    const label = !resumable ? 'Play' : playing ? 'Pause' : 'Resume';
    btn.innerHTML = `${playing ? icons.pause : icons.play}<span>${label}</span>`;
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    if (active === this) active = null;
    document.removeEventListener('keydown', this.onKey, true);
    this.unwatch();
    document.body.classList.remove('no-scroll');
    releaseScroll();
    this.root.classList.remove('open');
    window.setTimeout(() => this.root.remove(), 220);
  }
}
