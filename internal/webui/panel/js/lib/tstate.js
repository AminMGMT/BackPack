/* What the server means by a tunnel's state.
 *
 * manage.Health reports "online", "offline" or "stopped", and the panel adds
 * "unknown" when it has not been told. The screens used to compare against
 * "running", which nothing on this codebase has ever produced: every screen
 * therefore treated a working tunnel as a dead one — the card said Stopped, the
 * overview counted it among the ones that are not running, and the link test
 * refused to offer it at all.
 *
 * "offline" is its own answer and not a synonym for stopped: the service is up
 * and the configuration is live, but nothing is answering on the other side.
 * Saying "Stopped" there sends somebody to restart a service that is running.
 */
/* Not exported: the state string is this module's business, and every screen
 * that decided for itself got it wrong. Ask isUp. */
const UP = 'online';

export const isUp = t => !!t && t.state === UP;

/* Stopped on purpose — the service is not running — as against offline. */
export const isStopped = t => !!t && t.state === 'stopped';

/* A tunnel that is online and delivering into nothing.
 *
 * The state is still "online" and that is not a mistake: the tunnel is up, and
 * calling it offline would send somebody to restart a service that is running
 * and working. What is wrong is one hop further on — the service it forwards
 * to, on the other machine, is refusing every connection. The server sends the
 * sentence; this is the one place that decides what to do with it. */
export const serviceDown = t => !!(t && t.serviceDown);

/* A tunnel that has carried its traffic limit is offline on purpose: the
 * engine stopped it and holds it until the limit is raised. Said as that, not
 * as "Offline", which sends somebody looking for a fault. */
const limitReached = t => !!(t && t.quotaHit);

export const stateLabel = t => (limitReached(t) ? 'Limit reached' : serviceDown(t) ? 'No service' : ({
  online: 'Online',
  offline: 'Offline',
  stopped: 'Stopped',
}[t && t.state] || 'Unknown'));

/* Three shades, because there are three things to say: it works, it is up but
 * unanswered, it is not up at all. A tunnel whose far service is missing takes
 * the middle one — it is up, and it is not carrying anything. */
export const stateTone = t =>
  (limitReached(t) || serviceDown(t) ? 'warn' : (isUp(t) ? 'ok' : (t && t.state === 'offline' ? 'warn' : 'off')));
