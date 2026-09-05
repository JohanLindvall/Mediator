/**
 * What a browser will and will not play, and what the player does about it.
 *
 * These two predicates decide which route a viewer takes, and getting either
 * wrong does not look like a wrong answer — it looks like a file that will
 * not play. Both have been wrong in exactly that way, so both are pinned here
 * against the agent strings and file names that caused it.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { defaultMode, fallbackMode, modeShown } from './content.ts';
import { withoutTrackNumber } from './format.ts';
import {
  castStep,
  shouldSave,
  audioSilent,
  PLAYER_KEYS, endedOnSet,
  REWRAP_WAIT_LIMIT,
  decodesAudio,
  decodesHEVC,
  decodesVideo,
  nativeHLS,
  opensDirectly,
  cropScale,
  pickAudioTrack,
  pictureRoute,
  playButtonIcon,
  playsOnReceiver,
  readFault,
  resumeStart,
  watchState,
  START_FLOOR_S,
  WATCHED_FRACTION,
  trackLabel,
  rewrapWorthTheWait,
  tapChoice,
} from './playback.ts';

/** Real agent strings, trimmed to what the check looks at. */
const AGENTS = {
  iphone:
    'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1',
  ipad: 'Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/604.1',
  macSafari:
    'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15',
  chromeLinux:
    'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36',
  chromeMac:
    'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36',
  edge: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36 Edg/127.0.0.0',
  firefox: 'Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0',
  androidChrome:
    'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Mobile Safari/537.36',
};

/** What each engine really answers for Apple's playlist type. */
const saysMaybe = (): string => 'maybe';
const saysNothing = (): string => '';

test('HLS: WebKit plays it natively', () => {
  for (const ua of [AGENTS.iphone, AGENTS.ipad, AGENTS.macSafari]) {
    assert.equal(nativeHLS(ua, saysMaybe), true, ua);
  }
});

test('HLS: Chrome claims "maybe" and cannot — the bug this exists for', () => {
  // Every one of these agents answers "maybe" for the playlist type. Taking
  // that at its word sent them to a route that fetched a playlist, fetched
  // nothing else, and reported the file unplayable.
  for (const ua of [AGENTS.chromeLinux, AGENTS.chromeMac, AGENTS.edge, AGENTS.androidChrome]) {
    assert.equal(nativeHLS(ua, saysMaybe), false, ua);
  }
});

test('HLS: Gecko does not play it either', () => {
  assert.equal(nativeHLS(AGENTS.firefox, saysMaybe), false);
});

test('HLS: Chrome on a Mac is not Safari, whatever the agent says', () => {
  // The trap: Chrome's agent carries both "Safari" and "AppleWebKit".
  assert.match(AGENTS.chromeMac, /Safari/);
  assert.match(AGENTS.chromeMac, /AppleWebKit/);
  assert.equal(nativeHLS(AGENTS.chromeMac, saysMaybe), false);
});

test('HLS: a WebKit that says it cannot is believed', () => {
  assert.equal(nativeHLS(AGENTS.macSafari, saysNothing), false);
});

/** A browser that opens MP4 and QuickTime and nothing else, like Safari. */
const opensMP4 = (type: string): string =>
  type === 'video/mp4' || type === 'video/quicktime' ? 'maybe' : '';
/** One that opens WebM as well, like Chrome. */
const opensMP4AndWebM = (type: string): string =>
  type === 'video/webm' ? 'probably' : opensMP4(type);

test('containers: what the browser opens is played directly', () => {
  assert.equal(opensDirectly('film.mp4', opensMP4), true);
  assert.equal(opensDirectly('film.m4v', opensMP4), true);
  assert.equal(opensDirectly('film.mov', opensMP4), true);
  assert.equal(opensDirectly('clip.webm', opensMP4AndWebM), true);
});

test('containers: what it refuses is not offered to it', () => {
  // The measured cost of offering it anyway: 664 MiB pulled in 68 s from one
  // film, and 7.6 GiB across the attempts, before the browser gave up. It
  // does not fail fast on a container it cannot open — it downloads it.
  for (const name of [
    'film.mkv',
    'film.avi',
    'film.flv',
    'film.wmv',
    // The transport streams a capture and a camcorder write, and the older
    // wrappers. Each was measured on a real disk before being listed.
    'film.ts',
    'clip.mts',
    'clip.m2ts',
    'film.divx',
    'old.rmvb',
  ]) {
    assert.equal(opensDirectly(name, opensMP4), false, name);
  }
});

test('containers: a DVD title is refused before it is downloaded', () => {
  // Four gigabytes of MPEG-2 that nothing decodes: the one container where
  // letting the element try it costs the whole film off the disk first.
  assert.equal(opensDirectly('film.vob', opensMP4), false);
});

test('containers: WebM is refused by a browser that has no WebM', () => {
  assert.equal(opensDirectly('clip.webm', opensMP4), false);
});

test('containers: an unknown one is left to the element to try', () => {
  // Only a definitive "no" is acted on; silence about a container this does
  // not know is not a no.
  assert.equal(opensDirectly('film.mxf', opensMP4), true);
  // Theora in Ogg is the awkward one: video/ogg is not a definitive answer
  // about it either way, so it is left off the list and found out by playing.
  assert.equal(opensDirectly('film.ogv', opensMP4), true);
  assert.equal(opensDirectly('noextension', opensMP4), true);
});

test('containers: the extension is read case-insensitively', () => {
  assert.equal(opensDirectly('FILM.MKV', opensMP4), false);
  assert.equal(opensDirectly('FILM.MP4', opensMP4), true);
});

test('containers: a name with dots in it uses the last one', () => {
  assert.equal(opensDirectly('Some.Film.2026.2160p.WEB.h265.mkv', opensMP4), false);
  assert.equal(opensDirectly('Some.Film.2026.1080p.WEB.h264.mp4', opensMP4), true);
});

/** A browser with hardware HEVC, like Safari. */
const opensHEVC = (type: string): string => (type.includes('hvc1') ? 'probably' : '');
/** One without, like Chrome on a machine that has no HEVC decoder. */
const noHEVC = (): string => '';

test('HEVC: a browser that decodes it is worth a rewrap', () => {
  assert.equal(decodesHEVC(opensHEVC), true);
});

test('HEVC: one that does not is sent straight to the converter', () => {
  // Copying the whole file first would leave it with something it still
  // cannot decode, having waited through the copy.
  assert.equal(decodesHEVC(noHEVC), false);
});

test('picture: frames decoded means nothing is wrong', () => {
  assert.equal(
    pictureRoute({ frames: 120, width: 1920, rewrapAvailable: true, hevcDecodes: true }),
    'ok',
  );
});

test('picture: a browser with HEVC is offered the copy first', () => {
  // The case this exists for: HEVC an iPhone decodes in hardware, written
  // as hev1, which it refuses. Seconds of copying against re-encoding
  // every frame of the film.
  assert.equal(
    pictureRoute({ frames: 0, width: 1920, rewrapAvailable: true, hevcDecodes: true }),
    'rewrap',
  );
});

test('picture: a browser without HEVC goes straight to the converter', () => {
  // It would otherwise wait through a copy of the whole file and still have
  // nothing it could decode.
  assert.equal(
    pictureRoute({ frames: 0, width: 1920, rewrapAvailable: true, hevcDecodes: false }),
    'convert',
  );
});

test('picture: the copy is offered once', () => {
  // Having had the rewrap and still decoded nothing, there is nothing left
  // to copy — and asking again would loop.
  assert.equal(
    pictureRoute({ frames: 0, width: 1920, rewrapAvailable: false, hevcDecodes: true }),
    'convert',
  );
});

test('picture: without a frame counter the width stands in', () => {
  // Firefox keeps no counter; videoWidth comes from the metadata, so a zero
  // there means the track was not even understood.
  assert.equal(
    pictureRoute({ frames: null, width: 1920, rewrapAvailable: true, hevcDecodes: true }),
    'ok',
  );
  assert.equal(
    pictureRoute({ frames: null, width: 0, rewrapAvailable: true, hevcDecodes: true }),
    'rewrap',
  );
});

test('rewrap: a browser without native HLS always waits for it', () => {
  // Its alternative is a pipe with no ranges and no seeking, so the copy is
  // worth any wait it costs.
  assert.equal(rewrapWorthTheWait(50 * 1024 ** 3, false), true);
  assert.equal(rewrapWorthTheWait(1024, false), true);
});

test('rewrap: with HLS to fall back on, only while the copy is quick', () => {
  // Measured at about 270 MB/s, so the limit is a few seconds of waiting.
  assert.equal(rewrapWorthTheWait(405 * 1024 ** 2, true), true);
  assert.equal(rewrapWorthTheWait(REWRAP_WAIT_LIMIT, true), true);
  assert.equal(rewrapWorthTheWait(REWRAP_WAIT_LIMIT + 1, true), false);
  assert.equal(rewrapWorthTheWait(17 * 1024 ** 3, true), false);
});

test('rewrap: an unknown size is not waited on', () => {
  // Nothing to judge the wait by, and HLS starts in a third of a second.
  assert.equal(rewrapWorthTheWait(0, true), false);
});

test('resume: a position worth returning to is returned to', () => {
  assert.equal(resumeStart({ t: 127.8, d: 599.2 }, 599284), 127.8);
});

test('resume: the first few seconds are where it starts anyway', () => {
  assert.equal(resumeStart({ t: 3, d: 599 }, 599000), 0);
  assert.equal(resumeStart(undefined, 599000), 0);
});

test('resume: a file watched to the end starts again', () => {
  assert.equal(resumeStart({ t: 590, d: 599 }, 599000), 0);
});

test('resume: the measured length catches a record with none of its own', () => {
  // This is the state a mis-saved position leaves behind: 599 seconds
  // written against a file, with no length beside it. Taken at its word on
  // a shorter file it opens past the end, ends at once, and rolls on to the
  // next — which is what going back to the previous video looked like.
  assert.equal(resumeStart({ t: 599.265, d: 0 }, 400_000), 0);
  // ... and on a long one it is still a real position.
  assert.equal(resumeStart({ t: 599.265, d: 0 }, 2_217_840), 599.265);
});

test('resume: with no length known anywhere it is trusted', () => {
  assert.equal(resumeStart({ t: 120, d: 0 }, 0), 120);
  assert.equal(resumeStart({ t: 120, d: 0 }, undefined), 120);
});

test('content: an unrestricted face offers every view', () => {
  for (const m of ['all', 'video', 'image', 'audio', 'albums', 'artists', 'genres'] as const) {
    assert.equal(modeShown(m, null), true, m);
    assert.equal(modeShown(m, []), true, m);
  }
});

test('content: a music face offers all, albums, artists and genres', () => {
  const music = ['music'];
  assert.equal(modeShown('all', music), true);
  assert.equal(modeShown('albums', music), true);
  assert.equal(modeShown('artists', music), true);
  assert.equal(modeShown('genres', music), true);
  // The Music chip would only repeat All: the whole library is music here.
  assert.equal(modeShown('audio', music), false);
  assert.equal(modeShown('video', music), false);
  assert.equal(modeShown('image', music), false);
});

test('content: a video face has no releases to group', () => {
  const videos = ['videos'];
  assert.equal(modeShown('all', videos), true);
  assert.equal(modeShown('albums', videos), false);
  assert.equal(modeShown('artists', videos), false);
  assert.equal(modeShown('genres', videos), false);
  assert.equal(modeShown('video', videos), false);
});

test('content: two classes bring the chips back', () => {
  const both = ['videos', 'music'];
  assert.equal(modeShown('video', both), true);
  assert.equal(modeShown('audio', both), true);
  assert.equal(modeShown('albums', both), true);
  assert.equal(modeShown('genres', both), true);
  assert.equal(modeShown('image', both), false);
});

test('content: a link to a view this face lacks opens the listing', () => {
  assert.equal(fallbackMode('albums', ['videos']), 'all');
  assert.equal(fallbackMode('albums', ['music']), 'albums');
  assert.equal(fallbackMode('image', null), 'image');
});

/** Chrome on Linux: no AC3, no DTS, everything else usual. */
const chromeAudio = (type: string): string =>
  /ac-3|ec-3/.test(type) ? '' : type.includes('mp4a') || type === 'audio/mpeg' ? 'probably' : 'maybe';
/** Safari, which does have Dolby. */
const safariAudio = (): string => 'probably';

test('audio: a soundtrack the browser cannot decode is known before it is played', () => {
  // The case: an AC3 film. Chrome plays the picture and is silent about the
  // silence, so the file's own codec is the only thing that says so early.
  assert.equal(decodesAudio('ac3', chromeAudio), false);
  assert.equal(decodesAudio('eac3', chromeAudio), false);
  assert.equal(decodesAudio('ac3', safariAudio), true);
});

test('audio: the ordinary ones are left alone', () => {
  assert.equal(decodesAudio('aac', chromeAudio), true);
  assert.equal(decodesAudio('mp3', chromeAudio), true);
});

test('audio: the cinema formats need no asking', () => {
  // No type string to put to canPlayType, and no browser has ever had a
  // decoder for them.
  for (const codec of ['dts', 'dca', 'truehd', 'mlp']) {
    assert.equal(decodesAudio(codec, safariAudio), false, codec);
  }
});

test('audio: a codec with nothing definitive to ask is left to the player', () => {
  // Silence is still detected the old way; guessing here would convert
  // files that would have played.
  assert.equal(decodesAudio('wmav2', chromeAudio), null);
  assert.equal(decodesAudio('', chromeAudio), null);
});

/** Chrome on Linux: H.264 and VP9, no HEVC. */
const chromeVideo = (type: string): string =>
  /hvc1|hev1/.test(type) ? '' : 'probably';

test('video: a picture the browser cannot decode is known before it is played', () => {
  // And it has to be asked first: the soundtrack conversion copies the
  // picture through, so choosing it here would play nothing at all.
  assert.equal(decodesVideo('hevc', chromeVideo), false);
  assert.equal(decodesVideo('h264', chromeVideo), true);
  assert.equal(decodesVideo('vp9', chromeVideo), true);
});

test('video: MPEG-2 is known to need converting before anything plays', () => {
  // A DVD title is MPEG-2, and no browser has ever decoded it — so the
  // conversion starts at once instead of after a black picture and a stall.
  const noMPEG = (type: string): string => (type === 'video/mpeg' ? '' : 'probably');
  assert.equal(decodesVideo('mpeg2video', noMPEG), false);
  assert.equal(decodesVideo('mpeg1video', noMPEG), false);
});

test('video: the older codecs are left to the player to find out', () => {
  // No settled type string for MPEG-4 ASP or the Windows ones, and guessing
  // would convert files that play.
  for (const codec of ['mpeg4', 'msmpeg4v3', 'wmv3', 'vc1', '']) {
    assert.equal(decodesVideo(codec, chromeVideo), null, codec);
  }
});

const soundtracks = [
  { index: 0, codec: 'ac3', lang: 'dan', channels: 6, default: true },
  { index: 1, codec: 'ac3', lang: 'swe', channels: 6 },
  { index: 2, codec: 'aac', lang: 'eng', title: 'Commentary Track', comment: true },
];

test("audio: the file's own default is the starting point", () => {
  assert.equal(pickAudioTrack(soundtracks), 0);
});

test('audio: a language chosen once is chosen again', () => {
  // The whole of the "smart" part: pick Swedish on one Nordic release and
  // the next one starts in Swedish.
  assert.equal(pickAudioTrack(soundtracks, { prefer: 'swe' }), 1);
  assert.equal(pickAudioTrack(soundtracks, { prefer: 'nor' }), 0);
});

test('audio: what was chosen for this film wins over both', () => {
  assert.equal(pickAudioTrack(soundtracks, { remembered: 1, prefer: 'dan' }), 1);
  // Remembered from a file that has since changed: ignored rather than
  // played as some other track.
  assert.equal(pickAudioTrack(soundtracks, { remembered: 7 }), 0);
});

test('audio: a commentary is never the default', () => {
  const commentaryFirst = [
    { index: 0, codec: 'aac', title: 'Commentary', comment: true, default: true },
    { index: 1, codec: 'ac3', lang: 'eng' },
  ];
  assert.equal(pickAudioTrack(commentaryFirst), 1);
  // ... unless it is the only thing there, when it is the film's sound.
  assert.equal(pickAudioTrack([commentaryFirst[0]!]), 0);
  // And it is still a choice anyone can make.
  assert.equal(pickAudioTrack(soundtracks, { remembered: 2 }), 2);
});

test('audio: a track is named by what it says about itself', () => {
  assert.equal(trackLabel({ index: 0, lang: 'swe', codec: 'ac3', channels: 6 }, 0), 'SWE · AC3 · 6ch');
  assert.equal(trackLabel({ index: 1, title: 'Director' }, 1), 'Director');
  assert.equal(trackLabel({ index: 2 }, 2), 'Track 3');
});

test('titles: the number the file carries is dropped, the list numbers it', () => {
  assert.equal(withoutTrackNumber('01. คนมีเสน่ห์'), 'คนมีเสน่ห์');
  assert.equal(withoutTrackNumber('05 - Mr. Torture'), 'Mr. Torture');
  assert.equal(withoutTrackNumber('3) Bergtatt'), 'Bergtatt');
  assert.equal(withoutTrackNumber('12_Skyfall'), 'Skyfall');
});

test('titles: a number that is part of the title stays', () => {
  assert.equal(withoutTrackNumber('44 Winters'), '44 Winters');
  assert.equal(withoutTrackNumber('1979'), '1979');
  assert.equal(withoutTrackNumber('24K Magic'), '24K Magic');
  // And a track actually called "01" keeps being called that.
  assert.equal(withoutTrackNumber('01.'), '01.');
});

test('crop: a padded picture fills the screen it can fill', () => {
  // The real case: a 270x480 portrait picture padded into an 854x480 frame.
  // On a phone held upright there is a great deal to give back.
  const padded = { x: 292, y: 0, w: 270, h: 480, frameW: 854, frameH: 480 };
  assert.ok(cropScale(padded, 400, 800) > 3);
  // In a landscape window the same file has nothing to give: the picture is
  // already as tall as the window, and the black beside it is the window's.
  assert.equal(cropScale(padded, 1600, 900), 1);
});

test('crop: a 4:3 film in a 16:9 frame gives its width back on a wide screen', () => {
  const pillarboxed = { x: 240, y: 0, w: 1440, h: 1080, frameW: 1920, frameH: 1080 };
  // Tall window: the picture can grow.
  assert.ok(cropScale(pillarboxed, 900, 1600) > 1.3);
  // Wide window: it is height-limited already.
  assert.equal(cropScale(pillarboxed, 1600, 900), 1);
});

test('crop: a frame with nothing to trim is left alone', () => {
  assert.equal(cropScale({ x: 0, y: 0, w: 1920, h: 1080, frameW: 1920, frameH: 1080 }, 1600, 900), 1);
  // A couple of pixels is the encoder rounding, not a border.
  assert.equal(cropScale({ x: 1, y: 1, w: 1918, h: 1078, frameW: 1920, frameH: 1080 }, 1600, 900), 1);
  assert.equal(cropScale({}, 1600, 900), 1);
});

test('crop: an off-centre box is refused rather than half-corrected', () => {
  // Scaling about the centre would put the picture askew, which is worse
  // than the borders it was meant to remove.
  assert.equal(cropScale({ x: 0, y: 0, w: 270, h: 480, frameW: 854, frameH: 480 }, 400, 800), 1);
});

test('crop: a detection gone wrong is refused', () => {
  // Nearly everything trimmed: the film faded to black where it was looked
  // at, and cropping to that would show a smear of one corner.
  assert.equal(cropScale({ x: 940, y: 520, w: 40, h: 40, frameW: 1920, frameH: 1080 }, 1600, 900), 1);
});

test('crop: nothing real is pushed off the screen to remove a border', () => {
  // A 2.35:1 film inside a 16:9 frame, in a window wider than either. The
  // picture already reaches both sides, so the trim is bounded by that: the
  // black that is left over is the window's, not the file's.
  const wide = { x: 0, y: 72, w: 1920, h: 936, frameW: 1920, frameH: 1080 };
  const scale = cropScale(wide, 2000, 1000);
  assert.ok(scale > 1 && scale <= 2000 / 1778 + 0.001, `scale ${scale} loses the sides`);
});

test('crop: a stretched picture is measured as it is shown, not as it is stored', () => {
  // Anamorphic PAL: a 16:9 picture coded in a 720x576 grid, with a 2.35:1
  // film letterboxed inside that. Measured in coded pixels the trim comes
  // out a fifth too large and cuts the sides off; measured as the element
  // lays it out, it stops where the picture reaches the edge.
  const dvd = { x: 0, y: 72, w: 720, h: 432, frameW: 720, frameH: 576 };
  const coded = cropScale(dvd, 2000, 1000);
  const shown = cropScale(dvd, 2000, 1000, { w: 1024, h: 576 });
  assert.ok(shown < coded, `stored ${coded} should be more than shown ${shown}`);
  assert.ok(Math.abs(shown - 2000 / 1778) < 0.01, `got ${shown}`);
  // Square pixels are the ordinary case and must not move.
  const square = { x: 0, y: 72, w: 1920, h: 936, frameW: 1920, frameH: 1080 };
  assert.equal(cropScale(square, 2000, 1000), cropScale(square, 2000, 1000, { w: 1920, h: 1080 }));
});

test('receiver: what a television can be handed as it is', () => {
  assert.equal(playsOnReceiver('h264'), true);
  assert.equal(playsOnReceiver('hevc'), true);
  // The one that bites: the phone plays it, the television has no decoder,
  // and what arrives is sound with a black screen behind it.
  assert.equal(playsOnReceiver('av1'), false);
  assert.equal(playsOnReceiver('vp9'), false);
  assert.equal(playsOnReceiver('mpeg4'), false);
});

test('receiver: an unprobed file is left alone', () => {
  // Converting on the chance that it might not play is the more expensive
  // way to be wrong.
  assert.equal(playsOnReceiver(undefined), true);
  assert.equal(playsOnReceiver(''), true);
});

test('a music-only face opens on the performers, and nothing else does', () => {
  // The whole library is music, so the file listing is a thousand tracks in
  // the order they were written to disk — and the shelf is what is wanted.
  assert.equal(defaultMode(['music']), 'artists');
  // A face showing everything has no one shelf to open on, and a face of
  // films has no grouping to open into.
  assert.equal(defaultMode(null), 'all');
  assert.equal(defaultMode([]), 'all');
  assert.equal(defaultMode(['videos']), 'all');
  assert.equal(defaultMode(['images']), 'all');
  assert.equal(defaultMode(['music', 'videos']), 'all');
});

test('a play/pause toggle shows the action on offer, not the state', () => {
  // The music bar shipped with this the other way around — a triangle while
  // the music played — which no test could see while the mapping lived
  // inline in the DOM code.
  assert.equal(playButtonIcon(true), 'pause');
  assert.equal(playButtonIcon(false), 'play');
});

test('a deck is copied where it can be, and moved only where it cannot', () => {
  // The move is irreversible and the copy is not, so the copy wins wherever
  // it is on offer — including where it is not ready yet.
  assert.equal(tapChoice(true, 1), 'copy');
  assert.equal(tapChoice(true, 2), 'copy');
  // Not ready is not a reason to spend the element's output: ask again.
  assert.equal(tapChoice(true, 0), 'wait');
  // No capture at all is the one case worth the move — the alternative
  // there is no spectrum on that browser at all.
  assert.equal(tapChoice(false, 0), 'move');
  assert.equal(tapChoice(false, 1), 'move');
});

// The grid's tick, the player's offer to resume and the server's chips all
// read a saved position by one pair of thresholds. They used to differ by a
// percent on this side: a film at 96.5% wore the tick on its tile and still
// resumed from there when opened.
test('watchState: the one rule for started and finished', () => {
  assert.equal(watchState(0, 600), 'none');
  assert.equal(watchState(START_FLOOR_S - 0.1, 600), 'none');
  assert.equal(watchState(START_FLOOR_S, 600), 'started');
  assert.equal(watchState(300, 600), 'started');
  assert.equal(watchState(600 * WATCHED_FRACTION, 600), 'started');
  assert.equal(watchState(600 * WATCHED_FRACTION + 1, 600), 'done');
  // No length known: nothing can be said, however far in.
  assert.equal(watchState(300, 0), 'none');
  assert.equal(watchState(300, NaN), 'none');
});

test('resumeStart agrees with watchState about the end', () => {
  const d = 600;
  const nearlyDone = d * WATCHED_FRACTION + 1;
  assert.equal(watchState(nearlyDone, d), 'done');
  assert.equal(resumeStart({ t: nearlyDone, d }, d * 1000), 0);
  const notYet = d * WATCHED_FRACTION - 1;
  assert.equal(watchState(notYet, d), 'started');
  assert.equal(resumeStart({ t: notYet, d }, d * 1000), notYet);
});

// A film that will not start asks the stream endpoint for one byte before
// trying any conversion, and the status is the whole answer: a disk that
// has stopped answering and a file that has left the library each get their
// own words, and everything else means the file is fine and the format is
// the question.
test('readFault: which statuses say the file itself cannot be had', () => {
  assert.equal(readFault(503), 'This file cannot be read');
  assert.equal(
    readFault(503, 'file unavailable: the filesystem it is on is damaged and needs repair\n'),
    'This file cannot be read: the filesystem it is on is damaged and needs repair',
  );
  assert.match(readFault(404) ?? '', /no longer in the library/);
  for (const ok of [200, 206, 416, 500, 0]) assert.equal(readFault(ok), null, String(ok));
});

test('a television that stopped at the end has ended; one stopped early was stopped', () => {
  assert.ok(endedOnSet(2580, 2600), 'inside the last twenty seconds');
  assert.ok(endedOnSet(2560, 2600), 'within three percent of the end');
  assert.ok(!endedOnSet(1200, 2600), 'half way is somebody with the remote');
  assert.ok(!endedOnSet(2580, 0), 'a length nobody knows is a stop');
});

test('a poll of the television: opening, playing, paused, ended, stopped', () => {
  // A set still opening the file reports STOPPED before it has played.
  assert.deepEqual(castStep(false, { state: 'STOPPED' }, 0, 2600), { seen: false, action: 'opening' });
  // Once seen playing, STOPPED at the end is the film ending…
  assert.deepEqual(castStep(false, { state: 'PLAYING', position: 10 }, 10, 2600), { seen: true, action: 'playing' });
  assert.deepEqual(castStep(true, { state: 'STOPPED' }, 2590, 2600), { seen: true, action: 'ended' });
  // …and STOPPED half way is somebody with the remote.
  assert.deepEqual(castStep(true, { state: 'STOPPED' }, 1200, 2600), { seen: true, action: 'stopped' });
  assert.deepEqual(castStep(true, { state: 'NO_MEDIA_PRESENT' }, 1200, 2600), { seen: true, action: 'stopped' });
  // The set's own length outranks the library's when it has one.
  assert.deepEqual(castStep(true, { state: 'STOPPED', duration: 1210 }, 1200, 2600), { seen: true, action: 'ended' });
  assert.deepEqual(castStep(true, { state: 'PAUSED_PLAYBACK' }, 1200, 2600), { seen: true, action: 'paused' });
  assert.deepEqual(castStep(true, null, 1200, 2600), { seen: true, action: 'nothing' });
});

test('a position is saved when it has moved, or when something forces it', () => {
  assert.ok(!shouldSave(100, 100, true), 'the same instant twice');
  assert.ok(!shouldSave(100.5, 100, false), 'under a second, unforced');
  assert.ok(shouldSave(100.5, 100, true), 'under a second, forced');
  assert.ok(!shouldSave(0.5, -1, false), 'the first second of a file');
  assert.ok(shouldSave(0.5, -1, true), 'unless forced');
  assert.ok(shouldSave(107, 100, false));
});

test('a silent soundtrack is read off whichever count the browser keeps', () => {
  assert.ok(audioSilent({ webkitAudioDecodedByteCount: 0 }));
  assert.ok(!audioSilent({ webkitAudioDecodedByteCount: 4096 }));
  assert.ok(audioSilent({ mozHasAudio: false }));
  assert.ok(!audioSilent({ mozHasAudio: true }));
  assert.ok(!audioSilent({}), 'a browser that says neither is believed');
});

test('every key in the help does something, and no key is listed twice', () => {
  const seen = new Set<string>();
  for (const line of PLAYER_KEYS) {
    assert.ok(line.does.length > 0 && line.keys.length > 0);
    for (const k of line.keys) {
      assert.ok(!seen.has(k), `${k} is listed twice`);
      seen.add(k);
    }
  }
});
