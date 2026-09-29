/* One journal line, taken apart.
 *
 * The server reads the journal as short-iso — "2026-09-29T17:59:21+03:30 host
 * backpack[12]: …" — and the pattern the Logs dialog used was written for the
 * classic "Sep 29 17:59:21" form, so it matched nothing: every row carried the
 * date, the host name and the process in front of the message, and no clock.
 * Inside that is the engine's own line, "29-Sep 17:59:21 [INFO] …", whose tag
 * is the level it was logged at — a better answer than guessing from words.
 *
 * Pure, so paneltest/logline.test.js can hold it to that.
 */

/* journald hands the panel free text, so without a tag the level is read from
   the line. The list is the failures a tunnel actually produces, not a generic
   word list: a bind clash and a refused dial are errors however phrased. */
function levelOf(line) {
  const s = line.toLowerCase();
  if (/error|fatal|panic|refused|failed|address already in use|no route to host|permission denied|cannot|could not/.test(s))
    return 'error';
  if (/warn|retry|flap|dropped|overflow|timeout|reverted/.test(s)) return 'warn';
  return 'info';
}

const TAGS = { trace: 'info', debug: 'info', info: 'info', warn: 'warn', warning: 'warn',
               error: 'error', fatal: 'error', panic: 'error' };

export function parseLine(text) {
  let when = '', msg = String(text);
  let m = msg.match(/^\d{4}-\d\d-\d\dT(\d\d:\d\d:\d\d)\S*\s+\S+\s+[^\s:]+:\s?(.*)$/);
  if (m) { when = m[1]; msg = m[2]; }
  else if ((m = msg.match(/^[A-Z][a-z]{2}\s+\d+\s+(\d\d:\d\d:\d\d)\s+\S+\s+[^\s:]+:\s?(.*)$/))) {
    when = m[1]; msg = m[2];
  }
  let lv = '';
  const e = msg.match(/^\d\d-[A-Za-z]{3}\s+(\d\d:\d\d:\d\d)\s+\[([A-Za-z]+)\]\s?(.*)$/);
  if (e) {
    when = when || e[1];
    lv = TAGS[e[2].toLowerCase()] || '';
    msg = e[3];
  }
  return { when, msg, lv: lv || levelOf(msg) };
}
