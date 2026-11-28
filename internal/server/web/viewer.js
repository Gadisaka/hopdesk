'use strict';
// hopdesk viewer. Served with a strict Content-Security-Policy, so all script lives here.
const $ = (id) => document.getElementById(id);
const surface = $('surface'), video = $('video'), canvas = $('canvas'), audio = $('audio');
const ctx2d = canvas.getContext('2d');
let pc = null, framesDc = null, ctrl = null, mode = 'jpeg', connected = false;
let frames = new Map(), lastDrawn = -1;
const pressed = new Set();
let remoteHome = '', cwd = '', parentDir = '', sep = '/', lsReq = 0, remoteOS = '';

const ERR_RE = /error|wrong|fail|refused|stopped|declined|too many|could not|changed|not online|needs https/i;
function setStatus(t) {
  $('status').textContent = t;
  $('status').classList.toggle('err', ERR_RE.test(t));
  $('liveStatus').textContent = t.startsWith('Connected to') ? '' : t;
}
function send(obj) { if (ctrl && ctrl.readyState === 'open') ctrl.send(JSON.stringify(obj)); }
function active() { return mode === 'h264' ? video : canvas; }
function fmtSize(n) { if (n < 1024) return n + ' B'; const u = ['KB','MB','GB','TB']; let i = -1; do { n /= 1024; i++; } while (n >= 1024 && i < 3); return n.toFixed(n < 10 ? 1 : 0) + ' ' + u[i]; }

let toastTimer = null;
function toast(text, btnText, onBtn, ms = 4000) {
  $('toastText').textContent = text;
  const b = $('toastBtn');
  b.hidden = !btnText;
  if (btnText) { b.textContent = btnText; b.onclick = () => { onBtn(); $('toast').hidden = true; }; }
  $('toast').hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { $('toast').hidden = true; }, ms);
}

function waitIce(pc) {
  return new Promise((resolve) => {
    if (pc.iceGatheringState === 'complete') return resolve();
    const done = () => { if (pc.iceGatheringState === 'complete') { pc.removeEventListener('icegatheringstatechange', done); resolve(); } };
    pc.addEventListener('icegatheringstatechange', done);
    setTimeout(resolve, 5000);
  });
}

// ---------- authentication (same steps as internal/auth in Go) ----------
const te = new TextEncoder();
const b64 = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf)));
const unb64 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
function randomB64(n) { return b64(crypto.getRandomValues(new Uint8Array(n))); }
function dtlsFingerprint(sdp) {
  const m = /^a=fingerprint:sha-256 ([0-9a-f:]+)\s*$/im.exec(sdp);
  if (!m) throw new Error('no DTLS fingerprint in SDP');
  return m[1].toUpperCase();
}
function binding(viewerSDP, agentSDP) { return 'dtls:' + dtlsFingerprint(viewerSDP) + ',' + dtlsFingerprint(agentSDP); }
async function verifyAgent(pubB64, bind, nonceV, sigB64) {
  const key = await crypto.subtle.importKey('raw', unb64(pubB64), { name: 'Ed25519' }, false, ['verify']);
  return crypto.subtle.verify({ name: 'Ed25519' }, key, unb64(sigB64), te.encode('hopdesk-agent-v1\n' + bind + '\n' + nonceV));
}
async function makeProof(password, saltB64, iter, bind, nonceV, nonceA) {
  const base = await crypto.subtle.importKey('raw', te.encode(password), 'PBKDF2', false, ['deriveBits']);
  const k = await crypto.subtle.deriveBits({ name: 'PBKDF2', hash: 'SHA-256', salt: unb64(saltB64), iterations: iter }, base, 256);
  const hk = await crypto.subtle.importKey('raw', k, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  return b64(await crypto.subtle.sign('HMAC', hk, te.encode('hopdesk-viewer-v1\n' + bind + '\n' + nonceV + '\n' + nonceA)));
}
async function fingerprint(pubB64) {
  const h = new Uint8Array(await crypto.subtle.digest('SHA-256', unb64(pubB64)));
  const hex = Array.from(h.slice(0, 16), (x) => x.toString(16).padStart(2, '0')).join('').toUpperCase();
  return hex.match(/.{4}/g).join(' ');
}
window.hopAuth = { binding, verifyAgent, makeProof, fingerprint }; // used by the tests

// Saved agent identities ("pins"), per server and device ID.
const pinKey = (id) => 'hopdesk-pin:' + location.host + ':' + id;
function getPin(id) { try { return localStorage.getItem(pinKey(id)); } catch (_) { return null; } }
function setPin(id, pub) { try { localStorage.setItem(pinKey(id), pub); } catch (_) {} }
function forgetPin(id) { try { localStorage.removeItem(pinKey(id)); } catch (_) {} }

function ask({ title, text, code, ok, cancel, danger }) {
  return new Promise((resolve) => {
    const m = $('modal');
    m.classList.toggle('danger', !!danger);
    $('mTitle').textContent = title; $('mText').textContent = text;
    $('mFp').hidden = !code; $('mFp').textContent = code || '';
    $('mOk').textContent = ok; $('mOk').className = danger ? 'danger' : 'primary';
    $('mCancel').textContent = cancel;
    const done = (v) => { m.hidden = true; $('mOk').onclick = $('mCancel').onclick = null; resolve(v); };
    $('mOk').onclick = () => done(true);
    $('mCancel').onclick = () => done(false);
    m.hidden = false;
    $('mCancel').focus();
  });
}

let session = null; // { id, password, nonceV }

async function onAuth1(m) {
  const s = session;
  if (!s || s.step !== 1) return;
  s.step = 2;
  const fail = (msg) => { setStatus(msg); disconnect(true); };
  try {
    const bind = binding(pc.localDescription.sdp, pc.remoteDescription.sdp);
    if (!(await verifyAgent(m.pub, bind, s.nonceV, m.sig))) {
      return fail('Stopped: the device could not prove its identity on this connection. Someone may be intercepting it.');
    }
    const fp = await fingerprint(m.pub);
    const pinned = getPin(s.id);
    if (pinned && pinned !== m.pub) {
      const forget = await ask({
        danger: true, title: 'Device identity changed',
        text: `"${s.id}" is answering with a different identity than last time. Your password was NOT sent.\n\nThis happens if someone is impersonating the device, or if HopDesk was reinstalled there. Only forget the old identity if you reinstalled it yourself.\n\nNew fingerprint:`,
        code: fp, ok: 'Forget old identity', cancel: 'Disconnect',
      });
      if (forget) forgetPin(s.id);
      return fail(forget ? 'Old identity forgotten. Connect again and compare the fingerprint.' : 'Disconnected: device identity changed.');
    }
    if (!pinned) {
      const trust = await ask({
        title: 'First connection to ' + s.id,
        text: 'Check that it matches the fingerprint shown in HopDesk on that computer. Your password is only used after you confirm.',
        code: fp, ok: 'It matches, connect', cancel: 'Cancel',
      });
      if (!trust) return fail('Cancelled.');
      if (session !== s) return;
      setPin(s.id, m.pub);
    }
    if (!(m.iter >= 100000 && m.iter <= 5000000)) return fail('The device sent unusual security settings; stopped.');
    setStatus('Checking password…');
    const proof = await makeProof(s.password, m.salt, m.iter, bind, s.nonceV, m.nonce);
    if (session !== s) return;
    s.password = null;
    send({ t: 'auth2', proof });
  } catch (err) {
    fail('Error: ' + err.message);
  }
}

// ---------- connect ----------
async function connect() {
  const id = $('id').value.trim(), password = $('pw').value;
  if (!id || !password) { setStatus('Enter the device ID and password'); return; }
  disconnect(true);
  setStatus('Connecting…');
  const cfg = await fetch('config').then((r) => r.json());
  pc = new RTCPeerConnection({ iceServers: cfg.iceServers || [] });
  pc.addTransceiver('video', { direction: 'recvonly' });
  pc.addTransceiver('audio', { direction: 'recvonly' });
  pc.ontrack = (e) => {
    if (e.track.kind === 'audio') {
      audio.srcObject = new MediaStream([e.track]);
      try { e.receiver.jitterBufferTarget = 0; } catch (_) {}
      return;
    }
    video.srcObject = e.streams[0] || new MediaStream([e.track]);
    try { e.receiver.playoutDelayHint = 0; } catch (_) {}
    try { e.receiver.jitterBufferTarget = 0; } catch (_) {}
  };
  framesDc = pc.createDataChannel('frames', { ordered: true });
  framesDc.binaryType = 'arraybuffer';
  framesDc.onmessage = (e) => onChunk(e.data);
  ctrl = pc.createDataChannel('input', { ordered: true });
  session = { id, password, nonceV: randomB64(18), step: 1 };
  ctrl.onopen = () => send({ t: 'auth1', nonce: session.nonceV });
  ctrl.onmessage = (e) => onCtrl(JSON.parse(e.data));
  pc.onconnectionstatechange = () => {
    const s = pc && pc.connectionState;
    if (s === 'failed') { setStatus('Connection failed (a TURN relay may be needed)'); disconnect(true); }
    else if (s === 'disconnected') setStatus('Connection interrupted…');
    else if (s === 'connected' && connected) setStatus('Connected to ' + id);
  };

  await pc.setLocalDescription(await pc.createOffer());
  await waitIce(pc);

  const base = location.pathname.replace(/[^/]*$/, '');
  const ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + base + 'ws');
  ws.onopen = () => ws.send(JSON.stringify({ type: 'offer', id, sdp: pc.localDescription.sdp }));
  ws.onmessage = async (ev) => {
    const m = JSON.parse(ev.data);
    ws.close();
    if (!pc) return;
    if (m.type === 'answer') { await pc.setRemoteDescription({ type: 'answer', sdp: m.sdp }); setStatus('Verifying device…'); }
    else { setStatus('Error: ' + m.message); disconnect(true); }
  };
  ws.onerror = () => setStatus('Could not reach the server');
}

function disconnect(keepStatus) {
  releaseKeys();
  session = null;
  if (!$('modal').hidden) $('mCancel').click();
  if (pc) pc.close();
  pc = framesDc = ctrl = null; connected = false; helloSeen = false;
  cwd = ''; $('list').innerHTML = ''; $('transfers').innerHTML = '';
  frames.clear(); lastDrawn = -1;
  video.srcObject = null; video.hidden = true; canvas.hidden = true;
  audio.srcObject = null; audio.muted = true; soundOn = false; showSound(false);
  $('hint').hidden = false;
  $('login').hidden = false; $('controls').hidden = true; $('files').hidden = true;
  if (window.HopTouch) HopTouch.onDisconnected();
  if (window.HopDevices) HopDevices.shown();
  if (!keepStatus) setStatus('Not connected');
}

// ---------- control channel ----------
function onCtrl(m) {
  switch (m.t) {
    case 'auth1': onAuth1(m); break;
    case 'auth':
      if (!m.ok) {
        const why = { password: 'Wrong password', locked: 'Too many wrong passwords from your network. Try again later.',
          declined: 'The person at that computer declined the connection.' }[m.reason] || 'Connection refused';
        setStatus(why); disconnect(true); return;
      }
      connected = true;
      if (window.HopDevices) HopDevices.remember($('id').value.trim());
      $('pw').value = '';
      $('crumbName').textContent = $('id').value.trim();
      setStatus('Connected to ' + $('id').value.trim());
      $('login').hidden = true; $('controls').hidden = false; $('hint').hidden = true;
      surface.focus();
      setTimeout(() => { if (connected && !(window.HopTouch && HopTouch.isTouch)) startSessionTour(false); }, 1200);
      break;
    case 'hello': onHello(m); break;
    case 'clip': onRemoteClip(m.text); break;
    case 'ls': onLs(m); break;
    case 'bye': setStatus(m.reason === 'idle' ? 'Disconnected after a period without activity' : 'Disconnected by the device'); disconnect(true); break;
  }
}

let helloSeen = false;
function onHello(m) {
  mode = m.mode;
  video.hidden = mode !== 'h264';
  canvas.hidden = mode === 'h264';
  const sel = $('display');
  sel.innerHTML = '';
  m.displays.forEach((d, i) => sel.add(new Option(`Display ${i + 1} (${d.w}×${d.h})`, i)));
  sel.hidden = m.displays.length < 2;
  sel.value = m.settings.display;
  $('quality').value = m.settings.quality;
  $('fps').value = m.settings.fps;
  remoteHome = m.home;
  remoteOS = m.os || '';
  if (window.HopTouch) HopTouch.onConnected();
  showSound(!!m.audio);
  if (!helloSeen) {
    helloSeen = true;
    if (m.audio && prefGet('hopdesk-sound') === '1') setSound(true, true);
    if (m.os === 'windows' && /Mac/.test(navigator.platform)) $('swap').checked = true;
  }
  $('controls').title = mode === 'h264' ? 'H.264 video' : 'JPEG frames (install ffmpeg on the remote for smoother video)';
}
// ---------- sound ----------
// Sound starts off: browsers only play audio after a tap, and the computer only
// captures sound while someone is listening.
let soundOn = false;
function prefGet(k) { try { return localStorage.getItem(k); } catch (_) { return null; } }
function prefSet(k, v) { try { localStorage.setItem(k, v); } catch (_) {} }
function showSound(avail) {
  $('soundBtn').hidden = !avail;
  $('volumeRow').hidden = !avail;
  paintSound();
}
function paintSound() {
  const b = $('soundBtn');
  b.setAttribute('aria-pressed', soundOn ? 'true' : 'false');
  const t = soundOn ? 'Turn sound off' : 'Turn sound on';
  b.title = t; b.setAttribute('aria-label', t);
}
function setSound(on, auto) {
  soundOn = on;
  send({ t: 'audio', on });
  audio.muted = !on;
  if (!auto) prefSet('hopdesk-sound', on ? '1' : '0');
  paintSound();
  if (on) {
    const p = audio.play();
    if (p && p.catch) p.catch(() => {
      // The browser wants a tap first. Stay off until the person taps the speaker.
      soundOn = false; audio.muted = true; send({ t: 'audio', on: false }); paintSound();
      if (auto) toast('Tap the speaker to hear sound');
    });
  }
}
$('soundBtn').addEventListener('click', () => { setSound(!soundOn); surface.focus(); });
$('volume').value = prefGet('hopdesk-volume') || '100';
audio.volume = (+$('volume').value || 0) / 100;
$('volume').addEventListener('input', () => {
  audio.volume = (+$('volume').value) / 100;
  prefSet('hopdesk-volume', $('volume').value);
});

function sendSettings() {
  send({ t: 'settings', display: +$('display').value, quality: $('quality').value, fps: +$('fps').value });
  surface.focus();
}
['display', 'quality', 'fps'].forEach((id) => $(id).addEventListener('change', sendSettings));

// ---------- JPEG frames: [u32 frameId][u16 index][u16 count][bytes] ----------
function onChunk(buf) {
  const dv = new DataView(buf);
  const fid = dv.getUint32(0), idx = dv.getUint16(4), count = dv.getUint16(6);
  let f = frames.get(fid);
  if (!f) { f = { parts: new Array(count), got: 0 }; frames.set(fid, f); }
  if (!f.parts[idx]) { f.parts[idx] = buf.slice(8); f.got++; }
  if (f.got !== count) return;
  frames.delete(fid);
  for (const k of frames.keys()) if (k < fid) frames.delete(k);
  createImageBitmap(new Blob(f.parts, { type: 'image/jpeg' })).then((bmp) => {
    if (fid < lastDrawn) return;
    lastDrawn = fid;
    if (canvas.width !== bmp.width || canvas.height !== bmp.height) { canvas.width = bmp.width; canvas.height = bmp.height; }
    ctx2d.drawImage(bmp, 0, 0);
    bmp.close();
  }).catch(() => {});
}

// ---------- mouse ----------
// The picture is scaled to fit (object-fit: contain), so find where it
// actually sits inside the element before turning the pointer into 0..1.
// On a phone the page can be turned sideways in CSS (see touch.js). Then
// screen coordinates are turned back into page coordinates first.
let rotated = false;
function toPage(x, y) { return rotated ? { x: y, y: window.innerWidth - x } : { x, y }; }
function pageRect(el) {
  const r = el.getBoundingClientRect();
  if (!rotated) return { left: r.left, top: r.top, width: r.width, height: r.height, right: r.right, bottom: r.bottom };
  const left = r.top, top = window.innerWidth - r.right;
  return { left, top, width: r.height, height: r.width, right: left + r.height, bottom: top + r.width };
}
function norm(e) {
  const el = active(), r = pageRect(el), p = toPage(e.clientX, e.clientY);
  const iw = el === video ? video.videoWidth : canvas.width;
  const ih = el === video ? video.videoHeight : canvas.height;
  let left = r.left, top = r.top, w = r.width, h = r.height;
  if (iw && ih) {
    const s = Math.min(r.width / iw, r.height / ih);
    w = iw * s; h = ih * s;
    left += (r.width - w) / 2; top += (r.height - h) / 2;
  }
  return { x: Math.min(1, Math.max(0, (p.x - left) / w)), y: Math.min(1, Math.max(0, (p.y - top) / h)) };
}
let pendingMove = null, wheelX = 0, wheelY = 0, rafQueued = false;
function flush() {
  rafQueued = false;
  if (pendingMove) { send({ t: 'move', ...pendingMove }); pendingMove = null; }
  if (wheelX || wheelY) { send({ t: 'wheel', dx: Math.round(wheelX), dy: Math.round(wheelY) }); wheelX = wheelY = 0; }
}
function queue() { if (!rafQueued) { rafQueued = true; requestAnimationFrame(flush); } }
surface.addEventListener('mousemove', (e) => { if (connected) { pendingMove = norm(e); queue(); } });
surface.addEventListener('mousedown', (e) => { surface.focus(); if (!connected) return; flush(); send({ t: 'down', b: e.button, n: e.detail, ...norm(e) }); e.preventDefault(); });
surface.addEventListener('mouseup', (e) => { if (!connected) return; flush(); send({ t: 'up', b: e.button, n: e.detail, ...norm(e) }); e.preventDefault(); });
surface.addEventListener('contextmenu', (e) => e.preventDefault());
surface.addEventListener('wheel', (e) => {
  if (!connected) return;
  const k = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1;
  wheelX += e.deltaX * k; wheelY += e.deltaY * k; queue(); e.preventDefault();
}, { passive: false });

// ---------- keyboard ----------
function mapCode(code) {
  if ($('swap').checked) {
    if (code === 'MetaLeft') return 'ControlLeft';
    if (code === 'MetaRight') return 'ControlRight';
  }
  return code;
}
let pendingPaste = null;
function sendPendingPaste() {
  if (!pendingPaste) return;
  const p = pendingPaste; pendingPaste = null;
  send({ t: 'key', code: p.code, key: p.key, down: true });
}
function keyEvent(e, down) {
  if (!connected || e.isComposing) return;
  const code = mapCode(e.code);
  if (down && code === 'KeyV' && (e.metaKey || e.ctrlKey)) {
    // Let the browser fire 'paste' so the local clipboard is sent over first.
    pendingPaste = { code, key: e.key };
    pressed.add(code);
    setTimeout(sendPendingPaste, 150);
    return;
  }
  e.preventDefault();
  if (down) pressed.add(code); else pressed.delete(code);
  send({ t: 'key', code, key: e.key, down });
}
function releaseKeys() { for (const code of pressed) send({ t: 'key', code, key: '', down: false }); pressed.clear(); }
surface.addEventListener('keydown', (e) => keyEvent(e, true));
surface.addEventListener('keyup', (e) => keyEvent(e, false));
surface.addEventListener('blur', releaseKeys);

// ---------- clipboard ----------
document.addEventListener('paste', (e) => {
  if (!connected || document.activeElement !== surface) return;
  e.preventDefault();
  const text = e.clipboardData.getData('text/plain');
  if (text && text.length <= 60000) send({ t: 'clip', text });
  sendPendingPaste(); // ordered channel: the agent sets the clipboard before the key arrives
});
let remoteClip = '';
// The remote clipboard is never written to yours automatically (a hacked
// remote could swap in a malicious command for you to paste). You choose.
function onRemoteClip(text) {
  remoteClip = text;
  const preview = text.length > 40 ? text.slice(0, 40) + '…' : text;
  toast('Remote copied: ' + preview.replace(/\s+/g, ' '), 'Copy here', () => {
    navigator.clipboard.writeText(remoteClip).then(() => toast('Copied'), () => toast('Could not copy'));
    surface.focus();
  }, 8000);
}

// ---------- remote file browser ----------
function joinPath(dir, name) { return dir.endsWith(sep) ? dir + name : dir + sep + name; }
function ls(path) { $('list').innerHTML = '<div class="note">Loading…</div>'; send({ t: 'ls', path, req: ++lsReq }); }
function onLs(m) {
  if (m.req !== lsReq) return;
  const list = $('list');
  list.innerHTML = '';
  if (m.error) { list.innerHTML = '<div class="note"></div>'; list.firstChild.textContent = m.error; return; }
  cwd = m.path; parentDir = m.parent; sep = m.sep;
  $('path').textContent = '\u200E' + cwd + '\u200E'; $('path').title = cwd;
  for (const e of m.entries) {
    const row = document.createElement('div');
    row.className = e.dir ? 'row dir' : 'row';
    row.innerHTML = (e.dir ? ICON_FOLDER : ICON_FILE) + '<span class="name"></span><span class="size"></span>';
    row.querySelector('.name').textContent = e.name;
    row.querySelector('.size').textContent = e.dir ? '' : fmtSize(e.size);
    row.title = e.dir ? 'Open folder' : 'Download to this computer';
    row.onclick = () => e.dir ? ls(joinPath(cwd, e.name)) : download(joinPath(cwd, e.name), e.name);
    list.appendChild(row);
  }
  if (!m.entries.length) list.innerHTML = '<div class="note">Empty folder</div>';
  if (m.truncated) { const n = document.createElement('div'); n.className = 'note'; n.textContent = 'Only the first items are shown.'; list.appendChild(n); }
}
const ICON_FOLDER = '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M3 7a2 2 0 0 1 2-2h4.2l2 2H19a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>';
const ICON_FILE = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4"/></svg>';
$('filesClose').onclick = () => { $('files').hidden = true; surface.focus(); };
$('filesBtn').onclick = () => { const f = $('files'); f.hidden = !f.hidden; if (!f.hidden && !cwd) ls(''); };
$('up').onclick = () => ls(parentDir || cwd);
$('home').onclick = () => ls(remoteHome);

// ---------- transfers ----------
function addTransfer(label, size) {
  const el = document.createElement('div');
  el.className = 'xfer';
  el.innerHTML = '<div class="top"><span></span><span class="pct"></span></div><progress max="1" value="0"></progress><div class="msg"></div>';
  el.querySelector('.top span').textContent = label;
  $('transfers').prepend(el);
  const bar = el.querySelector('progress'), pct = el.querySelector('.pct'), msg = el.querySelector('.msg');
  let total = size;
  return {
    setSize(n) { total = n; },
    progress(n) { if (total) { bar.value = n / total; pct.textContent = Math.floor(100 * n / total) + '%'; } else pct.textContent = fmtSize(n); },
    done(t) { bar.value = 1; pct.textContent = '✓'; msg.textContent = t; },
    fail(t) { el.classList.add('err'); pct.textContent = '✕'; msg.textContent = t; },
  };
}

function upload(file) {
  if (!pc || !connected) return;
  $('files').hidden = false;
  const row = addTransfer('↑ ' + file.name, file.size);
  const dc = pc.createDataChannel('upload', { ordered: true });
  dc.bufferedAmountLowThreshold = 256 * 1024;
  dc.onopen = async () => {
    dc.send(JSON.stringify({ t: 'meta', name: file.name, size: file.size }));
    const CH = 16000;
    let off = 0;
    while (off < file.size) {
      if (dc.readyState !== 'open') return;
      if (dc.bufferedAmount > 1024 * 1024) { await new Promise((r) => dc.addEventListener('bufferedamountlow', r, { once: true })); continue; }
      const buf = await file.slice(off, off + CH).arrayBuffer();
      dc.send(buf);
      off += buf.byteLength;
      row.progress(off);
    }
    dc.send(JSON.stringify({ t: 'end' }));
  };
  dc.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.t === 'ok') { row.done('Saved to ' + m.path); if (!$('files').hidden && cwd) ls(cwd); }
    else row.fail(m.message || 'failed');
    dc.close();
  };
  dc.onclose = () => {};
}

function download(path, name) {
  const row = addTransfer('↓ ' + name, 0);
  const dc = pc.createDataChannel('download', { ordered: true });
  dc.binaryType = 'arraybuffer';
  const parts = [];
  let got = 0;
  dc.onopen = () => dc.send(JSON.stringify({ path }));
  dc.onmessage = (e) => {
    if (typeof e.data !== 'string') { parts.push(e.data); got += e.data.byteLength; row.progress(got); return; }
    const m = JSON.parse(e.data);
    if (m.t === 'meta') { row.setSize(m.size); name = m.name; }
    else if (m.t === 'end') {
      const url = URL.createObjectURL(new Blob(parts));
      const a = document.createElement('a');
      a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 30000);
      row.done('Downloaded');
      dc.close();
    } else if (m.t === 'error') { row.fail(m.message); dc.close(); }
  };
}

$('uploadBtn').onclick = () => $('uploadInput').click();
$('uploadInput').onchange = (e) => { for (const f of e.target.files) upload(f); e.target.value = ''; };
surface.addEventListener('dragover', (e) => { if (connected) { e.preventDefault(); surface.classList.add('drop'); } });
surface.addEventListener('dragleave', () => surface.classList.remove('drop'));
surface.addEventListener('drop', (e) => {
  surface.classList.remove('drop');
  if (!connected) return;
  e.preventDefault();
  for (const f of e.dataTransfer.files) upload(f);
});

// A form submit (button or Enter) lets password managers offer to save the password.
$('login').addEventListener('submit', (e) => {
  e.preventDefault();
  connect().catch((err) => { setStatus('Error: ' + err.message); disconnect(true); });
});
$('disconnect').onclick = () => disconnect();

// The cryptography above needs a secure page (HTTPS or localhost).
if (!window.isSecureContext || !window.crypto || !crypto.subtle) {
  $('connect').disabled = true;
  $('hintText').textContent = 'This page has to be opened over HTTPS (or on localhost). Use the https:// address shown in the HopDesk app.';
  setStatus('Needs HTTPS');
}

// ---------- first-run tours (Skip remembers it in this browser) ----------

const CONNECT_TOUR = [
  { title: 'Welcome to HopDesk',
    text: ['From this page you can see and control another computer that has the HopDesk app installed.',
           'Quick tour of what to type? It takes 30 seconds, or skip it.'] },
  { el: '#recent', title: 'Your devices',
    text: ['Computers you have connected to from this browser, with a green dot when they are online. Tap one, then enter its password.'] },
  { el: '#id', title: 'Device',
    text: ['The name of the computer you want to control, for example studio-mac.',
           'You can find it at the top of the HopDesk app on that computer.'] },
  { el: '#pw', title: 'Password',
    text: ["That computer's password, from its HopDesk app. It never leaves this browser: it is used to prove you know it, without sending it."] },
  { el: '#connect', title: 'Connect',
    text: ['The first time, you will see a fingerprint (a code like 0C36 86F9 …). Check it matches the one in the HopDesk app on that computer, then confirm.',
           'Your browser remembers it, so you only do this once.'] },
  { el: '#helpLink', title: 'Not sure about something?',
    text: ['The guide explains what everything means: the agent, the server, the viewer and every setting.'] },
];

const SESSION_TOUR = [
  { el: '#surface', title: "You're connected",
    text: ['This is the other computer\'s screen. Click on it, and your mouse and keyboard now control that computer.'] },
  { el: '#display', title: 'Display', text: ['That computer has more than one screen. Pick which one to see.'] },
  { el: '#quality', title: 'Quality', text: ['How sharp the picture is. Lower it if things feel slow.'] },
  { el: '#fps', title: 'Smoothness (fps)', text: ['Pictures per second. 30 is a good balance; 60 is smoother but uses more internet.'] },
  { el: '#controls .toggle', title: 'Cmd → Ctrl', text: ['On a Mac controlling a Windows PC? Turn this on so Cmd shortcuts (copy, paste…) work as Ctrl.'] },
  { el: '#filesBtn', title: 'Files', text: ['Browse that computer\'s files: download them here, or upload files to it.'] },
  { el: '#disconnect', title: 'Disconnect', text: ['Ends the session. Copy and paste also works between the two computers while connected.'] },
];

function startSessionTour(force) {
  HopUI.tour.start({ id: 'viewer-session-v1', steps: SESSION_TOUR, force, doneLabel: 'Start using it',
    onEnd: () => surface.focus() });
}

if (window.isSecureContext && !$('login').hidden) {
  setTimeout(() => {
    if (!connected) HopUI.tour.start({ id: 'viewer-connect-v1', steps: CONNECT_TOUR, onEnd: () => $('id').focus() });
  }, 500);
}

// ---------- narrow screens: picture settings live behind one button ----------
$('moreBtn').addEventListener('click', (e) => {
  e.stopPropagation();
  const open = $('controls').classList.toggle('open');
  $('moreBtn').setAttribute('aria-expanded', String(open));
});
document.addEventListener('click', (e) => {
  if ($('controls').classList.contains('open') && !e.target.closest('#opts') && !e.target.closest('#moreBtn')) {
    $('controls').classList.remove('open');
    $('moreBtn').setAttribute('aria-expanded', 'false');
  }
});
