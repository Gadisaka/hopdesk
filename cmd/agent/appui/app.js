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
  $('soundInfo').textContent = 'Sound: ' + (s.sound || 'Off');
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
  renderService(s);

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
    $('sound').checked = !c.noSound;
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

// ---------- updates ----------

const UPD_ICONS = {
  ok: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>',
  new: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4v11M7 10l5 5 5-5M5 20h14"/></svg>',
  spin: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12a8 8 0 1 1-2.3-5.6M20 4v5h-5"/></svg>',
  bad: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 7.5v5.5M12 16.5h.01"/></svg>',
  off: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M8 12h8"/></svg>',
};
let lastUpdState = '', seenVersion = '';

function ago(iso) {
  if (!iso || iso.startsWith('0001')) return '';
  const m = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (m < 1) return 'just now';
  if (m < 60) return m + (m === 1 ? ' minute ago' : ' minutes ago');
  const h = Math.round(m / 60);
  if (h < 24) return h + (h === 1 ? ' hour ago' : ' hours ago');
  const d = Math.round(h / 24);
  return d + (d === 1 ? ' day ago' : ' days ago');
}

function renderUpdate(u) {
  const icon = $('updIcon'), text = $('updText');
  let title = '', sub = '', kind = 'ok', err = false;
  const checked = ago(u.checked);
  switch (u.state) {
    case 'off':
      title = 'Automatic updates are off'; sub = u.message || ''; kind = 'off'; break;
    case 'checking':
      title = 'Checking for updates…'; sub = 'You have ' + u.current; kind = 'spin'; break;
    case 'available':
      title = 'HopDesk ' + u.latest + ' is available';
      sub = u.message || ('You have ' + u.current + '. Installing restarts HopDesk, and anyone connected is disconnected for a few seconds.');
      err = !!u.message; kind = 'new'; break;
    case 'downloading':
      title = 'Downloading HopDesk ' + u.latest + '…'; sub = (u.progress || 0) + '%'; kind = 'spin'; break;
    case 'installing':
      title = 'Installing HopDesk ' + u.latest + '…'; sub = 'HopDesk will restart in a moment. This page reconnects by itself.'; kind = 'spin'; break;
    case 'error':
      title = "Couldn't check for updates"; sub = u.message || ''; kind = 'bad'; err = true; break;
    case 'current':
      title = "You're up to date"; sub = 'HopDesk ' + u.current + (checked ? ' · checked ' + checked : ''); break;
    default:
      title = 'HopDesk ' + u.current; sub = 'Not checked yet'; kind = 'off';
  }
  $('updTitle').textContent = title;
  text.textContent = sub;
  text.className = err ? 'err' : '';
  icon.className = 'ic ' + (kind === 'ok' ? 'ok' : kind === 'new' ? 'new' : kind === 'bad' ? 'bad' : '');
  icon.innerHTML = UPD_ICONS[kind] || UPD_ICONS.off;
  const busy = ['checking', 'downloading', 'installing'].includes(u.state);
  $('updCheck').hidden = u.state === 'off' || u.state === 'available' && !u.message || u.state === 'downloading' || u.state === 'installing';
  $('updCheck').disabled = busy;
  $('updInstall').hidden = u.state !== 'available';
  $('updInstall').textContent = u.message ? 'Try again' : 'Install update';
  $('updNotes').hidden = !(u.notesUrl && ['available', 'downloading', 'installing'].includes(u.state));
  if (u.notesUrl) $('updNotes').href = u.notesUrl;
  $('updBar').hidden = !(u.state === 'downloading' || u.state === 'installing');
  $('updBarFill').style.width = (u.state === 'installing' ? 100 : u.progress || 0) + '%';
  $('updAutoRow').hidden = u.state === 'off';
  $('updAuto').checked = !!u.auto;
  $('updPill').hidden = u.state !== 'available';
  $('updPill').textContent = 'Update to ' + u.latest;
  lastUpdState = u.state;
}

$('updCheck').onclick = async () => {
  try { await api('update-check', {}); setTimeout(refresh, 300); } catch (e) { msg(e.message); }
};
$('updInstall').onclick = async () => {
  const u = state && state.update;
  if (!u) return;
  const who = state.sessions ? ' The ' + state.sessions + ' viewer(s) connected now will be disconnected for a few seconds.' : '';
  if (!confirm('Install HopDesk ' + u.latest + '? HopDesk will restart.' + who)) return;
  try { await api('update-install', {}); setTimeout(refresh, 300); } catch (e) { msg(e.message); }
};
$('updAuto').onchange = async () => {
  try { await api('update-auto', { on: $('updAuto').checked }); } catch (e) { msg(e.message); }
};
$('updPill').onclick = () => $('updCard').scrollIntoView({ behavior: 'smooth', block: 'center' });

// ---------- login-screen control (Windows) ----------

let svcRestarting = false;
function renderService(s) {
  const v = s.service || {};
  $('svcCard').hidden = !v.supported;
  if (!v.supported) return;
  if (!v.busy && !svcRestarting) $('svcOn').checked = v.on;
  $('svcOn').disabled = v.busy || svcRestarting;
  $('svcUpdate').disabled = v.busy || svcRestarting;
  let text = 'Off. HopDesk works while someone is logged in to Windows.';
  if (v.busy) text = 'Waiting for Windows… Click Yes in the administrator prompt.';
  else if (svcRestarting) text = 'Done. HopDesk is restarting…';
  else if (v.on && v.running) text = 'On. You can reach the login screen, the lock screen and admin prompts, and press Ctrl+Alt+Del from the viewer.';
  else if (v.on) text = 'On, but the HopDesk service isn’t running. Turn this off and on again, or restart the computer.';
  $('svcText').textContent = text;
  $('svcErr').hidden = !v.error; $('svcErr').textContent = v.error || '';
  $('svcStaleRow').hidden = !(v.on && v.stale) || v.busy;
}

async function setService(on) {
  try {
    await api('service', { on });
    $('svcText').textContent = 'Waiting for Windows… Click Yes in the administrator prompt.';
    $('svcOn').disabled = true;
    const poll = setInterval(async () => {
      try {
        const s = await api('state');
        if (!s.service.busy) {
          clearInterval(poll);
          if (s.service.error) { render(s); return; }
          svcRestarting = true; render(s);
          setTimeout(() => { svcRestarting = false; firstLoad = true; refresh(); }, 4000);
        }
      } catch (e) { /* restarting */ }
    }, 1000);
  } catch (e) { msg(e.message); $('svcOn').checked = !on; }
}
$('svcOn').onchange = () => {
  const on = $('svcOn').checked;
  const q = on
    ? 'Turn on control of the login screen and admin prompts?\n\nWindows will ask for administrator permission. With this on, anyone who has this computer’s HopDesk password can also sign in to Windows here and approve admin prompts, so keep the password private.'
    : 'Turn off control of the login screen and admin prompts? Windows will ask for administrator permission to remove the HopDesk service.';
  if (!confirm(q)) { $('svcOn').checked = !on; return; }
  setService(on);
};
$('svcUpdate').onclick = () => setService(true);

async function refresh() {
  try {
    const s = await api('state');
    if (seenVersion && s.version !== seenVersion) msg('Updated to HopDesk ' + s.version, true);
    seenVersion = s.version;
    render(s);
    renderUpdate(s.update);
    maybeTour();
  } catch (e) {
    if (lastUpdState === 'installing' || svcRestarting) { $('pill').textContent = 'Restarting…'; $('pill').className = 'pill starting'; return; }
    $('pill').textContent = 'Not running'; $('pill').className = 'pill error';
  }
}

async function newKey() {
  try { $('hostKey').value = (await api('new-key', {})).agentKey; dirty = true; } catch (e) { msg(e.message); }
}

document.querySelectorAll('input:not([data-nodirty]), select').forEach((el) => el.addEventListener('input', () => { dirty = true; }));
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
    confirm: $('confirm').checked, startAtLogin: $('startAtLogin').checked, noSound: !$('sound').checked,
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
  { el: '#updCard', title: 'Updates',
    text: ['HopDesk checks for a new version once a day and tells you when one is ready. Click Install update and it does the rest, then restarts by itself.'] },
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
