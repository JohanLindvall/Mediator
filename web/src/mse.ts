/**
 * A conversion fed to the element by the page, not fetched by the browser.
 *
 * A live conversion is one response of unknown length with no ranges, and a
 * browser managing its own buffer treats that badly: it reads until its
 * buffer is full, drops the connection, and reconnects asking for the byte
 * it wants next — which a pipe cannot give it, so it is answered with the
 * conversion from the beginning and discards everything up to where it was.
 * Measured on one viewing (see CLAUDE.md): ten connections, 963 MB across
 * the link to move a 167 MB stream. The waste is not a constant, it grows
 * with the playback position, and on the one link where a lower bitrate is
 * wanted at all — a slow one — it is the whole of the link.
 *
 * So the page holds the connection instead. One fetch, read once from start
 * to end, each chunk handed to a Media Source Extensions buffer as it
 * arrives; the browser decodes out of that buffer and never sees a URL to
 * reconnect to. Back-pressure is the page's: it stops reading when enough is
 * buffered ahead, which stalls the response, which stalls ffmpeg on its
 * write, so a paused film costs the server nothing. What has been played is
 * dropped behind the playhead so the buffer's quota is never reached.
 *
 * The one thing this costs is a receiver: AirPlay and remote playback hand
 * a *URL* to a set, and an object URL is nothing a set can fetch. The
 * player hides the receiver button while a conversion is fed this way.
 *
 * Safari does not come through here — it has native HLS, whose segments
 * are ordinary files with ranges, and none of this is needed there.
 */

/** How far ahead of the playhead to keep buffered before the read pauses. */
export const FEED_AHEAD_S = 45;
/** How far behind the playhead is kept before it is dropped from the buffer. */
export const FEED_BEHIND_S = 60;

/** The four-character type and the bounds of one box. */
interface Box {
  type: string;
  start: number; // where the payload begins
  end: number; // one past the box
}

/**
 * Walk the boxes between pos and end. Sizes of 1 mean a 64-bit size follows,
 * 0 means to the end — the same two rules mp4box.go reads by.
 */
function boxes(b: Uint8Array, pos: number, end: number): Box[] {
  const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const out: Box[] = [];
  while (pos + 8 <= end) {
    let size = v.getUint32(pos);
    let payload = pos + 8;
    if (size === 1) {
      if (pos + 16 > end) break;
      size = Number(v.getBigUint64(pos + 8));
      payload = pos + 16;
    } else if (size === 0) {
      size = end - pos;
    }
    if (size < 8 || pos + size > end) break;
    const type = String.fromCharCode(b[pos + 4]!, b[pos + 5]!, b[pos + 6]!, b[pos + 7]!);
    out.push({ type, start: payload, end: pos + size });
    pos += size;
  }
  return out;
}

function child(b: Uint8Array, pos: number, end: number, type: string): Box | null {
  return boxes(b, pos, end).find((x) => x.type === type) ?? null;
}

/**
 * Where the initialisation segment ends: the offset of the first `moof`, or
 * -1 while the bytes in hand do not reach it yet. Everything before it —
 * `ftyp` and `moov` — is what a SourceBuffer has to be given first and
 * whole, and the codec string has to be read out of it before the buffer
 * can even be made.
 */
export function initSegmentEnd(b: Uint8Array): number {
  for (const x of boxes(b, 0, b.length)) {
    if (x.type === 'moof') return x.start - 8;
  }
  return -1;
}

/**
 * The MIME type a SourceBuffer has to be created with, read from the
 * initialisation segment: the H.264 profile, compatibility and level bytes
 * out of the `avcC` box, spelled the way the codecs parameter wants them,
 * and AAC-LC for the soundtrack, which is the one thing every conversion
 * here makes. Null where the segment is not one this can name.
 */
export function codecStringOf(init: Uint8Array): string | null {
  const moov = child(init, 0, init.length, 'moov');
  if (!moov) return null;
  const codecs: string[] = [];
  for (const trak of boxes(init, moov.start, moov.end)) {
    if (trak.type !== 'trak') continue;
    const mdia = child(init, trak.start, trak.end, 'mdia');
    const minf = mdia && child(init, mdia.start, mdia.end, 'minf');
    const stbl = minf && child(init, minf.start, minf.end, 'stbl');
    const stsd = stbl && child(init, stbl.start, stbl.end, 'stsd');
    if (!stsd) continue;
    // stsd: version+flags (4) and an entry count (4), then the entries.
    for (const entry of boxes(init, stsd.start + 8, stsd.end)) {
      if (entry.type === 'avc1' || entry.type === 'avc3') {
        // A visual sample entry is 78 bytes of fields before its children.
        const avcC = child(init, entry.start + 78, entry.end, 'avcC');
        if (!avcC) return null;
        const p = init[avcC.start + 1]!;
        const c = init[avcC.start + 2]!;
        const l = init[avcC.start + 3]!;
        codecs.push(`${entry.type}.${hex(p)}${hex(c)}${hex(l)}`);
      } else if (entry.type === 'mp4a') {
        codecs.push('mp4a.40.2');
      }
    }
  }
  if (!codecs.some((c) => c.startsWith('avc'))) return null;
  return `video/mp4; codecs="${codecs.join(',')}"`;
}

function hex(n: number): string {
  return n.toString(16).toUpperCase().padStart(2, '0');
}

/** Whether this browser can be fed a conversion this way at all. */
export function canFeed(): boolean {
  return typeof MediaSource !== 'undefined' && typeof MediaSource.isTypeSupported === 'function';
}

/**
 * One fed conversion: the fetch, the buffer, and the loop between them.
 *
 * `attach` returns the object URL to hand the element; `stop` ends
 * everything and is safe to call twice. Errors come out through `onError`
 * with the HTTP status where there was one, so the player can tell a file
 * the disk will not hand over (503) from a stream that simply failed.
 */
/** What a fed conversion is told, beyond its URL. */
export interface FeedOptions {
  /** The film's running time from this stream's origin, for the element's duration. */
  duration?: number;
  onError: (why: string, status?: number) => void;
  onEnd?: () => void;
}

export class FedSource {
  private readonly ms = new MediaSource();
  private readonly abort = new AbortController();
  private readonly url: string;
  private readonly o: FeedOptions;
  private objectUrl = '';
  private sb: SourceBuffer | null = null;
  private video: HTMLMediaElement | null = null;
  private stopped = false;

  // No parameter properties: the test runner strips types and refuses
  // them, which is what keeps preview.ts out of its reach.
  constructor(url: string, o: FeedOptions) {
    this.url = url;
    this.o = o;
  }

  attach(video: HTMLMediaElement): string {
    this.video = video;
    this.objectUrl = URL.createObjectURL(this.ms);
    this.ms.addEventListener('sourceopen', () => void this.run(), {
      once: true,
    });
    return this.objectUrl;
  }

  stop(): void {
    if (this.stopped) return;
    this.stopped = true;
    this.abort.abort();
    if (this.ms.readyState === 'open') {
      try {
        if (this.sb && !this.sb.updating) this.ms.endOfStream();
      } catch {
        // Already ended, or never got as far as a buffer: nothing to end.
      }
    }
    if (this.objectUrl) URL.revokeObjectURL(this.objectUrl);
  }

  private async run(): Promise<void> {
    let res: Response;
    try {
      res = await fetch(this.url, { signal: this.abort.signal });
    } catch (e) {
      if (!this.stopped) this.o.onError(String(e));
      return;
    }
    if (!res.ok || !res.body) {
      this.o.onError(res.statusText, res.status);
      return;
    }
    if (this.o.duration !== undefined && this.o.duration > 0) {
      try {
        this.ms.duration = this.o.duration;
      } catch {
        // A closed source refuses; the element's own clock still works.
      }
    }
    const reader = res.body.getReader();
    let pending: Uint8Array<ArrayBuffer> = new Uint8Array(0);
    try {
      for (;;) {
        if (this.stopped) return;
        await this.throttle();
        const { value, done } = await reader.read();
        if (done) break;
        if (!value) continue;
        if (!this.sb) {
          // The buffer cannot be made until the codec string is known, and
          // the codec string is inside the initialisation segment, which
          // may take more than one chunk to arrive whole.
          pending = concat(pending, value);
          const end = initSegmentEnd(pending);
          if (end < 0) continue;
          const mime = codecStringOf(pending.subarray(0, end));
          if (!mime || !MediaSource.isTypeSupported(mime)) {
            this.o.onError(`unsupported stream ${mime ?? ''}`);
            return;
          }
          this.sb = this.ms.addSourceBuffer(mime);
          this.sb.mode = 'segments';
          await this.append(pending);
          pending = new Uint8Array(0);
          continue;
        }
        await this.append(value);
      }
      await this.settled();
      if (!this.stopped && this.ms.readyState === 'open') this.ms.endOfStream();
      this.o.onEnd?.();
    } catch (e) {
      if (!this.stopped) this.o.onError(String(e));
    }
  }

  /**
   * Hand one chunk to the buffer, and if the buffer is full, make room
   * behind the playhead and try once more. The quota is the browser's and
   * unannounced; the eviction below keeps it out of reach in the ordinary
   * case, and this is the belt.
   */
  private async append(chunk: Uint8Array<ArrayBuffer>): Promise<void> {
    const sb = this.sb!;
    await this.settled();
    try {
      sb.appendBuffer(chunk);
    } catch (e) {
      if (!(e instanceof DOMException && e.name === 'QuotaExceededError')) throw e;
      await this.evict(10);
      await this.settled();
      sb.appendBuffer(chunk);
    }
    await this.settled();
  }

  /** Resolve once the buffer is not in the middle of an update. */
  private settled(): Promise<void> {
    const sb = this.sb;
    if (!sb || !sb.updating) return Promise.resolve();
    return new Promise((resolve) =>
      sb.addEventListener('updateend', () => resolve(), { once: true }),
    );
  }

  /**
   * Wait while enough is buffered ahead. Polled rather than driven by
   * timeupdate, because a paused element fires none and the loop still has
   * to notice a seek that emptied the buffer under it.
   */
  private async throttle(): Promise<void> {
    for (;;) {
      if (this.stopped) return;
      const ahead = this.ahead();
      if (ahead < FEED_AHEAD_S) return;
      await this.evict(FEED_BEHIND_S);
      await new Promise((r) => setTimeout(r, 400));
    }
  }

  /** Seconds buffered beyond the playhead, or 0 with nothing to say. */
  private ahead(): number {
    const v = this.video;
    const sb = this.sb;
    if (!v || !sb || sb.buffered.length === 0) return 0;
    return sb.buffered.end(sb.buffered.length - 1) - v.currentTime;
  }

  /** Drop what is more than `keep` seconds behind the playhead. */
  private async evict(keep: number): Promise<void> {
    const v = this.video;
    const sb = this.sb;
    if (!v || !sb || sb.buffered.length === 0) return;
    const from = sb.buffered.start(0);
    const to = v.currentTime - keep;
    if (to <= from + 1) return;
    await this.settled();
    if (this.stopped || this.ms.readyState !== 'open') return;
    sb.remove(from, to);
    await this.settled();
  }
}

function concat(a: Uint8Array<ArrayBuffer>, b: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> {
  if (a.length === 0) return b;
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}
