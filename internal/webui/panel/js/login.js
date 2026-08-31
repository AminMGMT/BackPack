/* The sign-in page.
 *
 * The corridor behind the card is the panel's own subject drawn once: rounded
 * rectangles receding toward a vanishing point with packets riding the corners.
 * Its speed is a state — it drifts while you type, runs during the handshake,
 * and stops dead when the address is locked out, because a stalled corridor is
 * the one picture that says "nothing is getting through".
 */

export function corridor() {
  const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const cv = document.getElementById('tun5');
  const ctx = cv?.getContext('2d');
  const host = document.getElementById('host5');
  if (host) host.textContent = location.host;

  /* caps lock is the commonest reason a right password reads as wrong */
  const pw = document.getElementById('pw5'), fld = document.getElementById('fld5');
  const caps = e => { if (e.getModifierState) fld.classList.toggle('caps', e.getModifierState('CapsLock')); };
  pw?.addEventListener('keydown', caps);
  pw?.addEventListener('keyup', caps);

  document.getElementById('eye5')?.addEventListener('click', () => {
    const show = pw.type === 'password';
    pw.type = show ? 'text' : 'password';
    pw.focus();
  });

  /* The button carries the handshake itself, so nothing new appears. */
  document.getElementById('lcard')?.addEventListener('submit', () => {
    const b = document.getElementById('unlock5');
    document.getElementById('lblP').textContent = 'Checking password';
    b.disabled = true;
    spdT = 7;
  });

  if (!ctx) return;
  let W = 0, H = 0, dpr = 1, spd = 1, spdT = 1, ink = [255, 255, 255];
  const rings = Array.from({ length: 16 }, (_, i) => ({ p: i / 16 }));
  const packs = Array.from({ length: 22 }, (_, j) => ({ p: Math.random(), c: j % 4, v: .55 + Math.random() * .9 }));
  const MINS = .05, MAXS = 2.6, LK = Math.log(MAXS / MINS);
  const scaleAt = p => MINS * Math.exp(p * LK);
  const rgba = a => `rgba(${ink[0]},${ink[1]},${ink[2]},${a.toFixed(3)})`;

  function readInk() {
    const c = getComputedStyle(document.body).color.match(/\d+/g);
    if (c) ink = [+c[0], +c[1], +c[2]];
  }
  function size() {
    const r = cv.getBoundingClientRect();
    dpr = Math.min(2, devicePixelRatio || 1);
    W = Math.max(1, r.width); H = Math.max(1, r.height);
    cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }
  function rrect(x, y, w, h, r) {
    ctx.beginPath();
    if (ctx.roundRect) return ctx.roundRect(x, y, w, h, r);
    ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r);
    ctx.arcTo(x + w, y + h, x, y + h, r); ctx.arcTo(x, y + h, x, y, r);
    ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
  }
  function frame() {
    ctx.clearRect(0, 0, W, H);
    const cx = W / 2, cy = H * .44;
    for (const r of rings) {
      const s = scaleAt(r.p), a = Math.sin(Math.min(1, r.p) * Math.PI);
      ctx.strokeStyle = rgba(.085 * a); ctx.lineWidth = 1;
      rrect(cx - 150 * s, cy - 107 * s, 300 * s, 214 * s, 26 * s); ctx.stroke();
    }
    for (const q of packs) {
      const s = scaleAt(q.p), a = Math.sin(Math.min(1, q.p) * Math.PI);
      const w = 300 * s, h = 214 * s;
      const x = cx + (q.c === 0 || q.c === 3 ? -w / 2 : w / 2);
      const y = cy + (q.c < 2 ? -h / 2 : h / 2);
      ctx.fillStyle = rgba(.34 * a);
      ctx.beginPath(); ctx.arc(x, y, Math.max(.6, 1.5 * s * .9 + .5), 0, 6.284); ctx.fill();
    }
  }
  let last = 0;
  function tick(t) {
    const dt = Math.min(.05, (t - last) / 1000) || 0; last = t;
    spd += (spdT - spd) * Math.min(1, dt * 3.2);
    for (const r of rings) { r.p += dt * .055 * spd; if (r.p > 1) r.p -= 1; }
    for (const q of packs) { q.p += dt * .055 * spd * q.v; if (q.p > 1) q.p -= 1; }
    frame(); requestAnimationFrame(tick);
  }
  size(); readInk();
  reduce ? frame() : requestAnimationFrame(tick);
  addEventListener('resize', () => { size(); if (reduce) frame(); });
}
