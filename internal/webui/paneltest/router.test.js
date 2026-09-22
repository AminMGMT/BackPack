/* The panel's routing.
 *
 * Every screen in the panel is reached through this, and a route that stops
 * matching is a screen that silently redirects to the overview — the router
 * falls back to "/" for anything it cannot match, so a broken pattern looks
 * like a working panel that will not open one of its pages.
 *
 * The module reads `location` and `history` in the parts that navigate, so
 * those are stubbed. Everything asserted here is the matching itself, which is
 * pure.
 */

import { test, before } from 'node:test';
import assert from 'node:assert/strict';

/* The smallest browser the module will accept. It is stubbed rather than
 * emulated: nothing below asserts on navigation, only on what a path resolves
 * to. */
globalThis.location = { hash: '#/' };
globalThis.history = { replaceState() {}, pushState() {} };
globalThis.window = { addEventListener() {} };

const router = await import('../panel/js/router.js');

before(() => {
  router.route('/', () => 'overview');
  router.route('/tunnels', () => 'tunnels');
  router.route('/t/:name/metrics', () => 'metrics');
  router.route('/t/:name/logs', () => 'logs');
  router.route('/settings', () => 'settings');
});

test('a plain path matches its own route and nothing else', () => {
  assert.equal(router.match('/').pattern, '/');
  assert.equal(router.match('/tunnels').pattern, '/tunnels');
  assert.equal(router.match('/settings').pattern, '/settings');
});

test('a parameter is captured and decoded', () => {
  const hit = router.match('/t/fr-relay/metrics');
  assert.equal(hit.pattern, '/t/:name/metrics');
  assert.equal(hit.params.name, 'fr-relay');
});

/* Tunnel names are free text. A name with a space or a slash in it has to come
 * back out of the URL as it went in, or the screen asks the server about a
 * tunnel that does not exist. */
test('a name that had to be escaped comes back as it was', () => {
  assert.equal(router.match('/t/' + encodeURIComponent('my tunnel') + '/logs').params.name, 'my tunnel');
  assert.equal(router.match('/t/' + encodeURIComponent('a+b') + '/logs').params.name, 'a+b');
});

/* A parameter matches one segment. A name containing a slash would otherwise
 * swallow the screen after it and open the wrong page. */
test('a parameter stops at the next slash', () => {
  assert.equal(router.match('/t/a/b/metrics'), null);
});

test('an unknown path matches nothing, so the router can fall back', () => {
  assert.equal(router.match('/nothing-here'), null);
  assert.equal(router.match('/t/fr-relay'), null);
});

test('parse splits the hash into a path and a query', () => {
  const { path, query } = router.parse('#/t/fr-relay/logs?end=peer&lines=200');
  assert.equal(path, '/t/fr-relay/logs');
  assert.equal(query.get('end'), 'peer');
  assert.equal(query.get('lines'), '200');
});

test('an empty hash is the root, not an empty path', () => {
  assert.equal(router.parse('').path, '/');
  assert.equal(router.parse('#').path, '/');
  assert.equal(router.parse('#/').path, '/');
});

/* Closing a dialog goes back to the page it was opened over. Getting this wrong
 * is how pressing the cross on a dialog opened from the overview used to land
 * somebody on the tunnels. */
test('the page a dialog returns to is the one that was set, not a default', () => {
  assert.equal(router.getHome(), '/');
  router.setHome('/servers');
  assert.equal(router.getHome(), '/servers');
  router.setHome('/');
});
