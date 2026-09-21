// Skipping a show's credits: which marks apply, when a skip is on offer, and
// where the next episode begins. Each of these is a way to throw a viewer
// out of a scene they wanted, so each is pinned.
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { SKIP_MARGIN_S, effectiveMarks, parseClock, skipOffer, startAfterIntro } from './skips.ts';

test('skips: the narrowest scope that says anything wins', () => {
  const season = { introStart: 0, introEnd: 90, outro: 60 };
  const series = { introStart: 0, introEnd: 80 };
  assert.deepEqual(effectiveMarks({ season, series }), season);
  assert.deepEqual(effectiveMarks({ series }), series);
  // An episode marked on its own outranks its season.
  const episode = { introStart: 30, introEnd: 100 };
  assert.deepEqual(effectiveMarks({ episode, season, series }), episode);
  // An empty record at a narrower scope says nothing and is passed over.
  assert.deepEqual(effectiveMarks({ episode: {}, season }), season);
  assert.deepEqual(effectiveMarks({ episode: { introStart: 5, introEnd: 5 }, season }), season);
  assert.equal(effectiveMarks({}), null);
  assert.equal(effectiveMarks(null), null);
});

test('skips: what is offered where', () => {
  const m = { introStart: 20, introEnd: 110, outro: 60 };
  const dur = 1320;
  assert.equal(skipOffer(m, 0, dur), null, 'the cold open before the intro');
  assert.equal(skipOffer(m, 20, dur), 'intro');
  assert.equal(skipOffer(m, 100, dur), 'intro');
  assert.equal(skipOffer(m, 110 - SKIP_MARGIN_S, dur), null, 'the tail of the intro is not worth a button');
  assert.equal(skipOffer(m, 600, dur), null);
  assert.equal(skipOffer(m, 1260, dur), 'credits');
  assert.equal(skipOffer(m, 1319, dur), 'credits');
  assert.equal(skipOffer(m, 1320, dur), null, 'the end itself is the end');
  // Nothing is offered until the length is known: the credits are measured
  // from it.
  assert.equal(skipOffer(m, 1260, 0), null);
  // An intro alone, credits alone, nothing at all.
  assert.equal(skipOffer({ introStart: 0, introEnd: 60 }, 1300, dur), null);
  assert.equal(skipOffer({ outro: 60 }, 30, dur), null);
  assert.equal(skipOffer({ outro: 60 }, 1300, dur), 'credits');
  assert.equal(skipOffer(null, 30, dur), null);
});

test('skips: the next episode begins past its intro, and only then', () => {
  const m = { introStart: 0, introEnd: 90 };
  assert.equal(startAfterIntro(m, 0), 90);
  assert.equal(startAfterIntro(m, 45), 90);
  // Already past it — resumed from where it was left — is left alone.
  assert.equal(startAfterIntro(m, 600), 600);
  // A cold open before the intro is watched; the intro is offered when it
  // comes.
  const cold = { introStart: 60, introEnd: 150 };
  assert.equal(startAfterIntro(cold, 0), 0);
  assert.equal(startAfterIntro(cold, 70), 150);
  // No intro marked, nothing to skip to.
  assert.equal(startAfterIntro({ outro: 60 }, 0), 0);
  assert.equal(startAfterIntro(null, 12), 12);
});

test('skips: a clock as a person writes one', () => {
  assert.equal(parseClock('1:32'), 92);
  assert.equal(parseClock('0:07'), 7);
  assert.equal(parseClock('1:02:03'), 3723);
  assert.equal(parseClock('92'), 92);
  assert.equal(parseClock(' 92.5 '), 92.5);
  assert.equal(parseClock(''), 0);
  assert.ok(Number.isNaN(parseClock('a minute')));
  assert.ok(Number.isNaN(parseClock('1:2:3:4')));
  assert.ok(Number.isNaN(parseClock('-5')));
});
