/**
 * Thumbnail loader: small concurrency cap, visible-first, abortable.
 *
 * A browser opens only ~6 connections per origin, and /api/events holds one
 * permanently. On a cold start every mounted cell wants a thumbnail at once,
 * and video thumbnails can take the server seconds each (ffmpeg), so naive
 * <img src> loads pin the whole connection pool — even starting a video then
 * stalls behind them. This loader:
 *
 *  - keeps at most MAX_ACTIVE thumbnail fetches in flight, leaving
 *    connections free for media streaming, paging and state writes;
 *  - picks the newest request whose cell is actually inside the viewport
 *    first, so on-screen thumbnails beat overscan rows;
 *  - aborts loads whose cell was recycled or scrolled away, which also
 *    cancels the server-side work (queued generation drops out, running
 *    ffmpeg is killed), keeping the server focused on visible thumbnails.
 *
 * Thumbnail responses are immutable, so after the fetch lands in the HTTP
 * cache the img.src assignment is served from cache without a new request.
 */

const MAX_ACTIVE = 3;

/**
 * A fetch older than this no longer counts against the concurrency cap.
 * Generating one video thumbnail can take many seconds (ffmpeg, serialized
 * server-side), and with a hard cap of three such jobs would wedge every
 * slot: the rest of the screen then only filled in when scrolling recycled
 * those cells and aborted them. Slow work keeps running, it just stops
 * blocking everything behind it.
 */
const SLOW_MS = 4000;

/** Ceiling even for slow jobs, to leave connections for media playback. */
const MAX_SLOW_ACTIVE = 6;

/** How often a failed thumbnail is retried before giving up on it. */
const MAX_TRIES = 3;
const RETRY_MS = 5000;

interface Job {
  url: string;
  img: HTMLImageElement;
  owner: HTMLElement;
  ctrl: AbortController;
  restart: boolean; // aborted by holdThumbs: re-queue instead of dropping
  started: number; // performance.now() when the fetch began
  tries: number;
}

let holds = 0;
const pending: Job[] = [];
const inflight = new Set<Job>();
const jobs = new WeakMap<HTMLElement, Job>();

/**
 * Cells whose thumbnail did not arrive (an error, or a request that never
 * completed). They are retried while they remain on screen: a cell is no
 * longer re-rendered just because the library changed, so without this a
 * single failure left a permanent grey tile.
 */
const failed = new Set<Job>();
let retryTimer = 0;

/**
 * Pause thumbnail loading while an overlay (video, lightbox) owns the screen:
 * the grid is hidden anyway, and both the browser's connection pool and the
 * server's disk/CPU should serve the overlay's media instead. In-flight
 * fetches are aborted and silently re-queued for when the overlay closes.
 */
export function holdThumbs(): void {
  holds++;
  if (holds > 1) return;
  for (const job of inflight) {
    job.restart = true;
    job.ctrl.abort();
  }
}

/** Undo one holdThumbs; loading resumes when no holds remain. */
export function releaseThumbs(): void {
  holds = Math.max(0, holds - 1);
  if (holds === 0) pump();
}

/** Start loading url into img; owner keys cancellation (one load per cell). */
export function loadThumb(owner: HTMLElement, img: HTMLImageElement, url: string): void {
  cancelThumb(owner);
  const job: Job = {
    url, img, owner,
    ctrl: new AbortController(),
    restart: false, started: 0, tries: 0,
  };
  jobs.set(owner, job);
  pending.push(job);
  pump();
}

/** Abort the pending or in-flight thumbnail load owned by this cell, if any. */
export function cancelThumb(owner: HTMLElement): void {
  const job = jobs.get(owner);
  if (!job) return;
  jobs.delete(owner);
  failed.delete(job);
  job.restart = false;
  const i = pending.indexOf(job);
  if (i >= 0) pending.splice(i, 1);
  job.ctrl.abort();
}

function onScreen(el: HTMLElement): boolean {
  if (!el.isConnected) return false;
  const r = el.getBoundingClientRect();
  return r.bottom > 0 && r.top < window.innerHeight;
}

/**
 * Oldest job whose cell is on screen, else the newest queued job. Visible
 * cells win over overscan; among them the oldest goes first, so no tile is
 * starved by newer arrivals.
 */
function next(): Job | undefined {
  for (let i = 0; i < pending.length; i++) {
    if (onScreen(pending[i].owner)) return pending.splice(i, 1)[0];
  }
  return pending.pop();
}

/** In-flight jobs still counted against the cap (see SLOW_MS). */
function activeYoung(): number {
  const now = performance.now();
  let n = 0;
  for (const job of inflight) {
    if (now - job.started < SLOW_MS) n++;
  }
  return n;
}

function pump(): void {
  while (holds === 0 && activeYoung() < MAX_ACTIVE && inflight.size < MAX_SLOW_ACTIVE) {
    const job = next();
    if (!job) return;
    void run(job);
  }
}

/** Remember a job that produced no image, so it can be retried on screen. */
function markFailed(job: Job): void {
  if (job.tries >= MAX_TRIES || !job.owner.isConnected) return;
  failed.add(job);
  if (retryTimer === 0) retryTimer = window.setInterval(retryThumbs, RETRY_MS);
}

/**
 * Retry thumbnails that failed while their cell is still on screen. The
 * usual reason a retry succeeds is that the server had not finished (or had
 * abandoned) generating the image the first time it was asked.
 */
export function retryThumbs(): void {
  for (const job of [...failed]) {
    if (!job.owner.isConnected || jobs.get(job.owner) !== job) {
      failed.delete(job);
      continue;
    }
    if (!onScreen(job.owner)) continue; // still queued for when it scrolls back
    failed.delete(job);
    job.ctrl = new AbortController();
    pending.push(job);
  }
  if (failed.size === 0 && retryTimer !== 0) {
    window.clearInterval(retryTimer);
    retryTimer = 0;
  }
  pump();
}

async function run(job: Job): Promise<void> {
  inflight.add(job);
  job.started = performance.now();
  job.tries++;
  let delivered = false;
  try {
    const res = await fetch(job.url, { signal: job.ctrl.signal });
    if (jobs.get(job.owner) !== job) return; // cell was re-rendered meanwhile
    if (!res.ok) {
      job.img.dispatchEvent(new Event('error')); // renderer shows the fallback
      markFailed(job); // the image may exist by the time we ask again
      return;
    }
    await res.blob(); // drain so the full response is cached
    if (jobs.get(job.owner) !== job) return;
    job.img.src = job.url;
    delivered = true;
    job.restart = false; // loaded despite the abort racing us: nothing to redo
  } catch (err) {
    // Aborted (held or cell gone) is the ordinary case and says nothing; a
    // fault in our own handling would otherwise vanish here too.
    if (!(err instanceof DOMException && err.name === 'AbortError')) console.debug('thumbnail', err);
  } finally {
    inflight.delete(job);
    if (jobs.get(job.owner) === job) {
      if (job.restart) {
        job.restart = false;
        job.ctrl = new AbortController();
        pending.push(job);
      } else if (!delivered) {
        // Never arrived and the cell is still showing a blank tile.
        markFailed(job);
      }
    }
    pump();
  }
}
