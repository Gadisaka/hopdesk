'use strict';
// HopDesk viewer: the devices you've connected to from this browser.
// Kept in this browser only (localStorage), per server. Passwords are never
// stored here: the login form is set up so your password manager can save
// and fill them instead.
(function () {
  const KEY = 'hopdesk-devices:' + location.host;
  const MAX = 12;
  const list = $('recent'), items = $('recentList');

  function load() {
    try {
      const v = JSON.parse(localStorage.getItem(KEY) || '[]');
      return Array.isArray(v) ? v.filter((d) => d && typeof d.id === 'string') : [];
    } catch (e) { return []; }
  }
  function save(devs) { try { localStorage.setItem(KEY, JSON.stringify(devs.slice(0, MAX))); } catch (e) { /* private mode */ } }

  let online = {};

  function ago(t) {
    const m = Math.round((Date.now() - t) / 60000);
    if (m < 1) return 'just now';
    if (m < 60) return m + ' min ago';
    const h = Math.round(m / 60);
    if (h < 24) return h + (h === 1 ? ' hour ago' : ' hours ago');
    const d = Math.round(h / 24);
    return d === 1 ? 'yesterday' : d + ' days ago';
  }

  const ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>';
  const MORE = '<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.7"/><circle cx="12" cy="12" r="1.7"/><circle cx="19" cy="12" r="1.7"/></svg>';

  function render() {
    const devs = load();
    list.hidden = devs.length === 0;
    $('loginSep').hidden = devs.length === 0;
    items.replaceChildren();
    for (const d of devs) {
      const row = document.createElement('div');
      row.className = 'dev';
      row.dataset.id = d.id;
      const main = document.createElement('button');
      main.type = 'button';
      main.className = 'dev-main';
      main.innerHTML = '<span class="dev-ic">' + ICON + '</span><span class="dev-txt"><b></b><small></small></span><span class="dev-state"></span>';
      main.querySelector('b').textContent = d.nick || d.id;
      main.querySelector('small').textContent = (d.nick ? d.id + ' · ' : '') + (d.last ? ago(d.last) : '');
      const st = online[d.id];
      const state = main.querySelector('.dev-state');
      state.dataset.state = st === true ? 'on' : st === false ? 'off' : 'unknown';
      state.textContent = st === true ? 'Online' : st === false ? 'Offline' : '';
      main.setAttribute('aria-label', (d.nick || d.id) + (st === true ? ', online' : st === false ? ', offline' : '') + '. Connect');
      main.onclick = () => pick(d);
      const more = document.createElement('button');
      more.type = 'button';
      more.className = 'dev-more icon ghost sm';
      more.innerHTML = MORE;
      more.title = 'Rename or remove';
      more.setAttribute('aria-label', 'Options for ' + (d.nick || d.id));
      more.onclick = (e) => { e.stopPropagation(); openMenu(row, d); };
      row.append(main, more);
      items.appendChild(row);
    }
  }

  function pick(d) {
    $('id').value = d.id;
    $('pw').value = '';
    $('pw').focus();
    setStatus('Enter the password for ' + (d.nick || d.id));
    items.querySelectorAll('.dev').forEach((r) => r.classList.toggle('sel', r.dataset.id === d.id));
  }

  // ---------- rename / remove ----------
  let menu = null;
  function closeMenu() { if (menu) { menu.remove(); menu = null; } }
  document.addEventListener('click', (e) => { if (menu && !menu.contains(e.target)) closeMenu(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeMenu(); });

  function openMenu(row, d) {
    closeMenu();
    menu = document.createElement('div');
    menu.className = 'dev-menu';
    menu.setAttribute('role', 'menu');
    const ren = document.createElement('button');
    ren.type = 'button'; ren.textContent = 'Rename'; ren.setAttribute('role', 'menuitem');
    ren.onclick = () => { closeMenu(); rename(row, d); };
    const rem = document.createElement('button');
    rem.type = 'button'; rem.textContent = 'Remove from this list'; rem.className = 'danger-text'; rem.setAttribute('role', 'menuitem');
    rem.onclick = () => { closeMenu(); remove(d); };
    menu.append(ren, rem);
    row.appendChild(menu);
    ren.focus();
  }

  function rename(row, d) {
    const b = row.querySelector('.dev-txt b');
    const input = document.createElement('input');
    input.type = 'text';
    input.className = 'dev-rename';
    input.value = d.nick || '';
    input.placeholder = d.id;
    input.maxLength = 40;
    input.setAttribute('aria-label', 'Name for ' + d.id);
    b.replaceWith(input);
    input.focus(); input.select();
    let done = false;
    const finish = (keep) => {
      if (done) return;
      done = true;
      if (keep) {
        const devs = load(), x = devs.find((v) => v.id === d.id);
        if (x) { x.nick = input.value.trim().slice(0, 40); save(devs); }
      }
      render();
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); finish(true); }
      if (e.key === 'Escape') { e.preventDefault(); finish(false); }
    });
    input.addEventListener('click', (e) => e.stopPropagation());
    input.addEventListener('blur', () => finish(true));
  }

  function remove(d) {
    const before = load();
    save(before.filter((v) => v.id !== d.id));
    render();
    // The device's saved fingerprint stays, so it's still recognised if you add it back.
    toast('Removed ' + (d.nick || d.id), 'Undo', () => { save(before); render(); });
  }

  // ---------- online status ----------
  let polling = null;
  async function refreshOnline() {
    const devs = load();
    if (!devs.length || document.hidden || $('hint').hidden) return;
    try {
      const r = await fetch('online?ids=' + encodeURIComponent(devs.slice(0, 10).map((d) => d.id).join(',')), { cache: 'no-store' });
      if (!r.ok) return;
      online = (await r.json()).online || {};
      if (!$('hint').hidden) render();
    } catch (e) { /* offline or an older server: just don't show status */ }
  }
  function startPolling() {
    refreshOnline();
    clearInterval(polling);
    polling = setInterval(refreshOnline, 30000);
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshOnline(); });

  window.HopDevices = {
    // Called after a successful login.
    remember(id) {
      const devs = load().filter((d) => d.id !== id);
      const old = load().find((d) => d.id === id);
      devs.unshift({ id, nick: old ? old.nick : '', last: Date.now() });
      save(devs);
      online[id] = true;
    },
    shown() { render(); startPolling(); },
    render,
  };
  render();
  startPolling();
})();

// ---------- "add to home screen" tip (phones and tablets) ----------
(function () {
  const tip = $('installTip');
  const standalone = matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
  const touch = matchMedia('(pointer: coarse)').matches;
  let dismissed = false;
  try { dismissed = localStorage.getItem('hopdesk-install-tip') === 'hidden'; } catch (e) { /* private mode */ }
  if (standalone || !touch || dismissed) return;
  const ios = /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  $('installHow').textContent = ios
    ? 'Tap the Share button, then Add to Home Screen. HopDesk then opens full screen, like an app.'
    : "In your browser's menu, choose Add to Home screen (or Install app). HopDesk then opens full screen, like an app.";
  tip.hidden = false;
  let deferred = null;
  window.addEventListener('beforeinstallprompt', (e) => {
    e.preventDefault();
    deferred = e;
    $('installBtn').hidden = false;
    $('installHow').textContent = 'Opens full screen, like an app, with its own icon.';
  });
  $('installBtn').onclick = async () => {
    if (!deferred) return;
    deferred.prompt();
    const choice = await deferred.userChoice.catch(() => null);
    deferred = null;
    if (choice && choice.outcome === 'accepted') tip.hidden = true;
  };
  $('installX').onclick = () => {
    tip.hidden = true;
    try { localStorage.setItem('hopdesk-install-tip', 'hidden'); } catch (e) { /* private mode */ }
  };
  window.addEventListener('appinstalled', () => { tip.hidden = true; });
})();
