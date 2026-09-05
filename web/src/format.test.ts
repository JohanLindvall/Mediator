import { test } from 'node:test';
import assert from 'node:assert/strict';

import { chipCount, clamp, esc, extBadge, formatBytes, formatDate, formatDuration } from './format.ts';

test('bytes: each magnitude reads in its own unit', () => {
  assert.equal(formatBytes(0), '0 B');
  assert.equal(formatBytes(999), '999 B');
  assert.equal(formatBytes(1000), '1.0 KB');
  assert.equal(formatBytes(1500), '1.5 KB');
  // Two digits drop the decimal: "123.4 MB" is precision nobody reads.
  assert.equal(formatBytes(15_000_000), '15 MB');
  assert.equal(formatBytes(4.19e9), '4.2 GB');
  // Nothing sensible to say: say nothing rather than "NaN undefined".
  assert.equal(formatBytes(-1), '');
  assert.equal(formatBytes(NaN), '');
});

test('duration: the hour appears only when there is one', () => {
  assert.equal(formatDuration(0), '0:00');
  assert.equal(formatDuration(59), '0:59');
  assert.equal(formatDuration(61), '1:01');
  assert.equal(formatDuration(3600), '1:00:00');
  // 87:47 of a DVD title: hours, and the minutes gain their pad.
  assert.equal(formatDuration(5267), '1:27:47');
  assert.equal(formatDuration(NaN), '0:00');
});

test('badge: the extension, shouted, and never the whole name', () => {
  assert.equal(extBadge('film.mkv'), 'MKV');
  assert.equal(extBadge('Feature.VOB'), 'VOB');
  // No extension is no badge — not the name in capitals.
  assert.equal(extBadge('README'), '');
  assert.equal(extBadge('archive.'), '');
  // A long "extension" is cut, not allowed to cover the tile.
  assert.equal(extBadge('file.download'), 'DOWNL');
});

test('esc: what the browser would parse arrives as text', () => {
  assert.equal(esc('AC/DC & Friends <live>'), 'AC/DC &#38; Friends &#60;live&#62;');
  assert.equal(esc(`it's "quoted"`), 'it&#39;s &#34;quoted&#34;');
});

test('clamp holds both ends', () => {
  assert.equal(clamp(5, 0, 10), 5);
  assert.equal(clamp(-5, 0, 10), 0);
  assert.equal(clamp(15, 0, 10), 10);
});

test('chip counts: a phone reads thousands, a desktop the number', () => {
  assert.equal(chipCount(209_857, true), '210k');
  assert.equal(chipCount(85_118, true), '85k');
  assert.equal(chipCount(10_000, true), '10k');
  // Under ten thousand the exact figure is no wider than the rounding.
  assert.equal(chipCount(2_403, true), (2_403).toLocaleString());
  assert.equal(chipCount(0, true), '0');
  // And with room, the number is the number.
  assert.equal(chipCount(209_857, false), (209_857).toLocaleString());
});

test('a date is relative for a week and absolute beyond', () => {
  const now = Date.now();
  assert.equal(formatDate(now - 5_000), 'just now');
  assert.equal(formatDate(now - 5 * 60_000), '5m ago');
  assert.equal(formatDate(now - 3 * 3_600_000), '3h ago');
  assert.equal(formatDate(now - 6 * 86_400_000), '6d ago');
  assert.ok(!formatDate(now - 8 * 86_400_000).endsWith('ago'), 'a week on it is a date');
  assert.ok(!formatDate(now + 3_600_000).endsWith('ago'), 'the future is not "ago"');
});
