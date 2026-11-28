// HopDesk shared UI helpers: copy buttons and the first-run tour.
// Used by the viewer, the app's settings page and the guide. No dependencies.
'use strict';

(function () {
  const ICON_COPY = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V6a2 2 0 0 1 2-2h9"/></svg>';
  const ICON_CHECK = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>';

  // ---------- copy ----------

  async function copyText(text) {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return true;
      }
    } catch (e) { /* fall through */ }
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    ta.remove();
    return ok;
  }

  function sourceText(btn) {
    const el = document.getElementById(btn.dataset.copyFrom);
    if (!el) return '';
    const v = ('value' in el && el.tagName !== 'BUTTON') ? el.value : el.textContent;
    return (v || '').trim();
  }

  // <button data-copy-from="elementId">: copies that element's text (or input value)
  // and briefly shows "Copied".
  function wireCopy(btn) {
    if (btn.dataset.copyWired) return;
    btn.dataset.copyWired = '1';
    btn.classList.add('copy');
    const label = btn.dataset.copyLabel || 'Copy';
    const iconOnly = btn.classList.contains('icon');
    const idle = ICON_COPY + (iconOnly ? '' : '<span>' + label + '</span>');
    btn.innerHTML = idle;
    if (!btn.title) btn.title = 'Copy to clipboard';
    btn.setAttribute('aria-label', btn.getAttribute('aria-label') || btn.title);
    btn.addEventListener('click', async (ev) => {
      ev.preventDefault();
      const text = sourceText(btn);
      if (!text || text === '—') return;
      const ok = await copyText(text);
      btn.classList.toggle('copied', ok);
      btn.innerHTML = ok ? ICON_CHECK + (iconOnly ? '' : '<span>Copied</span>')
                         : ICON_COPY + (iconOnly ? '' : '<span>Press Ctrl+C</span>');
      clearTimeout(btn._t);
      btn._t = setTimeout(() => { btn.classList.remove('copied'); btn.innerHTML = idle; }, 1600);
    });
  }

  function wireAllCopy(root) {
    (root || document).querySelectorAll('[data-copy-from]').forEach(wireCopy);
  }

  // ---------- tour ----------

  const KEY = (id) => 'hopdesk-tour-' + id;
  function store(id, v) { try { localStorage.setItem(KEY(id), v); } catch (e) { /* private mode */ } }
  function seen(id) { try { return !!localStorage.getItem(KEY(id)); } catch (e) { return false; } }

  function visible(el) {
    if (!el) return false;
    if (el.closest('[hidden]')) return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  }

  function resolve(step) {
    if (!step.el) return null;
    const el = typeof step.el === 'function' ? step.el() : document.querySelector(step.el);
    return visible(el) ? el : undefined; // undefined = skip this step
  }

  let active = null;

  // HopTour.start({ id, steps: [{ el, title, text }], force, onEnd })
  // Steps without `el` are shown centered. Steps whose element isn't on screen are skipped.
  // Returns false if the tour was already seen (and not forced).
  function start(opts) {
    if (active) active.end(false);
    if (!opts.force && seen(opts.id)) return false;
    const steps = opts.steps.filter((s) => resolve(s) !== undefined);
    if (!steps.length) return false;

    const block = document.createElement('div');
    block.className = 'tour-block';
    const hole = document.createElement('div');
    hole.className = 'tour-hole';
    const pop = document.createElement('div');
    pop.className = 'tour-pop';
    pop.setAttribute('role', 'dialog');
    pop.setAttribute('aria-modal', 'true');
    pop.setAttribute('aria-labelledby', 'tourTitle');
    pop.innerHTML =
      '<div class="tour-top"><span class="tour-count"></span><button class="tour-x icon ghost sm" aria-label="Skip tour" title="Skip tour">' +
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6 6 18"/></svg></button></div>' +
      '<h3 id="tourTitle"></h3><div class="tour-text"></div>' +
      '<div class="tour-dots"></div>' +
      '<div class="tour-btns"><button class="tour-skip ghost sm">Skip tour</button><span class="tour-grow"></span>' +
      '<button class="tour-back sm">Back</button><button class="tour-next primary sm">Next</button></div>';
    document.body.append(block, hole, pop);

    const $ = (s) => pop.querySelector(s);
    const dots = $('.tour-dots');
    steps.forEach(() => dots.appendChild(document.createElement('i')));
    let i = 0;
    let target = null;

    function place() {
      const vw = window.innerWidth, vh = window.innerHeight, m = 12;
      pop.style.maxWidth = Math.min(380, vw - 2 * m) + 'px';
      if (!target) {
        hole.style.cssText = 'left:50%;top:50%;width:1px;height:1px';
        hole.classList.add('none');
        pop.classList.add('center');
        pop.style.left = pop.style.top = '';
        return;
      }
      hole.classList.remove('none');
      pop.classList.remove('center');
      const r = target.getBoundingClientRect();
      const pad = 6;
      const top = Math.max(r.top - pad, 4), bottom = Math.min(r.bottom + pad, vh - 4);
      hole.style.cssText = `left:${r.left - pad}px;top:${top}px;width:${r.width + 2 * pad}px;height:${Math.max(bottom - top, 0)}px`;
      const pw = pop.offsetWidth, ph = pop.offsetHeight;
      let y;
      if (bottom + m + ph <= vh) y = bottom + m;            // below
      else if (top - m - ph >= 0) y = top - m - ph;         // above
      else y = vh - ph - m;                                 // overlap, pinned to bottom
      let x = r.left + r.width / 2 - pw / 2;
      x = Math.max(m, Math.min(x, vw - pw - m));
      pop.style.left = x + 'px';
      pop.style.top = Math.max(m, y) + 'px';
    }

    function show(n) {
      i = n;
      const s = steps[i];
      target = resolve(s) || null;
      $('.tour-count').textContent = s.el ? `${i + 1} of ${steps.length}` : (i === 0 ? 'Quick tour' : `${i + 1} of ${steps.length}`);
      $('#tourTitle').textContent = s.title;
      const tx = $('.tour-text');
      tx.replaceChildren();
      (Array.isArray(s.text) ? s.text : [s.text]).forEach((p) => {
        const el = document.createElement('p');
        el.textContent = p;
        tx.appendChild(el);
      });
      [...dots.children].forEach((d, k) => d.classList.toggle('on', k === i));
      $('.tour-back').hidden = i === 0;
      const last = i === steps.length - 1;
      $('.tour-next').textContent = last ? (opts.doneLabel || 'Done') : (i === 0 && !s.el ? 'Start tour' : 'Next');
      $('.tour-skip').hidden = last;
      if (target) {
        // Put the target in the middle of the screen (or near the top if it's tall),
        // clear of sticky headers and bars.
        const r = target.getBoundingClientRect(), vh = window.innerHeight;
        const want = r.height > vh * 0.5 ? 84 : (vh - r.height) / 2 - 60;
        if (Math.abs(r.top - want) > 24) window.scrollBy({ top: r.top - Math.max(84, want), behavior: 'instant' });
      }
      pop.classList.remove('in'); void pop.offsetWidth; pop.classList.add('in');
      place();
      requestAnimationFrame(place);
      $('.tour-next').focus({ preventScroll: true });
    }

    function end(done) {
      store(opts.id, done ? 'done' : 'skipped');
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
      document.removeEventListener('keydown', onKey, true);
      block.remove(); hole.remove(); pop.remove();
      active = null;
      if (opts.onEnd) opts.onEnd(done);
    }

    function onKey(e) {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); end(false); }
      else if (e.key === 'ArrowRight') { e.preventDefault(); e.stopPropagation(); next(); }
      else if (e.key === 'ArrowLeft' && i > 0) { e.preventDefault(); e.stopPropagation(); show(i - 1); }
      else if (e.key === 'Tab') {
        const f = [...pop.querySelectorAll('button:not([hidden])')];
        const k = f.indexOf(document.activeElement);
        e.preventDefault();
        f[(k + (e.shiftKey ? -1 : 1) + f.length) % f.length].focus();
      } else { e.stopPropagation(); }
    }
    function next() { if (i < steps.length - 1) show(i + 1); else end(true); }

    $('.tour-next').onclick = next;
    $('.tour-back').onclick = () => show(Math.max(0, i - 1));
    $('.tour-skip').onclick = () => end(false);
    $('.tour-x').onclick = () => end(false);
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, true);
    document.addEventListener('keydown', onKey, true);

    active = { end };
    show(0);
    return true;
  }

  window.HopUI = { copyText, wireCopy, wireAllCopy, tour: { start, seen, reset: (id) => { try { localStorage.removeItem(KEY(id)); } catch (e) {} } } };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', () => wireAllCopy());
  else wireAllCopy();
})();
