/** App entry: header controls, virtual grid, routing between views, SSE. */
import './style.css';
import {
  tracksOf,
  getAlbum,
  getItem,
  getPositions,
  contentKnown,
  loadInfo,
  serverAbout,
  shownContent,
  subscribeEvents,
  streamUrl,
  thumbUrl,
  type Album,
  type Artist,
  type Counts,
  type Genre,
  type Season,
  type Series,
  type Item,
  type Position,
} from './api';
import { AudioPlayer } from './audio';
import { VirtualGrid, type GridAdapter } from './grid';
import { icons, kindIcon } from './icons';
import { openLightbox } from './lightbox';
import { openPrefs } from './prefs';
import { itemCellKey } from './cells';
import { previewLeave, watchPreview } from './preview';
import { trackViewport } from './viewport';
import { openAlbumPanel, reloadAlbumPanel, type AlbumPanelOpts } from './albumpanel';
import { openVideo, type VideoOpts } from './video';
import {
  AlbumsSource,
  findKind,
  ArtistsSource,
  GenresSource,
  SeriesSource,
  LibrarySource,
  type ItemSource,
  type QueryState,
} from './sources';
import { barShown, barStep } from './barhide';
import {
  chipCount,
  esc,
  extBadge,
  formatBytes,
  formatDate,
  formatDuration,
  mediaShape,
} from './format';
import { watchState } from './playback';
import { countKey, countsToShow, listFilters, narrowed } from './query';
import { defaultMode, fallbackMode, modeShown, queueSource, type ViewMode } from './content';
import { openingSort, sortOptions } from './sorts';
import { loadThumb, cancelThumb, retryThumbs } from './thumbs';
import { showToast } from './toast';
import { shareView } from './links';

type Mode = ViewMode;

interface AppState {
  mode: Mode;
  q: string;
  /**
   * The sort key. Which ones mean anything depends on the view — a release
   * has a year and a performer where a file has a size — so this is a plain
   * string and `sortOptions` decides what can be picked.
   */
  sort: string;
  desc: boolean;
  /**
   * What a shortlink asked to be opened on top of the view: one item, or one
   * release. Arrival state and nothing else — it is read out of the address
   * and never written back into it, because an overlay is not a view. The
   * thing behind it is, and a player that rewrote the address on every swipe
   * would be describing what is in front of the listing rather than the
   * listing itself.
   */
  item: string;
  album: string;
  /** Set while drilled into one performer from the artists view. */
  artist: string;
  /** Set while drilled into one genre. Never both at once — each drill-down
   * clears the other, or the album view would be asked for a performer and a
   * genre together and answer with whatever their intersection happened to
   * be, under a chip naming only one of them. */
  genre: string;
  /** Set while drilled into one show, and then into one of its seasons. */
  series: string;
  season: number;
  /**
   * Set while listing what sounds like one thing: an album id in the albums
   * view, a performer's name in the artists view. nearName is what the chip
   * says, since an id says nothing.
   */
  near: string;
  nearName: string;
}

const state: AppState = {
  mode: 'all',
  q: '',
  item: '',
  album: '',
  sort: 'mtime',
  desc: true,
  artist: '',
  genre: '',
  series: '',
  season: 0,
  near: '',
  nearName: '',
};

// ---- element handles ---------------------------------------------------

const $ = <T extends HTMLElement>(sel: string): T => document.querySelector(sel) as T;
const scroller = $('#scroller');
/**
 * Phones get the compact chip counts and the bar that leaves while they
 * browse; a desktop has the room and keeps both. Coarse pointer is the
 * question, not width: a narrow desktop window still has a wheel, and a bar
 * that ducked on every scroll tick there would be a twitch, not a saving.
 */
const compactChips = matchMedia('(pointer: coarse)').matches;
const spacer = $('#spacer');
const plane = $('#plane');
const statusEl = $('#status');
const searchInput = $<HTMLInputElement>('#search');
const searchClear = $('#search-clear');
const sortSelect = $<HTMLSelectElement>('#sort');
const prefsBtn = $('#prefs');
const shareBtn = $('#share');
const sortDirBtn = $('#sort-dir');
const queueAllBtn = $<HTMLButtonElement>('#queue-all');
const chipsNav = $('#chips');
const countEl = $('#count');
const connEl = $('#conn');

$('#searchbox').querySelector('.search-icon')!.innerHTML = icons.search;
searchClear.innerHTML = icons.close;
trackViewport();
shareBtn.innerHTML = icons.link;
shareBtn.addEventListener('click', () => {
  void shareView(shareLabel());
});
prefsBtn.innerHTML = icons.folder;
prefsBtn.addEventListener('click', () => openPrefs());
// Hidden until the server has said this caller may have it: the preferences
// name the directories the library is rooted at, and a face confined to one
// branch of that tree has no business learning what the others are called.
// Drawn hidden rather than shown and taken away, the answer arriving a moment
// after the page does.
prefsBtn.hidden = true;

// ---- data --------------------------------------------------------------

const libSource = new LibrarySource();
const albumsSource = new AlbumsSource();
const artistsSource = new ArtistsSource();
const genresSource = new GenresSource();
const seriesSource = new SeriesSource();
/**
 * The item listing as a position in a sequence, which is all the overlays
 * need of it: both step through the same one, so closing a picture and
 * opening the video below it keeps the viewer in the same place.
 */
const listingSource: ItemSource = {
  item: (i) => libSource.item(i),
  total: () => libSource.total,
};
const positions = new Map<string, Position>();
const audioPlayer = new AudioPlayer($('#audiobar'));

const THUMB_W = (window.devicePixelRatio || 1) > 1.4 ? 720 : 360;

// ---- cell renderers ----------------------------------------------------

function skeleton(el: HTMLElement): void {
  if (el.dataset.key === 'sk') return;
  cancelThumb(el);
  el.dataset.key = 'sk';
  el.className = 'cell card sk-card';
  el.innerHTML = `<div class="thumb sk"></div>
    <div class="meta"><div class="sk-line"></div><div class="sk-line short"></div></div>`;
}

/**
 * Point a cell's image at its artwork — or remove the element where there is
 * none and the fallback icon is the tile. One place, because the failure
 * handling is load-bearing and was pasted per renderer: the element stays on
 * error (it is invisible without .ok and the retry needs somewhere to load
 * into — removing it made every failure permanent).
 */
function wireThumb(el: HTMLElement, src: string | null): void {
  const img = el.querySelector('img')!;
  if (!src) {
    cancelThumb(el);
    img.remove();
    return;
  }
  img.onload = () => img.classList.add('ok');
  img.onerror = () => img.classList.remove('ok');
  loadThumb(el, img, src);
}

function thumbSrc(item: Item): string {
  // Browsers render SVG natively; everything else gets a generated thumb.
  return item.name.toLowerCase().endsWith('.svg')
    ? streamUrl(item.id)
    : thumbUrl(item.id, THUMB_W, item.mtime);
}

function progressHtml(item: Item): string {
  const p = positions.get(item.id);
  if (item.kind !== 'video' || !p) return '';
  // The same rule the player resumes by and the server counts by, so a tile
  // cannot wear the tick while the player still offers to carry on.
  switch (watchState(p.t, p.d)) {
    case 'none':
      return '';
    case 'done':
      return `<span class="watched" title="Watched">${icons.check}</span>`;
    default:
      return `<div class="prog"><i style="width:${(Math.min(1, p.t / p.d) * 100).toFixed(1)}%"></i></div>`;
  }
}

/** Duration chip + watch progress, rendered inside .thumb-overlays. */
function overlaysHtml(item: Item): string {
  const dur = item.duration ? `<span class="dur">${formatDuration(item.duration / 1000)}</span>` : '';
  // Only where there is something to say. A badge reading "0" on every tile
  // in a library nobody has played through yet is noise, and the absence is
  // already the answer.
  const plays = item.plays
    ? `<span class="plays" title="Played ${item.plays} time${item.plays === 1 ? '' : 's'}">${icons.play}${item.plays}</span>`
    : '';
  // And the verdict, which is what the popular orders sort on first: a
  // listing ordered by something invisible is a listing that looks wrong.
  // The resemblance to something judged (item.affinity) is deliberately
  // not drawn: it ranks the popular orders, but a thumb on a tile the
  // owner never touched read as their own verdict, faint or not. The
  // server still says it, for a later view that can say it better.
  const like = item.like
    ? `<span class="plays" title="${item.like > 0 ? 'Liked' : 'Disliked'}">${item.like > 0 ? icons.thumbUp : icons.thumbDown}</span>`
    : '';
  const spoken = item.spoken ? `<span class="plays" title="Spoken word">${icons.book}</span>` : '';
  // The marks share one row at the top-left, styled like the duration pill;
  // the watched tick keeps that corner and the row starts after it (CSS).
  const marks = plays || like || spoken ? `<div class="marks">${plays}${like}${spoken}</div>` : '';
  return progressHtml(item) + dur + marks;
}

/**
 * The play badge is only a second affordance where it does something the rest
 * of the cell does not. Opening a video or a track already starts it, and a
 * picture has nothing to play, so those keep the badge they had — a hover
 * hint, not a button. A collection is the case that differs: the cell opens
 * it, the badge plays it.
 */
function playBadgeAttrs(kind: Item['kind']): string {
  return kind === 'playlist' ? ' data-play title="Play this playlist"' : '';
}

function renderItemCell(el: HTMLElement, item: Item | undefined): void {
  if (!item) {
    skeleton(el);
    return;
  }
  // What is in the key, and why, is with the reading of it in cells.ts.
  const key = itemCellKey(item);
  if (el.dataset.key === key) {
    // Same item, same content: only refresh the thumb overlays (duration
    // arrives via enrichment, progress changes while watching).
    const holder = el.querySelector<HTMLElement>('.thumb-overlays');
    if (holder) holder.innerHTML = overlaysHtml(item);
    return;
  }
  el.dataset.key = key;
  el.className = `cell card kind-${item.kind}`;
  const badge = extBadge(item.name);
  const title = item.kind === 'audio' && item.title ? item.title : item.name;
  const sub =
    item.kind === 'audio' && item.artist
      ? subFacts(
          link('artist', item.artist, `Everything by ${item.artist}`),
          item.year,
          item.genre ? link('genre', item.genre, `Everything filed under ${item.genre}`) : '',
        )
      : `${formatDate(item.mtime)} · ${formatBytes(item.size)}`;
  // What the file is, technically, on hover: where it was found, and what it
  // was made with. It goes on the tile as a whole rather than on the name
  // alone, since the picture is what a pointer lands on — and on the name too,
  // which is the part a long one runs off the end of.
  const tip = facts([item.path, mediaShape(item)]).replace(' · ', '\n');
  el.title = tip;
  el.innerHTML = `
    <div class="thumb">
      <div class="thumb-fallback">${kindIcon(item.kind)}</div>
      <img alt="" decoding="async" draggable="false">
      <span class="badge">${esc(badge)}</span>
      <div class="thumb-overlays">${overlaysHtml(item)}</div>
      <span class="hover-play"${playBadgeAttrs(item.kind)}>${item.kind === 'image' ? icons.search : icons.play}</span>
    </div>
    <div class="meta">
      <div class="title" title="${esc(tip)}">${esc(title)}</div>
      <div class="sub">${sub}</div>
    </div>`;
  // Entering is where the item is read, since the grid recycles cells: a
  // listener that closed over this render's item would, three scrolls
  // later, preview a film that is no longer under the pointer.
  watchPreview(el, item);

  // Playlists always use the icon tile.
  wireThumb(el, item.kind === 'playlist' ? null : thumbSrc(item));
}

/**
 * A line of whatever is known, in the order it is worth reading.
 *
 * Everything here is optional: a release with no tags at all still needs a
 * second line, and one with all of them must not run its performer off the
 * end. Empty parts are dropped rather than leaving stray separators.
 */
function facts(parts: (string | number | undefined | null)[]): string {
  return parts.filter((p) => p !== undefined && p !== null && p !== '').join(' · ');
}

/**
 * The same line as `facts`, for pieces that are already markup — a name
 * written as a link has to reach the page as HTML, so it cannot go through
 * the escaping that plain values need.
 */
function subFacts(...parts: (string | number | undefined | null)[]): string {
  return parts
    .map((p) => (typeof p === 'number' ? String(p) : (p ?? '')))
    .filter((p) => p !== '')
    .join(' · ');
}

/** How often something has been played, or nothing where it has not. */
function plays(n: number | undefined): string {
  return n ? `${n.toLocaleString()} play${n === 1 ? '' : 's'}` : '';
}

/** A span of years: one where a performer released everything in one. */
function years(from?: number, to?: number): string {
  if (!from && !to) return '';
  if (!from || !to || from === to) return String(from || to);
  return `${from}\u2013${to}`;
}

/**
 * What every collection card is: a square cover with a fallback icon and a
 * badge, a title, and a line of facts under it. The albums, performers,
 * genres, shows and seasons each used to spell the whole of this out, and
 * what they actually differ in is the handful of fields below.
 */
interface CollectionCard {
  /** What makes this rendering: a card whose key is unchanged is left alone. */
  key: string;
  cls: string;
  icon: string;
  badge: string;
  title: string;
  /** The line under the title, already markup where a fact is a link. */
  sub: string;
  /** The same line in plain text, for the tooltip; defaults to sub. */
  subTitle?: string;
  /** A play badge over the cover, where the card has something to play. */
  play?: string;
  coverId?: string;
  mtime: number;
}

/** How alike something sounds, as a card says it: only on a "like this" listing. */
function alike(similarity?: number): string {
  return similarity ? `${Math.round(similarity * 100)}% alike` : '';
}

function renderCollectionCell(el: HTMLElement, card: CollectionCard): void {
  if (el.dataset.key === card.key) return;
  el.dataset.key = card.key;
  el.className = `cell card album-card ${card.cls}`.trimEnd();
  const play = card.play ? `<span class="hover-play" data-play title="${esc(card.play)}">${icons.play}</span>` : '';
  el.innerHTML = `
    <div class="thumb square">
      <div class="thumb-fallback">${card.icon}</div>
      <img alt="" decoding="async" draggable="false">
      <span class="badge">${card.badge}</span>${play}
    </div>
    <div class="meta">
      <div class="title" title="${esc(card.title)}">${esc(card.title)}</div>
      <div class="sub" title="${esc(card.subTitle ?? card.sub)}">${card.sub}</div>
    </div>`;
  wireThumb(el, card.coverId ? thumbUrl(card.coverId, THUMB_W, card.mtime) : null);
}

/** "3 albums", "1 track": a count with its noun, for the cards. */
function counted(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? '' : 's'}`;
}

/** The line under a show or a season: how many episodes, how long, how played. */
function episodesLine(episodes: number, duration: number | undefined, played: number | undefined): string {
  return facts([counted(episodes, 'episode'), duration ? formatDuration(duration / 1000) : '', plays(played)]);
}

function renderAlbumCell(el: HTMLElement, album: Album | undefined): void {
  if (!album) {
    skeleton(el);
    return;
  }
  const genres = album.genres ?? (album.genre ? [album.genre] : []);
  renderCollectionCell(el, {
    // The year and the genre arrive with enrichment, so they belong in the
    // key: a card rendered before the tags were read has to be redrawn when
    // they are, and nothing else about the release will have changed.
    key: `${album.id}:${album.mtime}:${album.coverId ?? ''}:${album.tracks}:${album.year ?? ''}:${genres.join('|')}:${album.plays ?? 0}`,
    cls: '',
    icon: icons.disc,
    badge: album.source === 'm3u' ? 'M3U' : `${album.tracks} ♪`,
    play: 'Play this album',
    title: album.name,
    subTitle: facts([alike(album.similarity), album.artist, album.year, genres.join(' · '), plays(album.plays)]),
    sub: subFacts(
      album.artist ? link('artist', album.artist, `Everything by ${album.artist}`) : `${album.tracks} tracks`,
      album.year,
      // Every genre the tag names, each its own way across — a release
      // filed under three of them is in three of them.
      genres.map((g) => link('genre', g, `Everything filed under ${g}`)).join(' · '),
      plays(album.plays),
    ),
    coverId: album.coverId,
    mtime: album.mtime,
  });
}

function renderArtistCell(el: HTMLElement, artist: Artist | undefined): void {
  if (!artist) {
    skeleton(el);
    return;
  }
  const tracks = counted(artist.tracks, 'track');
  const time = artist.duration ? formatDuration(artist.duration / 1000) : '';
  const span = years(artist.fromYear, artist.toYear);
  renderCollectionCell(el, {
    key: `${artist.id}:${artist.mtime}:${artist.coverId ?? ''}:${artist.tracks}:${artist.genre ?? ''}:${artist.fromYear ?? ''}:${artist.toYear ?? ''}:${artist.plays ?? 0}`,
    cls: 'artist-card',
    icon: icons.artist,
    badge: counted(artist.albums, 'album'),
    title: artist.name,
    // What they are mostly filed under, and the span their dated releases
    // cover — a performer is not one genre or one year, but a line that
    // says nothing at all is worse than one that says what is mostly true.
    subTitle: facts([alike(artist.similarity), tracks, time, span, artist.genre, plays(artist.plays)]),
    sub: subFacts(
      tracks,
      time,
      span,
      artist.genre ? link('genre', artist.genre, `Everything filed under ${artist.genre}`) : '',
      plays(artist.plays),
    ),
    coverId: artist.coverId,
    mtime: artist.mtime,
  });
}

// ---- grid adapters -----------------------------------------------------

/** Whether a cell press landed on the play badge rather than the cell. */
function viaPlayBadge(ev: Event): boolean {
  return !!(ev.target as HTMLElement).closest('.hover-play[data-play]');
}

/**
 * The performer or the genre named on a card, when the press landed on it:
 * one delegated press on a cell, and this is what decides whether it was a
 * name. Every adapter that draws a name has to ask, since a press it did
 * not check for falls through to the card — which for a track once meant
 * queueing the whole library.
 */
function viaLink(ev: Event): { kind: 'artist' | 'genre'; value: string } | null {
  const target = ev.target as HTMLElement;
  const artist = target.closest<HTMLElement>('.link-artist[data-artist]');
  if (artist) return { kind: 'artist', value: artist.dataset.artist ?? '' };
  const genre = target.closest<HTMLElement>('.link-genre[data-genre]');
  if (genre) return { kind: 'genre', value: genre.dataset.genre ?? '' };
  return null;
}

/** Follow a name pressed on a card; says whether the press was one. */
function followLink(ev: Event): boolean {
  const link = viaLink(ev);
  if (!link) return false;
  if (link.kind === 'artist') showArtist(link.value);
  else showGenre(link.value);
  return true;
}

/**
 * A name written as something to press.
 *
 * The performer under a release and the genre beside it are both the answer
 * to a question the card raises — what else is theirs, what else is like
 * this — so both are click targets wherever either is shown. They look like
 * the caption they replaced until they are pointed at: a card whose every
 * fact is a visible button is a card nobody can read.
 */
function link(kind: 'artist' | 'genre', value: string, title: string): string {
  return `<button type="button" class="link-${kind}" data-${kind}="${esc(value)}" title="${esc(title)}">${esc(value)}</button>`;
}

const panelOpts: AlbumPanelOpts = {
  // The sheet passes its own album id as the queue's context; that token, and
  // nothing about the queue's contents, is how it later recognises its own
  // playback.
  play: (items, start, shuffle, context) => audioPlayer.playItems(items, start, shuffle, context),
  enqueue: (items, context) => audioPlayer.enqueue(items, context),
  queueContext: () => audioPlayer.queueContext(),
  playingId: () => audioPlayer.current?.id ?? null,
  queuePos: () => audioPlayer.queuePos,
  playing: () => audioPlayer.isPlaying,
  canResume: () => audioPlayer.canResume(),
  toggle: () => audioPlayer.toggle(),
  watch: (fn) => audioPlayer.watch(fn),
  showArtist: (name) => showArtist(name),
  showGenre: (name) => showGenre(name),
  showSimilar: (id, name) => showNear('albums', id, name),
};

/**
 * Start a collection without opening it, for the play badge on its card.
 *
 * The tracks have to be fetched — a card knows a cover and a count, not a
 * running order — so this is the one place a click turns into a request
 * before anything sounds. Failure says so rather than doing nothing, since
 * from the outside a silent no-op and a slow load look the same.
 */
async function playCollection(id: string, what: string): Promise<void> {
  try {
    const detail = await getAlbum(id);
    if (detail.tracks.length === 0) {
      showToast(`Nothing to play in this ${what}`);
      return;
    }
    // The collection's own id is the queue's context, exactly as the sheet
    // passes it: a queue started from the card is the same queue the sheet
    // would recognise as its own.
    audioPlayer.playItems(detail.tracks, 0, false, id);
  } catch {
    showToast(`Could not open this ${what}`);
  }
}

async function playAudioFromGrid(item: Item): Promise<void> {
  // Queue = every audio file matching the current search/sort, from this one.
  // It belongs to no collection, so it is started with no context: an album
  // sheet showing one of these tracks marks the row but keeps offering "Play",
  // because pressing it should give the reader that album, not this queue.
  // One answer for the whole listing, however long — the same one "queue
  // all" takes — rather than a page at a time.
  try {
    const items = (await tracksOf('items', listFilters(queryState()))).tracks;
    let start = items.findIndex((x) => x.id === item.id);
    if (start < 0) {
      items.unshift(item);
      start = 0;
    }
    audioPlayer.playItems(items, start, false, null);
  } catch {
    audioPlayer.playItems([item], 0, false, null);
  }
}

/**
 * How the player is opened, wherever it is opened from: which listing it may
 * step through, where this file was left, and what to do when it moves. The
 * grid and a shortlink hand it the same thing — a film opened by address must
 * behave like one opened by clicking it.
 */
function playerOpts(src: ItemSource, index: number): VideoOpts {
  return {
    nav: { src, index },
    resumeFor: (id) => positions.get(id),
    onPosition: (id, t, d) => {
      positions.set(id, { t, d, u: Date.now() });
      grid.refresh();
    },
  };
}

/**
 * A listing of exactly one thing, for a viewer opened by a shortlink.
 *
 * The viewers step through the listing they were opened from, and a link was
 * not opened from one — it named a single film or photograph, which is what
 * whoever sent it meant. Stepping is a browsing affordance and this is not
 * browsing; closing the viewer leaves the listing behind it, where it is.
 */
function justThis(item: Item): ItemSource {
  return { item: (i) => Promise.resolve(i === 0 ? item : undefined), total: () => 1 };
}

/**
 * Open whatever the address asked to be opened on top of the view.
 *
 * Cleared as it is read: a link is an arrival and not a state to be returned
 * to, so closing the viewer and searching for something else must not put it
 * back. An item this face cannot see — a film on a music face, something
 * outside the directories this caller is confined to — answers 404 and says
 * so, rather than opening nothing and looking broken.
 */
async function openLinked(): Promise<void> {
  const itemId = state.item;
  const albumId = state.album;
  state.item = '';
  state.album = '';
  if (albumId) {
    openAlbumPanel(albumId, panelOpts);
    return;
  }
  if (!itemId) return;
  let item: Item;
  try {
    item = await getItem(itemId);
  } catch {
    showToast('That is not in this library');
    return;
  }
  const src = justThis(item);
  switch (item.kind) {
    case 'video':
      openVideo(item, playerOpts(src, 0));
      break;
    case 'image':
      openLightbox(src, 0);
      break;
    case 'audio':
      void playAudioFromGrid(item);
      break;
    case 'playlist':
      openAlbumPanel(`p${item.id}`, panelOpts);
      break;
  }
}

const itemAdapter: GridAdapter<Item | Album | Artist | Genre | Series | Season> = {
  count: () => libSource.total,
  get: (i) => libSource.get(i),
  itemKey: (x) => (x as Item).id,
  need: (a, b) => libSource.need(a, b),
  render: (el, item) => renderItemCell(el, item as Item | undefined),
  release: (el) => {
    // A cell recycled out from under the pointer takes its preview with it.
    previewLeave();
    cancelThumb(el);
  },
  onClick: (i, ev) => {
    const item = libSource.get(i);
    if (!item) return;
    // The performer and the genre under a track are ways across to the rest
    // of their work, exactly as they are on a release.
    if (followLink(ev)) return;
    switch (item.kind) {
      case 'video':
        openVideo(item, playerOpts(listingSource, i));
        break;
      case 'image':
        openLightbox(listingSource, i);
        break;
      case 'audio':
        void playAudioFromGrid(item);
        break;
      case 'playlist':
        if (viaPlayBadge(ev)) void playCollection(`p${item.id}`, 'playlist');
        else openAlbumPanel(`p${item.id}`, panelOpts);
        break;
    }
  },
  minCardWidth: (w) => (w < 480 ? 150 : w < 1100 ? 172 : 196),
  cellHeight: (cardW) => Math.round(cardW * 0.62) + 56,
};

function renderGenreCell(el: HTMLElement, genre: Genre | undefined): void {
  if (!genre) {
    skeleton(el);
    return;
  }
  const sub = facts([
    counted(genre.artists, 'artist'),
    counted(genre.tracks, 'track'),
    genre.duration ? formatDuration(genre.duration / 1000) : '',
    years(genre.fromYear, genre.toYear),
    plays(genre.plays),
  ]);
  renderCollectionCell(el, {
    key: `${genre.id}:${genre.mtime}:${genre.coverId ?? ''}:${genre.albums}:${genre.tracks}:${genre.plays ?? 0}`,
    cls: 'genre-card',
    icon: icons.tag,
    badge: counted(genre.albums, 'album'),
    title: genre.name,
    sub: esc(sub),
    subTitle: sub,
    coverId: genre.coverId,
    mtime: genre.mtime,
  });
}

/**
 * A show, and — when one is open — its seasons.
 *
 * Both are drawn by this one renderer because a season card is a show's card
 * with a number on it: the same shape, the same artwork, the same counts, one
 * level down. The seasons come inside the series object, so opening a show
 * costs no request at all.
 */
function renderSeriesCell(el: HTMLElement, show: Series | undefined): void {
  if (!show) {
    skeleton(el);
    return;
  }
  const sub = episodesLine(show.episodes, show.duration, show.plays);
  renderCollectionCell(el, {
    key: `${show.id}:${show.mtime}:${show.coverId ?? ''}:${show.episodes}:${show.plays ?? 0}`,
    cls: 'series-card',
    icon: icons.series,
    badge: counted(show.seasons.length, 'season'),
    title: show.name,
    sub: esc(sub),
    subTitle: sub,
    coverId: show.coverId,
    mtime: show.mtime,
  });
}

function renderSeasonCell(el: HTMLElement, season: Season | undefined): void {
  if (!season) {
    skeleton(el);
    return;
  }
  const sub = episodesLine(season.episodes, season.duration, season.plays);
  renderCollectionCell(el, {
    key: `${state.series}:${season.season}:${season.episodes}:${season.coverId ?? ''}:${season.plays ?? 0}`,
    cls: 'series-card',
    icon: icons.series,
    badge: `${season.episodes} ♦`,
    // A season with no number is what a folder of unnumbered episodes comes
    // to; calling it "Season 0" would be inventing one.
    title: season.season > 0 ? `Season ${season.season}` : 'Episodes',
    sub: esc(sub),
    subTitle: sub,
    coverId: season.coverId,
    mtime: season.mtime,
    play: 'Play this season',
  });
}

/** The size of a collection card, which is the same for every collection. */
const collectionSize = {
  minCardWidth: (w: number): number => (w < 480 ? 156 : w < 1100 ? 180 : 200),
  cellHeight: (cardW: number): number => cardW + 56,
};

const albumAdapter: GridAdapter<Item | Album | Artist | Genre | Series | Season> = {
  count: () => albumsSource.count(),
  get: (i) => albumsSource.get(i),
  itemKey: (x) => (x as Item).id,
  need: () => {},
  render: (el, album) => renderAlbumCell(el, album as Album | undefined),
  release: (el) => cancelThumb(el),
  onClick: (i, ev) => {
    const album = albumsSource.get(i);
    if (!album) return;
    // The performer's name under the title is a way to the rest of their
    // work, which is the question a release most often raises.
    if (followLink(ev)) return;
    // Anywhere but the badge still opens the sheet, which is where the
    // running order and the individual tracks are.
    if (viaPlayBadge(ev)) void playCollection(album.id, 'album');
    else openAlbumPanel(album.id, panelOpts);
  },
  ...collectionSize,
};

const artistAdapter: GridAdapter<Item | Album | Artist | Genre | Series | Season> = {
  count: () => artistsSource.count(),
  get: (i) => artistsSource.get(i),
  itemKey: (x) => (x as Item).id,
  need: () => {},
  render: (el, artist) => renderArtistCell(el, artist as Artist | undefined),
  release: (el) => cancelThumb(el),
  onClick: (i, ev) => {
    // The genre under their name goes across to it; anywhere else drills
    // down into this performer's own releases.
    if (followLink(ev)) return;
    const artist = artistsSource.get(i);
    if (artist) showArtist(artist.name);
  },
  ...collectionSize,
};

const genreAdapter: GridAdapter<Item | Album | Artist | Genre | Series | Season> = {
  count: () => genresSource.count(),
  get: (i) => genresSource.get(i),
  itemKey: (x) => (x as Item).id,
  need: () => {},
  render: (el, genre) => renderGenreCell(el, genre as Genre | undefined),
  release: (el) => cancelThumb(el),
  onClick: (i) => {
    // Drill down: the albums view, limited to this genre.
    const genre = genresSource.get(i);
    if (genre) showGenre(genre.name);
  },
  ...collectionSize,
};

/**
 * The shows, and the seasons of one show.
 *
 * Two adapters over one source: the seasons are already in the series object,
 * so opening a show is a change of adapter and nothing else — no request, no
 * spinner, no second endpoint.
 */
const seriesAdapter: GridAdapter<Item | Album | Artist | Genre | Series | Season> = {
  count: () => seriesSource.count(),
  get: (i) => seriesSource.get(i),
  itemKey: (x) => (x as Series).id,
  need: () => {},
  render: (el, show) => renderSeriesCell(el, show as Series | undefined),
  release: (el) => cancelThumb(el),
  onClick: (i) => {
    const show = seriesSource.get(i);
    if (show) showSeries(show.name);
  },
  ...collectionSize,
};

const seasonAdapter: GridAdapter<Item | Album | Artist | Genre | Series | Season> = {
  count: () => seasonsOf().length,
  get: (i) => seasonsOf()[i],
  itemKey: (x) => `s${(x as Season).season}`,
  need: () => {},
  render: (el, season) => renderSeasonCell(el, season as Season | undefined),
  release: (el) => cancelThumb(el),
  onClick: (i, ev) => {
    const season = seasonsOf()[i];
    if (!season) return;
    // The badge plays the season from its first episode; anywhere else
    // opens its episodes.
    if (viaPlayBadge(ev)) void playSeason(season.season);
    else showSeason(season.season);
  },
  ...collectionSize,
};

/**
 * Play a season from its first episode without opening its listing: the
 * player is handed the season's episodes in order as a listing of its own,
 * which it steps through as it steps through any — here, or on a
 * television, which is handed each episode in turn with the soundtrack and
 * subtitle the viewer chose.
 */
async function playSeason(season: number): Promise<void> {
  const episodes = new LibrarySource();
  episodes.setQuery({ kind: 'video', q: '', sort: 'episode', desc: false, series: state.series, season });
  const src: ItemSource = { item: (i) => episodes.item(i), total: () => episodes.total };
  const first = await findKind(src, 0, 1, 'video');
  if (!first) {
    showToast('No episodes to play');
    return;
  }
  openVideo(first.item, playerOpts(src, first.index));
}

/** The seasons of the show that is open, or none. */
function seasonsOf(): Season[] {
  return (state.series && seriesSource.find(state.series)?.seasons) || [];
}

const grid = new VirtualGrid<Item | Album | Artist | Genre | Series | Season>(
  scroller,
  spacer,
  plane,
  itemAdapter,
);

// The bar leaves while a phone browses and returns on the first upward
// flick — the decision lives in barhide.ts, tested; this is only the wiring.
// The grid takes the freed height by itself: the scroller grows, its
// ResizeObserver fires, and the window re-lays.
if (compactChips) {
  let bar = { ...barShown, y: scroller.scrollTop };
  scroller.addEventListener(
    'scroll',
    () => {
      bar = barStep(bar, scroller.scrollTop);
      document.body.classList.toggle('bar-away', bar.hidden);
    },
    { passive: true },
  );
}

// ---- header / chips ----------------------------------------------------

const CHIPS: { mode: Mode; label: string; icon: string; count: (c: Counts) => number }[] = [
  { mode: 'all', label: 'All', icon: icons.grid, count: (c) => c.total },
  { mode: 'video', label: 'Videos', icon: icons.film, count: (c) => c.video },
  { mode: 'image', label: 'Images', icon: icons.image, count: (c) => c.image },
  { mode: 'audio', label: 'Music', icon: icons.music, count: (c) => c.audio },
  { mode: 'started', label: 'In progress', icon: icons.restart, count: (c) => c.started },
  { mode: 'watched', label: 'Watched', icon: icons.check, count: (c) => c.watched },
  { mode: 'popular', label: 'Popular', icon: icons.play, count: (c) => c.played },
  { mode: 'albums', label: 'Albums', icon: icons.disc, count: (c) => c.albums },
  { mode: 'audiobooks', label: 'Audiobooks', icon: icons.book, count: (c) => c.audiobooks },
  { mode: 'artists', label: 'Artists', icon: icons.artist, count: (c) => c.artists },
  { mode: 'genres', label: 'Genres', icon: icons.tag, count: (c) => c.genres },
  { mode: 'series', label: 'Series', icon: icons.series, count: (c) => c.series },
];

/** The chips this face offers, in order. */
function chips(): typeof CHIPS {
  return CHIPS.filter((c) => modeShown(c.mode, shownContent()));
}

let lastCounts: Counts | null = null;
/**
 * What the chips count while the listing is narrowed: the hits in each view
 * rather than the size of the library. The totals stay beside it, because
 * everything else on the page still wants to know them — an empty result in
 * a full library is "no matches", not "library is empty".
 *
 * Only the view on screen writes this. Every source refetches on a library
 * change, including the one behind whichever view is *not* being looked at,
 * and that one is still holding an older question — with no search in it,
 * as often as not. Letting it answer made the chips flick between the hits
 * and the whole library and back, once per change event.
 */
let lastMatching: Counts | null = null;
/**
 * What lastMatching is an answer to. Counts held over are only worth showing
 * while the question has not changed underneath them — countsToShow decides,
 * and this is what it compares against.
 */
let matchingFor = '';

/**
 * What is on screen, so a redraw that changes nothing touches nothing.
 *
 * The counts move constantly — a library being written to sends a change
 * event every few seconds, and every listing answer updates them too — and
 * rebuilding this bar's markup on each of those **destroyed the button under
 * the pointer**. A browser only dispatches a click when the press and the
 * release land on the same element, so a chip replaced in between swallowed
 * the click entirely: pressing Music did nothing at all, and did it
 * intermittently, which is the worst way for a control to fail. The flashing
 * was the same rebuild, seen.
 *
 * So the structure is rebuilt only when the structure changes — a different
 * set of chips, or a drill-down arriving or leaving — and everything that
 * moves with the numbers is written into the elements that are already
 * there. Same reasoning as the grid reconciling cells by identity rather
 * than repainting the screen.
 */
let chipsShape = '';

function renderChips(): void {
  // Nothing until the server has said what this face may show. The live
  // stream opens before that answer arrives and would otherwise draw the
  // whole set of chips for a moment, on a page that has three of them.
  if (!contentKnown()) return;
  const list = chips();
  const shape = [
    ...list.map((c) => c.mode),
    state.artist ? `artist:${state.artist}` : '',
    state.genre ? `genre:${state.genre}` : '',
    state.series ? `series:${state.series}` : '',
    state.season ? `season:${state.season}` : '',
    state.near ? `near:${state.near}` : '',
  ].join('|');

  if (shape !== chipsShape) {
    let html = list
      .map(
        (c) =>
          `<button class="chip" data-mode="${c.mode}">${c.icon}<span>${c.label}</span></button>`,
      )
      .join('');
    // Drilled into one performer or one genre: shown as a chip that clears
    // back to the list it was reached from, so there is always a way out.
    if (state.artist) {
      html += `<button class="chip active" data-clear-artist title="Back to all artists">
        ${icons.artist}<span>${esc(state.artist)}</span>${icons.close}
      </button>`;
      // And the way to the performers that sound like them.
      html += `<button class="chip" data-similar-artist title="Performers that sound like ${esc(state.artist)}">
        ${icons.radio}<span>Similar artists</span>
      </button>`;
    }
    if (state.near) {
      html += `<button class="chip active" data-clear-near title="Back to the list">
        ${icons.radio}<span>Like ${esc(state.nearName || state.near)}</span>${icons.close}
      </button>`;
    }
    if (state.genre) {
      html += `<button class="chip active" data-clear-genre title="Back to all genres">
        ${icons.tag}<span>${esc(state.genre)}</span>${icons.close}
      </button>`;
    }
    // Television drills twice, so it gets a chip per level: the show, and
    // the season within it. Each goes back one level rather than all the
    // way out, which is what a trail of them means.
    if (state.series) {
      html += `<button class="chip active" data-clear-series title="Back to all series">
        ${icons.series}<span>${esc(state.series)}</span>${icons.close}
      </button>`;
    }
    if (state.series && state.season) {
      html += `<button class="chip active" data-clear-season title="Back to the seasons">
        <span>Season ${state.season}</span>${icons.close}
      </button>`;
    }
    chipsNav.innerHTML = html;
    chipsShape = shape;
  }

  const shown = countsToShow({
    narrowed: narrowed(state),
    answers: matchingFor,
    asking: countKey(state),
    held: lastMatching,
    totals: lastCounts,
  });
  const drilled = state.artist !== '' || state.genre !== '' || state.series !== '' || state.near !== '';
  for (const c of list) {
    const el = chipsNav.querySelector<HTMLElement>(`.chip[data-mode="${c.mode}"]`);
    if (!el) continue;
    el.classList.toggle('active', state.mode === c.mode && !drilled);
    const n = shown ? c.count(shown) : null;
    const b = el.querySelector('b');
    if (n === null) {
      b?.remove();
      continue;
    }
    const text = chipCount(n, compactChips);
    if (!b) el.insertAdjacentHTML('beforeend', `<b>${text}</b>`);
    else if (b.textContent !== text) b.textContent = text;
  }
}

chipsNav.addEventListener('click', (ev) => {
  const chip = (ev.target as HTMLElement).closest<HTMLElement>('.chip');
  if (!chip) return;
  if (chip.hasAttribute('data-clear-artist')) {
    state.artist = '';
    setModeForce('artists');
    return;
  }
  if (chip.hasAttribute('data-similar-artist')) {
    showNear('artists', state.artist, state.artist);
    return;
  }
  if (chip.hasAttribute('data-clear-near')) {
    state.near = '';
    state.nearName = '';
    setModeForce(state.mode);
    return;
  }
  if (chip.hasAttribute('data-clear-genre')) {
    state.genre = '';
    setModeForce('genres');
    return;
  }
  // The season goes back to the seasons; the show goes back to the shows.
  if (chip.hasAttribute('data-clear-season')) {
    state.season = 0;
    setModeForce('series');
    return;
  }
  if (chip.hasAttribute('data-clear-series')) {
    state.series = '';
    state.season = 0;
    setModeForce('series');
    return;
  }
  const mode = chip.dataset.mode as Mode;
  if (state.artist || state.genre || state.series || state.near) {
    // Leaving the drill-down for any other view drops its filter. Forced,
    // because stepping out of one into the very chip it was reached from is
    // not a change of mode and would otherwise be ignored.
    leaveDrillDowns();
    setModeForce(mode);
    return;
  }
  setMode(mode);
});

/**
 * Take a view that the state now describes: the chips, the sort options and
 * the query all follow it. Every way into a view goes through here — a chip,
 * a drill-down, the brand, a link — so none of them can forget a step.
 *
 * The sort options are refilled because drilling in changes what can be
 * sorted on: a performer's page is a listing of releases, and it offers the
 * tag keys — year, artist, genre — that a listing of performers has no use
 * for. Leaving the select as it was left the artist keys on screen over a
 * list of albums, so an album could not be sorted by year once opened from
 * an artist.
 */
function enterView(mode: Mode): void {
  state.mode = mode;
  renderChips();
  renderSortOptions();
  syncQueueAll();
  applyQuery(true, true); // a view is somewhere to come back to
}

/**
 * Leave every drill-down: the performer, the genre, the show and its season,
 * and what sounds like one thing. One at a time is the rule — two would ask
 * a view for two narrowings at once, under a chip naming only one of them —
 * so every way into one starts by leaving the others, and a chip that leaves
 * them all leaves them here.
 */
function leaveDrillDowns(): void {
  state.artist = '';
  state.genre = '';
  state.series = '';
  state.season = 0;
  state.near = '';
  state.nearName = '';
}

/** Open one show: its seasons, which came with it. */
function showSeries(name: string): void {
  leaveDrillDowns();
  state.series = name;
  enterView('series');
}

/**
 * List what sounds like one thing: the releases like a release, or the
 * performers like a performer, nearest first. A drill-down like the others —
 * one at a time, a chip back out — over the analysis of how the music
 * sounds, which is the server's (similar.go).
 */
function showNear(kind: 'albums' | 'artists', id: string, name: string): void {
  leaveDrillDowns();
  state.near = id;
  state.nearName = name;
  // Most alike first, whichever way the list it was reached from ran: a
  // performer listing sorted A to Z is ascending, and carrying that in
  // would open on the least alike. The toggle then means what it says.
  state.desc = true;
  renderSortDir();
  enterView(kind);
}

/**
 * Open one season: the episodes themselves, in order.
 *
 * A listing rather than a sheet, deliberately: it is the item listing the
 * player steps through, so watching one episode and going on to the next is
 * the thing the player already does — and a sheet would have to invent it.
 */
function showSeason(season: number): void {
  state.season = season;
  enterView('series');
}

/** Drill into one genre: the releases filed under it, with a chip back. */
function showGenre(name: string): void {
  leaveDrillDowns();
  state.genre = name;
  enterView('albums');
}

/** Drill into one performer: their albums, with a chip to step back. */
function showArtist(name: string): void {
  leaveDrillDowns();
  state.artist = name;
  // Their releases open by year (see openingSort), and the key is set
  // outright rather than only when the old one does not fit: the name key
  // survives the change from the artists view and would carry over.
  state.sort = openingSort('albums', state);
  enterView('albums');
}

/** Refill the sort select for the current view, keeping the key if it fits. */
function renderSortOptions(): void {
  const opts = sortOptions(state.mode, state);
  // A key that does not survive the change of view falls back to the one
  // that view opens on, rather than being sent to a server that would
  // quietly ignore it.
  if (!opts.some(([v]) => v === state.sort)) state.sort = openingSort(state.mode, state);
  sortSelect.innerHTML = opts
    .map(([v, label]) => `<option value="${v}">${esc(label)}</option>`)
    .join('');
  sortSelect.value = state.sort;
}

function setMode(mode: Mode): void {
  if (state.mode === mode) return;
  // Arriving at the popularity listing, take its key and its direction:
  // most played first is the only reading of it anybody wants. Leaving it,
  // the key goes back to whatever the next view opens on rather than
  // carrying "most played" into a view that has no such column.
  if (mode === 'popular' || state.mode === 'popular') {
    state.sort = openingSort(mode, state);
    state.desc = true;
  }
  enterView(mode);
}

function queryState(): QueryState {
  // Only the file listings map to a media kind; the grouped views do not.
  // The two watch views are film listings with a filter on top: nothing but
  // a video keeps a position, so the kind comes with the filter.
  const watching = state.mode === 'started' || state.mode === 'watched';
  const kind =
    state.mode === 'video' || state.mode === 'image' || state.mode === 'audio'
      ? state.mode
      : watching
        ? 'video'
        : '';
  return {
    kind,
    watch: state.mode === 'started' ? 'started' : state.mode === 'watched' ? 'done' : '',
    // The popularity listing is everything that has been played at all,
    // most played first: sorting the whole library by plays would bury the
    // handful that matter under the untouched majority.
    played: state.mode === 'popular',
    series: state.series,
    season: state.season,
    q: state.q,
    sort: state.sort,
    desc: state.desc,
    artist: state.artist,
    genre: state.genre,
    near: state.near,
    audiobooks: state.mode === 'audiobooks',
  };
}

/** Push current UI state into the data sources and reset the grid. */
function applyQuery(reset: boolean, push = false): void {
  writeHash(push);
  switch (state.mode) {
    case 'albums':
    case 'audiobooks':
      // The audiobook shelf is the album view over the other releases.
      grid.setAdapter(albumAdapter);
      albumsSource.load(queryState());
      break;
    case 'artists':
      grid.setAdapter(artistAdapter);
      artistsSource.load(queryState());
      break;
    case 'genres':
      grid.setAdapter(genreAdapter);
      genresSource.load(queryState());
      break;
    case 'series':
      // Three views behind one chip: the shows, one show's seasons, and one
      // season's episodes. Which is on screen is what the drill-down state
      // says, and only the first of them fetches anything.
      if (state.series && state.season) {
        if (reset) grid.setAdapter(itemAdapter);
        libSource.setQuery(queryState());
      } else if (state.series) {
        grid.setAdapter(seasonAdapter);
        grid.refresh();
      } else {
        grid.setAdapter(seriesAdapter);
        seriesSource.load(queryState());
      }
      break;
    default:
      if (reset) grid.setAdapter(itemAdapter);
      libSource.setQuery(queryState());
  }
  // No reset of the grid beyond what setAdapter did: pointing it at the same
  // adapter rewinds to the top and keeps the cells, so a new query crossfades
  // into place instead of blanking the screen and filling it again.
  syncStatus();
}

// Search with debounce.
let searchTimer = 0;
searchInput.addEventListener('input', () => {
  searchClear.hidden = searchInput.value === '';
  window.clearTimeout(searchTimer);
  searchTimer = window.setTimeout(() => {
    if (searchInput.value.trim() === state.q) return;
    state.q = searchInput.value.trim();
    applyQuery(true);
  }, 250);
});
searchClear.addEventListener('click', () => {
  searchInput.value = '';
  searchClear.hidden = true;
  if (state.q !== '') {
    state.q = '';
    applyQuery(true);
  }
  searchInput.focus();
});
searchInput.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape') {
    searchInput.value = '';
    searchInput.blur();
    if (state.q !== '') {
      state.q = '';
      applyQuery(true);
    }
  }
});
document.addEventListener('keydown', (ev) => {
  if (ev.key === '/' && document.activeElement !== searchInput && !document.querySelector('.overlay')) {
    ev.preventDefault();
    searchInput.focus();
    searchInput.select();
  }
});

sortSelect.addEventListener('change', () => {
  state.sort = sortSelect.value;
  applyQuery(true);
});

function renderSortDir(): void {
  sortDirBtn.innerHTML = state.desc ? icons.arrowDown : icons.arrowUp;
  sortDirBtn.title = state.desc ? 'Descending' : 'Ascending';
}
/** Offer "queue all" only where the listing is music's — see queueSource. */
function syncQueueAll(): void {
  queueAllBtn.hidden = queueSource(state.mode, shownContent()) == null;
}

/**
 * Put everything the view lists after what is already queued: every release
 * on screen, every release of every performer, every release in every
 * genre, or every track. The grid holds tiles and the queue needs tracks,
 * so the server flattens the view into its tracks in one answer rather
 * than the page asking release by release; the bar says what it did.
 */
async function queueAll(): Promise<void> {
  const of = queueSource(state.mode, shownContent());
  if (!of) return;
  const q = queryState();
  queueAllBtn.disabled = true;
  try {
    const res = await tracksOf(of, {
      ...listFilters(q),
      artist: q.artist || undefined,
      genre: q.genre || undefined,
    });
    audioPlayer.enqueue(res.tracks, null);
    if (res.truncated) showToast('The queue is full; the rest were left out', 3000);
  } catch {
    showToast('Could not fetch the tracks');
  } finally {
    queueAllBtn.disabled = false;
  }
}
queueAllBtn.innerHTML = icons.queueAdd;
queueAllBtn.addEventListener('click', () => void queueAll());

sortDirBtn.addEventListener('click', () => {
  state.desc = !state.desc;
  renderSortDir();
  applyQuery(true);
});

$('#brand').addEventListener('click', (ev) => {
  ev.preventDefault();
  // Back to the whole library: every narrowing goes, or the drill-down chip
  // it left behind stands over a listing it no longer describes.
  state.q = '';
  leaveDrillDowns();
  searchInput.value = '';
  searchClear.hidden = true;
  setModeForce('all');
});

/**
 * Enter a view even when it is the one already on screen: stepping out of a
 * drill-down into the very chip it was reached from is not a change of mode
 * and setMode would ignore it.
 */
function setModeForce(mode: Mode): void {
  enterView(mode);
}

// ---- status / counts ---------------------------------------------------

/**
 * The empty state of a view that has a library behind it. Two views are
 * empty for a reason worth saying: the audiobook shelf and a "like this"
 * listing fill as the analysis reads the library, and early on "nothing
 * found" would read as a fault where it is a wait.
 */
function emptyViewHtml(): string {
  if (state.mode === 'audiobooks' && !state.q) {
    return `<div class="empty">${icons.book}<h3>No audiobooks yet</h3><p>They appear here as the analysis reads the library; a genre tag that says audiobook shelves a release at once.</p></div>`;
  }
  if (state.near) {
    return `<div class="empty">${icons.radio}<h3>Nothing sounds like this yet</h3><p>The analysis has not read enough of the library to say.</p></div>`;
  }
  return `<div class="empty">${icons.search}<h3>No matches</h3><p>Nothing found${state.q ? ` for “${esc(state.q)}”` : ''} in ${searchedWhere()}.</p></div>`;
}

/** Where the empty answer was looked for, as prose rather than a mode key. */
function searchedWhere(): string {
  switch (state.mode) {
    case 'all':
      return 'the library';
    case 'video':
      return 'videos';
    case 'image':
      return 'images';
    case 'audio':
      return 'music';
    case 'started':
      return 'videos in progress';
    case 'watched':
      return 'watched videos';
    case 'popular':
      return 'played items';
    default:
      return state.mode; // albums, artists, genres, series read as they are
  }
}

/** How many things the view on screen holds, and what to call them. */
function viewCount(): { total: number; noun: string } {
  switch (state.mode) {
    case 'albums':
      return { total: albumsSource.count(), noun: 'albums' };
    case 'audiobooks':
      return { total: albumsSource.count(), noun: 'audiobooks' };
    case 'artists':
      return { total: artistsSource.count(), noun: 'artists' };
    case 'genres':
      return { total: genresSource.count(), noun: 'genres' };
    case 'series':
      // Three views behind one chip: the shows, one show's seasons, and the
      // episodes of one season, which are an item listing.
      if (!state.series) return { total: seriesSource.count(), noun: 'series' };
      if (!state.season) return { total: seasonsOf().length, noun: 'seasons' };
      return { total: libSource.total, noun: 'items' };
    default:
      return { total: libSource.total, noun: 'items' };
  }
}

function syncStatus(): void {
  const { total, noun } = viewCount();
  if (total < 0) {
    countEl.textContent = '';
    statusEl.hidden = true;
    return;
  }
  countEl.textContent = `${total.toLocaleString()} ${noun}`;
  if (total === 0) {
    const empty = !lastCounts || lastCounts.total === 0;
    statusEl.innerHTML = empty
      ? `<div class="empty">${icons.folder}<h3>Library is empty</h3><p>Files are indexed as soon as they appear in the watched folders.</p></div>`
      : emptyViewHtml();
    statusEl.hidden = false;
  } else {
    statusEl.hidden = true;
  }
}

/**
 * The grouped source on screen, if the view is a grouped one: the releases
 * (and the audiobook shelf, which is the same source over other releases),
 * the performers, the genres, the shows — and the seasons, which come with
 * the shows. Null for the item listings. Named once: the refetch on a
 * change and the counts on an answer each used to list the modes, and one
 * list had fallen behind the other.
 */
function collectionOnScreen(): { load(q: QueryState): void } | null {
  switch (state.mode) {
    case 'albums':
    case 'audiobooks':
      return albumsSource;
    case 'artists':
      return artistsSource;
    case 'genres':
      return genresSource;
    case 'series':
      return state.season ? null : seriesSource;
    default:
      return null;
  }
}

libSource.onUpdate = () => {
  lastCounts = libSource.counts;
  if (!collectionOnScreen()) {
    lastMatching = libSource.matching;
    matchingFor = countKey(state);
    grid.refresh();
  }
  renderChips();
  syncStatus();
};
libSource.onError = () => showToast('Failed to load library', 3000);
/**
 * Wire a grouped view's source to the screen. These views fetch no items,
 * so their own answer is where the chips learn what is in front of the
 * viewer — the search, and the performer or genre they drilled into — but
 * only while that view is the one on screen (see lastMatching).
 */
function bindCollection(
  source: { onUpdate: () => void; onError: (err: Error) => void; matching: Counts | null },
  onScreen: () => boolean,
): void {
  // A failure is said, not drawn as an empty answer: "no matches" would be
  // a lie about a library that simply did not answer.
  source.onError = () => {
    if (onScreen()) showToast('Could not load this view', 3000);
  };
  source.onUpdate = () => {
    if (onScreen()) {
      lastMatching = source.matching;
      matchingFor = countKey(state);
      grid.refresh();
    }
    renderChips();
    syncStatus();
  };
}
bindCollection(albumsSource, () => state.mode === 'albums' || state.mode === 'audiobooks');
bindCollection(artistsSource, () => state.mode === 'artists');
bindCollection(genresSource, () => state.mode === 'genres');
bindCollection(seriesSource, () => state.mode === 'series' && !state.season);

// Only the relayout: the bar appearing or disappearing changes the grid's
// height. The album sheet watches the player itself, and must not drag a
// full window re-render along with every pause tap.
audioPlayer.onTrackChange = () => {
  requestAnimationFrame(() => grid.layout());
};

/**
 * What to call this view where something asks for a title — the system share
 * sheet on a phone. The narrowing names it if there is one, since that is
 * what somebody sharing a view means to hand over; failing that the search
 * does; failing both it is the library itself.
 */
function shareLabel(): string {
  if (state.series) return state.season ? `${state.series} season ${state.season}` : state.series;
  if (state.artist) return state.artist;
  if (state.genre) return state.genre;
  if (state.q) return `Search: ${state.q}`;
  return 'Mediator';
}

// ---- hash routing ------------------------------------------------------

/**
 * Put the state in the address. A change of view is pushed, so Back returns
 * from a drill-down and a phone's back gesture does not leave the app; a
 * keystroke of a search or a change of sort replaces, or every letter typed
 * would be a page to go back through. Nothing is pushed when the address
 * already says this, and the hashchange handler re-applies without pushing,
 * so Back does not push what it is returning to.
 */
function writeHash(push = false): void {
  const p = new URLSearchParams();
  if (state.mode !== 'all') p.set('m', state.mode);
  if (state.artist) p.set('ar', state.artist);
  if (state.genre) p.set('g', state.genre);
  if (state.series) p.set('tv', state.series);
  if (state.season) p.set('se', String(state.season));
  if (state.near) p.set('n', state.near);
  if (state.nearName) p.set('nn', state.nearName);
  if (state.q) p.set('q', state.q);
  if (state.sort !== 'mtime') p.set('s', state.sort);
  if (!state.desc) p.set('o', 'asc');
  const h = p.toString();
  const url = h ? `#${h}` : location.pathname;
  if (push && location.hash !== (h ? `#${h}` : '')) history.pushState(null, '', url);
  else history.replaceState(null, '', url);
}

function readHash(): void {
  const p = new URLSearchParams(location.hash.slice(1));
  const m = p.get('m');
  // A view is one the chips know; anything else is the face's own default.
  const asked = CHIPS.some((c) => c.mode === m) ? (m as Mode) : defaultMode(shownContent());
  // A link can name a view this face does not offer — someone's bookmark of
  // the albums, opened on the one that shows only films. It opens the
  // listing rather than an empty screen with no way back.
  state.mode = fallbackMode(asked, shownContent());
  state.artist = p.get('ar') ?? '';
  state.genre = p.get('g') ?? '';
  state.series = p.get('tv') ?? '';
  state.season = Number(p.get('se') ?? '') || 0;
  state.near = p.get('n') ?? '';
  state.nearName = p.get('nn') ?? '';
  state.q = p.get('q') ?? '';
  // What a shortlink wants opened on top of all that. Deliberately absent
  // from writeHash: see AppState.
  state.item = p.get('i') ?? '';
  state.album = p.get('al') ?? '';
  // The key is checked against what this view actually offers rather than
  // against a fixed list: the views no longer sort by the same things, and a
  // key from one of them means nothing in another. renderSortOptions falls
  // back for anything that does not fit.
  const s = p.get('s') ?? '';
  state.sort = sortOptions(state.mode, state).some(([v]) => v === s) ? s : openingSort(state.mode, state);
  state.desc = p.get('o') !== 'asc';
}

// ---- live updates ------------------------------------------------------

let refreshTimer = 0;
let lastGroupReload = 0;
/**
 * A viewer is over the grid, so the grid is not worth refetching.
 *
 * A library that is being written to — a scan running, a download landing —
 * sends a change event every few seconds, and each one had the listing
 * fetched again behind an open film: two hundred items, over and over, for
 * a screen nobody can see, competing with the playback that is the one
 * thing that matters at that moment. The change is remembered instead and
 * the listing is caught up when the viewer closes.
 */
function viewerOpen(): boolean {
  return document.body.classList.contains('viewing');
}

let missedChange = false;

new MutationObserver(() => {
  if (viewerOpen() || !missedChange) return;
  missedChange = false;
  refreshLibrary();
}).observe(document.body, { attributeFilter: ['class'] });

/**
 * Fetch the grouped view on screen again, if one is. Every grouped view,
 * whichever route asks: the genres and the shows used to be missing from
 * one of the two, so sitting on either during a scan showed a listing that
 * had quietly stopped. An open album panel is a snapshot, so it is refetched
 * as well, and tags and durations appear as background enrichment fills
 * them in.
 */
function reloadGroupedView(): void {
  lastGroupReload = Date.now();
  collectionOnScreen()?.load(queryState());
  reloadAlbumPanel();
}

/** Take the library's changes into the views that are on screen. */
function refreshLibrary(): void {
  libSource.invalidate();
  retryThumbs();
  reloadGroupedView();
}

subscribeEvents(
  (version, counts) => {
    lastCounts = counts;
    renderChips();
    if (version === libSource.version) return;
    if (viewerOpen()) {
      // Caught up when it closes; see viewerOpen.
      missedChange = true;
      return;
    }
    // Debounce bursts; refresh in place (no scroll jump).
    window.clearTimeout(refreshTimer);
    refreshTimer = window.setTimeout(() => {
      libSource.invalidate();
      // Thumbnails that failed earlier may exist now that the library
      // changed (a file finished arriving, generation completed).
      retryThumbs();
      // The album/artist lists are heavyweight full fetches; during long
      // bursts (scans, enrichment) refresh them at a gentler pace than the
      // per-page listing. Unchanged versions come back as a free 304.
      if (Date.now() - lastGroupReload > 2000) reloadGroupedView();
    }, 300);
  },
  (connected) => connEl.classList.toggle('offline', !connected),
);

// ---- boot --------------------------------------------------------------

// The first listing waits on /api/info, because the answer goes into every
// thumbnail URL. Asking a moment too early would build the URLs the browser
// already has cached — immutable, for a year — which is exactly the staleness
// the epoch exists to break. It is one small request against a server that is
// about to serve a page of images.
void (async () => {
  try {
    await loadInfo();
  } catch {
    // No epoch: thumbnails still load, they are just versioned by mtime
    // alone, as they were before. Not a reason to refuse to start.
  }

  // The preferences are for a caller who can see the whole library. One
  // confined to part of it is refused by the server; leaving the button out
  // is the courtesy that matches.
  prefsBtn.hidden = serverAbout()?.confined === true;

  applyHash(false);

  getPositions()
    .then((res) => {
      for (const [id, p] of Object.entries(res.positions)) positions.set(id, p);
      grid.refresh();
    })
    .catch(() => {});
})();

/**
 * Take the address as the state and draw it: at boot, and again whenever
 * the address changes under the page. reset says whether the grid is to be
 * torn down for it, which a first draw has no need of.
 */
function applyHash(reset: boolean): void {
  readHash();
  searchInput.value = state.q;
  searchClear.hidden = state.q === '';
  renderSortOptions();
  renderSortDir();
  syncQueueAll();
  renderChips();
  applyQuery(reset);
  void openLinked();
}

window.addEventListener('hashchange', () => applyHash(true));
