/* The panel as an app on a phone.
 *
 * Three things make a browser treat a site as installable: a manifest, a
 * service worker that handles fetch, and a secure context. The server has
 * served the first two since the classic panel (handlers_app.go); this is the
 * half that was lost when the panel was rebuilt — nothing linked the manifest
 * and nothing registered the worker, so no phone ever offered to install it.
 *
 * The secure context is the operator's part. A service worker only registers
 * over HTTPS with a certificate the browser trusts, so Android and desktop
 * Chrome install from Let's Encrypt or an own certificate, and not from plain
 * HTTP or a self-signed one. iOS is different: "Add to Home Screen" makes an
 * app of any page that asks for it in its meta tags, worker or not.
 *
 * Installed, the panel runs full screen with no browser bar. body.app marks
 * that, and the stylesheet uses it to keep the header and the last row clear
 * of the notch and the home indicator. */

import * as api from '../api.js';
import { confirmBox } from './confirm.js';
import { toast } from './toast.js';

let deferred = null;   // Chrome's install prompt, held until the button is pressed

const standalone = () =>
  window.matchMedia('(display-mode: standalone)').matches
  || window.matchMedia('(display-mode: fullscreen)').matches
  || window.navigator.standalone === true;

/* iOS Safari has no install prompt and no beforeinstallprompt; it has the
   Share sheet. iPadOS reports itself as a Mac, so touch is what tells them
   apart. */
const isIOS = () =>
  /iphone|ipad|ipod/i.test(navigator.userAgent)
  || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

function paintButton() {
  const btn = document.getElementById('install-btn');
  if (!btn) return;
  btn.hidden = standalone() || !(deferred || isIOS());
}

async function install() {
  if (deferred) {
    const ev = deferred;
    deferred = null;
    ev.prompt();
    try {
      const { outcome } = await ev.userChoice;
      if (outcome === 'accepted') toast('Installing Backpack on this device.');
    } catch (e) { /* the prompt was dismissed */ }
    paintButton();
    return;
  }
  if (isIOS()) {
    await confirmBox({
      title: 'Install Backpack on this device',
      body: 'Safari installs it from the Share sheet. It opens full screen, like any other app.',
      lines: [
        { text: '1. Tap Share at the bottom of Safari' },
        { text: '2. Choose “Add to Home Screen”' },
        { text: '3. Tap Add' },
      ],
      go: 'Got it', icon: 'check',
    });
  }
}

export function startPWA() {
  if (standalone()) document.body.classList.add('app');

  /* Registered at the panel's own base path, so a panel served under one
     controls only itself. A failure here is a certificate the browser does not
     trust, and it costs installability and nothing else. */
  if ('serviceWorker' in navigator && window.isSecureContext) {
    const base = api.base();
    navigator.serviceWorker.register(base + '/sw.js', { scope: base + '/' })
      .catch(e => console.info('panel: not installable here —', e.message || e));
  }

  window.addEventListener('beforeinstallprompt', ev => {
    ev.preventDefault();
    deferred = ev;
    paintButton();
  });
  window.addEventListener('appinstalled', () => {
    deferred = null;
    paintButton();
  });
  window.matchMedia('(display-mode: standalone)').addEventListener?.('change', () => {
    document.body.classList.toggle('app', standalone());
    paintButton();
  });

  const btn = document.getElementById('install-btn');
  if (btn) btn.addEventListener('click', install);
  paintButton();
}
