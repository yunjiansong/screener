import { Analysis, AttentionSeries, Metric, Profile, pct } from "./api";
import { LineChart } from "./LineChart";

const fmt = (m: Pick<Metric, "format">, v: number | null | undefined) => {
  if (v == null || !isFinite(v)) return "—";
  switch (m.format) {
    case "pct":
      return pct(v, 1);
    case "x":
      return `${v.toFixed(1)}x`;
    case "days":
      return `${v.toFixed(0)} 天`;
    default:
      return v.toFixed(2);
  }
};

// Sparkline: one metric across fiscal years (+ latest TTM). Hover a point for its value.
function Spark({ m }: { m: Metric }) {
  const pts = (m.series ?? []).map((p, i) => ({ ...p, i })).filter((p) => p.v != null && isFinite(p.v)) as { end: string; ttm?: boolean; v: number; i: number }[];
  const n = (m.series ?? []).length;
  if (pts.length < 2) return <span className="spark muted small">—</span>;
  const W = 96, H = 24, P = 3;
  const lo = Math.min(...pts.map((p) => p.v)), hi = Math.max(...pts.map((p) => p.v));
  const x = (i: number) => P + (i / Math.max(1, n - 1)) * (W - 2 * P);
  const y = (v: number) => (hi === lo ? H / 2 : P + (1 - (v - lo) / (hi - lo)) * (H - 2 * P));
  const d = pts.map((p, k) => `${k ? "L" : "M"}${x(p.i).toFixed(1)},${y(p.v).toFixed(1)}`).join("");
  const last = pts[pts.length - 1];
  return (
    <svg className="spark" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${m.name} 走势`}>
      <path d={d} />
      {pts.map((p) => (
        <circle key={p.i} cx={x(p.i)} cy={y(p.v)} r={p === last ? 2.6 : 6} className={p === last ? "last" : "hit"}>
          <title>{`${p.ttm ? "TTM " : ""}${p.end.slice(0, 7)}：${fmt(m, p.v)}`}</title>
        </circle>
      ))}
    </svg>
  );
}

const zoneLabel = { safe: "安全区", grey: "灰色区", distress: "困境区" };

export function ProMetrics({ a }: { a?: Analysis }) {
  if (!a) return null;
  const roic = a.groups.flatMap((g) => g.metrics).find((m) => m.name === "ROIC");
  return (
    <div className="card">
      <div className="card-head">
        <strong>专业指标</strong>
        <span>辅助判断，不参与筛选 · 走势 = 近 6 个财年 + 最新 TTM，悬停看数值</span>
      </div>
      <div className="scores">
        {a.fscore && (
          <div className="score">
            <span>Piotroski F 分数</span>
            <b className={a.fscore.value >= 7 ? "good" : a.fscore.value <= 3 ? "bad" : ""}>{a.fscore.value} / {a.fscore.max}</b>
            <small>
              {a.fscore.period}。未通过：{a.fscore.items.filter((i) => !i.pass).map((i) => i.name).join("、") || "无"}。≥ 7 说明基本面在改善，≤ 3 在恶化。
            </small>
          </div>
        )}
        {a.zscore && (
          <div className="score">
            <span>Altman Z 分数</span>
            <b className={a.zscore.zone === "safe" ? "good" : a.zscore.zone === "distress" ? "bad" : ""}>{a.zscore.value.toFixed(1)} · {zoneLabel[a.zscore.zone]}</b>
            <small>&gt; 3 破产风险低，&lt; 1.8 有财务困境风险。原公式针对制造业，对轻资产公司只作粗筛。</small>
          </div>
        )}
        {roic && <div className="score"><span>ROIC</span><b className={roic.tone}>{fmt(roic, roic.value)}</b><small>{roic.asOf} · {roic.note}</small></div>}
      </div>
      <details className="metric-details"><summary>全部专业指标与历史走势</summary><div className="mgroups">
        {a.groups.map((g) => (
          <div key={g.title} className="mgroup">
            <h4>{g.title}</h4>
            {g.metrics.map((m) => (
              <div key={m.name} className="mrow" title={m.note}>
                <span className="mname">{m.name}</span>
                <Spark m={m} />
                <span className={`mval ${m.tone}`}>
                  {m.tone === "bad" ? "✕ " : m.tone === "warn" ? "! " : ""}
                  {fmt(m, m.value)}
                </span>
                <small className="mnote">{m.note}{m.asOf && !m.asOf.startsWith("TTM") && m.series && m.series.some((p) => p.ttm) ? `（最新值来自 ${m.asOf}，季报未披露该项）` : ""}</small>
              </div>
            ))}
          </div>
        ))}
      </div></details>
    </div>
  );
}

const compact = (v: number) => (v >= 1e9 ? `${(v / 1e9).toFixed(1)}B` : v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e4 ? `${(v / 1e3).toFixed(0)}K` : v.toFixed(0));

export function heat(r: number | null) {
  if (r == null) return { label: "历史不足", cls: "" };
  if (r >= 2) return { label: `明显升温 ×${r.toFixed(1)}`, cls: "hot" };
  if (r >= 1.3) return { label: `升温 ×${r.toFixed(1)}`, cls: "warm" };
  if (r <= 0.7) return { label: `降温 ×${r.toFixed(2)}`, cls: "cool" };
  return { label: `平常 ×${r.toFixed(2)}`, cls: "" };
}

export function AttentionChart({ s }: { s: AttentionSeries }) {
  const days = s.days;
  const cut = days.length ? new Date(new Date(days[days.length - 1].date).getTime() - 30 * 864e5).toISOString().slice(0, 10) : "";
  const from = Math.max(0, days.findIndex((d) => d.date > cut));
  const h = heat(s.ratio);
  return (
    <div className="att">
      <div className="att-head">
        <b>{s.label}</b>
        <span className={`heat ${h.cls}`}>{h.label}</span>
      </div>
      <div className="small muted">
        近 30 天日均 {compact(s.recent)} {s.unit}，此前日均 {compact(s.base)}
        {s.url && <> · <a href={s.url} target="_blank" rel="noreferrer">{s.source}</a></>}
      </div>
      <LineChart
        title=""
        data={days.map((d) => ({ date: d.date, value: d.v }))}
        format={compact}
        height={170}
        width={360}
        xTicks="months"
        shadeFrom={from}
        refLines={s.base > 0 ? [{ value: s.base, label: "此前均值" }] : []}
      />
    </div>
  );
}

export function AttentionCard({ profile, error }: { profile: Profile | null; error: string }) {
  const series = profile?.attention?.series ?? [];
  return (
    <div className="card">
      <div className="card-head">
        <strong>成交量与维基关注度</strong>
        <span>阴影 = 近 30 天 · 与此前约 3 个月对比</span>
      </div>
      {!profile && !error && <p className="profile-loading">正在获取…</p>}
      {profile && series.length === 0 && <p className="profile-loading">没有取到关注度数据。</p>}
      {error && <p className="profile-loading">关注度获取失败：{error}</p>}
      {series.length > 0 && <div className="attention-summary">{series.map((s) => <div key={s.key}><span>{s.label}</span><b>{heat(s.ratio).label}</b></div>)}</div>}
      <details className="metric-details"><summary>关注度走势与数据口径</summary>
      {series.length > 0 && (
        <div className="att-grid">
          {series.map((s) => <AttentionChart key={s.key} s={s} />)}
        </div>
      )}
      <p className="small muted att-foot">
        成交量看市场资金的关注，维基百科浏览量看大众的好奇心。X 社交观点见下方 Grok 搜索结果。
        "跌了很多、关注度却在降温"往往是被市场遗忘的阶段；"关注度暴涨"多半有新闻事件，先看新闻再判断。
      </p>
      </details>
    </div>
  );
}
