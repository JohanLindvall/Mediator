import assert from 'node:assert/strict';
import test from 'node:test';
import { CollectionSource, LibrarySource } from './sources.ts';
import type { QueryState } from './query.ts';
import type { Counts, Item, Result } from './types.gen.ts';

const query: QueryState = { kind: '', q: '', sort: 'mtime', desc: true };
const counts: Counts = {
  video: 0, image: 0, audio: 0, playlist: 0, albums: 0, audiobooks: 0,
  artists: 0, genres: 0, series: 0, started: 0, watched: 0, played: 0, total: 0,
};
const item: Item = { id: 'one', name: 'one.mp3', path: 'one.mp3', kind: 'audio', size: 1, mtime: 1 };
const answer = (items: Item[], total = items.length): Result => ({ items, total, counts, version: 1 });
const turn = () => new Promise<void>((resolve) => setImmediate(resolve));
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test('an empty library fetches again when files arrive', async () => {
  let calls = 0;
  const source = new LibrarySource(async () => answer(++calls === 1 ? [] : [item]));
  source.setQuery(query);
  await turn();
  assert.equal(source.count(), 0);
  source.invalidate();
  await turn();
  assert.equal(calls, 2);
  assert.equal(source.count(), 1);
  assert.equal(source.get(0)?.id, 'one');
});

test('a failed first page stops its loading placeholders and can be retried', async () => {
  const pending = deferred<Result>();
  const source = new LibrarySource(() => pending.promise);
  const drawn: number[] = [];
  let errors = 0;
  source.onUpdate = () => drawn.push(source.count());
  source.onError = () => { errors++; };
  source.setQuery(query);
  assert.equal(source.count(), -1);
  pending.reject(new Error('offline'));
  await turn();
  assert.equal(source.count(), 0);
  assert.equal(drawn.at(-1), 0);
  assert.equal(errors, 1);
});

test('a redraw after a failed page does not create a fetch loop', async () => {
  let calls = 0;
  const source = new LibrarySource(async () => {
    if (++calls === 1) return answer([item], 400);
    throw new Error('offline');
  });
  source.setQuery(query);
  await turn();
  source.onUpdate = () => source.need(200, 250);
  source.need(200, 250);
  await turn();
  assert.equal(calls, 2);
});

test('obsolete searches are cancelled and cannot replace the current results', async () => {
  const old = deferred<Result>();
  let signal: AbortSignal | undefined;
  let calls = 0;
  const source = new LibrarySource((_q, s) => {
    if (++calls === 1) { signal = s; return old.promise; }
    return Promise.resolve(answer([item]));
  });
  source.setQuery(query);
  source.setQuery({ ...query, q: 'new' });
  assert.equal(signal?.aborted, true);
  await turn();
  old.resolve(answer([]));
  await turn();
  assert.equal(source.get(0)?.id, 'one');
});

test('rows beyond a shrunken listing cannot come from stale pages', async () => {
  let empty = false;
  const source = new LibrarySource(async () => empty ? answer([]) : answer([item], 201));
  source.setQuery(query);
  await turn();
  source.need(200, 200);
  await turn();
  assert.equal(source.get(200)?.id, 'one');
  empty = true;
  source.invalidate();
  await turn();
  assert.equal(source.get(200), undefined);
});

test('collection reset cancels the pending request and ignores its answer', async () => {
  const pending = deferred<{ items: string[] }>();
  let signal: AbortSignal | undefined;
  const source = new CollectionSource<string>((_q, s) => { signal = s; return pending.promise; });
  source.load(query);
  assert.equal(source.count(), -1);
  source.reset();
  assert.equal(signal?.aborted, true);
  pending.resolve({ items: ['outdated'] });
  await turn();
  assert.equal(source.count(), 0);
  assert.equal(source.items, null);
});
