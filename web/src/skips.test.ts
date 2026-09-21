// Skipping a show's credits: which marks apply, when a skip is on offer, and
// where the next episode begins. Each of these is a way to throw a viewer
// out of a scene they wanted, so each is pinned.
import assert from 'node:assert/strict';
import { test } from 'node:test';

import { SKIP_MARGIN_S, marksEmpty, skipOffer, startAfterIntro } from './skips.ts';

test('skips: nothing is nothing, whatever shape it comes in', () => {
  assert.equal(marksEmpty(null), true);
  assert.equal(marksEmpty({}), true);
  assert.equal(marksEmpty({ introStart: 5, introEnd: 5 }), true);
  assert.equal(marksEmpty({ outro: 30 }), false);
  assert.equal(marksEmpty({ introStart: 0, introEnd: 60 }), false);
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
