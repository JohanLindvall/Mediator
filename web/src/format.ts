/** Small formatting helpers. */

/**
 * A size in the units a file manager uses: KB, MB, GB are thousands, as the
 * unit names say. Dividing by 1024 under those names — which this did —
 * showed a 4.19 GB file as 3.9 GB, a visible number that was simply wrong.
 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1000) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n;
  let u = -1;
  do {
    v /= 1000;
    u++;
  } while (v >= 1000 && u < units.length - 1);
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[u]}`;
}

const dateFmt = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });

/** Compact date: relative for the last week, absolute beyond. */
export function formatDate(ms: number): string {
  const d = new Date(ms);
  const now = Date.now();
  const diff = now - ms;
  const day = 86_400_000;
  if (diff < 60_000 && diff >= 0) return 'just now';
  if (diff < 3_600_000 && diff > 0) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < day && diff > 0) return `${Math.floor(diff / 3_600_000)}h ago`;
  if (diff < 7 * day && diff > 0) return `${Math.floor(diff / day)}d ago`;
  return dateFmt.format(d);
}

export function formatDateFull(ms: number): string {
  return `${dateFmt.format(ms)} ${timeFmt.format(ms)}`;
}

/** mm:ss or h:mm:ss. */
export function formatDuration(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return '0:00';
  const s = Math.floor(sec % 60);
  const m = Math.floor((sec / 60) % 60);
  const h = Math.floor(sec / 3600);
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
  return `${h > 0 ? h + ':' : ''}${mm}:${String(s).padStart(2, '0')}`;
}

/** Uppercase extension badge for a filename, e.g. "MP4". */
export function extBadge(name: string): string {
  const i = name.lastIndexOf('.');
  if (i < 0 || i === name.length - 1) return '';
  return name.slice(i + 1).toUpperCase().slice(0, 5);
}

/**
 * A chip's count, sized for where it is shown.
 *
 * On a phone the counts are what make the chips wide — a full library reads
 * "209,857" on one and "102,596" on the next, seven characters apiece — and
 * chip width is bar height, since they wrap. Five digits and up rounds to
 * thousands: "210k" says what the number is for, which is telling apart a
 * face's shelves at a glance, and the exact figure is one tap into the
 * count line below. Under ten thousand the count stays exact — "2,403"
 * albums is no wider than "2.4k" and rounding it buys nothing.
 */
export function chipCount(n: number, compact: boolean): string {
  if (!compact || n < 10_000) return n.toLocaleString();
  return `${Math.round(n / 1000)}k`;
}

export function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v));
}

/** Escape text for interpolation into HTML (element text or attributes). */
export function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
}

/**
 * A track title with the number the file carries taken off the front.
 *
 * A release names its files "01. Something", and the list they are shown in
 * numbers them again — so every line reads "1  01. Something". The number is
 * dropped only where a separator says it is a number and not the title: "44
 * Winters" and "1979" keep theirs, "01. ", "05 - " and "3)" lose them.
 * If that leaves nothing, the number was the whole name and it stays.
 */
export function withoutTrackNumber(title: string): string {
  const bare = title.replace(/^\s*\d{1,3}\s*[.\-_)]\s*/, '').trim();
  return bare === '' ? title : bare;
}

/**
 * How a codec is spelled for a reader, given the name a probe uses for it.
 *
 * ffprobe's names are the ones the rest of this app reasons with — "h264",
 * "eac3" — and they are not the ones anybody writes. Only the ones a library
 * actually holds are here; anything else is shown as it came, upper-cased,
 * which is wrong-looking but never a lie.
 */
const CODEC_NAMES: Record<string, string> = {
  h264: 'H.264',
  hevc: 'HEVC',
  av1: 'AV1',
  vp8: 'VP8',
  vp9: 'VP9',
  mpeg4: 'MPEG-4',
  mpeg2video: 'MPEG-2',
  mpeg1video: 'MPEG-1',
  wmv2: 'WMV',
  wmv3: 'WMV',
  vc1: 'VC-1',
  theora: 'Theora',
  aac: 'AAC',
  mp3: 'MP3',
  ac3: 'AC-3',
  eac3: 'E-AC-3',
  dts: 'DTS',
  truehd: 'TrueHD',
  opus: 'Opus',
  vorbis: 'Vorbis',
  flac: 'FLAC',
  alac: 'ALAC',
  wmav2: 'WMA',
  wmapro: 'WMA Pro',
};

export function codecName(codec: string | undefined): string {
  if (!codec) return '';
  return CODEC_NAMES[codec.toLowerCase()] ?? codec.toUpperCase();
}

/** The facts a media file carries about how it was made, or "" for none. */
export interface MediaShape {
  kind: string;
  vcodec?: string;
  acodec?: string;
  width?: number;
  height?: number;
  fps?: number;
  size?: number;
  duration?: number;
  name?: string;
}

/**
 * What a file is, technically: what it was encoded with and how big the
 * picture is. Shown on hover, where there is room for it and where it is not
 * in the way of anybody who does not care.
 *
 * Everything is optional and everything is omitted when absent — a library
 * learns these as it goes, and a line of separators around nothing would be
 * worse than no line. The one thing computed rather than read is a
 * soundtrack's bitrate, which is its size over its playing time: that is an
 * average rather than a nominal figure, true of a variable-rate file where
 * the nominal one is a fiction, and it costs nothing to know.
 */
export function mediaShape(it: MediaShape): string {
  const parts: string[] = [];
  if (it.kind === 'video') {
    parts.push(codecName(it.vcodec));
    if (it.width && it.height) parts.push(`${it.width}×${it.height}`);
    if (it.fps) parts.push(`${Math.round(it.fps * 100) / 100} fps`);
    if (it.acodec) parts.push(codecName(it.acodec));
    parts.push(bitrate(it.size, it.duration));
  } else if (it.kind === 'image') {
    if (it.width && it.height) parts.push(`${it.width}×${it.height}`);
  } else if (it.kind === 'audio') {
    // The container is what a listener calls the format, and it is in the
    // name — no probe reads a file to find out it is an mp3.
    parts.push(codecName(it.acodec) || extBadge(it.name ?? ''));
    parts.push(bitrate(it.size, it.duration));
  }
  return parts.filter(Boolean).join(' · ');
}

/**
 * What a file spends per second, as its size over its playing time.
 *
 * The whole file, so for a film it counts the soundtrack and the container
 * along with the picture — which is what every player means by a file's
 * bitrate, and the only figure obtainable without reading the stream. It is
 * an average, and for a variable-rate file that is the truer number: the
 * nominal one such a file declares is a fiction.
 *
 * Megabits once there are enough of them. "12.2 Mbps" is a film's rate at a
 * glance where "12200 kbps" has to be counted.
 */
function bitrate(size: number | undefined, durationMs: number | undefined): string {
  if (!size || !durationMs) return '';
  const kbps = (size * 8) / (durationMs / 1000) / 1000;
  if (kbps < 1) return '';
  if (kbps < 1000) return `${Math.round(kbps)} kbps`;
  return `${Math.round(kbps / 100) / 10} Mbps`;
}
