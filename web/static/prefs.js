// Applies the theme and animation preferences before the first paint
// (loaded without defer), and exposes fugacePrefs for the settings panel.
'use strict';
(() => {
  const root = document.documentElement;
  const dark = matchMedia('(prefers-color-scheme: dark)');
  const calm = matchMedia('(prefers-reduced-motion: reduce)');
  const pref = { theme: 'auto', motion: 'auto' };
  try {
    pref.theme = localStorage.getItem('fugace.theme') || 'auto';
    pref.motion = localStorage.getItem('fugace.motion') || 'auto';
  } catch { /* storage disabled: system defaults */ }
  const apply = () => {
    root.dataset.theme = pref.theme === 'auto' ? (dark.matches ? 'dark' : 'light') : pref.theme;
    root.dataset.motion = pref.motion === 'auto' ? (calm.matches ? 'reduced' : 'full') : pref.motion;
  };
  apply();
  dark.addEventListener('change', apply);
  calm.addEventListener('change', apply);
  window.fugacePrefs = Object.freeze({
    get: (kind) => pref[kind],
    set(kind, value) {
      if (!(kind in pref)) return;
      pref[kind] = value;
      try { localStorage.setItem('fugace.' + kind, value); } catch { /* not remembered */ }
      apply();
    },
  });
})();
