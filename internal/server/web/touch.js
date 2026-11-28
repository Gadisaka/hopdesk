'use strict';
// HopDesk viewer: phone and tablet controls.
//
//   Touchpad mode (default): drag to move the pointer, tap to click, double-tap and drag to
//   select or move things, two-finger tap to right-click, two-finger drag to scroll.
//   Direct mode: tap where you want to click, drag to drag, press and hold to right-click.
//   Both: pinch to zoom the picture, then move two fingers to look around.
//   Keyboard button: opens the phone's keyboard, plus keys phones don't have.
//
// Uses send(), norm(), active(), surface, connected, wheelY/queue() and remoteOS from viewer.js.
(function () {
  // Phones and tablets start with touch controls; a laptop with a touchscreen gets them at the first touch.
  let isTouch = matchMedia('(pointer: coarse)').matches;
  const store = {
    get(k, d) { try { return localStorage.getItem(k) || d; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* private mode */ } },
  };

  let tmode = store.get('hopdesk-touch-mode', 'touchpad');
  const cur = { x: 0.5, y: 0.5 };     // the remote pointer, 0..1 of the picture
  const view = { s: 1, tx: 0, ty: 0 }; // local zoom and pan of the picture

  const bar = $('touchbar'), cursorEl = $('tcursor'), kbd = $('kbd'), kbar = $('kbar');

  // ---------- geometry ----------

  // Where the picture is drawn on screen (object-fit: contain, plus our zoom).
  function picRect() {
    const el = active(), r = pageRect(el);
    const iw = el === video ? video.videoWidth : canvas.width;
    const ih = el === video ? video.videoHeight : canvas.height;
    let left = r.left, top = r.top, w = r.width, h = r.height;
    if (iw && ih) {
      const s = Math.min(r.width / iw, r.height / ih);
      w = iw * s; h = ih * s;
      left += (r.width - w) / 2; top += (r.height - h) / 2;
    }
    return { left, top, w, h };
  }
  const clamp01 = (v) => Math.min(1, Math.max(0, v));
  function toNorm(cx, cy) { const p = picRect(); return { x: clamp01((cx - p.left) / p.w), y: clamp01((cy - p.top) / p.h) }; }
  function toClient(n) { const p = picRect(); return { x: p.left + n.x * p.w, y: p.top + n.y * p.h }; }

  // ---------- zoom ----------

  function applyView() {
    const sr = pageRect(surface);
    view.s = Math.min(5, Math.max(1, view.s));
    if (view.s === 1) { view.tx = view.ty = 0; } else {
      view.tx = Math.min(0, Math.max(sr.width - sr.width * view.s, view.tx));
      view.ty = Math.min(0, Math.max(sr.height - sr.height * view.s, view.ty));
    }
    const t = view.s === 1 ? '' : `translate(${view.tx}px, ${view.ty}px) scale(${view.s})`;
    for (const el of [video, canvas]) { el.style.transformOrigin = '0 0'; el.style.transform = t; }
    $('tZoom').hidden = view.s === 1;
    drawCursor();
  }
  function zoomAt(newS, cx, cy) {
    const sr = pageRect(surface);
    const px = cx - sr.left, py = cy - sr.top;
    const k = Math.min(5, Math.max(1, newS)) / view.s;
    view.tx = px - (px - view.tx) * k;
    view.ty = py - (py - view.ty) * k;
    view.s *= k;
    applyView();
  }
  function resetZoom() { view.s = 1; applyView(); }

  // Keep the pointer on screen when zoomed in (touchpad mode).
  function followCursor() {
    if (view.s === 1) return;
    const sr = pageRect(surface), c = toClient(cur), m = 48;
    let dx = 0, dy = 0;
    if (c.x < sr.left + m) dx = sr.left + m - c.x; else if (c.x > sr.right - m) dx = sr.right - m - c.x;
    if (c.y < sr.top + m) dy = sr.top + m - c.y; else if (c.y > sr.bottom - m) dy = sr.bottom - m - c.y;
    if (dx || dy) { view.tx += dx; view.ty += dy; applyView(); }
  }

  // ---------- pointer ----------

  function drawCursor() {
    const show = !bar.hidden && tmode === 'touchpad'; // the bar shows only during a touch session
    cursorEl.toggleAttribute('hidden', !show); // an SVG element has no .hidden property
    if (!show) return;
    const sr = pageRect(surface), c = toClient(cur);
    cursorEl.style.transform = `translate(${c.x - sr.left}px, ${c.y - sr.top}px)`;
  }
  let moveQueued = false;
  function sendMove() {
    if (moveQueued) return;
    moveQueued = true;
    requestAnimationFrame(() => { moveQueued = false; send({ t: 'move', x: cur.x, y: cur.y }); });
  }
  function button(b, down, n) { send({ t: down ? 'down' : 'up', b, n: n || 1, x: cur.x, y: cur.y }); }
  function click(b, n) { send({ t: 'move', x: cur.x, y: cur.y }); button(b, true, n); button(b, false, n); }

  // ---------- gestures ----------

  const TAP_MS = 280, TAP_SLOP = 10, DOUBLE_MS = 320, HOLD_MS = 550;
  let g = null;                 // the gesture in progress
  let lastTap = { t: 0, x: 0, y: 0, n: 0 };

  const pt = (t) => toPage(t.clientX, t.clientY); // page coordinates, even when turned sideways
  const dist = (a, b) => Math.hypot(a.x - b.x, a.y - b.y);
  const mid = (a, b) => ({ x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 });

  function onStart(e) {
    if (!connected || e.target.closest('#touchbar')) return;
    e.preventDefault();
    if (!isTouch) { isTouch = true; HopTouch.onConnected(); }
    const ts = e.touches, now = performance.now();
    if (ts.length === 1) {
      const p = pt(ts[0]);
      const again = now - lastTap.t < DOUBLE_MS && dist(p, lastTap) < 40;
      g = { kind: 'one', start: p, last: p, t0: now, moved: false, again, dragging: false, hold: null };
      if (tmode === 'direct') {
        Object.assign(cur, toNorm(p.x, p.y));
        sendMove();
        g.hold = setTimeout(() => { if (g && !g.moved) { click(2); g.done = true; navigator.vibrate && navigator.vibrate(10); } }, HOLD_MS);
      }
    } else if (ts.length === 2) {
      if (g && g.dragging) button(0, false);
      if (g && g.hold) clearTimeout(g.hold);
      const a = pt(ts[0]), b = pt(ts[1]);
      g = { kind: 'two', d0: dist(a, b), m0: mid(a, b), last: mid(a, b), s0: view.s, t0: now, type: null };
    } else {
      g = { kind: 'ignore' };
    }
  }

  function onMove(e) {
    if (!g || !connected) return;
    e.preventDefault();
    const ts = e.touches;
    if (g.kind === 'one' && ts.length === 1) {
      const p = pt(ts[0]);
      if (!g.moved && dist(p, g.start) > TAP_SLOP) {
        g.moved = true;
        if (g.hold) clearTimeout(g.hold);
        // Double-tap then drag (touchpad) or any drag (direct): hold the button down.
        if (g.again || tmode === 'direct') { g.dragging = true; button(0, true); }
      }
      if (!g.moved) return;
      if (tmode === 'direct') {
        Object.assign(cur, toNorm(p.x, p.y));
      } else {
        const r = picRect();
        const dx = p.x - g.last.x, dy = p.y - g.last.y;
        const speed = Math.hypot(dx, dy);
        const accel = Math.min(2.5, 1 + speed / 18); // small moves are precise, quick flicks go far
        cur.x = clamp01(cur.x + (dx * accel) / r.w);
        cur.y = clamp01(cur.y + (dy * accel) / r.h);
        followCursor();
      }
      g.last = p;
      drawCursor();
      sendMove();
    } else if (g.kind === 'two' && ts.length === 2) {
      const a = pt(ts[0]), b = pt(ts[1]), m = mid(a, b), d = dist(a, b);
      if (!g.type) {
        if (Math.abs(d - g.d0) > 28) g.type = 'pinch';
        else if (dist(m, g.m0) > 12) g.type = view.s > 1 ? 'pan' : 'scroll';
        else return;
      }
      if (g.type === 'pinch' || g.type === 'pan') {
        if (g.type === 'pinch') zoomAt(g.s0 * (d / g.d0), m.x, m.y);
        view.tx += m.x - g.last.x; view.ty += m.y - g.last.y;
        applyView();
      } else {
        // Natural scrolling: the content follows your fingers.
        wheelX -= (m.x - g.last.x) * 2; wheelY -= (m.y - g.last.y) * 2; queue();
      }
      g.last = m;
    }
  }

  function onEnd(e) {
    if (!g || !connected) return;
    e.preventDefault();
    if (e.touches.length > 0) return; // wait until every finger is up
    const now = performance.now(), quick = now - g.t0 < TAP_MS;
    if (g.kind === 'one') {
      if (g.hold) clearTimeout(g.hold);
      if (g.dragging) button(0, false);
      else if (!g.moved && !g.done && quick) {
        const n = g.again ? Math.min(lastTap.n + 1, 3) : 1;
        if (tmode === 'direct') Object.assign(cur, toNorm(g.start.x, g.start.y));
        click(0, n);
        lastTap = { t: now, x: g.start.x, y: g.start.y, n };
        g = null;
        return;
      }
    } else if (g.kind === 'two' && !g.type && quick) {
      if (tmode === 'direct') Object.assign(cur, toNorm(g.m0.x, g.m0.y));
      click(2);
    }
    lastTap.t = 0;
    g = null;
  }

  surface.addEventListener('touchstart', onStart, { passive: false });
  surface.addEventListener('touchmove', onMove, { passive: false });
  surface.addEventListener('touchend', onEnd, { passive: false });
  surface.addEventListener('touchcancel', onEnd, { passive: false });
  // Safari's own pinch gesture events would zoom the whole page.
  surface.addEventListener('gesturestart', (e) => { if (connected) e.preventDefault(); });

  // ---------- keyboard ----------

  const SENTINEL = ' ';
  const sticky = new Set(); // modifiers waiting for the next key
  const MOD = { ctrl: 'ControlLeft', alt: 'AltLeft', meta: 'MetaLeft', shift: 'ShiftLeft' };

  function codeFor(ch) {
    if (/^[a-z]$/i.test(ch)) return 'Key' + ch.toUpperCase();
    if (/^[0-9]$/.test(ch)) return 'Digit' + ch;
    return { ' ': 'Space', '-': 'Minus', '=': 'Equal', ',': 'Comma', '.': 'Period', '/': 'Slash', ';': 'Semicolon', "'": 'Quote', '[': 'BracketLeft', ']': 'BracketRight', '\\': 'Backslash', '`': 'Backquote' }[ch] || '';
  }
  function key(code, k, down) { send({ t: 'key', code, key: k || '', down }); }
  // A key press with any sticky modifiers (Ctrl, Alt, Cmd, Shift) held around it.
  function press(code, k) {
    const mods = [...sticky].map((m) => MOD[m]);
    mods.forEach((c) => key(c, '', true));
    key(code, k, true); key(code, k, false);
    mods.slice().reverse().forEach((c) => key(c, '', false));
    clearSticky();
  }
  function typeText(text) {
    for (const ch of text) { // by code point, so emoji stay whole
      if (ch === '\n') press('Enter', 'Enter');
      else if (sticky.size && codeFor(ch)) press(codeFor(ch), ch);
      else { key('', ch, true); key('', ch, false); }
    }
    if (sticky.size) clearSticky();
  }
  function clearSticky() {
    sticky.clear();
    kbar.querySelectorAll('[data-mod]').forEach((b) => b.setAttribute('aria-pressed', 'false'));
  }

  function resetField() { kbd.value = SENTINEL; kbd.setSelectionRange(1, 1); }
  let composing = false;
  kbd.addEventListener('compositionstart', () => { composing = true; });
  kbd.addEventListener('compositionend', (e) => { composing = false; if (e.data) typeText(e.data); resetField(); });
  kbd.addEventListener('input', (e) => {
    if (composing || e.isComposing) return;
    const it = e.inputType || '';
    if (it === 'deleteContentBackward' || kbd.value === '') press('Backspace', 'Backspace');
    else if (it === 'deleteContentForward') press('Delete', 'Delete');
    else if (it === 'insertLineBreak' || it === 'insertParagraph') press('Enter', 'Enter');
    else if (it === 'insertFromPaste') { if (kbd.value.length <= 2001) typeText(kbd.value.slice(1)); }
    else if (e.data) typeText(e.data);
    else if (kbd.value.length > 1) typeText(kbd.value.slice(1));
    resetField();
  });
  const SPECIAL = { Enter: 'Enter', Tab: 'Tab', Escape: 'Escape', ArrowLeft: 'ArrowLeft', ArrowRight: 'ArrowRight', ArrowUp: 'ArrowUp', ArrowDown: 'ArrowDown', Delete: 'Delete', Home: 'Home', End: 'End', PageUp: 'PageUp', PageDown: 'PageDown' };
  kbd.addEventListener('keydown', (e) => {
    if (e.isComposing || e.keyCode === 229) return;
    if (SPECIAL[e.key]) { e.preventDefault(); press(SPECIAL[e.key], e.key); }
    else if (e.key === 'Backspace' && kbd.value === SENTINEL) { e.preventDefault(); press('Backspace', 'Backspace'); }
  });

  let wasRotated = false;
  function openKeyboard() {
    // The phone's keyboard is always upright, so stand the page up while typing.
    wasRotated = rotated;
    if (rotated) setCssRotated(false);
    resetField(); kbd.focus();
  }
  kbd.addEventListener('focus', () => { document.body.classList.add('kbd-open'); placeKeyBar(); });
  kbd.addEventListener('blur', () => {
    document.body.classList.remove('kbd-open'); clearSticky();
    if (wasRotated) { wasRotated = false; setTimeout(() => { if (connected) setCssRotated(true); }, 250); }
  });

  // The extra keys sit just above the phone's keyboard.
  function placeKeyBar() {
    const vv = window.visualViewport;
    const bottom = vv ? Math.max(0, window.innerHeight - (vv.height + vv.offsetTop)) : 0;
    kbar.style.bottom = bottom + 'px';
  }
  if (window.visualViewport) {
    visualViewport.addEventListener('resize', placeKeyBar);
    visualViewport.addEventListener('scroll', placeKeyBar);
  }

  // Buttons keep the keyboard open: don't let them take focus.
  kbar.addEventListener('pointerdown', (e) => { if (e.target.closest('button')) e.preventDefault(); });
  kbar.addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (!b) return;
    if (b.dataset.mod) {
      const on = !sticky.has(b.dataset.mod);
      on ? sticky.add(b.dataset.mod) : sticky.delete(b.dataset.mod);
      b.setAttribute('aria-pressed', String(on));
    } else if (b.dataset.code) {
      press(b.dataset.code, b.dataset.code);
    } else if (b.id === 'kDone') {
      kbd.blur();
    }
  });

  // ---------- rotate ----------
  // Android: full screen, locked to landscape. Elsewhere (iPhone), turn the page
  // itself 90° so you can hold the phone sideways even with rotation lock on.
  let nativeRotated = false;

  function setCssRotated(on) {
    rotated = on;
    document.body.classList.toggle('rotated', on);
    const st = document.body.style;
    if (on) {
      st.width = window.innerHeight + 'px';
      st.height = window.innerWidth + 'px';
      st.transform = `translate(${window.innerWidth}px, 0) rotate(90deg)`;
    } else {
      st.width = st.height = st.transform = '';
    }
    afterRotate();
  }
  function afterRotate() {
    const on = rotated || nativeRotated;
    $('tRotate').setAttribute('aria-pressed', String(on));
    $('tRotate').querySelector('span').textContent = on ? 'Upright' : 'Rotate';
    updateRotateButton();
    requestAnimationFrame(() => { applyView(); placeKeyBar(); });
  }
  function updateRotateButton() {
    // Offer it while the phone is upright (or to undo it); a phone already held sideways doesn't need it.
    const portrait = window.innerHeight > window.innerWidth;
    $('tRotate').hidden = !(portrait || rotated || nativeRotated);
  }
  async function rotate() {
    if (rotated) return setCssRotated(false);
    if (nativeRotated) {
      nativeRotated = false;
      try { screen.orientation.unlock(); } catch (e) { /* not locked */ }
      if (document.fullscreenElement) await document.exitFullscreen().catch(() => {});
      return afterRotate();
    }
    if (document.fullscreenEnabled && screen.orientation && screen.orientation.lock) {
      try {
        await document.documentElement.requestFullscreen({ navigationUI: 'hide' });
        await screen.orientation.lock('landscape');
        nativeRotated = true;
        return afterRotate();
      } catch (e) {
        if (document.fullscreenElement) await document.exitFullscreen().catch(() => {});
      }
    }
    setCssRotated(true);
  }
  document.addEventListener('fullscreenchange', () => {
    if (!document.fullscreenElement && nativeRotated) {
      nativeRotated = false;
      try { screen.orientation.unlock(); } catch (e) { /* ignore */ }
      afterRotate();
    }
  });
  window.addEventListener('resize', () => {
    if (!rotated) return updateRotateButton();
    // The phone was actually turned sideways: our own turn would make it upside down.
    if (window.innerWidth > window.innerHeight) setCssRotated(false);
    else setCssRotated(true); // keep the size in step when the address bar shows or hides
  });

  // ---------- toolbar ----------

  function setMode(m) {
    tmode = m;
    store.set('hopdesk-touch-mode', m);
    $('tMode').querySelector('span').textContent = m === 'touchpad' ? 'Touchpad' : 'Direct';
    $('tMode').title = m === 'touchpad' ? 'Touchpad mode: drag to move the pointer, tap to click' : 'Direct mode: tap exactly where you want to click';
    $('tMode').dataset.mode = m;
    drawCursor();
  }
  $('tKbd').addEventListener('click', openKeyboard);
  $('tMode').addEventListener('click', () => {
    setMode(tmode === 'touchpad' ? 'direct' : 'touchpad');
    toast(tmode === 'touchpad' ? 'Touchpad: drag to move the pointer, tap to click' : 'Direct: tap exactly where you want to click');
  });
  $('tZoom').addEventListener('click', resetZoom);
  $('tRotate').addEventListener('click', rotate);
  $('tHelp').addEventListener('click', () => { if (rotated) setCssRotated(false); startTouchTour(true); });

  const TOUCH_TOUR = () => [
    { el: '#surface', title: 'Control it with your fingers',
      text: tmode === 'touchpad'
        ? ['Touchpad mode: drag anywhere to move the pointer, then tap to click. Double-tap and drag to select text or move a window.',
           'Two fingers: tap to right-click, drag to scroll. Pinch to zoom in on small text.']
        : ['Direct mode: tap exactly where you want to click. Drag to drag, and press and hold to right-click.',
           'Two fingers: drag to scroll. Pinch to zoom in on small text.'] },
    { el: '#tMode', title: 'Touchpad or direct', text: ['Switch between moving a pointer like a laptop touchpad, and tapping right on the screen.'] },
    { el: '#tRotate', title: 'See it bigger', text: ['Tap Rotate and turn your phone sideways: the computer\'s screen fills your whole display. Tap Upright to go back to the menu bar.'] },
    { el: '#tKbd', title: 'Keyboard', text: ['Opens your keyboard, with extra keys phones don\'t have: Esc, Tab, Ctrl, Alt, Cmd and the arrows. Tap Ctrl then C for Ctrl+C.'] },
    { el: '#tHelp', title: 'Need a reminder?', text: ['Tap here any time to see these tips again.'] },
  ];
  function startTouchTour(force) {
    if (window.HopUI) HopUI.tour.start({ id: 'viewer-touch-v1', steps: TOUCH_TOUR(), force, doneLabel: 'Got it' });
  }

  window.HopTouch = {
    get isTouch() { return isTouch; },
    onConnected() {
      if (!isTouch) return;
      document.body.classList.add('touch');
      bar.hidden = false;
      $('kMeta').textContent = remoteOS === 'darwin' ? 'Cmd' : 'Win';
      cur.x = cur.y = 0.5;
      setMode(tmode);
      resetZoom();
      updateRotateButton();
      setTimeout(() => { if (connected) startTouchTour(false); }, 1500);
    },
    onDisconnected() {
      bar.hidden = true;
      cursorEl.setAttribute('hidden', '');
      if (document.activeElement === kbd) kbd.blur();
      document.body.classList.remove('touch');
      if (rotated) setCssRotated(false);
      if (nativeRotated) rotate();
      resetZoom();
    },
    startTour: startTouchTour,
  };
  window.addEventListener('resize', () => { applyView(); placeKeyBar(); });
  window.HopTouch.rotate = rotate;
})();
