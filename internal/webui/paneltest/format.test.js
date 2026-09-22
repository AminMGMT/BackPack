/* The panel's formatting, which every screen renders numbers through.
 *
 * Run with: node --test internal/webui/paneltest/
 *
 * These live outside panel/ on purpose. `//go:embed panel/js` takes the whole
 * directory, so a test file in there would be compiled into the binary and
 * served to browsers.
 *
 * Nothing here needs a DOM, a browser or a dependency. That is the whole
 * selection rule for what is tested on this side: the modules that decide what
 * a number *means* are pure, they are shared by every screen, and a unit
 * mistake in one of them is wrong on all of them at once.
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bytes, speed, pct, ping, ago, clock, kindLabel, flag } from '../panel/js/lib/format.js';

test('bytes moves up a unit exactly at the boundary', () => {
  assert.equal(bytes(0), '0 B');
  assert.equal(bytes(1023), '1023 B');
  assert.equal(bytes(1024), '1 KB');
  assert.equal(bytes(1024 * 1024), '1.0 MB');
  assert.equal(bytes(1024 ** 3), '1.0 GB');
  assert.equal(bytes(1024 ** 4), '1.00 TB');
});

test('bytes treats anything unparseable as nothing rather than NaN', () => {
  assert.equal(bytes(undefined), '0 B');
  assert.equal(bytes(null), '0 B');
  assert.equal(bytes('not a number'), '0 B');
});

/* A link is sold in bits and the panel quotes it in bits. Rendering bytes per
 * second as if they were bits understates a link eightfold, which reads as a
 * tunnel that is not performing. */
test('speed converts bytes per second into bits', () => {
  assert.equal(speed(1000), '8 Kb/s');
  assert.equal(speed(125000), '1.0 Mb/s');
  assert.equal(speed(125000000), '1.00 Gb/s');
  assert.equal(speed(0), '0 b/s');
});

test('pct keeps the digits it is asked for', () => {
  assert.equal(pct(12.345), '12%');
  assert.equal(pct(12.345, 1), '12.3%');
  assert.equal(pct(undefined), '0%');
});

/* -1 is the server saying "this does not apply here", not a measurement of
 * minus one millisecond. */
test('ping distinguishes not-applicable from a measurement', () => {
  assert.equal(ping(-1), '—');
  assert.equal(ping(undefined), '—');
  assert.equal(ping(0), '0 ms');
  assert.equal(ping(42), '42 ms');
});

test('ago reads in the largest unit that still says something', () => {
  const now = Math.floor(Date.now() / 1000);
  assert.equal(ago(now), 'just now');
  assert.equal(ago(now - 59), 'just now');
  assert.equal(ago(now - 60), '1 min ago');
  assert.equal(ago(now - 3600), '1 h ago');
  assert.equal(ago(now - 86400), '1 d ago');
});

/* A clock skew or a server timestamp slightly ahead must not render as a
 * negative age. */
test('ago never reads as being in the future', () => {
  assert.equal(ago(Math.floor(Date.now() / 1000) + 600), 'just now');
});

test('clock pads both halves', () => {
  const d = new Date(2026, 0, 2, 3, 4, 0);
  assert.equal(clock(Math.floor(d.getTime() / 1000)), '03:04');
});

/* The line under a tunnel's name on its card. A tunnel with no carrier still
 * has to say which direction it runs in. */
test('kindLabel names the direction and the carrier', () => {
  assert.equal(kindLabel({ direction: 'direct', carrier: 'pck' }), 'direct pck');
  assert.equal(kindLabel({ transport: 'wss' }), 'reverse wss');
  assert.equal(kindLabel({ direction: 'reverse' }), 'reverse ');
});

test('flag renders a two-letter country and nothing else', () => {
  assert.equal(flag('ir'), '🇮🇷');
  assert.equal(flag('DE'), '🇩🇪');
  assert.equal(flag(''), '');
  assert.equal(flag('xyz'), '');
  assert.equal(flag(undefined), '');
});
