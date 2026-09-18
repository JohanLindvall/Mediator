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
 * The bitrate some rippers write into the file name, at the end of it.
 *
 * Bounded to the rates that exist rather than to any number, because the
 * thing on the other side of this rule is a title that ends in a year or in
 * a number somebody meant: "_320" and "(256kbps)" come off, "1979" and
 * "Studio 54" stay. A bracketed form must say `k` — unbracketed digits after
 * a dash or an underscore are the shape this was measured on ("_320",
 * "-192"), where inside brackets a bare number is far likelier to be a year.
 */
const RATES = '32|40|48|56|64|80|96|112|128|160|192|224|256|320';
const bitrateEnd = new RegExp(
  `(?:[ _-]?[[(]\\s*(?:${RATES})\\s*k(?:bps|bit|b)?\\s*[\\])]|[_-](?:${RATES})\\s*k?(?:bps|bit|b)?)$`,
  'i',
);

/**
 * A leading "SOMEBODY - " taken off, but only where somebody is the
 * performer this library already knows for the track.
 *
 * That limit is the whole safety of the rule, and it is self-limiting in a
 * useful way: a release by several performers matches none of its tracks, so
 * a compilation whose files are named "Performer - Title" keeps every one of
 * them — which is the one place that prefix carries what the tags do not. An
 * untagged release under one performer is where it fires.
 */
function withoutPerformer(title: string, who: string | undefined): string {
  const name = (who ?? '').trim();
  if (name === '' || !title.toLowerCase().startsWith(name.toLowerCase())) return title;
  // The separator has to be there: "Bandana Blues" does not begin with the
  // performer "Band", it begins with a word that does.
  const rest = title.slice(name.length);
  const sep = /^\s*[-_]\s*/.exec(rest);
  if (!sep) return title;
  const bare = rest.slice(sep[0].length).trim();
  return bare === '' ? title : bare;
}

/** What a list has to know about a track in order to name it. */
export interface TrackNamed {
  name: string;
  title?: string;
  artist?: string;
  /**
   * Who the release this track is on is by, where the file itself names
   * nobody. The server fills it in from the release (Item.Performer), so
   * every list gets the same answer without having to know which collection
   * it happens to be drawing.
   */
  performer?: string;
}

/**
 * What a track is called, wherever one is named — the queue row, the bar's
 * own title, the album sheet. One spelling, because three had begun to
 * differ: two of them showed the file name with its extension still on it.
 *
 * **A tag is somebody's statement and is never edited.** Where the file
 * carries a title that is the title, verbatim, and nothing below happens.
 * The server takes the same care in the other direction — it refuses to read
 * a title out of a file name at all (`RecordingKey`), two different songs
 * called "01" not being one recording — and editing what a tag says is the
 * same mistake wearing the other hat.
 *
 * What is left is a file name, which is a title with everything a filesystem
 * and a ripper needed wrapped round it. Measured over this library's 28,686
 * audio items, 532 (1.9%) carry no title tag and are shown this way: all 532
 * would otherwise show an extension, 230 carry a leading track number, 15 a
 * leading performer prefix, and 13 a trailing bitrate marker. Off they come,
 * in that order — and the performer is asked about twice, since a number in
 * front of the performer ("01 - Band - Title") is as ordinary a shape as the
 * other way round ("BAND - 01.Title"), and the second ask costs one
 * comparison and refuses on exactly the same terms as the first.
 *
 * A step that would leave nothing is not taken. A name that is noisy is
 * worth more than no name at all.
 */
export function trackTitle(t: TrackNamed, performer?: string): string {
  if (t.title) return t.title;
  // The track's own performer first, then the release's as the server
  // worked it out, then whoever the collection being drawn belongs to. An
  // untagged file has no artist of its own, and the release above it is
  // where the library's answer lives — 382 of those 532 sit in a release
  // named that way and nothing else. The last of the three is a fallback for
  // a list that knows its collection before the server has built one.
  const who = t.artist || t.performer || performer;
  const named = t.name.replace(/\.[^./\\]+$/, '') || t.name;
  const bare = withoutPerformer(withoutTrackNumber(withoutPerformer(named, who)), who);
  return bare.replace(bitrateEnd, '').trim() || bare;
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
 * Where one frame of a scrub sheet goes inside a tile, and how big.
 *
 * The sheet is one image of `cols` by `rows` frames, and a frame keeps its
 * own proportions: fitted inside the tile, centred, with whatever is left
 * over filled black rather than stretched or cropped into. A preview is for
 * seeing what is in the film.
 *
 * **What is returned is the size of the window, not of the picture painted
 * in it**, and that distinction is the whole of it. A background image is
 * not clipped to one frame — the element it is painted on is what clips it —
 * so an element the size of the *tile* shows the neighbouring columns of the
 * sheet in whatever room the fitted frame does not fill. On a wide film the
 * frame fills the width, the neighbours fall outside and nobody ever saw it;
 * on a clip shot on a phone the frame is a third of the tile's width and the
 * previous and next frames show either side of it. Measured on a 404×720
 * clip in a 260×146 tile: an 82-pixel frame centred with 89 pixels spare on
 * each side — one and a tenth frames of room, so three pictures at once.
 */
export function fitFrame(
  box: { width: number; height: number },
  sheet: { width: number; height: number },
  grid: { cols: number; rows: number },
): { frameW: number; frameH: number; offX: number; offY: number } | null {
  const fw = sheet.width / grid.cols;
  const fh = sheet.height / grid.rows;
  if (box.width <= 0 || box.height <= 0 || fw <= 0 || fh <= 0) return null;
  const scale = Math.min(box.width / fw, box.height / fh);
  const frameW = fw * scale;
  const frameH = fh * scale;
  return {
    frameW,
    frameH,
    offX: (box.width - frameW) / 2,
    offY: (box.height - frameH) / 2,
  };
}

/**
 * What a track belongs to: the release, the year and the genre, in that
 * order and each left out when absent.
 *
 * **The rule every hover here follows is that it carries what the row does
 * not already show.** A grid tile prints the performer, the year and the
 * genre under the name, so its hover is the path and the technical line
 * instead; a queue row prints the performer and the running time and
 * nothing else, so its hover is this, the artwork and the technical line.
 * Saying the same thing twice — once in the row and again a moment later
 * under the pointer — is how a tooltip becomes noise, and these two
 * surfaces had drifted into describing a track in different words anyway.
 * This is the one wording, so they cannot.
 */
export function belongsTo(it: {
  album?: string;
  year?: number;
  genre?: string;
}): string {
  // Falsy rather than merely absent, because a year of nought is a year
  // nobody knows rather than the year nought — the wire leaves it out
  // altogether (omitempty), and a caller building one by hand should not be
  // able to print it either.
  return [it.album, it.year, it.genre].filter(Boolean).join(' · ');
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
