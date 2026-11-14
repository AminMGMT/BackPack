import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statsOf, trendText } from '../panel/js/lib/trend.js';

/* ~8 MB over twelve minutes: 24 samples of roughly 11 KB/s, rising. */
const live = Array.from({ length: 24 }, (_, i) => 8000 + i * 600);

test('a realistic live series above the floor shows a percentage', () => {
  assert.match(trendText(statsOf(live), 'rate'), /^\d+\.\d%$/);
});

test('a near-idle live series shows no percentage', () => {
  assert.equal(trendText(statsOf([10, 40, 90]), 'rate'), '');
});

test('day windows keep the 64 KiB total floor', () => {
  assert.equal(trendText(statsOf([10000, 150000]), 'total'), '');
  assert.match(trendText(statsOf([100000, 150000]), 'total'), /^50\.0%$/);
});
