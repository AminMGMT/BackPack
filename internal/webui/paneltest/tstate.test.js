/* What the panel believes a tunnel's state means.
 *
 * This module exists because every screen used to decide for itself, and they
 * decided wrongly in the same way: they compared against "running", which
 * nothing in this codebase has ever produced. Every screen therefore showed a
 * working tunnel as a dead one — the card said Stopped, the overview counted it
 * among the ones that are not running, and the link test refused to offer it.
 *
 * So these tests are about the vocabulary, not about formatting. Each one names
 * a state the server really sends.
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isUp, serviceDown, stateLabel, stateTone } from '../panel/js/lib/tstate.js';

/* The state string itself is not exported — every screen that decided for
 * itself what it was got it wrong, so the only thing a screen may use is this
 * interface. */
test('isUp is true only for online', () => {
  assert.equal(isUp({ state: 'online' }), true);
  assert.equal(isUp({ state: 'offline' }), false);
  assert.equal(isUp({ state: 'stopped' }), false);
  assert.equal(isUp({ state: 'running' }), false, 'no server has ever sent "running"');
  assert.equal(isUp({}), false);
  assert.equal(isUp(undefined), false);
});

/* offline is its own answer, not a synonym for stopped: the service is up and
 * the configuration is live, but nothing is answering on the other side.
 * Labelling it "Stopped" sends somebody to restart a service that is running. */
test('every state the server sends has its own label', () => {
  assert.equal(stateLabel({ state: 'online' }), 'Online');
  assert.equal(stateLabel({ state: 'offline' }), 'Offline');
  assert.equal(stateLabel({ state: 'stopped' }), 'Stopped');
  assert.equal(stateLabel({ state: 'something new' }), 'Unknown');
  assert.equal(stateLabel(undefined), 'Unknown');
});

/* A tunnel that is up and delivering into nothing. The state is still online
 * and that is not a mistake — what is wrong is one hop further on. */
test('a tunnel whose far service is refusing says so instead of saying online', () => {
  const t = { state: 'online', serviceDown: true };
  assert.equal(serviceDown(t), true);
  assert.equal(isUp(t), true, 'the tunnel itself is still up');
  assert.equal(stateLabel(t), 'No service');
  assert.equal(stateTone(t), 'warn');
});

test('the three tones say three different things', () => {
  assert.equal(stateTone({ state: 'online' }), 'ok');
  assert.equal(stateTone({ state: 'offline' }), 'warn');
  assert.equal(stateTone({ state: 'stopped' }), 'off');
  assert.equal(stateTone(undefined), 'off');
});

test('serviceDown is false rather than undefined when the server says nothing', () => {
  assert.equal(serviceDown({ state: 'online' }), false);
  assert.equal(serviceDown(undefined), false);
});
