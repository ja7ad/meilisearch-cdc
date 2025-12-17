// Tiny hand-written SVG charts: Sparkline and AreaChart (responsive, with hover readout).
import { html, useEffect, useRef, useState } from '../vendor/htm-preact.js';
import { fmtTime } from './util.js';

let gid = 0;
const useId = () => { const r = useRef(null); if (r.current == null) r.current = `g${++gid}`; return r.current; };

function useWidth() {
  const ref = useRef(null);
  const [w, setW] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return undefined;
    setW(el.clientWidth);
    const ro = new ResizeObserver(([e]) => setW(Math.round(e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

function path(pts) {
  if (!pts.length) return '';
  let d = `M${pts[0][0].toFixed(1)},${pts[0][1].toFixed(1)}`;
  for (let i = 1; i < pts.length; i++) {
    // smooth with a horizontal-tangent cubic between points
    const [x0, y0] = pts[i - 1];
    const [x1, y1] = pts[i];
    const cx = (x0 + x1) / 2;
    d += ` C${cx.toFixed(1)},${y0.toFixed(1)} ${cx.toFixed(1)},${y1.toFixed(1)} ${x1.toFixed(1)},${y1.toFixed(1)}`;
  }
  return d;
}

/** Sparkline({values:number[], tone}) — fixed-size inline trend line. */
export function Sparkline({ values = [], width = 120, height = 32, tone = 'primary', label }) {
  const id = useId();
  const v = values.slice(-60);
  if (v.length < 2) return html`<svg class="spark" width=${width} height=${height} aria-hidden="true"><line x1="0" x2=${width} y1=${height - 2} y2=${height - 2} class="spark-base" /></svg>`;
  const max = Math.max(...v, 1e-9);
  const pts = v.map((y, i) => [(i / (v.length - 1)) * width, height - 3 - (y / max) * (height - 6)]);
  const d = path(pts);
  const last = pts[pts.length - 1];
  return html`<svg class=${`spark spark-${tone}`} width=${width} height=${height} viewBox=${`0 0 ${width} ${height}`} role=${label ? 'img' : undefined} aria-label=${label} aria-hidden=${label ? undefined : 'true'}>
    <defs><linearGradient id=${id} x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="currentColor" stop-opacity=".32" /><stop offset="1" stop-color="currentColor" stop-opacity="0" /></linearGradient></defs>
    <path d=${`${d} L${width},${height} L0,${height} Z`} fill=${`url(#${id})`} />
    <path d=${d} fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" />
    <circle cx=${last[0]} cy=${last[1]} r="2.4" fill="currentColor" />
  </svg>`;
}

// At least 4 so the quarter ticks are whole numbers on idle charts.
const niceMax = (m) => {
  if (m <= 4) return 4;
  const p = 10 ** Math.floor(Math.log10(m));
  const n = m / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * p;
};

/**
 * AreaChart({series:[{name, tone, points:[{t, v}]}], height, format})
 * All series share the time axis and the y scale.
 */
export function AreaChart({ series = [], height = 220, format = (v) => String(Math.round(v)), label = 'Chart', empty = 'Collecting data…' }) {
  const [ref, w] = useWidth();
  const [hover, setHover] = useState(null);
  const ids = series.map(() => useId()); // eslint-disable-line react-hooks/rules-of-hooks
  const all = series.flatMap((s) => s.points);
  const pad = { l: 46, r: 12, t: 12, b: 26 };
  const W = Math.max(0, w);
  const iw = W - pad.l - pad.r;
  const ih = height - pad.t - pad.b;

  if (all.length < 2 || W === 0) {
    return html`<div class="chart" ref=${ref} style=${`height:${height}px`}>
      ${all.length < 2 && html`<div class="chart-empty"><span class="spinner" aria-hidden="true"></span>${empty}</div>`}
    </div>`;
  }
  const t0 = Math.min(...all.map((p) => p.t));
  const t1 = Math.max(...all.map((p) => p.t));
  const ymax = niceMax(Math.max(...all.map((p) => p.v)) * 1.1);
  const x = (t) => pad.l + (t1 === t0 ? iw : ((t - t0) / (t1 - t0)) * iw);
  const y = (v) => pad.t + ih - (v / ymax) * ih;
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * ymax);
  const xticks = W > 420 ? [0, 0.25, 0.5, 0.75, 1] : [0, 0.5, 1];

  const onMove = (e) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - r.left;
    const t = t0 + ((px - pad.l) / iw) * (t1 - t0);
    const s0 = series[0].points;
    if (!s0.length) return;
    let best = 0;
    for (let i = 1; i < s0.length; i++) if (Math.abs(s0[i].t - t) < Math.abs(s0[best].t - t)) best = i;
    setHover({ i: best, t: s0[best].t });
  };

  return html`<div class="chart" ref=${ref} style=${`height:${height}px`}>
    <svg width=${W} height=${height} role="img" aria-label=${label} onMouseMove=${onMove} onMouseLeave=${() => setHover(null)}>
      <defs>${series.map((s, i) => html`<linearGradient id=${ids[i]} x1="0" y1="0" x2="0" y2="1" class=${`tone-fill-${s.tone}`}>
        <stop offset="0" stop-color="currentColor" stop-opacity=".35" /><stop offset="1" stop-color="currentColor" stop-opacity="0" /></linearGradient>`)}</defs>
      ${ticks.map((v) => html`<g class="chart-grid"><line x1=${pad.l} x2=${W - pad.r} y1=${y(v)} y2=${y(v)} />
        <text x=${pad.l - 8} y=${y(v) + 4} text-anchor="end">${format(v)}</text></g>`)}
      ${xticks.map((f) => html`<text class="chart-xlabel" x=${pad.l + f * iw} y=${height - 6} text-anchor=${f === 0 ? 'start' : f === 1 ? 'end' : 'middle'}>${fmtTime(t0 + f * (t1 - t0)).replace(/:\d\d(\s|$)/, '$1')}</text>`)}
      ${series.map((s, i) => {
        if (s.points.length < 2) return null;
        const pts = s.points.map((p) => [x(p.t), y(p.v)]);
        const d = path(pts);
        return html`<g class=${`chart-series tone-stroke-${s.tone}`}>
          <path class="chart-area" d=${`${d} L${pts[pts.length - 1][0]},${pad.t + ih} L${pts[0][0]},${pad.t + ih} Z`} fill=${`url(#${ids[i]})`} />
          <path class="chart-line" d=${d} fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" />
        </g>`;
      })}
      ${hover && html`<g class="chart-hover"><line x1=${x(hover.t)} x2=${x(hover.t)} y1=${pad.t} y2=${pad.t + ih} />
        ${series.map((s) => { const p = s.points[Math.min(hover.i, s.points.length - 1)]; return p && html`<circle class=${`tone-stroke-${s.tone}`} cx=${x(p.t)} cy=${y(p.v)} r="4" />`; })}</g>`}
    </svg>
    ${hover && html`<div class="chart-tip" style=${`left:${Math.min(W - 150, Math.max(0, x(hover.t) + 10))}px`}>
      <div class="chart-tip-time">${fmtTime(hover.t)}</div>
      ${series.map((s) => { const p = s.points[Math.min(hover.i, s.points.length - 1)]; return p && html`<div class="chart-tip-row"><span class=${`legend-dot tone-bg-${s.tone}`}></span>${s.name}<b>${format(p.v)}</b></div>`; })}
    </div>`}
  </div>`;
}

/** Horizontal stacked bar showing the share of each category. */
export function StackBar({ parts, label }) {
  const total = parts.reduce((a, p) => a + p.value, 0) || 1;
  return html`<div class="stackbar" role="img" aria-label=${label}>
    ${parts.filter((p) => p.value > 0).map((p) => html`<span class=${`tone-bg-${p.tone}`} style=${`flex:${p.value / total}`} title=${`${p.label}: ${p.value}`}></span>`)}
  </div>`;
}
