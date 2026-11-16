/* Twelve TCP connects to the tunnel port, then the transport the measurement
 * argues for. The branch logic is RecommendTransport's, in its order.
 *
 * CLI: Manage → Link Test.
 */

import { $$, esc } from '../lib/dom.js';
import { kindLabel } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops } from '../ui/toast.js';

const KCP_CAVEATS = [
  'KCP runs over UDP — if UDP is throttled it will be worse, so test first',
  'if KCP struggles, try QUIC: it recovers losses itself and needs no tuning',
  'if UDP is the problem, TCP + PCK carries KCP in TCP-shaped packets — Linux and root on both ends',
];

/* The same branches the server takes, so the panel never explains a different
   recommendation from the one the CLI would give. */
function recommend(r, current) {
  const loss = r.lossPct ?? 0, usable = r.usable;
  const out = { preset: 'Turbo', why: [], caveats: [], fec: null };
  if (!usable) {
    out.label = 'WSS'; out.transport = 'wss';
    out.why = ["the tunnel port barely answered — usually filtering, not a slow link",
               'WSS looks like ordinary HTTPS, so it is the likeliest to get through'];
    out.caveats = ['first check the server is running and the port is open — a stopped server looks the same'];
  } else if (loss >= 20) {
    out.label = 'UDP + KCP + FEC'; out.transport = 'kcp'; out.preset = 'Aggressive';
    out.why = [`${loss.toFixed(0)}% of probes never completed — this link loses a lot`,
               'KCP repairs losses with error correction instead of retransmits'];
    out.caveats = KCP_CAVEATS;
  } else if (loss >= 2) {
    out.label = 'UDP + KCP + FEC'; out.transport = 'kcp';
    out.why = [`${loss.toFixed(0)}% packet loss — enough to keep TCP backing off`,
               "KCP's error correction recovers it without a round trip"];
    out.caveats = KCP_CAVEATS;
  } else if (r.jitterMs > r.avgMs / 3) {
    out.label = 'TCP Mux'; out.transport = 'tcpmux';
    out.why = [`latency swings (±${r.jitterMs}ms around ${r.avgMs}ms) — a congested or shaped path`,
               'multiplexing over a few steady connections rides this out better'];
  } else {
    out.label = 'TCP Mux'; out.transport = 'tcpmux';
    out.why = [`the link is clean and steady (${r.avgMs}ms, ±${r.jitterMs}ms, no loss)`,
               'plain multiplexed TCP is the fastest and lightest on CPU'];
  }
  if (out.transport === 'kcp') {
    out.fec = loss < 1 ? ['20:5', 'light 25% parity is plenty on a clean route']
            : loss < 5 ? ['10:5', '10:5 repairs bursts up to ~33%']
            : loss < 15 ? ['8:8', '8:8 repairs bursts up to ~50%']
            : ['4:8', '4:8 trades 200% overhead for repairing up to ~67%'];
    out.fec[1] = `${loss.toFixed(0)}% loss — ${out.fec[1]}`;
  }
  if (out.transport === current) out.same = 'already what the tunnel uses';
  return out;
}

function paint(root, res, current) {
  const set = (id, html) => { const n = root.querySelector(id); if (n) n.innerHTML = html; };
  set('#v_min', `${res.minMs}<em>ms</em>`);
  set('#v_avg', `${res.avgMs}<em>ms</em>`);
  set('#v_max', `${res.maxMs}<em>ms</em>`);
  set('#v_jit', `±${res.jitterMs}<em>ms</em>`);
  set('#v_loss', `${(res.lossPct ?? 0).toFixed(0)}<em>%</em>`);
  const lossBox = root.querySelector('#n_loss');
  if (lossBox) lossBox.className = 'n8' + (res.lossPct >= 20 ? ' bad8' : res.lossPct >= 2 ? ' wr8' : '');

  /* The server reports the aggregate — sent, received, min/avg/max/jitter —
     and not the twelve individual round trips. So the strip is a tally of what
     came back, not twelve different bars: drawing a height per probe would be
     inventing a measurement the panel was never given. */
  const cells = $$('.c8', root);
  cells.forEach((c, i) => {
    c.className = 'c8' + (i < res.received ? ' got' : i < res.sent ? ' lost' : '');
    const bar = c.querySelector('i');
    if (bar) bar.style.height = i < res.received ? '52%' : '0';
    const lab = c.querySelector('u');
    if (lab) lab.textContent = '';
  });
  const prog = root.querySelector('#prog8');
  if (prog) prog.textContent = `${res.sent} of 12 · ${res.received} answered`;

  const r = recommend(res, current);
  set('#rt8', esc(r.label));
  set('#rp8', esc(r.preset));
  set('#why8', r.why.map(w => `<li>${esc(w)}</li>`).join('') +
                (r.same ? `<li class="same">${esc(r.same)}</li>` : ''));
  const fec = root.querySelector('#fec8');
  if (fec) {
    fec.hidden = !r.fec;
    if (r.fec) { set('#fr8', r.fec[0]); set('#fw8', esc(r.fec[1])); }
  }
  const cav = root.querySelector('#cav8');
  if (cav) {
    cav.hidden = !r.caveats.length;
    set('#cavl8', r.caveats.map(c => `<li>${esc(c)}</li>`).join(''));
  }
  const rec = root.querySelector('#rec8');
  if (rec) rec.hidden = false;
}

/* The two cases the server refuses before it probes anything. */
function refusal(t) {
  if (t.role !== 'client') return [
    'The link test runs on the client (kharej) side, which dials out.',
    'A server tunnel waits to be dialled and has nothing to probe. Run it from the kharej side.'];
  if (['udp', 'kcp', 'quic'].includes(t.transport)) return [
    'A UDP tunnel can’t be probed over TCP — its metrics (loss, FEC repairs) measure the link.',
    'A TCP connect would report a working datagram tunnel as dead. Use the metrics screen.'];
  return null;
}

export async function linkTestView(ctx) {
  const name = ctx.params.name;
  /* A deep link arrives before the first poll, so the tunnel may not be in the
     store yet; without this the header falls back to the preview's own text. */
  if (!store.tunnel(name)) await store.loadTunnels();
  const t = store.tunnel(name) || {};

  openScreen('linktest', {
    pick: '.dlg',
    bind: async (root, close) => {
      const sub = root.querySelector('#sub8');
      if (sub) sub.textContent = `${name} · ${t.role === 'client' ? 'kharej' : 'iran'} side · ${t.transport || ''}`;
      const ad = root.querySelector('#ad8');
      if (ad) ad.textContent = t.addr || '—';

      const no = refusal(t);
      if (no) {
        root.querySelector('#body8').hidden = true;
        root.querySelector('#no8').hidden = false;
        root.querySelector('#nob8').textContent = no[0];
        root.querySelector('#nos8').textContent = no[1];
        root.querySelector('#run8').disabled = true;
        root.querySelector('#hint8').textContent = 'The panel refuses before it starts — nothing was probed';
        ctx.setTeardown(close);
        return;
      }

      const run = root.querySelector('#run8');
      let poll = null;

      const tick = async () => {
        try {
          const s = await api.linkTestStatus();
          if (s.running) {
            run.disabled = true; run.textContent = 'Probing…';
            return;
          }
          clearInterval(poll); poll = null;
          run.disabled = false; run.textContent = 'Run again';
          if (s.result) paint(root, s.result, t.transport);
        } catch (e) { clearInterval(poll); oops(e); }
      };

      run?.addEventListener('click', async () => {
        run.disabled = true; run.textContent = 'Probing…';
        $$('.c8', root).forEach(c => { c.className = 'c8'; });
        try { await api.linkTestRun(name); }
        catch (e) { oops(e, name); run.disabled = false; return; }
        poll = setInterval(tick, 900);
      });

      await tick();
      ctx.setTeardown(() => { if (poll) clearInterval(poll); close(); });
    },
  }).catch(oops);
}
