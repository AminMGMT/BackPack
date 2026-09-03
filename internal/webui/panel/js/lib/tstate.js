/* What the server means by a tunnel's state.
 *
 * manage.Health reports "online", "offline" or "stopped", and the panel adds
 * "unknown" when it has not been told. The screens used to compare against
 * "running", which nothing on this codebase has ever produced: every screen
 * therefore treated a working tunnel as a dead one — the card said Stopped, the
 * overview counted it among the ones that are not running, and the speed test
 * and the link test refused to offer it at all.
 *
 * "offline" is its own answer and not a synonym for stopped: the service is up
 * and the configuration is live, but nothing is answering on the other side.
 * Saying "Stopped" there sends somebody to restart a service that is running.
 */
export const UP = 'online';

export const isUp = t => !!t && t.state === UP;

export const stateLabel = t => ({
  online: 'Online',
  offline: 'Offline',
  stopped: 'Stopped',
}[t && t.state] || 'Unknown');

/* Three shades, because there are three things to say: it works, it is up but
 * unanswered, it is not up at all. */
export const stateTone = t => (isUp(t) ? 'ok' : (t && t.state === 'offline' ? 'warn' : 'off'));
