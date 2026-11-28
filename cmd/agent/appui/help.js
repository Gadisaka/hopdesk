'use strict';
// Guide page: back button, setup tabs, word search, table-of-contents highlight.
(function () {
  const $ = (id) => document.getElementById(id);

  // Back: the settings page opens the guide in its own window; the viewer links to it.
  const back = $('back');
  if (window.opener && !window.opener.closed) {
    back.querySelector('span').textContent = 'Close guide';
    back.addEventListener('click', (e) => { e.preventDefault(); window.close(); });
  }

  // Setup tabs
  const tabs = [...document.querySelectorAll('.tabs [role=tab]')];
  function pick(tab) {
    tabs.forEach((t) => {
      const on = t === tab;
      t.setAttribute('aria-selected', String(on));
      $(t.getAttribute('aria-controls')).hidden = !on;
    });
  }
  tabs.forEach((t) => t.addEventListener('click', () => pick(t)));

  // Word search
  const input = $('glossFilter'), terms = [...document.querySelectorAll('#gloss .term')];
  function filter() {
    const q = input.value.trim().toLowerCase();
    let n = 0;
    terms.forEach((t) => {
      const hit = !q || t.textContent.toLowerCase().includes(q);
      t.hidden = !hit;
      if (hit) n++;
    });
    $('glossEmpty').hidden = n > 0;
    $('glossCount').textContent = q ? `${n} of ${terms.length}` : `${terms.length} words`;
  }
  input.addEventListener('input', filter);
  filter();

  // Links to a word clear the search so the word is visible.
  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[href^="#w-"]');
    if (a && input.value) { input.value = ''; filter(); }
  });

  // Highlight the section being read: the last one whose heading has scrolled past the header.
  const links = [...document.querySelectorAll('nav.toc a')];
  const sections = links.map((a) => document.getElementById(a.getAttribute('href').slice(1)));
  function spy() {
    let k = 0;
    sections.forEach((sec, n) => { if (sec && sec.getBoundingClientRect().top < 140) k = n; });
    if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 4) k = sections.length - 1;
    links.forEach((a, n) => a.classList.toggle('on', n === k));
  }
  window.addEventListener('scroll', spy, { passive: true });
  window.addEventListener('resize', spy);
  spy();
})();
