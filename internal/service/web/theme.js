// The sign-in, app connection and GitHub/Microsoft return pages use the theme
// chosen in the site app (/app/), which keeps it in localStorage on this
// origin. Runs in <head>, before anything paints. No saved choice (or
// "system"): the system's light or dark setting. The same rule as
// app-start.js and the Viewer's index.html.
try {
  const pref = localStorage.getItem('slate-viewer.theme');
  const dark = pref === 'dark' || (pref !== 'light' && pref !== 'paper' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.dataset.theme = pref === 'paper' ? 'paper' : dark ? 'dark' : 'light';
} catch {}
