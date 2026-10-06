// SPDX-License-Identifier: MIT

import assert from 'node:assert/strict';
import test from 'node:test';
import { infoRefreshDelay, listMedia } from './api.ts';

test('stream credentials refresh before expiry with a bounded retry delay', () => {
  const now = 1_700_000_000_000;
  assert.equal(infoRefreshDelay((now + 12 * 3_600_000) / 1000, now), 3_600_000);
  assert.equal(infoRefreshDelay((now + 600_000) / 1000, now), 300_000);
  for (const expiry of [undefined, NaN, Infinity, 0, now / 1000]) {
    assert.equal(infoRefreshDelay(expiry, now), 60_000);
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
