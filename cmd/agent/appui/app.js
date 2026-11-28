'use strict';
// HopDesk setup page. The token comes from the URL fragment (never sent to the server in a URL).
const $ = (id) => document.getElementById(id);
const token = location.hash.slice(1);
let state = null, mode = 'connect', dirty = false, firstLoad = true;

async function api(path, body) {
  const opts = { headers: { 'X-HopDesk-Token': token } };
  if (body !== undefined) { opts.method = 'POST'; opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
  const r = await fetch('/api/' + path, opts);
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
  return data;
}

function msg(text, good) { $('msg').textContent = text || ''; $('msg').className = 'msg' + (good ? ' good' : ''); }

const STATE_TEXT = { online: 'Online', connecting: 'Connecting…', setup: 'Setup needed', offline: 'Offline', error: 'Problem', starting: 'Starting…' };

function setMode(m) {
  mode = m;
  $('modeConnect').classList.toggle('sel', m === 'connect');
  $('modeHost').classList.toggle('sel', m === 'host');
  $('connectBox').hidden = m !== 'connect';
  $('hostBox').hidden = m !== 'host';
  if (m === 'host' && !$('hostKey').value) newKey();
}

function render(s) {
  state = s;
  $('ver').textContent = 'Version ' + s.version;
  $('pill').textContent = STATE_TEXT[s.state] || s.state;
  $('pill').className = 'pill ' + s.state;
  $('detail').hidden = !s.detail; $('detail').textContent = s.detail || '';
  $('detail').className = 'alert ' + (s.state === 'setup' ? 'info' : s.state === 'connecting' || s.state === 'starting' ? 'warn' : 'bad');
  $('fp').textContent = s.fingerprint || '—';
  $('pwState').innerHTML = s.passwordSet ? '<span class="badge ok">Set</span>' : '<span class="badge bad">Not set</span>';
  $('crumbDevice').textContent = s.config.deviceId;
  $('newPwCard').hidden = !s.newPassword;
  $('newPw').textContent = s.newPassword || '';
  $('video').textContent = s.video;
  $('sessions').textContent = s.sessions ? s.sessions + ' viewer(s) connected now' : '';
  $('autostartRow').hidden = !s.autostartSupported;

  $('permCard').hidden = !s.perms.supported;
  if (s.perms.supported) {
    const icon = (ok) => ok ? '<span class="badge ok">Allowed</span>' : '<span class="badge bad">Off</span>';
    $('permScreenIcon').innerHTML = icon(s.perms.screen);
    $('permAxIcon').innerHTML = icon(s.perms.accessibility);
    $('grantScreen').hidden = s.perms.screen;
    $('grantAx').hidden = s.perms.accessibility;
  }

  renderTailnet(s);

  // Fill the form once, then leave the user's edits alone.
  if (firstLoad || !dirty) {
    const c = s.config;
    $('deviceId').value = c.deviceId;
    $('serverUrl').value = c.mode === 'connect' ? c.serverUrl : '';
    $('agentKey').value = c.mode === 'connect' ? c.agentKey : '';
    $('hostKey').value = c.mode === 'host' ? c.agentKey : '';
    $('hostListen').value = c.hostListen;
    $('quality').value = c.quality; $('fps').value = String(c.fps); $('clipboard').value = c.clipboard;
    $('confirm').checked = c.confirm; $('startAtLogin').checked = c.startAtLogin;
    setMode(c.mode || 'connect');
    firstLoad = false;
  }
}

// Text with https:// links turned into real links (built with DOM nodes, never innerHTML).
function linkified(text) {
  const frag = document.createDocumentFragment();
  let last = 0;
  for (const m of text.matchAll(/https:\/\/[^\s)]+/g)) {
    frag.append(text.slice(last, m.index));
    const a = document.createElement('a');
    a.href = m[0]; a.textContent = m[0]; a.target = '_blank'; a.rel = 'noopener noreferrer';
    frag.append(a);
    last = m.index + m[0].length;
  }
  frag.append(text.slice(last));
  return frag;
}

function renderTailnet(s) {
  const t = s.host.tailnet, note = $('tsNote');
  const savedHost = s.setupDone && s.config.mode === 'host';
  note.className = 'note';
  note.replaceChildren();
  $('tsLinks').hidden = !(savedHost && t.public);
  $('tsSignOutRow').hidden = !(savedHost && (t.state === 'running'));
  $('viewerUrl').textContent = s.host.viewerUrl || '';
  $('agentUrl').textContent = s.host.agentUrl || '';
  if (!savedHost) {
    note.textContent = 'After you save, HopDesk connects this computer to Tailscale (built in, nothing to install) so your other devices and any browser can reach it from anywhere. You only need a free Tailscale account on this computer.';
    return;
  }
  switch (t.state) {
    case 'starting':
      note.textContent = 'Connecting to Tailscale…';
      break;
    case 'needs-login': {
      note.classList.add('warn');
      note.append('Sign in to Tailscale (free) once, so your other devices can reach this computer from anywhere.');
      const b = document.createElement('button');
      b.textContent = t.authUrl ? 'Sign in to Tailscale' : 'Getting the sign-in link…';
      b.disabled = !t.authUrl;
      b.onclick = () => window.open(t.authUrl, '_blank', 'noopener');
      note.append(b);
      break;
    }
    case 'running':
      if (t.public) {
        note.classList.add('good');
        note.append('Reachable from anywhere. Open the address below in any browser, or give it to your other computers. Nothing else to install.');
      } else {
        note.classList.add('warn');
        note.append('Signed in to Tailscale as ' + t.dnsName + '. One more step: ');
        note.append(linkified(t.problem || 'waiting for Tailscale Funnel…'));
        note.append(' (HopDesk retries automatically.)');
      }
      break;
    case 'error':
      note.classList.add('warn');
      note.append(linkified('Tailscale: ' + (t.problem || 'something went wrong; see the log.')));
      break;
    default:
      note.textContent = 'Tailscale is off.';
  }
}

async function refresh() {
  try { render(await api('state')); maybeTour(); }
  catch (e) { $('pill').textContent = 'Not running'; $('pill').className = 'pill error'; }
}

async function newKey() {
  try { $('hostKey').value = (await api('new-key', {})).agentKey; dirty = true; } catch (e) { msg(e.message); }
}

document.querySelectorAll('input, select').forEach((el) => el.addEventListener('input', () => { dirty = true; }));
$('modeConnect').onclick = () => { setMode('connect'); dirty = true; };
$('modeHost').onclick = () => { setMode('host'); dirty = true; };
$('showKey').onclick = () => { const k = $('hostKey'); k.type = k.type === 'password' ? 'text' : 'password'; };
$('tsSignOut').onclick = async () => {
  if (!confirm('Sign this computer out of Tailscale? Your other devices will lose access until you sign in again.')) return;
  try { await api('tailscale-logout', {}); msg('Signed out. Restarting…', true); } catch (e) { msg(e.message); }
};
$('newKey').onclick = () => { if (confirm('Make a new agent key? Your other computers will need the new one.')) newKey(); };

$('ackPw').onclick = async () => { await api('password', { action: 'ack' }); refresh(); };
$('genPw').onclick = async () => {
  if (state && state.passwordSet && !confirm('Generate a new password? The old one stops working.')) return;
  try { await api('password', { action: 'generate' }); refresh(); } catch (e) { msg(e.message); }
};
$('setPwBtn').onclick = () => { $('setPwRow').hidden = false; $('ownPw').focus(); };
$('savePw').onclick = async () => {
  try { await api('password', { action: 'set', password: $('ownPw').value }); $('ownPw').value = ''; $('setPwRow').hidden = true; msg('Password changed', true); refresh(); }
  catch (e) { msg(e.message); }
};

$('grantScreen').onclick = async () => { await api('permission', { which: 'screen' }); $('permRestart').hidden = false; };
$('grantAx').onclick = async () => { await api('permission', { which: 'accessibility' }); };
$('restartBtn').onclick = async () => { await api('restart', {}); msg('Restarting…', true); };

$('save').onclick = async () => {
  const body = {
    deviceId: $('deviceId').value, mode,
    serverUrl: mode === 'connect' ? $('serverUrl').value : '',
    agentKey: mode === 'connect' ? $('agentKey').value : $('hostKey').value,
    hostListen: $('hostListen').value,
    quality: $('quality').value, fps: +$('fps').value, clipboard: $('clipboard').value,
    confirm: $('confirm').checked, startAtLogin: $('startAtLogin').checked,
  };
  $('save').disabled = true;
  try {
    const r = await api('config', body);
    msg(r.warning || 'Saved. Restarting…', !r.warning);
    dirty = false; firstLoad = true;
    setTimeout(refresh, 2500);
  } catch (e) { msg(e.message); }
  finally { setTimeout(() => { $('save').disabled = false; }, 1500); }
};

$('openLog').onclick = () => api('open-log', {});
$('quit').onclick = async () => {
  if (!confirm('Quit HopDesk? Nobody will be able to connect to this computer until you open it again.')) return;
  await api('quit', {}); $('pill').textContent = 'Stopped'; $('pill').className = 'pill error';
};

// ---------- guide and first-run tour ----------

$('helpLink').onclick = (e) => {
  e.preventDefault();
  const w = window.open('help.html', 'hopdesk-guide');
  if (w) w.focus();
};

const TOUR = [
  { title: 'Welcome to HopDesk',
    text: ['This page sets up this computer so you can see and control it from your phone or another computer, from anywhere.',
           'Here is a one-minute tour of each part. You can skip it now and replay it any time from the bottom of the page.'] },
  { el: '#newPwCard', title: 'Your password',
    text: ['Anyone who wants to control this computer needs this password, so keep it private.',
           'Click Copy and save it in a password manager or somewhere safe. It is shown only once.'] },
  { el: '#rowDevice', title: 'Device name',
    text: ['The name of this computer, like a contact name. You type it in the browser to pick which computer to connect to.',
           'Choose something easy to remember, like home-mac or office-pc.'] },
  { el: '#rowFp', title: 'Fingerprint',
    text: ['A code that belongs only to this computer, like a signature.',
           'The first time you connect from a browser, it shows this code. Check they match: that is how you know you reached your real computer. You never have to type it.'] },
  { el: '#pwFoot', title: 'Change the password',
    text: ['Lost the password, or gave it to someone who should no longer have it? Make a new one here. Choose my own lets you type your own.'] },
  { el: '#permCard', title: 'Mac permissions',
    text: ['macOS asks before any app can see the screen or move the mouse.',
           'Screen Recording sends the picture of this screen. Accessibility lets your mouse and keyboard work. Click Grant and switch on HopDesk in the window that opens.'] },
  { el: '#modes', title: 'How your devices find this computer',
    text: ['This computer is the server: choose this for your first HopDesk computer. It becomes the meeting point, and you get an address to open in any browser.',
           'Use a HopDesk server: choose this for each extra computer, to join the server you already set up.'] },
  { el: '#prefCard', title: 'Preferences',
    text: ['Picture quality, smoothness, copy and paste between computers, and whether someone here must approve each connection. The defaults are fine for most people.'] },
  { el: '#save', title: 'Save and restart',
    text: ['Changes on this page take effect when you click this. Anyone connected is briefly disconnected.'] },
  { el: '#helpLink', title: 'Need more help?',
    text: ['This guide explains every word on this page in plain language, including the difference between the agent, the viewer and the server.'] },
];

function startTour(force) {
  return HopUI.tour.start({ id: 'settings-v1', steps: TOUR, force, doneLabel: 'Got it' });
}
$('tourBtn').onclick = () => startTour(true);

let tourChecked = false;
function maybeTour() {
  if (tourChecked) return;
  tourChecked = true;
  setTimeout(() => startTour(false), 400); // first visit only; Skip or Done remembers it
}

if (!token) { $('pill').textContent = 'Open HopDesk from its menu'; $('pill').className = 'pill error'; }
else { refresh(); setInterval(refresh, 2000); }
