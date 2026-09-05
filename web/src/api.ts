/**
 * Client for the backend JSON API.
 * The data model is generated from the Go types — see types.gen.ts.
 */
import { nativeHLS } from './playback';
import type {
  AlbumDetailResponse,
  TracksResponse,
  LikeUpdate,
  LikeResponse,
  ConvertProgress,
  LinkRequest,
  LinkResponse,
  PrefsResponse,
  PrefsUpdate,
  AlbumsResponse,
  ArtistsResponse,
  GenresResponse,
  SeriesResponse,
  Counts,
  Flags,
  FlagsResponse,
  FlagUpdate,
  Kind,
  ListQueryParams,
  Item,
  PositionsResponse,
  PositionUpdate,
  Result,
  CropResponse,
  InfoResponse,
  KeyframeResponse,
  SubtitlesResponse,
} from './types.gen';
import { SPRITE } from './types.gen';

import type { QueueSource } from './content';

export * from './types.gen';

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json() as Promise<T>;
}

/**
 * Send a JSON body and read one back. A refusal carries the server's own
 * reason where it gave one — the difference between reporting a typo and
 * appearing to do nothing — and the status otherwise.
 */
async function sendJSON<T>(method: 'PUT' | 'POST', url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error((await res.text()).trim() || `${res.status} ${res.statusText}`);
  return res.json() as Promise<T>;
}

function putJSON<T>(url: string, body: unknown): Promise<T> {
  return sendJSON<T>('PUT', url, body);
}

/** What selects and orders a listing — the same words the m3u export takes. */
export type ListFilters = Pick<
  ListQueryParams,
  | 'kind'
  | 'watch'
  | 'played'
  | 'series'
  | 'season'
  | 'q'
  | 'sort'
  | 'order'
  | 'seed'
  | 'hidden'
  | 'fav'
>;

function listParams(q: ListFilters): URLSearchParams {
  const p = new URLSearchParams();
  if (q.kind) p.set('kind', q.kind);
  if (q.watch) p.set('watch', q.watch);
  if (q.played) p.set('played', q.played);
  if (q.series) p.set('series', q.series);
  if (q.season) p.set('season', q.season);
  if (q.q) p.set('q', q.q);
  if (q.sort) p.set('sort', q.sort);
  p.set('order', q.order ?? 'desc');
  // The shuffle seed must travel with every page of a random sort: without
  // it the server deals a new hand per page, repeating and skipping items.
  if (q.seed) p.set('seed', String(q.seed));
  if (q.hidden) p.set('hidden', q.hidden);
  if (q.fav) p.set('fav', q.fav);
  return p;
}

export function listMedia(q: ListQueryParams): Promise<Result> {
  const p = listParams(q);
  p.set('offset', String(q.offset ?? 0));
  p.set('limit', String(q.limit ?? 200));
  return getJSON<Result>(`/api/library?${p}`);
}

/**
 * Download link for the current selection as an m3u playlist (capped
 * server-side). Not a fetch: its entries are absolute stream URLs, meant for
 * a player outside the browser.
 */
export function playlistUrl(q: ListFilters): string {
  return `/api/playlist.m3u?${listParams(q)}`;
}

/**
 * What a grouped view is asked: search, sort, direction, the drill-downs —
 * and `near`, which lists what sounds like one thing (an album id, or a
 * performer's name) nearest first, and `audiobooks`, the shelf of readings.
 */
export type GroupedQuery = Pick<ListQueryParams, 'q' | 'sort' | 'order' | 'artist' | 'genre'> & {
  near?: string;
  audiobooks?: boolean;
};

/** The query string every grouped view takes. */
function groupedQuery(q: GroupedQuery): URLSearchParams {
  const p = new URLSearchParams();
  if (q.q) p.set('q', q.q);
  if (q.artist) p.set('artist', q.artist);
  if (q.genre) p.set('genre', q.genre);
  if (q.near) p.set('near', q.near);
  if (q.audiobooks) p.set('audiobooks', '1');
  if (q.sort) p.set('sort', q.sort);
  if (q.order) p.set('order', q.order);
  return p;
}

export function listAlbums(q: GroupedQuery): Promise<AlbumsResponse> {
  return getJSON<AlbumsResponse>(`/api/albums?${groupedQuery(q)}`);
}

export function listArtists(q: GroupedQuery): Promise<ArtistsResponse> {
  return getJSON<ArtistsResponse>(`/api/artists?${groupedQuery(q)}`);
}

export function listSeries(
  q: Pick<ListQueryParams, 'q' | 'sort' | 'order'>,
): Promise<SeriesResponse> {
  return getJSON<SeriesResponse>(`/api/series?${groupedQuery(q)}`);
}

export function listGenres(
  q: Pick<ListQueryParams, 'q' | 'sort' | 'order'>,
): Promise<GenresResponse> {
  return getJSON<GenresResponse>(`/api/genres?${groupedQuery(q)}`);
}

/**
 * The tracks behind a view, in the order a queue plays them — every release
 * listed, every release of every performer listed, and so on. One answer
 * for the whole view, since the grid holds tiles and the queue needs
 * tracks; `truncated` says the view holds more than a queue takes.
 */
export function tracksOf(
  of: QueueSource | 'similar',
  q: ListFilters & GroupedQuery & { id?: string; n?: number },
): Promise<TracksResponse> {
  let p: URLSearchParams;
  if (of === 'items') {
    p = listParams(q);
  } else if (of === 'similar') {
    // The tracks that sound like one: a radio's worth, for the queue.
    p = new URLSearchParams();
    if (q.id) p.set('id', q.id);
    if (q.n) p.set('n', String(q.n));
  } else {
    p = groupedQuery(of === 'audiobooks' ? { ...q, audiobooks: true } : q);
  }
  p.set('of', of === 'audiobooks' ? 'albums' : of);
  return getJSON<TracksResponse>(`/api/tracks?${p}`);
}

/** The directories the server is indexing. */
export function getPrefs(): Promise<PrefsResponse> {
  return getJSON<PrefsResponse>('/api/prefs');
}

/**
 * Replace the directories to index. The answer is the set that ended up in
 * force, which is not always the set that was asked for — a directory inside
 * another one on the list is dropped, since the enclosing one already covers
 * it. A refusal carries the server's reason, which is worth showing: it is
 * the difference between reporting a typo and appearing to do nothing.
 */
export function setPrefs(roots: string[]): Promise<PrefsResponse> {
  return putJSON<PrefsResponse>('/api/prefs', { roots } satisfies PrefsUpdate);
}

/**
 * Everything a release is, as one download: the audio, the sleeve, the nfo —
 * a release is a directory, not a track list. A playlist has no directory of
 * its own, so for one of those it is the entries it names.
 */
export function albumZipUrl(id: string): string {
  return `/api/albums/${encodeURIComponent(id)}/zip`;
}

export function getAlbum(id: string): Promise<AlbumDetailResponse> {
  return getJSON<AlbumDetailResponse>(`/api/albums/${encodeURIComponent(id)}`);
}

export function getPositions(): Promise<PositionsResponse> {
  return getJSON<PositionsResponse>('/api/state');
}

export function savePosition(id: string, t: number, d: number): void {
  const body: PositionUpdate = { t, d };
  // keepalive lets the final save survive page unload / overlay close.
  void fetch(`/api/state/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    keepalive: true,
  }).catch(() => {});
}

/**
 * How long something has to have actually played before it counts as played.
 *
 * The same five seconds that decide whether a film counts as started, and
 * for the same reason: a file opened and shut, or stepped over on the way to
 * the next one, is not a play. It is measured in playing time rather than
 * wall clock, so a paused track never creeps over the line.
 */
export const PLAY_AFTER = 5;

/**
 * Count one play. Fire-and-forget: nothing on screen waits for it, and a
 * lost count is worth less than a stall.
 */
export function recordPlay(id: string): void {
  void fetch(`/api/plays/${encodeURIComponent(id)}`, { method: 'POST' }).catch(() => {});
}

/**
 * Record the owner's verdict on an item: 1, -1, or 0 to withdraw it. Answers
 * with what is now stored, which is what the button lights.
 */
export function setLike(id: string, like: number): Promise<LikeResponse> {
  const body: LikeUpdate = { like };
  return sendJSON<LikeResponse>('POST', `/api/like/${encodeURIComponent(id)}`, body);
}

export function clearPosition(id: string): void {
  void fetch(`/api/state/${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => {});
}

/** One judgement about an item; a field left out keeps the value it has. */
export type FlagChange = Pick<FlagUpdate, 'hidden' | 'favourite' | 'rotation' | 'nocrop'>;

/**
 * Record what the owner thinks of one item. The response holds the flags it
 * now has — hiding something does not clear whether it is a favourite.
 */
export async function setFlags(id: string, flags: FlagChange): Promise<Record<string, Flags>> {
  const res = await putJSON<FlagsResponse>(`/api/flags/${encodeURIComponent(id)}`, flags);
  return res.flags;
}

/**
 * The same judgement over a whole selection: one request, one write and one
 * change event, however many items were picked. Ids the library does not
 * know are skipped, so the response says which items were actually judged.
 */
export async function setFlagsBatch(
  ids: string[],
  flags: FlagChange,
): Promise<Record<string, Flags>> {
  const body: FlagUpdate = { ids, ...flags };
  const res = await putJSON<FlagsResponse>('/api/flags', body);
  return res.flags;
}

export function streamUrl(id: string, download = false): string {
  return signed(`/api/stream/${encodeURIComponent(id)}`) + (download ? '?dl=1' : '');
}

/** The credential the media URLs carry. See sign.go and loadInfo. */
let streamToken = '';

/**
 * Put the token into a media URL.
 *
 * Signed by default, so that a URL handed to something without a browser
 * behind it — an AirPlay receiver above all, which fetches the media itself
 * and has no credentials to offer — is not refused by whatever guards this
 * deployment. Everything the receiver needs goes through here: the file, the
 * conversion that makes it playable, the segments of that conversion (which
 * a playlist names relative to itself, so they follow it under the same
 * token) and the subtitles beside it. Without a token the plain path is
 * returned and nothing is worse off than it was.
 */
function signed(path: string): string {
  if (!streamToken) return path;
  return `/api/signed/${encodeURIComponent(streamToken)}${path.slice('/api'.length)}`;
}

/**
 * Thumbnail URL. v should be the source's mtime: thumbnails are served
 * immutable, so without a version in the URL the browser would keep the
 * thumbnail of a file's FIRST readable state forever — for a torrent that
 * finished downloading after the grid saw it, that is a black frame or
 * nothing at all.
 */
export function thumbUrl(id: string, w = 360, v = 0): string {
  const e = epochParam();
  return `/api/thumb/${encodeURIComponent(id)}?w=${w}${v ? `&v=${v}` : ''}${e ? `&${e}` : ''}`;
}

/**
 * Width of a preview still: big enough to fill an overlay, small enough to
 * arrive long before the media itself would.
 */
export const PREVIEW_W = 1600;

/**
 * A large still of the item — the same thumbnail endpoint, which caches per
 * width, so a preview neither evicts nor is evicted by the grid tile. v is
 * the source's mtime, for the reason thumbUrl needs it.
 */
export function previewUrl(id: string, v = 0): string {
  return thumbUrl(id, PREVIEW_W, v);
}

/**
 * Scrub sheet for a video: SPRITE.frames frames tiled SPRITE.cols by
 * SPRITE.rows, frame i sampled at duration * (i + 0.5) / SPRITE.frames — so
 * everything needed to place and time a frame comes from the item's
 * duration. The server answers 404 when there is no sheet to make (shorter
 * than SPRITE.minDurationMs, unknown duration, no ffmpeg); that means "no
 * scrubbing here", not an error worth showing. v is the source's mtime, as
 * for thumbnails.
 *
 * `hover` says the request is a preview under the pointer rather than a
 * player asking for what it needs: the server makes a sheet for a hover
 * only while nothing is streaming, a hover being the most speculative work
 * in the app and the disk being what playback needs.
 */
export function spriteUrl(id: string, v = 0, hover = false): string {
  const parts = [v ? `v=${v}` : '', hover ? 'hover=1' : '', epochParam()]
    .filter(Boolean)
    .join('&');
  return `/api/sprite/${encodeURIComponent(id)}${parts ? `?${parts}` : ''}`;
}

/**
 * Which store the images come from.
 *
 * Thumbnails and sprites are served immutable for a year, and `v=<mtime>`
 * only changes when the source file does — so a server whose database has
 * been deleted regenerates everything while the browser goes on showing
 * images from the store that is gone. Hanging the store's identity on the URL
 * ties the two together: a new database means new URLs and a refetch, the
 * same database means the cache still stands, which is what keeps thumbnail
 * loads off the connections playback needs.
 */
let thumbEpoch = '';

function epochParam(): string {
  return thumbEpoch ? `e=${encodeURIComponent(thumbEpoch)}` : '';
}

/**
 * Read what the server says about itself. Worth awaiting before the first
 * thumbnail is asked for: a URL built without the epoch is the old URL, and
 * the browser would answer it from the very cache this exists to bypass.
 */
export async function loadInfo(): Promise<void> {
  try {
    const info = await getJSON<InfoResponse>('/api/info');
    thumbEpoch = info.thumbEpoch ?? '';
    content = info.content ?? null;
    streamToken = info.streamToken ?? '';
    serverInfo = info;
  } finally {
    // Known either way. Without an answer the page offers everything, which
    // is what it did before there was anything to ask — and the server
    // refuses what this caller may not have regardless.
    known = true;
  }
}

/**
 * The classes of media this server is showing us, or null for all of them.
 *
 * A proxy in front can restrict a face to music, videos or images
 * (`X-Media-Content`), and the server enforces that on every answer. This is
 * only how the page knows not to offer views it would get nothing from — the
 * page cannot set the header itself, since an <img> or a <video> sends what
 * it likes.
 */
let content: string[] | null = null;
let known = false;

export function shownContent(): string[] | null {
  return content;
}

/** The whole of what /api/info said, for the box that reports it. */
let serverInfo: InfoResponse | null = null;

/**
 * What is running here and what it can do. Null until the first answer,
 * which every page waits for before drawing anything anyway.
 */
export function serverAbout(): InfoResponse | null {
  return serverInfo;
}

/**
 * Whether the server has said yet. Until it has, a restricted face looks
 * exactly like an unrestricted one, so anything drawn from `shownContent`
 * would be drawn wrong and redrawn a moment later — which is what the chips
 * did: all six of them, then the three this face has.
 */
export function contentKnown(): boolean {
  return known;
}

/**
 * Which frame of the sheet covers time t (seconds), and where it sits in the
 * tiled image as background-position percentages. Percentages rather than
 * pixels because the frame height follows the video's aspect ratio, which
 * this side never has to know.
 */
export function spriteFrame(
  t: number,
  durationMs: number,
): { index: number; xPct: number; yPct: number } {
  const per = durationMs / 1000 / SPRITE.frames;
  const index = per > 0 ? Math.min(Math.max(Math.floor(t / per), 0), SPRITE.frames - 1) : 0;
  const col = index % SPRITE.cols;
  const row = Math.floor(index / SPRITE.cols);
  return {
    index,
    xPct: SPRITE.cols > 1 ? (col / (SPRITE.cols - 1)) * 100 : 0,
    yPct: SPRITE.rows > 1 ? (row / (SPRITE.rows - 1)) * 100 : 0,
  };
}

/**
 * Server-side conversion of a video the browser cannot play, from t seconds.
 * mode 'audio' keeps the original video stream and only re-encodes the
 * soundtrack — much cheaper, and enough when only the audio codec is the
 * problem.
 */
export function transcodeUrl(id: string, t = 0, mode: 'full' | 'audio' = 'full', audio = 0): string {
  const m = mode === 'audio' ? '&mode=audio' : '';
  return signed(`/api/transcode/${encodeURIComponent(id)}`) + `?t=${t.toFixed(2)}${m}${audioParam(audio)}`;
}

/** Which soundtrack, when the film carries more than one. */
function audioParam(track: number): string {
  return track > 0 ? `&a=${track}` : '';
}

/**
 * The same item with its streams moved into an MP4, served as an ordinary
 * file: ranges, a length and real seeking. That is what a container the
 * browser will not open needs when the streams inside it are ones it decodes
 * perfectly well — and it is the only shape iOS will play, since it refuses a
 * media URL that cannot answer a range request.
 *
 * audio is which soundtrack the copy keeps, as with the conversions — the
 * server has carried `?a=` all along (the cast path uses it), and a rewrap
 * of a dual-language release that always kept the first track was the one
 * route the choice could not reach.
 *
 * mode 'audio' asks for the other copy the endpoint makes: the picture
 * through untouched with the soundtrack converted beside it, for a film whose
 * only fault is a soundtrack the browser has no decoder for. That is the case
 * the live conversion serves worst — it cannot answer a range request, so a
 * browser managing its own buffer reconnects and is answered from the
 * beginning every time — and the file is what puts an end to it.
 *
 * mode 'track' asks for the third: both streams copied through with the
 * container left as it was, holding only the chosen soundtrack. For a file
 * the browser already opens that is all a change of language needs — nothing
 * is moved and nothing re-encoded — which is what a video shipping automatic
 * dubs wants, its languages being separate tracks in one Matroska file.
 *
 * 404 when copying would not help, which the player reads as "convert".
 */
export function remuxUrl(id: string, audio = 0, mode?: 'audio' | 'track'): string {
  const q = [audioParam(audio), mode ? `&mode=${mode}` : ''].join('');
  return signed(`/api/remux/${encodeURIComponent(id)}`) + (q ? `?${q.slice(1)}` : '');
}

/**
 * The same conversion as `transcodeUrl`, delivered as HLS: short segments and
 * a playlist that grows as they appear, instead of one response that never
 * ends. Safari opens a media URL with a byte-range request and will not play
 * a resource that cannot answer one, which a conversion of unknown length
 * never can — so on that browser this is the only route that plays at all,
 * and it starts after the first segment rather than after the last.
 */
export function hlsUrl(
  id: string,
  t = 0,
  mode: 'full' | 'audio' = 'full',
  audio = 0,
  sub = -1,
): string {
  const m = mode === 'audio' ? '&mode=audio' : '';
  // Which subtitle rendition the master marks DEFAULT — the stream is what
  // draws subtitles on this path, inline, fullscreen and on an AirPlay
  // receiver alike, so the choice has to be in the playlist rather than on
  // a <track> element the receiver never sees.
  const c = sub >= 0 ? `&sub=${sub}` : '';
  return (
    signed(`/api/hls/${encodeURIComponent(id)}/index.m3u8`) +
    `?t=${t.toFixed(2)}${m}${audioParam(audio)}${c}`
  );
}

/** Whether this browser plays HLS by itself — see playback.ts. */
export function playsHLS(): boolean {
  return nativeHLS(navigator.userAgent, (t) =>
    document.createElement('video').canPlayType(t),
  );
}

/**
 * How far a conversion of this item has reached. Asked while the player is
 * waiting on one, so the wait can say what it is doing instead of spinning.
 */
export function convertProgress(id: string): Promise<ConvertProgress> {
  return getJSON<ConvertProgress>(`/api/convert/${encodeURIComponent(id)}`);
}

/**
 * One item, with its metadata read first if that has not happened yet.
 * Opening something is a strong hint that it should jump the enrichment
 * queue, and the player needs the codecs to know whether the browser can
 * play the file at all.
 */
export function getItem(id: string): Promise<Item> {
  return getJSON<Item>(`/api/item/${encodeURIComponent(id)}`);
}

export function listSubs(id: string): Promise<SubtitlesResponse> {
  return getJSON<SubtitlesResponse>(`/api/subs/${encodeURIComponent(id)}`);
}

/**
 * One subtitle track as WebVTT. shift rebases the cues onto a transcoded
 * stream, whose clock starts at the keyframe it was opened at rather than at
 * the start of the film — without it the browser matches cue times against
 * the wrong origin and shows nothing worth reading.
 */
export function subUrl(id: string, index: number, shift = 0): string {
  const s = shift > 0 ? `?shift=${shift.toFixed(3)}` : '';
  return signed(`/api/subs/${encodeURIComponent(id)}/${index}`) + s;
}

/**
 * Where a transcode that copies the video stream would really begin if asked
 * to seek to t: the keyframe at or before it, which can be ten seconds
 * earlier on a 4K release. The player needs this to keep its timeline, its
 * saved position and its subtitles pointing at the same moment.
 */
export function keyframeStart(id: string, t: number): Promise<KeyframeResponse> {
  return getJSON<KeyframeResponse>(`/api/keyframe/${encodeURIComponent(id)}?t=${t.toFixed(3)}`);
}

/** Subscribe to library change events; auto-reconnects via EventSource. */
export function subscribeEvents(
  onChange: (version: number, counts: Counts) => void,
  onStatus?: (connected: boolean) => void,
): () => void {
  const es = new EventSource('/api/events');
  es.addEventListener('change', (ev) => {
    try {
      const data = JSON.parse((ev as MessageEvent).data) as { version: number; counts: Counts };
      onChange(data.version, data.counts);
    } catch {
      /* ignore malformed events */
    }
  });
  es.onopen = () => onStatus?.(true);
  es.onerror = () => onStatus?.(false);
  return () => es.close();
}

export type { Kind };

/**
 * Where the picture actually is inside this file's frame, when the file
 * carries black borders of its own. A zero box means there is nothing to
 * trim, which is the ordinary case; the answer costs the server a few
 * seconds of ffmpeg the first time and nothing ever after.
 */
export function getCrop(id: string): Promise<CropResponse> {
  return getJSON<CropResponse>(`/api/crop/${encodeURIComponent(id)}`);
}

/**
 * Ask for a short name for a piece of app state — a performer, a genre, a
 * programme, a search, or one particular film, photograph or track.
 *
 * The target is the URL fragment the app already writes for that view, so
 * nothing here has to describe a view to the server; asking twice for the
 * same place gives the same link back rather than a second name for it.
 */
export function createLink(target: string): Promise<LinkResponse> {
  return sendJSON<LinkResponse>('POST', '/api/links', { target } satisfies LinkRequest);
}
