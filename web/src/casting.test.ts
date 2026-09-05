/**
 * The transport a television is driven through: the clock carried between
 * polls, when the set is asked, and what its answers mean. The music bar
 * and the player each used to spell this out, and the two had begun to
 * disagree — so the rules are pinned here once, against a fake set and a
 * fake clock.
 *
 * Run with `node --test` (Node strips the types); no test framework involved.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { CastTransport, type CastAnswer, type CastHooks, type CastOptions } from './casting.ts';

/** Real time, for the answers a fake set gives through a promise. */
const settle = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

/** A set that answers what it is told to, and remembers what it was asked. */
function fakeSet() {
  const calls: string[] = [];
  let answer: CastAnswer | null = null;
  let asked = 0;
  return {
    calls,
    get asked() {
      return asked;
    },
    says(st: CastAnswer | null) {
      answer = st;
    },
    cast: {
      status: async () => {
        asked++;
        return answer;
      },
      queueNext: async (item: string) => `uri:${item}`,
      play: () => {
        calls.push('play');
      },
      pause: () => {
        calls.push('pause');
      },
      seek: (s: number) => {
        calls.push(`seek:${s}`);
      },
    },
  };
}

/** A clock that ticks when the test says so. */
function fakeClock() {
  let fn: (() => void) | null = null;
  let sets = 0;
  return {
    timers: {
      setInterval: (f: () => void) => {
        fn = f;
        sets++;
        return sets;
      },
      clearInterval: () => {
        fn = null;
      },
    },
    tick(n = 1) {
      for (let i = 0; i < n; i++) fn?.();
    },
    get running() {
      return fn != null;
    },
    get sets() {
      return sets;
    },
  };
}

function harness(opts: Omit<CastOptions, 'timers'>) {
  const set = fakeSet();
  const clock = fakeClock();
  const events: string[] = [];
  const shown = { pos: -1, dur: -1 };
  const seeking = { on: false };
  const ref: { tv?: CastTransport } = {};
  const hooks: CastHooks = {
    seeking: () => seeking.on,
    onClock: (pos, dur) => {
      shown.pos = pos;
      shown.dur = dur;
    },
    onPlaying: () => events.push('playing'),
    onEnded: () => events.push('ended'),
    onStopped: () => events.push(`stopped@${ref.tv?.dur}`),
    onAdvanced: (uri) => events.push(`advanced:${uri}`),
    onPoll: () => events.push('poll'),
  };
  const tv = new CastTransport(set.cast, hooks, { ...opts, timers: clock.timers });
  ref.tv = tv;
  return { tv, set, clock, events, shown, seeking };
}

/** The events that say something happened, without the poll's own heartbeat. */
const told = (events: string[]): string[] => events.filter((e) => e !== 'poll');

test('cast: the clock carries forward while playing, and not while paused or seeking', () => {
  const { tv, set, clock, shown, seeking } = harness({ pollEvery: 100 });
  tv.begin(10, 300);
  tv.run();
  clock.tick(3);
  assert.equal(tv.pos, 13);
  assert.deepEqual(shown, { pos: 13, dur: 300 });

  tv.pause();
  clock.tick(2);
  assert.equal(tv.pos, 13, 'paused, the clock stands');

  tv.play();
  seeking.on = true;
  clock.tick(2);
  assert.equal(tv.pos, 13, 'a finger on the bar holds the clock');
  seeking.on = false;
  clock.tick(1);
  assert.equal(tv.pos, 14);

  // Each request went to the set once; pressing play while playing sends nothing.
  tv.play();
  assert.deepEqual(set.calls, ['pause', 'play']);
});

test('cast: seek and toggle move the readout at once and ask the set', () => {
  const { tv, set } = harness({ pollEvery: 4 });
  tv.begin(0, 100);
  tv.seek(42);
  assert.equal(tv.pos, 42);
  tv.toggle();
  assert.equal(tv.playing, false);
  tv.toggle();
  assert.equal(tv.playing, true);
  assert.deepEqual(set.calls, ['seek:42', 'pause', 'play']);
});

test('cast: the set is asked every pollEvery ticks, and every tick inside the end window', async () => {
  const { tv, set, clock } = harness({ pollEvery: 3, endWindow: 6 });
  set.says({ state: 'PLAYING' });
  tv.begin(0, 100);
  tv.run();
  clock.tick(2);
  await settle();
  assert.equal(set.asked, 0);
  clock.tick(1);
  await settle();
  assert.equal(set.asked, 1, 'the third tick asks');
  clock.tick(3);
  await settle();
  assert.equal(set.asked, 2, 'and the sixth');
  // Six seconds from the end, every tick asks: a gap between songs is heard.
  tv.seek(93);
  clock.tick(1);
  await settle();
  assert.equal(set.asked, 3);
  clock.tick(1);
  await settle();
  assert.equal(set.asked, 4);
});

test('cast: without an end window the cadence holds to the end', async () => {
  const { tv, set, clock } = harness({ pollEvery: 4 });
  set.says({ state: 'PLAYING' });
  tv.begin(95, 100);
  tv.run();
  clock.tick(3);
  await settle();
  assert.equal(set.asked, 0);
  clock.tick(1);
  await settle();
  assert.equal(set.asked, 1);
});

test('cast: an answer corrects the clock, and the set is believed about its length', async () => {
  const { tv, set, clock, events, shown } = harness({ pollEvery: 1 });
  set.says({ state: 'PLAYING', position: 50.4, duration: 200 });
  tv.begin(0, 100);
  tv.run();
  clock.tick(1);
  await settle();
  assert.equal(tv.pos, 50.4);
  assert.equal(tv.dur, 200);
  assert.equal(tv.playing, true);
  assert.equal(tv.seen, true);
  assert.deepEqual(shown, { pos: 50.4, dur: 200 });
  assert.deepEqual(events, ['playing', 'poll']);

  // Paused: the clock stands where the set says.
  set.says({ state: 'PAUSED_PLAYBACK', position: 50.4 });
  clock.tick(1);
  await settle();
  assert.equal(tv.playing, false);
  assert.equal(tv.pos, 50.4);
});

test('cast: a set still opening the file is not believed about zero, and has not ended', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 1 });
  set.says({ state: 'STOPPED', position: 0 });
  tv.begin(30, 100);
  tv.run();
  clock.tick(1);
  await settle();
  assert.equal(tv.pos, 31, 'a zero from a set that has not opened the file is not a position');
  assert.equal(tv.seen, false);
  assert.deepEqual(told(events), []);
});

test('cast: ended and stopped are told apart by where the clock stood', async () => {
  const near = harness({ pollEvery: 1 });
  near.set.says({ state: 'PLAYING', position: 95 });
  near.tv.begin(0, 100);
  near.tv.run();
  near.clock.tick(1);
  await settle();
  near.set.says({ state: 'STOPPED' });
  near.clock.tick(1);
  await settle();
  assert.deepEqual(told(near.events), ['playing', 'ended']);

  const short = harness({ pollEvery: 1 });
  short.set.says({ state: 'PLAYING', position: 40 });
  short.tv.begin(0, 100);
  short.tv.run();
  short.clock.tick(1);
  await settle();
  short.set.says({ state: 'NO_MEDIA_PRESENT' });
  short.clock.tick(1);
  await settle();
  assert.deepEqual(told(short.events), ['playing', 'stopped@100']);
});

test('cast: the verdict is reached by the length the set reports, and the caller sees that length', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 1 });
  set.says({ state: 'PLAYING', position: 95 });
  tv.begin(0, 100);
  tv.run();
  clock.tick(1);
  await settle();
  // Near the end of what the library measured, a third of the way into
  // what the set measured: a stop, and the hook is told the set's number.
  set.says({ state: 'STOPPED', duration: 300 });
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing', 'stopped@300']);
});

test('cast: a stop with something queued waits for the handover, then gives up', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 1, handover: 3 });
  tv.begin(0, 100);
  tv.run();
  await tv.queueNext('b');
  assert.equal(tv.nextUri, 'uri:b');
  set.says({ state: 'PLAYING', position: 98 });
  clock.tick(1);
  await settle();
  set.says({ state: 'STOPPED' });
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing'], 'the first stop is probably the handover');
  assert.equal(tv.playing, false, 'and the clock stands meanwhile');
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing']);
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing', 'ended'], 'the third is taken at its word');
});

test('cast: a set seen playing again re-arms the handover wait', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 1, handover: 2 });
  tv.begin(0, 100);
  tv.run();
  await tv.queueNext('b');
  set.says({ state: 'PLAYING', position: 98 });
  clock.tick(1);
  await settle();
  set.says({ state: 'STOPPED' });
  clock.tick(1);
  await settle();
  set.says({ state: 'PLAYING', position: 99 });
  clock.tick(1);
  await settle();
  set.says({ state: 'STOPPED' });
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing', 'playing'], 'one stop since it last played');
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing', 'playing', 'ended']);
});

test('cast: with nothing queued a stop is taken at its word at once', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 1, handover: 5 });
  set.says({ state: 'PLAYING', position: 98 });
  tv.begin(0, 100);
  tv.run();
  clock.tick(1);
  await settle();
  set.says({ state: 'STOPPED' });
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing', 'ended']);
});

test('cast: the set moving on to what it was handed is followed, not re-sent', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 1 });
  tv.begin(0, 100);
  tv.run();
  await tv.queueNext('b');
  set.says({ state: 'PLAYING', uri: 'uri:a', position: 50 });
  clock.tick(1);
  await settle();
  assert.deepEqual(events, ['playing', 'poll'], 'another URI is this file');
  set.says({ state: 'PLAYING', uri: 'uri:b', position: 2 });
  clock.tick(1);
  await settle();
  assert.deepEqual(events, ['playing', 'poll', 'advanced:uri:b']);
  assert.equal(tv.pos, 51, 'the clock is the caller\'s to restart for the next file');
});

test('cast: stop() ends the clock and drops an answer in flight', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 3 });
  set.says({ state: 'PLAYING', position: 50 });
  tv.begin(0, 100);
  tv.run();
  tv.run();
  assert.equal(clock.sets, 1, 'started twice is started once');
  clock.tick(3);
  assert.equal(set.asked, 1);
  tv.stop();
  await settle();
  assert.deepEqual(events, [], 'the answer arrived after the poll was stopped');
  assert.equal(tv.pos, 3);
  assert.equal(clock.running, false);
  tv.run();
  assert.equal(clock.sets, 2, 'and it can be started again');
});

test('cast: begin() is a new file — nothing seen of it, and answers about the last one are dropped', async () => {
  const { tv, set, clock, events } = harness({ pollEvery: 1 });
  set.says({ state: 'PLAYING', position: 95 });
  tv.begin(0, 100);
  tv.run();
  clock.tick(1);
  await settle();
  assert.equal(tv.seen, true);
  const queued = tv.queueNext('b');

  // The last file's final poll is in flight when the next file begins: the
  // set says STOPPED about the old one, and that must not end the new one.
  set.says({ state: 'STOPPED' });
  clock.tick(1);
  tv.begin(0, 200);
  await queued;
  await settle();
  assert.equal(tv.nextUri, null, 'what the set accepted was queued behind the last file');
  assert.equal(tv.seen, false);
  assert.equal(tv.pos, 0);
  assert.equal(tv.dur, 200);
  assert.equal(tv.playing, true);
  assert.deepEqual(told(events), ['playing'], 'the stale answer landed on nothing');

  // And a STOPPED about the new file, before it has played, is a set still opening it.
  clock.tick(1);
  await settle();
  assert.deepEqual(told(events), ['playing']);
  assert.equal(tv.seen, false);
});
