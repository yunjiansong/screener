import { useMemo, useRef, useState } from "react";

export interface RefLine {
  value: number;
  label: string;
  fromIndex?: number; // start the line here (e.g. a 5-year high only spans the last 5 years)
}

export interface Mark {
  index: number;
  label: string;
  title: string; // shown on hover
  down: boolean;
}

interface Props {
  title: string;
  note?: string;
  data: { date: string; value: number | undefined }[];
  format: (v: number) => string;
  refLines?: RefLine[];
  log?: boolean; // logarithmic y axis (values must be > 0)
  height?: number;
  width?: number; // viewBox width; smaller = larger text when the chart is drawn small
  xTicks?: "years" | "months";
  shadeFrom?: number; // shade from this index to the end (e.g. "the last 30 days")
  marks?: Mark[]; // annotated points, e.g. news with a big price reaction
}

const DEFAULT_W = 600;
const DEFAULT_H = 220;
const PAD = { top: 10, right: 78, bottom: 26, left: 42 };

// Single-series line chart with a crosshair tooltip. Reference lines are dashed and
// direct-labeled at the right edge, so no legend is needed.
export function LineChart({ title, note, data, format, refLines = [], log = false, marks = [], height, xTicks = "years", shadeFrom, width }: Props) {
  const H = height ?? DEFAULT_H;
  const W = width ?? DEFAULT_W;
  const [hover, setHover] = useState<number | null>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  // Keep positions aligned with the input so refLine.fromIndex / marks index the same series.
  const points = useMemo(() => data.map((d) => ({ date: d.date, value: d.value != null && isFinite(d.value) && (!log || d.value > 0) ? d.value : null })), [data, log]);
  const valid = points.filter((p) => p.value != null) as { date: string; value: number }[];

  const caption = (
    <figcaption>
      <b>{title}</b>
      <span>{note}</span>
    </figcaption>
  );
  if (valid.length < 2)
    return (
      <figure className="chart">
        {caption}
        <p className="muted">数据不足</p>
      </figure>
    );

  const T = (v: number) => (log ? Math.log(v) : v);
  const inv = (t: number) => (log ? Math.exp(t) : t);
  const values = [...valid.map((p) => p.value), ...refLines.map((r) => r.value)].filter((v) => !log || v > 0).map(T);
  const rawMin = Math.min(...values);
  let lo = rawMin;
  let hi = Math.max(...values);
  const span = hi - lo || 1;
  lo = log ? lo - span * 0.06 : Math.max(rawMin >= 0 ? 0 : -Infinity, lo - span * 0.06);
  hi += span * 0.06;

  const innerW = W - PAD.left - PAD.right;
  const innerH = H - PAD.top - PAD.bottom;
  const x = (i: number) => PAD.left + (i / (points.length - 1)) * innerW;
  const y = (v: number) => PAD.top + (1 - (T(v) - lo) / (hi - lo)) * innerH;
  const base = PAD.top + innerH;

  // Keep direct labels of nearby reference lines from overlapping.
  const sortedRefs = [...refLines].sort((a, b) => b.value - a.value);
  const labelY = new Map<string, number>();
  let prev = -Infinity;
  for (const r of sortedRefs) {
    const ly = Math.max(y(r.value), prev + 13);
    labelY.set(r.label, ly);
    prev = ly;
  }

  // Gaps (missing values) break the line instead of being bridged.
  let line = "";
  let pen = false;
  points.forEach((p, i) => {
    if (p.value == null) return void (pen = false);
    line += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(p.value).toFixed(1)}`;
    pen = true;
  });
  const firstI = points.findIndex((p) => p.value != null);
  const lastI = points.length - 1 - [...points].reverse().findIndex((p) => p.value != null);
  const area = `${line}L${x(lastI).toFixed(1)},${base}L${x(firstI).toFixed(1)},${base}Z`;
  const ticks = [0, 1, 2, 3].map((k) => inv(lo + ((hi - lo) * k) / 3));
  const yearTicks =
    xTicks === "years"
      ? points.map((p, i) => ({ i, year: p.date.slice(0, 4), month: p.date.slice(5, 7) })).filter((p) => p.month === "01" && Number(p.year) % 2 === 0)
      : points
          .map((p, i) => ({ i, year: `${Number(p.date.slice(5, 7))}月`, first: i > 0 && p.date.slice(0, 7) !== points[i - 1].date.slice(0, 7) }))
          .filter((p) => p.first);

  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const rect = svgRef.current!.getBoundingClientRect();
    const px = ((e.clientX - rect.left) / rect.width) * W;
    const i = Math.round(((px - PAD.left) / innerW) * (points.length - 1));
    const c = Math.max(0, Math.min(points.length - 1, i));
    setHover(points[c].value == null ? null : c);
  };

  const h = hover == null ? null : (points[hover] as { date: string; value: number });
  const last = points[lastI] as { date: string; value: number };
  return (
    <figure className="chart">
      {caption}
      <div className="chart-wrap">
        <svg ref={svgRef} viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${title}，最新 ${format(last.value)}`} onPointerMove={onMove} onPointerLeave={() => setHover(null)}>
          {ticks.map((t) => (
            <g key={t}>
              <line className="grid" x1={PAD.left} x2={W - PAD.right} y1={y(t)} y2={y(t)} />
              <text className="axis" x={PAD.left - 6} y={y(t)} dy="0.32em" textAnchor="end">
                {format(t)}
              </text>
            </g>
          ))}
          {yearTicks.map((t) => (
            <text key={t.i} className="axis" x={x(t.i)} y={H - 6} textAnchor="middle">
              {t.year}
            </text>
          ))}
          {shadeFrom != null && shadeFrom < points.length && (
            <rect className="shade" x={x(shadeFrom)} y={PAD.top} width={x(points.length - 1) - x(shadeFrom)} height={innerH} />
          )}
          <path className="area" d={area} />
          {refLines.map((r) => (
            <g key={r.label}>
              <line className="ref" x1={x(r.fromIndex ?? 0)} x2={W - PAD.right} y1={y(r.value)} y2={y(r.value)} />
              <text className="ref-label" x={W - PAD.right + 6} y={labelY.get(r.label)} dy="0.32em">
                {r.label} {format(r.value)}
              </text>
            </g>
          ))}
          <path className="series" d={line} />
          <circle className="end-dot" cx={x(lastI)} cy={y(last.value)} r={4} />
          {marks.map((m) => {
            const p = points[m.index];
            if (!p || p.value == null) return null;
            const cy = y(p.value);
            return (
              <g key={m.index} className={`mark ${m.down ? "down" : "up"}`}>
                <title>{m.title}</title>
                <circle cx={x(m.index)} cy={cy} r={10} className="mark-hit" />
                <circle cx={x(m.index)} cy={cy} r={4.5} className="mark-dot" />
                {m.label && <text x={x(m.index) - 4} y={cy - 9} textAnchor="end">{m.label}</text>}
              </g>
            );
          })}
          {h && (
            <g>
              <line className="crosshair" x1={x(hover!)} x2={x(hover!)} y1={PAD.top} y2={base} />
              <circle className="hover-dot" cx={x(hover!)} cy={y(h.value)} r={4.5} />
            </g>
          )}
        </svg>
        {h && (
          <div className="tooltip" style={{ left: `${(x(hover!) / W) * 100}%`, transform: hover! > points.length / 2 ? "translateX(-108%)" : "translateX(8%)" }}>
            <span className="muted">{h.date.slice(0, 7)}</span>
            <strong>{format(h.value)}</strong>
          </div>
        )}
      </div>
    </figure>
  );
}
