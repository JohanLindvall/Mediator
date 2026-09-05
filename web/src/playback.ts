/**
 * What a browser will and will not play.
 *
 * Two predicates, and both decide which route a viewer takes rather than how
 * something looks: get either wrong and the symptom is not a wrong answer but
 * a file that will not play. Both have been wrong in exactly that way, which
 * is why they live here — no imports, nothing from the DOM at the top level —
 * so they can be tested against real agent strings and real file names.
 */

/**
 * Whether a user agent's engine plays HLS from a plain `<video src>`.
 *
 * canPlayType cannot answer this and answers it misleadingly: Chrome returns
 * "maybe" for Apple's playlist type and then cannot play one, which sent
 * every Chrome viewer to a route that fetched a playlist, fetched nothing
 * else, and reported the file unplayable. Firefox is the same.
 *
 * So this asks which engine it is, which is the thing that actually decides.
 * Native HLS is WebKit's — Safari on either platform — while Blink and Gecko
 * both want a library this player does not carry. Feature detection is the
 * better tool nearly always; here the feature lies, and the trap is that
 * Chrome's own agent string carries both "Safari" and "AppleWebKit".
 */
export function nativeHLS(ua: string, canPlay: (type: string) => string): boolean {
  if (/Chrome|Chromium|Edg\/|OPR\/|Firefox|Android/.test(ua)) return false;
  return /Safari|AppleWebKit/.test(ua) && canPlay('application/vnd.apple.mpegurl') !== '';
}

/**
 * What a file's container is, as a browser names it. Only the ones worth
 * asking about: anything not here is left to the element to try, which is
 * what it did for all of them before.
 */
const CONTAINER_TYPES: Record<string, string> = {
  mp4: 'video/mp4',
  m4v: 'video/mp4',
  mov: 'video/quicktime',
  webm: 'video/webm',
  mkv: 'video/x-matroska',
  avi: 'video/x-msvideo',
  flv: 'video/x-flv',
  wmv: 'video/x-ms-wmv',
  mpg: 'video/mpeg',
  mpeg: 'video/mpeg',
  // A DVD title, which is an MPEG-2 program stream by another name.
  vob: 'video/mpeg',
  ts: 'video/mp2t',
  mts: 'video/mp2t',
  m2ts: 'video/mp2t',
  divx: 'video/x-msvideo',
  rm: 'application/vnd.rn-realmedia',
  rmvb: 'application/vnd.rn-realmedia',
  '3gp': 'video/3gpp',
};

/**
 * Whether the browser will even attempt this file's container.
 *
 * An empty answer from canPlayType is the browser's own definitive no and is
 * worth believing, because handing it the file anyway is not a cheap failure:
 * it downloads the thing hunting for something it can play. Measured on one
 * film, that was 664 MiB in 68 s, and 7.6 GiB across the attempts, before it
 * gave up. Anything else — "maybe" included — is left to the element, which
 * is what knows about the codecs inside.
 */
export function opensDirectly(name: string, canPlay: (type: string) => string): boolean {
  const ext = name.slice(name.lastIndexOf('.') + 1).toLowerCase();
  const type = CONTAINER_TYPES[ext];
  if (!type) return true; // unknown container: let it try, as it always did
  return canPlay(type) !== '';
}

/**
 * Whether this browser decodes HEVC inside an MP4.
 *
 * It answers one question: when a picture failed to decode, is a rewrap
 * worth asking the server for before re-encoding every frame? The only
 * rewrap that can help a decode failure is a re-tag — HEVC written as
 * "hev1", which Apple's decoders refuse and which ffmpeg writes by default —
 * so it is worth a copy of the whole file only where the codec itself is
 * playable. A browser with no HEVC at all would wait through that copy and
 * still have nothing it could decode.
 *
 * The codec string is a concrete Main-profile level 3.1 one because
 * canPlayType is entitled to answer on the details, and an empty answer is
 * the only definitive one it gives.
 */
export function decodesHEVC(canPlay: (type: string) => string): boolean {
  return decodesVideo('hevc', canPlay) === true;
}

/**
 * How a browser is asked about a picture. As with the soundtracks, only the
 * codecs there is a settled type string for: the older ones (MPEG-4 ASP,
 * WMV, VC-1) have none, and are left to be found out by playing.
 */
const VIDEO_TYPES: Record<string, string> = {
  h264: 'video/mp4; codecs="avc1.42E01E"',
  // A DVD title is MPEG-2, which nothing decodes — and asking saves the
  // stall of playing a black picture for a few seconds to find that out.
  mpeg2video: 'video/mpeg',
  mpeg1video: 'video/mpeg',
  hevc: 'video/mp4; codecs="hvc1.1.6.L93.B0"',
  vp8: 'video/webm; codecs="vp8"',
  vp9: 'video/webm; codecs="vp9"',
  av1: 'video/mp4; codecs="av01.0.05M.08"',
};

/**
 * Whether this browser can decode a picture of this codec: true, false, or
 * null when there is no way to ask.
 *
 * Worth knowing before the soundtrack is looked at, because the two
 * conversions are not alternatives: the one that fixes a soundtrack copies
 * the picture through untouched, which is no use at all when the picture is
 * the part that cannot be decoded — and then nothing plays, so the check
 * that would have noticed never runs, waiting as it does for playback to
 * get somewhere.
 */
export function decodesVideo(codec: string, canPlay: (type: string) => string): boolean | null {
  const type = VIDEO_TYPES[codec.toLowerCase()];
  if (!type) return null;
  return canPlay(type) !== '';
}

/** What to do about a picture that has had a moment to prove itself. */
export type PictureRoute = 'ok' | 'rewrap' | 'convert';

/**
 * Decide, from what the element reports, how a stream that decoded no
 * picture should be rescued.
 *
 * A codec the browser cannot handle produces a black picture rather than an
 * error, so the frame counter is the only signal there is — and where the
 * browser will not keep one, the video's own width stands in, which is set
 * from the metadata and so at least says the track was understood.
 *
 * Two ways out, and picking the wrong one is expensive both ways. A rewrap
 * is a copy: seconds, lossless, and the file then plays natively — but only
 * where the picture is one this browser could always decode and the file
 * merely labels in a way it refuses (HEVC written as hev1). A browser with
 * no HEVC at all would wait through a copy of the whole film and still have
 * nothing to show, so it goes straight to the converter. And the rewrap is
 * offered once: having already had it and still decoded nothing, there is
 * nothing left to copy.
 */
export function pictureRoute(o: {
  frames: number | null;
  width: number;
  rewrapAvailable: boolean;
  hevcDecodes: boolean;
}): PictureRoute {
  const decoded = o.frames === null ? o.width > 0 : o.frames > 0;
  if (decoded) return 'ok';
  return o.rewrapAvailable && o.hevcDecodes ? 'rewrap' : 'convert';
}

/**
 * How large a file may be before the wait for a lossless rewrap stops being
 * worth it to a browser that has the segmented conversion to fall back on.
 *
 * A rewrap runs at disk speed — measured, a 405 MiB FLV was copied in 1.5 s,
 * and a 351 MiB MP4 relabelled in 2 s, both about 270 MB/s — so this is
 * something like eight seconds of waiting at the top end.
 */
export const REWRAP_WAIT_LIMIT = 2 * 1024 * 1024 * 1024;

/**
 * Whether to spend the wait on a rewrap before falling back to a conversion.
 *
 * The rewrap is by far the better outcome and not close: the file plays
 * natively, seeks by byte range anywhere in itself at once, keeps its own
 * clock and its own quality, costs the server nothing after the copy, and is
 * still there the next time it is opened. What it costs is that nothing
 * plays until the copy is finished, because a browser will not start a file
 * that is still being written.
 *
 * A browser with no native HLS has nothing better to wait for — its
 * conversion is a pipe it cannot seek in — so it always waits. One that
 * plays HLS can start on the first segment instead, in about a third of a
 * second, and for a film large enough that the copy would take a visible
 * while that is the better trade; below the limit, waiting a moment for the
 * real thing is not.
 */
export function rewrapWorthTheWait(size: number, hasNativeHLS: boolean): boolean {
  if (!hasNativeHLS) return true;
  return size > 0 && size <= REWRAP_WAIT_LIMIT;
}

/**
 * Which icon a play/pause toggle shows: the action on offer, not the state —
 * every player convention, and what this app's own video player does. The
 * music bar once had it the other way around (a triangle while music
 * played), which is exactly the inversion a pure answer makes testable.
 */
export function playButtonIcon(playing: boolean): 'pause' | 'play' {
  return playing ? 'pause' : 'play';
}

/**
 * The two thresholds the grid, the player and the server all read a saved
 * position by. They are the library's own (`watchFloorSec` and
 * `WatchedFraction` in watched.go): under five seconds in is not a start,
 * and past 96% is finished. One pair on this side too, because the grid's
 * checkmark and the player's offer to resume used to disagree by a percent —
 * a film at 96.5% wore the tick on its tile and still resumed from there.
 */
export const START_FLOOR_S = 5;
export const WATCHED_FRACTION = 0.96;

/** What a saved position says has happened to the file, as the grid marks it. */
export type WatchState = 'none' | 'started' | 'done';

export function watchState(t: number, d: number): WatchState {
  if (!(d > 0) || t < START_FLOOR_S) return 'none';
  return t / d > WATCHED_FRACTION ? 'done' : 'started';
}

/**
 * What a stream's status says about the file itself, asked when a film will
 * not start.
 *
 * None of the ways round a format the browser refuses — the rewrap, the
 * segmented conversion, the pipe — can help a file the disk will not hand
 * over: every one of them fails at the same open, ten seconds later, and
 * the player then blamed the format. So before any of them is tried, the
 * stream endpoint is asked for one byte, and its status is read here: 503
 * is "known, and the disk is not answering", 404 is "not in the library any
 * more", and anything else says nothing about the file, so the ordinary
 * route carries on. A network failure is not an answer either.
 */
export function readFault(status: number, body = ''): string | null {
  switch (status) {
    case 503: {
      // The server says why, in the viewer's words, after a fixed prefix:
      // a disk that has stopped answering and a filesystem that needs
      // repair are different things to be told.
      const reason = body.replace(/^file unavailable:?\s*/, '').trim();
      return reason ? `This file cannot be read: ${reason}` : 'This file cannot be read';
    }
    case 404:
      return 'This file is no longer in the library';
    default:
      return null;
  }
}

/** What the server remembers about where a file was left. */
export interface Resume {
  /** Seconds into the file. */
  t: number;
  /** How long the file was when that was written, or 0 if it was not known. */
  d: number;
}

/**
 * Where a file should start, from what was saved for it.
 *
 * Two things are refused. A position in the first few seconds is not worth
 * restoring — it is where the file starts anyway. And a position at or past
 * the end means the file was watched through, so it starts again from the
 * beginning rather than at its own last frame.
 *
 * "The end" is judged against whatever length is known: the one recorded with
 * the position, and failing that the one the library measured. That second
 * source matters, because a record written with no length at all used to be
 * resumed wherever it said — and a position saved against the wrong file
 * opened a shorter one past its end, where it ended at once and rolled on to
 * the next, which is what going back to the previous video looked like it
 * was refusing to do.
 */
export function resumeStart(resume: Resume | undefined, durationMs?: number): number {
  if (!resume || resume.t < START_FLOOR_S) return 0;
  const known = resume.d > 0 ? resume.d : (durationMs ?? 0) / 1000;
  if (known > 0 && watchState(resume.t, known) === 'done') return 0;
  return resume.t;
}

/**
 * How a browser is asked about a soundtrack. Only the codecs there is a
 * settled type string for: an answer of "" is a definitive no, and there is
 * nothing definitive to be had for a codec the browser has never heard a
 * name for.
 */
const AUDIO_TYPES: Record<string, string> = {
  aac: 'audio/mp4; codecs="mp4a.40.2"',
  mp3: 'audio/mpeg',
  ac3: 'audio/mp4; codecs="ac-3"',
  eac3: 'audio/mp4; codecs="ec-3"',
  opus: 'audio/webm; codecs="opus"',
  vorbis: 'audio/webm; codecs="vorbis"',
  flac: 'audio/flac',
};

/**
 * Soundtracks no browser decodes, whatever it is asked. These are the
 * cinema formats — there is no type string to put to canPlayType for them,
 * and no browser has ever shipped a decoder.
 */
const NO_BROWSER_AUDIO = new Set(['dts', 'dca', 'truehd', 'mlp']);

/**
 * Whether this browser can decode a soundtrack of this codec: true, false,
 * or null when there is no way to ask.
 *
 * Worth asking before playing rather than after, because a soundtrack the
 * browser cannot decode makes no error — it makes silence — and because of
 * what a browser does with a file that offers it several. Presented with an
 * AC3 track it cannot decode and an AAC commentary track it can, Chrome
 * plays the commentary: perfectly reasonable of it, and not what anybody
 * wants. Deciding from what the file leads with keeps that choice ours.
 */
export function decodesAudio(codec: string, canPlay: (type: string) => string): boolean | null {
  const name = codec.toLowerCase();
  if (NO_BROWSER_AUDIO.has(name)) return false;
  const type = AUDIO_TYPES[name];
  if (!type) return null;
  return canPlay(type) !== '';
}

/** One of a film's soundtracks, as the server describes it. */
export interface Track {
  index: number;
  codec?: string;
  lang?: string;
  title?: string;
  channels?: number;
  default?: boolean;
  comment?: boolean;
}

/**
 * Which soundtrack to start with.
 *
 * In order: the one chosen for this film before, then the last language
 * anyone chose anywhere — pick Swedish once and the next Nordic release
 * starts in Swedish, which is the whole of the "smart" part — then the one
 * the file marks as its own default, then simply the first. A commentary
 * track is never any of these unless it is the only thing there: it is a
 * legitimate choice and never a default one, and a file that leads with one
 * is exactly how a viewer ends up listening to two men discussing the film
 * they are trying to watch.
 */
export function pickAudioTrack(
  tracks: readonly Track[],
  opts: { remembered?: number; prefer?: string } = {},
): number {
  if (tracks.length === 0) return 0;
  const real = tracks.filter((t) => !t.comment);
  const pool = real.length > 0 ? real : tracks;

  if (opts.remembered !== undefined && tracks.some((t) => t.index === opts.remembered)) {
    return opts.remembered;
  }
  const prefer = (opts.prefer ?? '').toLowerCase();
  if (prefer) {
    const spoken = pool.find((t) => (t.lang ?? '').toLowerCase().startsWith(prefer));
    if (spoken) return spoken.index;
  }
  return (pool.find((t) => t.default) ?? pool[0]!).index;
}

/** How a soundtrack is named in the menu. */
export function trackLabel(t: Track, n: number): string {
  const parts = [t.title, t.lang ? t.lang.toUpperCase() : '', t.codec?.toUpperCase()].filter(
    Boolean,
  ) as string[];
  if (t.channels && t.channels > 2) parts.push(`${t.channels}ch`);
  if (parts.length === 0) parts.push(`Track ${n + 1}`);
  return parts.join(' · ');
}

/** A picture's place inside a frame, as the server reports it. */
export interface CropBox {
  x?: number;
  y?: number;
  w?: number;
  h?: number;
  frameW?: number;
  frameH?: number;
}

/**
 * How much to scale a picture by to put its own black borders off the edges
 * of the screen, given the box it is being shown in. 1 when there is nothing
 * to gain.
 *
 * The element shows the whole frame, fitted; scaling it up about its centre
 * pushes the borders out of the box, which clips them. That works because
 * the borders that occur in practice are symmetrical — a portrait clip
 * padded into a landscape frame, a 4:3 film in a 16:9 one — so the picture
 * stays where it is and only grows. A box that is off-centre is refused
 * rather than half-corrected: scaling that would put the picture askew,
 * which is worse than the borders.
 *
 * The box matters as much as the file does. A portrait picture inside a
 * landscape frame, shown in a landscape window, is already as large as it
 * can be — the black at its sides belongs to the window, not to the file,
 * and no cropping will fill it. Turn the phone upright and the same file has
 * a great deal to give back. So this answers for the screen it is asked
 * about, and answers 1 whenever the trimming would buy nothing.
 *
 * The picture never grows past the box: the limit is whichever edge it
 * reaches first, so what is trimmed is the *smaller* of the two borders and
 * nothing real is ever pushed off. Some black can therefore be left, and
 * that black is the window's — a film framed at 2.39:1 in a 2:1 window has
 * bars that no amount of scaling can remove without losing the sides.
 *
 * `shown` is the size the element actually lays the picture out at, which is
 * not always the size it is stored at: anamorphic video — every DVD — codes
 * a 16:9 picture in a 4:3 grid of pixels and stretches it on display. The
 * detection is in coded pixels, so it is used only as *fractions* of the
 * frame, and the geometry is done in the coordinates the element uses. Doing
 * it in coded pixels over-scaled a DVD by a fifth and cut the sides off.
 */
export function cropScale(
  box: CropBox,
  boxW: number,
  boxH: number,
  shown: { w: number; h: number } | null = null,
  tolerance = 2,
): number {
  const { x = 0, y = 0, w = 0, h = 0, frameW = 0, frameH = 0 } = box;
  if (w <= 0 || h <= 0 || frameW <= 0 || frameH <= 0 || boxW <= 0 || boxH <= 0) return 1;
  if (w > frameW || h > frameH) return 1;
  // Centred, give or take a pixel of rounding on either side.
  if (Math.abs(frameW - w - 2 * x) > tolerance) return 1;
  if (Math.abs(frameH - h - 2 * y) > tolerance) return 1;
  // How much of the frame the picture is, which is all the detection is
  // good for once the frame may be stretched on the way to the screen.
  const fx = w / frameW;
  const fy = h / frameH;
  const shownW = shown && shown.w > 0 ? shown.w : frameW;
  const shownH = shown && shown.h > 0 ? shown.h : frameH;
  // What the whole frame is displayed at, and then how much bigger the
  // picture inside it could be before it stopped fitting.
  const fit = Math.min(boxW / shownW, boxH / shownH);
  const scale = Math.min(boxW / (shownW * fx * fit), boxH / (shownH * fy * fit));
  // Under a percent is not worth a transform; over eight times is a
  // detection that has gone wrong, and cropping to it would show a smear.
  if (scale < 1.01 || scale > 8) return 1;
  return scale;
}

/**
 * Video codecs a television on the other end of AirPlay can be expected to
 * decode.
 *
 * The receiver never says what it can do, and there is no asking it — so
 * this is the short list of what is safe to hand over. H.264 is decoded by
 * everything that has ever had a network port; HEVC by every set that can
 * show 4K, which is every set anyone AirPlays to. AV1 is the one that
 * catches people out: a phone from the last two years decodes it happily,
 * and the television it is being sent to almost certainly does not — a 2020
 * panel has no AV1 decoder at all, and what arrives is sound with a black
 * screen behind it. VP9 and VP8 are the same story from the other direction:
 * a television may well decode them, an Apple TV will not.
 */
const RECEIVER_CODECS = new Set(['h264', 'hevc']);

/**
 * Whether a file can be handed to a receiver as it is, or has to be
 * converted first. An unknown codec is left alone: converting a film nobody
 * has probed yet, on the chance that it might not play, is the more
 * expensive way to be wrong.
 */
export function playsOnReceiver(vcodec: string | undefined): boolean {
  const codec = (vcodec ?? '').toLowerCase();
  return codec === '' || RECEIVER_CODECS.has(codec);
}

/**
 * Whether the spectrum is being given any audio to draw.
 *
 * A browser can accept the routing and pass nothing. WebKit on a phone does
 * exactly that for a video element: `createMediaElementSource` succeeds, the
 * film goes on playing out of the speakers as though nothing had happened,
 * and the analyser reads digital silence for ever. From inside the page the
 * two are indistinguishable — a panel of bars at rest is what a quiet passage
 * looks like too — so the only honest thing is to wait, and then say so.
 *
 * Three rules, and each is why this is a reducer rather than a flag:
 *
 * - **Once something has been heard it is never said.** A graph that has
 *   drawn one bar is working, whatever it does afterwards, and a film with a
 *   long silence in the middle of it must not accuse the browser.
 * - **Only playing time counts.** A paused film produces exactly the same
 *   reading as a broken audio path, and the difference is the whole question.
 * - **It is taken back.** If sound arrives later the report goes, so nothing
 *   is claimed that turned out to be untrue.
 */
export interface DeafState {
  /** Any sound has reached the graph at some point. */
  heard: boolean;
  /** Seconds of playing with nothing reaching it. */
  silent: number;
  /** Whether the panel is currently reporting that it gets nothing. */
  deaf: boolean;
}

export const DEAF_AFTER = 6;

export function deafStep(
  s: DeafState,
  ev: { heard: boolean; sounding: boolean; dt: number },
): DeafState {
  if (ev.heard) return { heard: true, silent: 0, deaf: false };
  if (s.heard || !ev.sounding) return s;
  const silent = s.silent + ev.dt;
  return { heard: false, silent, deaf: silent >= DEAF_AFTER };
}

/**
 * How much of a file has to be converted, given what is known about its
 * picture — and, crucially, whether that knowledge was *earned*.
 *
 * The two callers ask the same words and mean opposite things, which is why
 * this takes `proven` rather than being one rule:
 *
 * - **The file is already playing** (a soundtrack is being changed). The
 *   browser has demonstrably decoded this picture, so anything not known to
 *   be undecodable is copied through. An unknown codec is *proven* here by
 *   the frames on screen.
 * - **The browser refused the container** and nothing has decoded at all.
 *   Nothing is proven, so only a picture positively known to play is copied.
 *   An unknown codec converted needlessly costs processor time; an unknown
 *   codec copied when it could not be decoded costs a stream that plays
 *   nothing, an error on screen, and the wait for the watchdog to escalate —
 *   which is what a WMV did: `wmv2` has no type string to ask about, so it
 *   read as "not known to be undecodable" and was copied into an MP4 that
 *   cannot even hold it, and ffmpeg answered with nothing at all.
 */
export function convertMode(
  vcodec: string | undefined,
  canPlay: (type: string) => string,
  proven: boolean,
  reencode = false,
): 'full' | 'audio' {
  // A picture that lies about how far it reorders its frames plays right
  // only where something re-encodes it; a copy — which is what the audio
  // mode makes of the picture — keeps the lie exactly (see plannedRoute).
  if (reencode) return 'full';
  const decodes = vcodec ? decodesVideo(vcodec, canPlay) : null;
  if (proven) return decodes === false ? 'full' : 'audio';
  return decodes === true ? 'audio' : 'full';
}

/**
 * What to do about a file before it plays, from what the server knows of it.
 *
 * Three answers. A picture the browser cannot decode needs the full
 * conversion, and it is asked about first because the soundtrack conversion
 * copies the picture through — no use at all when the picture is the part
 * that cannot be decoded, and then nothing plays, which is the one state the
 * decode check cannot get out of. A picture the server says must be
 * re-encoded (`reencode`) needs it too: the stream reorders its frames
 * further than it declares, and a browser handed the file trusts the
 * declaration exactly as it trusts a copy — measured on a phone, two MP4s
 * from one muxer stuttered for their whole length played as they were. Then
 * a soundtrack the browser cannot decode needs only its own conversion. And
 * null is a file worth handing to the element as it is.
 */
export function plannedRoute(
  o: { vcodec?: string; acodec?: string; reencode?: boolean },
  canPlay: (type: string) => string,
): 'full' | 'audio' | null {
  if (o.vcodec && decodesVideo(o.vcodec, canPlay) === false) return 'full';
  if (o.reencode) return 'full';
  if (o.acodec && decodesAudio(o.acodec, canPlay) === false) return 'audio';
  return null;
}

/**
 * Which soundtrack to name when handing a film to a renderer, or undefined
 * for "say nothing and let the set take the file as it is".
 *
 * Saying nothing is the cheap path — the set plays the file's own default —
 * and naming a track costs a copy of the film with only that soundtrack in
 * it, DLNA having no way to say "play stream two" (see castSource). So the
 * track is named exactly when the viewer's choice differs from what the set
 * would have picked anyway: the file's default.
 *
 * The rule this replaces sent the track whenever it was non-zero, which was
 * wrong twice over. A viewer choosing track 0 over a non-zero default was
 * not sent at all — the one language they asked for was the one dropped —
 * and a viewer whose choice *was* the default (just not track 0) paid for a
 * copy that changed nothing.
 */
export function castAudioChoice(tracks: readonly Track[], chosen: number): number | undefined {
  if (tracks.length < 2) return undefined;
  const fileDefault = tracks.find((t) => t.default)?.index ?? tracks[0]!.index;
  return chosen === fileDefault ? undefined : chosen;
}

/**
 * How a deck's sound can be got at: copied, waited for, or moved.
 *
 * The whole safety of the spectrum turns on this one choice, so it is here
 * rather than inside the view, where nothing could check it.
 *
 * A **copy** (`captureStream`) leaves the element playing exactly as it was:
 * the tap can be dropped and taken again, and one that yields nothing costs
 * an empty panel. A **move** (`createMediaElementSource`) takes the
 * element's output away from the speakers once and for the element's whole
 * life — so whatever the graph then fails to do is silence with no repair,
 * and it takes the element's AirPlay route with it. Measured, the hard way:
 * a film whose source changed under a moved element went mute until the page
 * was reloaded, the node having kept the file that was no longer there.
 *
 * Hence **wait**: a capture taken before the deck has its soundtrack carries
 * no track to read, and falling back to a move there would spend the film's
 * sound to save a moment of empty panel. It is asked again when data
 * arrives. Only a browser with no capture at all is worth the move, and
 * there it is the difference between a spectrum and none.
 */
export type TapChoice = 'copy' | 'wait' | 'move';

export function tapChoice(canCapture: boolean, audioTracks: number): TapChoice {
  if (!canCapture) return 'move';
  return audioTracks > 0 ? 'copy' : 'wait';
}

/**
 * Whether a film a television reports as stopped had reached its end, as
 * against being stopped there by hand. A set says STOPPED for both, and the
 * two want opposite things: an ended episode rolls on to the next, a film
 * somebody stopped with the remote must not start something else. The
 * clock is what tells them apart — the last position this page carried,
 * against the length the set or the library gave — with a margin for the
 * credits a set skips and the second or two a poll is behind. Nothing
 * known about the length is a stop.
 */
export function endedOnSet(position: number, duration: number): boolean {
  if (!(duration > 0)) return false;
  return position >= duration - 20 || position / duration >= 0.97;
}

/** Whether a television's transport state means it is done with what it was given. */
export function castOver(state: string): boolean {
  return state === 'STOPPED' || state === 'NO_MEDIA_PRESENT';
}

/** What a poll of a television reports. */
export interface CastPoll {
  state: string;
  position?: number;
  duration?: number;
}

/**
 * What a poll of the television means, and what the transport (casting.ts,
 * shared by the player and the music bar) does about it.
 *
 * - `nothing`: the set could not be reached; the clock carries on.
 * - `opening`: it has not played yet and reports STOPPED — a slow set still
 *   opening the file, which read as an ending would stop a film it had
 *   just started.
 * - `playing` and `paused`: what they say, once it has been seen playing.
 * - `ended`: stopped at the end of the file — the season, or the queue,
 *   rolls on.
 * - `stopped`: stopped well short of it, which is somebody with the
 *   remote, and the page stops being a remote control.
 *
 * `seen` is whether the set has been seen playing this file, and the answer
 * carries it forward. Pure, so the three transitions are pinned by tests.
 */
export type CastAction = 'nothing' | 'opening' | 'playing' | 'paused' | 'ended' | 'stopped';

export function castStep(
  seen: boolean,
  st: CastPoll | null,
  position: number,
  duration: number,
): { seen: boolean; action: CastAction } {
  if (!st) return { seen, action: 'nothing' };
  if (st.state === 'PLAYING') return { seen: true, action: 'playing' };
  if (seen && castOver(st.state)) {
    return { seen, action: endedOnSet(position, st.duration || duration) ? 'ended' : 'stopped' };
  }
  return { seen, action: seen ? 'paused' : 'opening' };
}

/**
 * Whether a position is worth writing down: not the one already saved,
 * not a movement under a second unless something forces it (a pause, a
 * step away, the file ending), and not the first second of a file unless
 * forced — a film opened and shut has no position worth keeping.
 */
export function shouldSave(t: number, last: number, force: boolean): boolean {
  if (t === last) return false;
  if (!force && Math.abs(t - last) < 1) return false;
  if (t < 1 && !force) return false;
  return true;
}

/**
 * Whether the element is decoding no sound at all, from what the browser
 * says about it. WebKit counts the bytes, Gecko says whether there is a
 * track; a browser that says neither is taken at its word that all is well,
 * since converting a soundtrack on no evidence is the expensive way to be
 * wrong.
 */
export function audioSilent(el: { webkitAudioDecodedByteCount?: number; mozHasAudio?: boolean }): boolean {
  if (typeof el.webkitAudioDecodedByteCount === 'number') return el.webkitAudioDecodedByteCount === 0;
  if (typeof el.mozHasAudio === 'boolean') return !el.mozHasAudio;
  return false;
}

/** One line of the player's key help: the keys, as written on a keycap, and what they do. */
export interface KeyHelp {
  keys: readonly string[];
  does: string;
}

/**
 * The player's keys, in one table the help overlay is drawn from and the
 * handler is written against — so the two cannot drift, and a key that is
 * not here is a key nobody can find.
 */
export const PLAYER_KEYS: readonly KeyHelp[] = [
  { keys: ['Space', 'K'], does: 'Play or pause' },
  { keys: ['←', 'J'], does: 'Back ten seconds' },
  { keys: ['→', 'L'], does: 'Forward ten seconds' },
  { keys: ['Home', 'End'], does: 'Start or end of the film' },
  { keys: ['↑', '↓'], does: 'Volume' },
  { keys: ['M'], does: 'Mute' },
  { keys: ['F'], does: 'Fullscreen' },
  { keys: ['C'], does: 'Next subtitle' },
  { keys: ['A'], does: 'Next soundtrack' },
  { keys: ['R', 'Shift R'], does: 'Rotate the picture' },
  { keys: ['?'], does: 'This list' },
  { keys: ['Esc'], does: 'Close a menu, or the player' },
];
