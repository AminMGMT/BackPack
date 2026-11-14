/* The numbers behind a card's trend badge: pure, so they can be tested.
 *
 * What a series holds depends on the window. The live and 24-hour windows are
 * RATES (bytes per second); the 7- and 30-day windows are TOTALS (bytes per
 * day). A percentage needs a baseline worth dividing by, and what is "worth
 * dividing by" is a different number for each: 64 KiB is a sensible floor for a
 * total, but as a rate it means the first sample has to be 512 kbit/s — a
 * tunnel carrying 8 MB over twelve minutes never gets there, so the badge
 * never showed a figure. */

/* Under this, the window is flat. Matches the reference design's own dead
   zone: without one, a card twitches between up and down forever. */
export const NEUTRAL_PCT = 0.5;

/* Smallest baseline that gets a percentage: 0.01 -> 0.15 MB reads "1486.3%",
   which is arithmetic, not news. Below it the badge keeps its arrow only. */
const TREND_FLOOR = { total: 64 * 1024, rate: 1024 };

export function statsOf(vals) {
  if (!vals.length) return null;
  const first = vals[0], last = vals[vals.length - 1];
  const prev = vals.length > 1 ? vals[vals.length - 2] : first;
  const sum = vals.reduce((a, b) => a + b, 0);
  const net = last - first;
  return {
    net, step: last - prev, base: Math.abs(first),
    pct: first ? (net / first) * 100 : 0,
    peak: Math.max(...vals), low: Math.min(...vals), avg: sum / vals.length,
  };
}

export const trendText = (st, unit) =>
  st.base < (TREND_FLOOR[unit] ?? TREND_FLOOR.total) ? '' : Math.abs(st.pct).toFixed(1) + '%';
