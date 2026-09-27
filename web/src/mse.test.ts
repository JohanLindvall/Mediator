// The pure half of feeding a conversion to the element: reading the codec
// string out of the initialisation segment, and finding where that segment
// ends. Both are exercised against the box layout ffmpeg actually writes
// for a fragmented MP4 (measured on a live conversion: ftyp, moov with an
// avc1 sample entry carrying avcC and an mp4a one carrying esds, then moof).
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { FEED_RESUMES, FedSource, codecStringOf, initSegmentEnd, resumeAt } from './mse.ts';

function box(type: string, ...payload: Uint8Array[]): Uint8Array {
  const len = payload.reduce((n, p) => n + p.length, 0);
  const out = new Uint8Array(8 + len);
  new DataView(out.buffer).setUint32(0, out.length);
  out.set(new TextEncoder().encode(type), 4);
  let at = 8;
  for (const p of payload) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

function bytes(n: number, fill = 0): Uint8Array {
  return new Uint8Array(n).fill(fill);
}

/** A sample-description box holding the given entries. */
function stsd(...entries: Uint8Array[]): Uint8Array {
  const head = new Uint8Array(8); // version+flags, entry count
  new DataView(head.buffer).setUint32(4, entries.length);
  return box('stsd', head, ...entries);
}

function trak(sampleEntries: Uint8Array): Uint8Array {
  return box('trak', box('mdia', box('minf', box('stbl', sampleEntries))));
}

// A visual sample entry: 78 bytes of fields, then the children.
function avc1(profile: number, compat: number, level: number): Uint8Array {
  const avcC = box('avcC', new Uint8Array([1, profile, compat, level, 0xff, 0xe1]));
  return box('avc1', bytes(78), avcC);
}

// An audio sample entry: 28 bytes of fields, then the children.
function mp4a(): Uint8Array {
  return box('mp4a', bytes(28), box('esds', bytes(12)));
}

const ftyp = box(
  'ftyp',
  new TextEncoder().encode('iso5'),
  bytes(4),
  new TextEncoder().encode('iso5'),
);

test('mse: the codec string is read out of the initialisation segment', () => {
  // High profile at level 4.2, which is what the live conversion measured.
  const init = new Uint8Array([
    ...ftyp,
    ...box('moov', trak(stsd(avc1(100, 0, 42))), trak(stsd(mp4a()))),
  ]);
  assert.equal(codecStringOf(init), 'video/mp4; codecs="avc1.64002A,mp4a.40.2"');
  // Main profile at level 3.1, the hardware encoder's usual answer.
  const main = new Uint8Array([
    ...ftyp,
    ...box('moov', trak(stsd(avc1(77, 0x40, 31))), trak(stsd(mp4a()))),
  ]);
  assert.equal(codecStringOf(main), 'video/mp4; codecs="avc1.4D401F,mp4a.40.2"');
  // The picture alone still names a stream; a stream with no picture is
  // not one this feeds.
  const silent = new Uint8Array([...ftyp, ...box('moov', trak(stsd(avc1(100, 0, 42))))]);
  assert.equal(codecStringOf(silent), 'video/mp4; codecs="avc1.64002A"');
  assert.equal(codecStringOf(new Uint8Array([...ftyp, ...box('moov', trak(stsd(mp4a())))])), null);
  // No moov at all — a chunk that is not an initialisation segment.
  assert.equal(codecStringOf(ftyp), null);
});

test('mse: the initialisation segment ends where the first fragment begins', () => {
  const moov = box('moov', trak(stsd(avc1(100, 0, 42))));
  const moof = box('moof', bytes(16));
  const mdat = box('mdat', bytes(32));
  const whole = new Uint8Array([...ftyp, ...moov, ...moof, ...mdat]);
  assert.equal(initSegmentEnd(whole), ftyp.length + moov.length);
  // Not yet: the fragment has not arrived, so the segment cannot be known
  // to be complete — a chunk boundary can fall anywhere.
  assert.equal(initSegmentEnd(new Uint8Array([...ftyp, ...moov])), -1);
  assert.equal(initSegmentEnd(new Uint8Array([...ftyp, ...moov.subarray(0, 20)])), -1);
  // A 64-bit size is read as one, so a large mdat elsewhere in the file
  // does not throw the walk off.
  const big = new Uint8Array(16 + 8);
  new DataView(big.buffer).setUint32(0, 1);
  big.set(new TextEncoder().encode('mdat'), 4);
  new DataView(big.buffer).setBigUint64(8, BigInt(big.length));
  const withBig = new Uint8Array([...ftyp, ...moov, ...big, ...moof]);
  assert.equal(initSegmentEnd(withBig), ftyp.length + moov.length + big.length);
  const wideMoof = new Uint8Array(24);
  new DataView(wideMoof.buffer).setUint32(0, 1);
  wideMoof.set(new TextEncoder().encode('moof'), 4);
  new DataView(wideMoof.buffer).setBigUint64(8, 24n);
  assert.equal(initSegmentEnd(new Uint8Array([...ftyp, ...moov, ...wideMoof])), ftyp.length + moov.length);
});

test('mse: truncated codec records are rejected without throwing', () => {
  for (let size = 0; size < 4; size++) {
    const record = box('avc1', bytes(78), box('avcC', bytes(size)));
    const init = new Uint8Array([...ftyp, ...box('moov', trak(stsd(record)))]);
    assert.equal(codecStringOf(init), null);
  }
});

test('mse: a broken feed is picked up where its buffer ends', () => {
  // The stream began nine minutes into the film and the buffer reaches a
  // further 75 s, so that is where to ask for the conversion again — and
  // the new stream's own clock, which starts at zero, has to be shifted by
  // the same 75 s to land there.
  assert.deepEqual(resumeAt(540, 75, 0), { film: 615, offset: 75 });
  // From the start of the film the two are the same number.
  assert.deepEqual(resumeAt(0, 12.5, 3), { film: 12.5, offset: 12.5 });
  // Nothing buffered is nothing to resume from: the connection came apart
  // before it delivered anything, and asking again from the same place is
  // what the player's own retry is for.
  assert.equal(resumeAt(540, null, 0), null);
  assert.equal(resumeAt(540, 0, 0), null);
  // Bounded, or a conversion that fails the instant it is asked for would
  // be asked for ever.
  assert.equal(resumeAt(540, 75, FEED_RESUMES), null);
  assert.equal(resumeAt(540, 75, FEED_RESUMES - 1)?.film, 615);
});

test('mse: failed or stopped appends cancel their response', async (t) => {
  for (const action of ['error', 'stop'] as const) {
    await t.test(action, { timeout: 2000 }, async (t) => {
      const appended = Promise.withResolvers<void>();
      const cancelled = Promise.withResolvers<void>();
      const reported = Promise.withResolvers<string>();
      let ends = 0;
      let errors = 0;
      class Buffer extends EventTarget {
        updating = false;
        buffered = { length: 0 };
        appendBuffer(): void {
          this.updating = true;
          appended.resolve();
        }
      }
      const buffer = new Buffer();
      class Source extends EventTarget {
        static isTypeSupported(): boolean { return true; }
        readyState = 'open';
        addSourceBuffer(): Buffer { return buffer; }
        endOfStream(): void { this.readyState = 'ended'; }
      }
      const original = Object.getOwnPropertyDescriptor(globalThis, 'MediaSource');
      Object.defineProperty(globalThis, 'MediaSource', { configurable: true, value: Source });
      t.after(() => {
        if (original) Object.defineProperty(globalThis, 'MediaSource', original);
        else Reflect.deleteProperty(globalThis, 'MediaSource');
      });
      let source: Source;
      t.mock.method(URL, 'createObjectURL', (ms: Source) => { source = ms; return 'blob:fixture'; });
      const revoked = t.mock.method(URL, 'revokeObjectURL', () => {});
      const init = new Uint8Array([...ftyp, ...box('moov', trak(stsd(avc1(100, 0, 42)))), ...box('moof', bytes(16))]);
      const body = new ReadableStream<Uint8Array>({
        start(controller) { controller.enqueue(init); },
        cancel() { cancelled.resolve(); },
      });
      t.mock.method(globalThis, 'fetch', async () => new Response(body));
      const feed = new FedSource('/conversion', {
        onError(why) { errors++; reported.resolve(why); },
        onEnd() { ends++; },
      });
      t.after(() => feed.stop());
      feed.attach({ currentTime: 0 } as HTMLMediaElement);
      source!.dispatchEvent(new Event('sourceopen'));
      await appended.promise;
      if (action === 'error') {
        buffer.updating = false;
        buffer.dispatchEvent(new Event('error'));
        buffer.dispatchEvent(new Event('updateend'));
        assert.match(await reported.promise, /media buffer rejected/);
      } else {
        feed.stop();
        feed.stop();
        assert.equal(revoked.mock.callCount(), 1);
      }
      await cancelled.promise;
      // Let pump's finally and run's catch finish after cancellation.
      await new Promise<void>((resolve) => setImmediate(resolve));
      assert.equal(body.locked, false);
      assert.equal(errors, action === 'error' ? 1 : 0);
      assert.equal(ends, 0);
    });
  }
});
