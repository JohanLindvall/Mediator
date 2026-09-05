import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { convertMode, plannedRoute } from './playback.ts';

// Chrome: H.264, VP9 and AV1 answer; the old codecs have no type string at all.
const chrome = (type: string): string =>
  /avc1|vp9|vp8|av01/.test(type) ? 'probably' : '';

// The browser refused the container, so nothing has decoded and nothing is
// proven. Only a picture positively known to play may be copied through.
test('an unproven picture is converted unless it is known to decode', () => {
  assert.equal(convertMode('h264', chrome, false), 'audio', 'h264 plays everywhere');
  assert.equal(convertMode('vp9', chrome, false), 'audio', 'the browser said it decodes vp9');
  // The case that broke: no type string to ask about, so nothing is known —
  // and copying it produced a stream the browser could not decode out of a
  // container that cannot even hold it.
  assert.equal(convertMode('wmv2', chrome, false), 'full');
  assert.equal(convertMode('mpeg4', chrome, false), 'full');
  assert.equal(convertMode('vc1', chrome, false), 'full');
  assert.equal(convertMode(undefined, chrome, false), 'full', 'codecs not read yet');
});

// The file is playing, so the picture is proven whatever it is called. Only a
// definite no sends it to a full conversion.
test('a proven picture is copied unless it is known not to decode', () => {
  assert.equal(convertMode('wmv2', chrome, true), 'audio', 'it is on screen');
  assert.equal(convertMode('h264', chrome, true), 'audio');
  const noVp9 = (type: string): string => (/avc1/.test(type) ? 'probably' : '');
  assert.equal(convertMode('vp9', noVp9, true), 'full', 'this browser has no vp9');
});

// The distinction is the whole point: the same codec, answered both ways.
test('the same unknown codec answers differently by what has been proven', () => {
  assert.notEqual(convertMode('wmv2', chrome, true), convertMode('wmv2', chrome, false));
});

// A stream the server says must be re-encoded gets the full conversion from
// either caller: the audio mode copies the picture, and a copy keeps the lie.
test('convertMode: a reordering lie outranks a decodable picture', () => {
  const chrome = (t: string): string => (t.includes('avc1') || t.includes('vp9') ? 'probably' : '');
  assert.equal(convertMode('h264', chrome, true, true), 'full');
  assert.equal(convertMode('h264', chrome, false, true), 'full');
  assert.equal(convertMode('h264', chrome, true, false), 'audio');
});

// What is decided before a file is handed to the element at all.
test('plannedRoute: picture first, then the reordering lie, then the sound', () => {
  const chrome = (t: string): string =>
    t.includes('avc1') || t.includes('vp9') || t.includes('mp4a') || t.includes('opus') ? 'probably' : '';
  assert.equal(plannedRoute({ vcodec: 'h264', acodec: 'aac' }, chrome), null, 'plays as it is');
  assert.equal(plannedRoute({ vcodec: 'hevc', acodec: 'aac' }, chrome), 'full', 'picture undecodable');
  assert.equal(plannedRoute({ vcodec: 'h264', acodec: 'ac3' }, chrome), 'audio', 'only the sound');
  assert.equal(plannedRoute({ vcodec: 'h264', acodec: 'aac', reencode: true }, chrome), 'full', 'the lie');
  assert.equal(plannedRoute({ vcodec: 'hevc', acodec: 'ac3', reencode: true }, chrome), 'full');
  assert.equal(plannedRoute({ acodec: 'ac3', reencode: true }, chrome), 'full', 'the lie outranks the sound');
  assert.equal(plannedRoute({}, chrome), null, 'nothing known: let it try');
  assert.equal(plannedRoute({ vcodec: 'wmv2' }, chrome), null, 'no way to ask: let it try');
});
