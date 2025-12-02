// Resolve the theme before first paint: stored choice, else the OS preference.
// A classic (blocking) script, kept external so the CSP needs no 'unsafe-inline'.
(function () {
  var t = null;
  try { t = localStorage.getItem('cdc.theme'); } catch (e) {}
  if (t !== 'light' && t !== 'dark') t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  document.documentElement.dataset.theme = t;
})();
