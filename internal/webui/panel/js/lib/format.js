/* Formatting.
 *
 * The server sends raw numbers — bytes, bytes per second, unix seconds — and
 * every screen has to render them the same way, so it is done in one place.
 */

const KB = 1024, MB = KB * 1024, GB = MB * 1024, TB = GB * 1024;

export function bytes(n) {
  n = Number(n) || 0;
  if (n >= TB) return (n / TB).toFixed(2) + ' TB';
  if (n >= GB) return (n / GB).toFixed(1) + ' GB';
  if (n >= MB) return (n / MB).toFixed(1) + ' MB';
  if (n >= KB) return (n / KB).toFixed(0) + ' KB';
  return n + ' B';
}

/* Link speed is quoted in bits, the way a link is sold. */
export function speed(bytesPerSec) {
  const bits = (Number(bytesPerSec) || 0) * 8;
  if (bits >= 1e9) return (bits / 1e9).toFixed(2) + ' Gb/s';
  if (bits >= 1e6) return (bits / 1e6).toFixed(1) + ' Mb/s';
  if (bits >= 1e3) return (bits / 1e3).toFixed(0) + ' Kb/s';
  return Math.round(bits) + ' b/s';
}

export function pct(n, digits = 0) { return (Number(n) || 0).toFixed(digits) + '%'; }

/* -1 is the server's "not applicable", not a measurement. */
export function ping(ms) { return ms === -1 || ms === undefined ? '—' : ms + ' ms'; }

export function clock(unixSeconds) {
  const d = new Date(unixSeconds * 1000);
  return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
}

export function ago(unixSeconds) {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unixSeconds));
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + ' min ago';
  if (s < 86400) return Math.floor(s / 3600) + ' h ago';
  return Math.floor(s / 86400) + ' d ago';
}

/* "reverse wss", "direct pck" — what the card says under the name. */
export function kindLabel(t) {
  const dir = t.direction === 'direct' ? 'direct' : 'reverse';
  return dir + ' ' + (t.carrier || t.transport || '');
}

export function flag(cc) {
  if (!cc || cc.length !== 2) return '';
  return String.fromCodePoint(...[...cc.toUpperCase()].map(c => 0x1f1a5 + c.charCodeAt(0)));
}
