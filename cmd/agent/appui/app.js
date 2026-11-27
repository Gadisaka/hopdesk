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
  $('fp').textContent = s.fingerprint || '—';
  $('pwState').innerHTML = s.passwordSet ? '<span class="ok">✓ Set</span>' : '<span class="bad">Not set</span>';
  $('newPwCard').hidden = !s.newPassword;
  $('newPw').textContent = s.newPassword || '';
  $('video').textContent = s.video;
  $('sessions').textContent = s.sessions ? s.sessions + ' viewer(s) connected now' : '';
  $('autostartRow').hidden = !s.autostartSupported;

  $('permCard').hidden = !s.perms.supported;
  if (s.perms.supported) {
    const icon = (ok) => ok ? '<span class="ok">✓</span>' : '<span class="bad">✕</span>';
    $('permScreenIcon').innerHTML = icon(s.perms.screen);
    $('permAxIcon').innerHTML = icon(s.perms.accessibility);
    $('grantScreen').hidden = s.perms.screen;
    $('grantAx').hidden = s.perms.accessibility;
  }

  $('viewerUrl').textContent = s.host.viewerUrl;
  $('agentUrl').textContent = s.host.agentUrl || '(needs Tailscale, see above)';
  const ts = $('tsNote');
  ts.classList.toggle('warn', !s.host.dnsName);
  if (!s.host.tailscale) {
    ts.innerHTML = '<b>Install Tailscale</b> (free) on this and your other computers so they can reach this one securely from anywhere, then come back here.';
  } else if (!s.host.dnsName) {
    ts.textContent = 'Tailscale is installed but not signed in. Open Tailscale, sign in, then reload this page.';
  } else {
    ts.innerHTML = 'Tailscale name: <code></code>. Turn on HTTPS so other computers and browsers can connect: <button class="secondary" id="tsServe">Enable HTTPS with Tailscale</button>';
    ts.querySelector('code').textContent = s.host.dnsName;
    $('tsServe').onclick = async () => {
      try { await api('tailscale-serve', {}); msg('HTTPS is on: open ' + s.host.viewerUrl + ' from your other devices.', true); }
      catch (e) { msg(e.message); }
    };
  }

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

async function refresh() {
  try { render(await api('state')); }
  catch (e) { $('pill').textContent = 'Not running'; $('pill').className = 'pill error'; }
}

async function newKey() {
  try { $('hostKey').value = (await api('new-key', {})).agentKey; dirty = true; } catch (e) { msg(e.message); }
}

document.querySelectorAll('input, select').forEach((el) => el.addEventListener('input', () => { dirty = true; }));
$('modeConnect').onclick = () => { setMode('connect'); dirty = true; };
$('modeHost').onclick = () => { setMode('host'); dirty = true; };
$('showKey').onclick = () => { const k = $('hostKey'); k.type = k.type === 'password' ? 'text' : 'password'; };
$('copyKey').onclick = () => navigator.clipboard.writeText($('hostKey').value).then(() => msg('Agent key copied', true));
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

if (!token) { $('pill').textContent = 'Open HopDesk from its menu'; $('pill').className = 'pill error'; }
else { refresh(); setInterval(refresh, 2000); }
