// SPDX-License-Identifier: MIT

import assert from 'node:assert/strict';
import test from 'node:test';
import { infoRefreshDelay, listFolders, listMedia, playlistUrl, tracksOf } from './api.ts';

test('stream credentials refresh before expiry with a bounded retry delay', () => {
  const now = 1_700_000_000_000;
  assert.equal(infoRefreshDelay((now + 12 * 3_600_000) / 1000, now), 3_600_000);
  assert.equal(infoRefreshDelay((now + 600_000) / 1000, now), 300_000);
  for (const expiry of [undefined, NaN, Infinity, 0, now / 1000]) {
    assert.equal(infoRefreshDelay(expiry, now), 60_000);
  }
});

test('folder names and search survive listing, queue and export URL encoding', async (t) => {
  const folder = 'Leaf & + #';
  const urls: URL[] = [];
  t.mock.method(globalThis, 'fetch', async (url: string) => {
    urls.push(new URL(url, 'https://example.com'));
    return new Response('{}', { status: 200 });
  });
  const filters = { kind: 'audio' as const, folder, q: 'river lantern', sort: 'name', order: 'asc' };
  await listMedia(filters);
  await tracksOf('items', filters);
  await listFolders({ ...filters, folder: undefined });
  urls.push(new URL(playlistUrl(filters), 'https://example.com'));
  assert.equal(urls[2]!.pathname, '/api/folders');
  for (const url of [urls[0]!, urls[1]!, urls[3]!]) {
    assert.equal(url.searchParams.get('folder'), folder);
    assert.equal(url.searchParams.get('q'), filters.q);
    assert.equal(url.searchParams.get('kind'), 'audio');
  }
});

test('cancelling a superseded query does not report a network fault', async (t) => {
  const abort = new AbortController();
  abort.abort();
  let calls = 0;
  t.mock.method(globalThis, 'fetch', async (_url: string, init: RequestInit) => {
    calls++;
    assert.equal(init.signal, abort.signal);
    throw new DOMException('Aborted', 'AbortError');
  });
  await assert.rejects(listMedia({}, abort.signal), { name: 'AbortError' });
  assert.equal(calls, 1);
});
