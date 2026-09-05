import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { codecName, mediaShape } from './format.ts';

// The names a probe uses are the ones the rest of the app reasons with, and
// none of them are the ones anybody writes.
test('codecs are spelled the way they are written', () => {
  assert.equal(codecName('h264'), 'H.264');
  assert.equal(codecName('HEVC'), 'HEVC');
  assert.equal(codecName('eac3'), 'E-AC-3');
  assert.equal(codecName('wmav2'), 'WMA');
  assert.equal(codecName(''), '');
  assert.equal(codecName(undefined), '');
  // Something nothing here knows is shown as it came rather than guessed at:
  // wrong-looking, but never a lie.
  assert.equal(codecName('somethingnew'), 'SOMETHINGNEW');
});

test('a film says what it was encoded with and how big it is', () => {
  assert.equal(
    mediaShape({ kind: 'video', vcodec: 'h264', acodec: 'aac', width: 1920, height: 1080, fps: 25 }),
    'H.264 · 1920×1080 · 25 fps · AAC',
  );
  // And what it spends per second, once there is a length to divide by.
  assert.equal(
    mediaShape({ kind: 'video', vcodec: 'h264', size: 1_500_000_000, duration: 1_000_000 }),
    'H.264 · 12 Mbps',
  );
  // A library learns these as it goes, so every part has to be able to be
  // missing without leaving a line of separators around nothing.
  assert.equal(mediaShape({ kind: 'video', vcodec: 'hevc' }), 'HEVC');
  assert.equal(mediaShape({ kind: 'video', width: 640, height: 480 }), '640×480');
  assert.equal(mediaShape({ kind: 'video' }), '');
  // A frame rate that is not a whole number is not rounded into one.
  assert.equal(mediaShape({ kind: 'video', fps: 23.976 }), '23.98 fps');
});

test('a photograph says how big it is and nothing else', () => {
  assert.equal(mediaShape({ kind: 'image', width: 3024, height: 4032 }), '3024×4032');
  assert.equal(mediaShape({ kind: 'image' }), '');
  // A still has no codec worth naming and no playing time to divide by.
  assert.equal(mediaShape({ kind: 'image', width: 8, height: 8, size: 1000 }), '8×8');
});

// A soundtrack's bitrate is its size over its playing time: an average rather
// than a nominal figure, which is the truth of a variable-rate file where the
// nominal one is a fiction.
test('a track says its format and what it averages', () => {
  // 5 MB over 300 s is 133 kbps.
  assert.equal(
    mediaShape({ kind: 'audio', name: 'a track.mp3', size: 5_000_000, duration: 300_000 }),
    'MP3 · 133 kbps',
  );
  // Where a probe named the codec, that wins over the file's extension.
  assert.equal(
    mediaShape({ kind: 'audio', acodec: 'flac', name: 'a track.mp3', size: 1000, duration: 1000 }),
    'FLAC · 8 kbps',
  );
  // Nothing to divide by, so nothing claimed.
  assert.equal(mediaShape({ kind: 'audio', name: 'a track.opus' }), 'OPUS');
});

// The whole file over its playing time: the only figure obtainable without
// reading the stream, and the truer one for a variable-rate file, whose
// declared rate is a fiction.
test('a rate is counted in megabits once there are enough of them', () => {
  const rate = (size: number, ms: number): string =>
    mediaShape({ kind: 'audio', name: 'a.mp3', acodec: 'mp3', size, duration: ms }).split(' · ')[1] ?? '';
  assert.equal(rate(5_000_000, 300_000), '133 kbps');
  assert.equal(rate(1_500_000_000, 1_000_000), '12 Mbps');
  assert.equal(rate(2_000_000_000, 1_000_000), '16 Mbps');
  // Nothing to divide by, and nothing claimed.
  assert.equal(rate(0, 300_000), '');
  assert.equal(rate(5_000_000, 0), '');
});
