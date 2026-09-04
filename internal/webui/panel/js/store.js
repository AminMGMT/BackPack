/* One copy of the live state, polled once and shared.
 *
 * Every screen reads the same tunnel list. Without this each one would poll
 * for itself, so opening a dialog would double the request rate and the card
 * behind it could show a different state from the dialog in front of it.
 *
 * The cadences are the ones the panel has always used: the host every 4s, the
 * tunnels every 6s, the alert badge every 30s.
 */

import * as api from './api.js';

const state = { stats: null, tunnels: [], alerts: null, error: null };
const subs = new Set();
let timers = [];

export function get() { return state; }

export function subscribe(fn) {
  subs.add(fn);
  if (state.stats) fn(state);
  return () => subs.delete(fn);
}

function emit() { subs.forEach(fn => fn(state)); }

export async function loadStats() {
  try { state.stats = await api.stats(); state.error = null; }
  catch (e) { state.error = e; }
  emit();
  return state.stats;
}

export async function loadTunnels() {
  try { state.tunnels = await api.tunnels() || []; state.error = null; }
  catch (e) { state.error = e; }
  emit();
  return state.tunnels;
}

export async function loadAlerts() {
  try { state.alerts = await api.alerts(); } catch (e) { /* the badge is not worth an error */ }
  emit();
  return state.alerts;
}

export function tunnel(name) { return state.tunnels.find(t => t.name === name) || null; }

/* Polling pauses while the tab is hidden: a panel left open in a background
   tab was making 15 requests a minute at nothing. */
export function startPolling() {
  stopPolling();
  timers = [
    setInterval(() => { if (!document.hidden) loadStats(); }, 4000),
    setInterval(() => { if (!document.hidden) loadTunnels(); }, 6000),
    setInterval(() => { if (!document.hidden) loadAlerts(); }, 30000),
  ];
  document.addEventListener('visibilitychange', onVisible);
}

function onVisible() { if (!document.hidden) { loadStats(); loadTunnels(); } }

function stopPolling() {
  timers.forEach(clearInterval);
  timers = [];
  document.removeEventListener('visibilitychange', onVisible);
}

/* After an action the card is stale until the next poll, so ask immediately. */
export async function refresh() { await Promise.all([loadStats(), loadTunnels()]); }
