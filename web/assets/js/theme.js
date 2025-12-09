// Light/dark theme: data-theme on <html>, explicit choice stored in localStorage.
import { useEffect, useState } from '../vendor/htm-preact.js';
import { lsGet, lsSet } from './util.js';

export function currentTheme() { return document.documentElement.dataset.theme || 'light'; }
export function setTheme(t) {
  document.documentElement.dataset.theme = t;
  lsSet('cdc.theme', t);
  window.dispatchEvent(new CustomEvent('cdc:theme', { detail: t }));
}
export function useTheme() {
  const [t, setT] = useState(currentTheme());
  useEffect(() => {
    const on = (e) => setT(e.detail);
    window.addEventListener('cdc:theme', on);
    // follow the OS while the user has not chosen explicitly
    const mq = matchMedia('(prefers-color-scheme: dark)');
    const onMq = () => { if (!lsGet('cdc.theme')) { document.documentElement.dataset.theme = mq.matches ? 'dark' : 'light'; setT(currentTheme()); } };
    mq.addEventListener('change', onMq);
    return () => { window.removeEventListener('cdc:theme', on); mq.removeEventListener('change', onMq); };
  }, []);
  return t;
}

