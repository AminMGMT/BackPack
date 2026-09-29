/* The Logs dialog's reading of a journal line.
 *
 * Run with: node --test internal/webui/paneltest/
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseLine } from '../panel/js/lib/logline.js';

test('a short-iso journal line gives its clock and the engine message', () => {
  const r = parseLine('2026-09-29T17:59:21+03:30 ir-1 backpack[812]: 29-Sep 17:59:21 [WARNING] pool below target');
  assert.equal(r.when, '17:59:21');
  assert.equal(r.msg, 'pool below target');
  assert.equal(r.lv, 'warn');
});

test('the engine tag decides the level, not the words', () => {
  // "failed" would read as an error; the engine said INFO.
  const r = parseLine('2026-09-29T17:59:21+03:30 ir-1 backpack[812]: 29-Sep 17:59:21 [INFO] retried the failed dial, now up');
  assert.equal(r.lv, 'info');
});

test('a line the engine did not write keeps its text and a guessed level', () => {
  const r = parseLine('2026-09-29T17:59:21+03:30 ir-1 systemd[1]: backpack-a.service: Failed with result exit-code.');
  assert.equal(r.when, '17:59:21');
  assert.match(r.msg, /^backpack-a\.service: Failed/);
  assert.equal(r.lv, 'error');
});

test('the classic journal form still reads', () => {
  const r = parseLine('Sep 29 17:59:21 ir-1 backpack[812]: 29-Sep 17:59:21 [ERROR] bind: address already in use');
  assert.equal(r.when, '17:59:21');
  assert.equal(r.lv, 'error');
  assert.equal(r.msg, 'bind: address already in use');
});

test('journal notes pass through untouched', () => {
  const r = parseLine('-- No entries --');
  assert.equal(r.when, '');
  assert.equal(r.msg, '-- No entries --');
});
