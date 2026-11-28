'use strict';
// Theme: "system" (default), "light" or "dark". Loaded in <head> so the page
// never flashes the wrong colors. Any element with data-theme-choice switches it.
(function () {
  const KEY = 'hopdesk-theme';
  const mq = window.matchMedia('(prefers-color-scheme: dark)');
  const stored = () => { try { return localStorage.getItem(KEY) || 'system'; } catch (_) { return 'system'; } };
  function apply(pref) {
    const root = document.documentElement;
    root.dataset.theme = pref === 'system' ? (mq.matches ? 'dark' : 'light') : pref;
    root.dataset.themePref = pref;
    document.querySelectorAll('[data-theme-choice]').forEach((b) =>
      b.setAttribute('aria-pressed', String(b.dataset.themeChoice === pref)));
  }
  apply(stored());
  mq.addEventListener('change', () => { if (stored() === 'system') apply('system'); });
  document.addEventListener('DOMContentLoaded', () => apply(stored()));
  document.addEventListener('click', (e) => {
    const b = e.target.closest && e.target.closest('[data-theme-choice]');
    if (!b) return;
    try { localStorage.setItem(KEY, b.dataset.themeChoice); } catch (_) {}
    apply(b.dataset.themeChoice);
  });
})();
