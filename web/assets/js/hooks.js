// Generic hooks: data fetching, intervals, tweened numbers, hash routing.
import { useCallback, useEffect, useRef, useState } from '../vendor/htm-preact.js';
import { clearApiCache } from './api.js';
import { reducedMotion } from './util.js';

/**
 * useFetch(loader, deps) -> {data, error, loading, reload, setData}
 * Keeps the previous data while reloading so the UI does not flash.
 */
export function useFetch(loader, deps = [], { interval = 0, keep = true } = {}) {
  const [state, setState] = useState({ data: undefined, error: null, loading: true });
  const seq = useRef(0);
  const load = useCallback(async (silent = false) => {
    const my = ++seq.current;
    if (!silent) setState((s) => ({ ...s, loading: true, data: keep ? s.data : undefined }));
    try {
      const data = await loader();
      if (my === seq.current) setState({ data, error: null, loading: false });
      return data;
    } catch (error) {
      if (my === seq.current) setState((s) => ({ ...s, error, loading: false }));
      return undefined;
    }
  }, deps); // eslint-disable-line react-hooks/exhaustive-deps
  // Opening a page may reuse recent lists; a reload asked for (button, after a change, poll) never does.
  const reload = useCallback((silent) => { clearApiCache(); return load(silent); }, [load]);
  useEffect(() => { load(); }, [load]);
  useInterval(() => reload(true), interval);
  const setData = (fn) => setState((s) => ({ ...s, data: typeof fn === 'function' ? fn(s.data) : fn }));
  return { ...state, reload, setData };
}

export function useInterval(fn, ms) {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    if (!ms) return undefined;
    // A hidden tab does not poll; it catches up as soon as it is visible again.
    const id = setInterval(() => { if (!document.hidden) ref.current(); }, ms);
    const onShow = () => { if (!document.hidden) ref.current(); };
    document.addEventListener('visibilitychange', onShow);
    return () => { clearInterval(id); document.removeEventListener('visibilitychange', onShow); };
  }, [ms]);
}

/** Re-render every `ms` (for relative times). */
export function useTick(ms = 1000) {
  const [, set] = useState(0);
  useInterval(() => set((n) => n + 1), ms);
}

/** Animate a number towards `value` (easeOutCubic). Jumps when reduced motion is on. */
export function useTween(value, duration = 700) {
  const [shown, setShown] = useState(value ?? 0);
  const from = useRef(value ?? 0);
  const raf = useRef(0);
  useEffect(() => {
    if (value == null || Number.isNaN(value)) return undefined;
    cancelAnimationFrame(raf.current);
    const start = from.current;
    if (reducedMotion() || start === value) { from.current = value; setShown(value); return undefined; }
    const t0 = performance.now();
    const step = (now) => {
      const p = Math.min(1, (now - t0) / duration);
      const e = 1 - (1 - p) ** 3;
      const v = start + (value - start) * e;
      from.current = v;
      setShown(v);
      if (p < 1) raf.current = requestAnimationFrame(step);
    };
    raf.current = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf.current);
  }, [value]);
  return shown;
}
