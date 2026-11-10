import { useEffect, useState } from "react";
import {
  AIStatus,
  Check,
  DiagnosisResult,
  DiagnosisRow,
  Evaluation,
  FalsifyRule,
  Falsifier,
  PeerMetric,
  PeerRow,
  Peers,
  Profile,
  RollupResult,
  Stance,
  XPulseResult,
  aiApi,
  api,
  engineOrder,
  engineShort,
  pct,
  stanceLabel,
  times,
  triggerLabel,
} from "./api";
import { AttentionChart, heat } from "./Metrics";
import { useDiagnoses, when } from "./AI";
import { Scenarios, latestRevGrowth, usd } from "./Value";
import { causeLevelText, rankedCauses } from "./Causes";

type AI = ReturnType<typeof useDiagnoses>;

const usd2 = (v?: number | null) =>
  v == null ? "—" : Math.abs(v) >= 1e9 ? `$${(v / 1e9).toFixed(2)}B` : Math.abs(v) >= 1e6 ? `$${(v / 1e6).toFixed(0)}M` : v === 0 ? "0" : `$${v.toFixed(0)}`;

// ---------- the latest AI picture of a company ----------

export interface AIPicture {
  latest: DiagnosisRow<DiagnosisResult>[]; // latest successful diagnosis per engine
  rollup?: DiagnosisRow<RollupResult>; // only when no newer diagnosis came after it
  pulse?: DiagnosisRow<XPulseResult>;
  stance?: Stance;
  agree: number;
}

export function aiPicture(ai: AI): AIPicture {
  const items = ai.items ?? [];
  const ok = items.filter((d) => !d.error);
  const latest = engineOrder
    .map((e) => ok.find((d) => d.kind === "diagnosis" && d.engine === e))
    .filter(Boolean) as DiagnosisRow<DiagnosisResult>[];
  const r = ok.find((d) => d.kind === "rollup") as DiagnosisRow<RollupResult> | undefined;
  const rollup = r && !latest.some((d) => d.at > r.at) ? r : undefined;
  const pulse = ok.find((d) => d.kind === "xpulse") as DiagnosisRow<XPulseResult> | undefined;
  const count: Record<string, number> = {};
  latest.forEach((d) => (count[d.result.stance] = (count[d.result.stance] ?? 0) + 1));
  const top = Object.entries(count).sort((a, b) => b[1] - a[1])[0]?.[0] as Stance | undefined;
  const stance = rollup?.result.stance ?? top;
  return { latest, rollup, pulse, stance, agree: latest.filter((d) => d.result.stance === stance).length };
}

// ---------- KPI strip ----------

const Est = ({ on }: { on: boolean }) => (on ? <em className="est" title="依赖估算的资本开支（未在 XBRL 中单列）">估</em> : null);

// PriceRefresh: one click re-evaluates the company on Yahoo's latest price.
function PriceRefresh({ ticker, onRefreshed }: { ticker: string; onRefreshed: () => Promise<unknown> }) {
  const [state, setState] = useState<"" | "busy" | string>("");
  const pull = async () => {
    setState("busy");
    try {
      const r = await api.refreshPrice(ticker);
      await onRefreshed();
      const moved = r.stage !== r.prevStage ? ` · 筛选层级 L${r.prevStage} → L${r.stage}` : "";
      const was = r.prevPrice != null && r.price != null && r.prevPrice !== r.price ? `原 $${r.prevPrice.toFixed(2)} · ` : "";
      setState(`${was}${new Date().toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })} 已更新${moved}`);
    } catch (err) {
      setState(`更新失败：${String((err as Error).message ?? err)}`);
    }
  };
  return (
    <>
      <button className="link-btn" disabled={state === "busy"} onClick={pull} title="从 Yahoo 拉取最新股价，并按它重算估值、上下行和筛选层级（财报数据不变）">
        {state === "busy" ? "更新中…" : "更新股价"}
      </button>
      {state && state !== "busy" && <em className="price-note">{state}</em>}
    </>
  );
}

export function Kpis({ e, sc, onRefreshed }: { e: Evaluation; sc: Scenarios | null; onRefreshed?: () => Promise<unknown> }) {
  const v = e.valuation!;
  const dcf = e.analysis?.reverseDcf;
  const last = e.ttm?.at(-1) ?? e.years.at(-1);
  const est = sc?.imputed ?? false;
  const capped = v.target != null && v.median != null && v.target < v.median - 0.05;
  const g = (x?: number | null, d = 1) => (x == null ? "—" : `${x >= 0 ? "+" : ""}${pct(x, d)}`);
  const local = (x?: number) => (x == null || !e.fx ? usd2(x) : usd2(x / e.fx));
  const fxTip = e.fx ? `原币 ${e.currency}，按 1 USD = ${e.fx.toFixed(2)} ${e.currency}（筛选时的最新汇率）换算` : "";
  const netCash = (last?.cash ?? 0) - (last?.debt ?? 0);
  return (
    <div className="card kpis">
      <div className="kpi">
        <span>现价{onRefreshed && <PriceRefresh ticker={e.ticker} onRefreshed={onRefreshed} />}</span>
        <b>${v.price.toFixed(2)} <em className="dd">{pct(v.drawdown)}</em></b>
        <small>较 5 年高点 ${v.high5y.toFixed(2)}</small>
      </div>
      <div className="kpi">
        <span>P/FCF <Est on={est} /></span>
        <b>{times(v.multiple)}</b>
        <small>历史分位 {pct(v.percentile)} · 中位 {v.median != null ? `${v.median.toFixed(0)}x` : "—"} · FCF 收益率 {pct(v.fcfYield, 1)}</small>
      </div>
      <div className="kpi hl">
        <span>反向 DCF · 按 EV <Est on={est} /></span>
        <b className={dcf && dcf.impliedGrowth < 0 ? "neg" : ""}>{dcf ? `${g(dcf.impliedGrowth)} / 年` : "—"}</b>
        <small title={dcf ? `价格隐含的 FCF 增速：持续 ${dcf.years} 年，折现率 ${pct(dcf.discount)}，之后永续 ${pct(dcf.terminal)}。用 EV（市值 + 有息负债 − 现金）反推，净现金不算进经营业务的价格。过去 5 年实际 FCF ${g(dcf.histFcf)} / 年` : ""}>
          {dcf
            ? `EV ${usd2(dcf.evUsd)}${netCash > 0 ? "（市值 − 净现金）" : netCash < 0 ? "（市值 + 净负债）" : ""}${dcf.byMarketCap != null ? ` · 按市值 ${g(dcf.byMarketCap)}` : ""} · 过去 5 年实际 ${g(dcf.histFcf)}`
            : "数据不足，无法计算"}
        </small>
      </div>
      <div className="kpi hl">
        <span>内在价值 / 股 · 3 情景 <Est on={est} /></span>
        <b>{sc?.bear != null && sc.bull != null ? `${usd(sc.bear)} – ${usd(sc.bull)}` : "—"}</b>
        <small title={sc ? `悲观：DCF，起始增长 ${pct(sc.bearGrowth, 1)}（最近一年实际收入增速，不高于 0）；基准：DCF，起始增长 ${pct(sc.baseGrowth, 1)} 线性降到永续（与「安全边际」同一套假设）；乐观：回到 ${times(sc.bullMultiple)} P/FCF` : ""}>
          {sc?.base != null ? `悲观 ${usd(sc.bear)} · 基准 ${usd(sc.base)} · 乐观 ${usd(sc.bull)} · 基准比现价${sc.base >= v.price ? "高" : "低"} ${pct(Math.abs(sc.base / v.price - 1))}` : "FCF 为负，不适用"}
        </small>
      </div>
      <div className="kpi">
        <span>上行 / 下行 <Est on={est} /></span>
        <b className="updown"><span className="up">{pct(v.upside)}</span> / <span className="down">{pct(v.downside)}</span></b>
        <small>回到 {times(v.target)}{capped ? `（非历史中位 ${v.median!.toFixed(0)}x）` : "（历史中位）"} · 凸性 {times(v.convexity)}</small>
      </div>
      <div className="kpi">
        <span>市值 · 资产负债（USD）</span>
        <b>{usd2(v.marketCapUsd)}{v.marketCapEstimated && <em title="按股价 × 股数估算">估</em>}</b>
        <small title={fxTip}>现金+短投 {local(last?.cash)} · 有息负债 {local(last?.debt)}{e.fx ? ` · 按 ${e.fx.toFixed(2)} ${e.currency} 换算` : ""}</small>
      </div>
    </div>
  );
}

// ---------- industry comparison ----------

// up: higher is better; down: lower is better; null: no better/worse (drawdown cuts both ways).
const peerCols: { k: PeerMetric; label: string; fmt: (v: number) => string; better: "up" | "down" | null; tip: string }[] = [
  { k: "grossMargin", label: "毛利率", fmt: (v) => pct(v), better: "up", tip: "5 年中位毛利率" },
  { k: "opMargin", label: "营业利润率", fmt: (v) => pct(v), better: "up", tip: "5 年中位营业利润率" },
  { k: "fcfMargin", label: "FCF 利润率", fmt: (v) => pct(v), better: "up", tip: "5 年中位 FCF / 收入" },
  { k: "revenueCagr", label: "收入增速", fmt: (v) => pct(v), better: "up", tip: "5 年收入复合增速" },
  { k: "roic", label: "ROIC", fmt: (v) => pct(v), better: "up", tip: "投入资本回报率（5 年中位）" },
  { k: "fcfYield", label: "FCF 收益率", fmt: (v) => pct(v, 1), better: "up", tip: "正常化 FCF / 市值，越高越便宜" },
  { k: "multiple", label: "P/FCF", fmt: (v) => times(v), better: "down", tip: "市值 / 正常化 FCF，越低越便宜" },
  { k: "drawdown", label: "较 5 年高点", fmt: (v) => pct(v), better: null, tip: "跌得深可能是机会，也可能是基本面恶化" },
];

export function usePeers(ticker: string) {
  const [d, setD] = useState<Peers | null>(null);
  useEffect(() => {
    setD(null);
    api.peers(ticker).then(setD).catch(() => setD({ available: false, reason: "加载失败" }));
  }, [ticker]);
  return d;
}

// The two metrics where this company sits furthest from the industry median (relative gap).
function topGaps(d: Peers): Map<PeerMetric, number> {
  const gaps: [PeerMetric, number][] = [];
  for (const c of peerCols) {
    const v = d.self?.metrics[c.k];
    const m = d.median?.[c.k];
    if (v == null || m == null || m === 0) continue;
    gaps.push([c.k, (v - m) / Math.abs(m)]);
  }
  return new Map(gaps.sort((a, b) => Math.abs(b[1]) - Math.abs(a[1])).slice(0, 2).filter(([, x]) => Math.abs(x) >= 0.25));
}

// One compact table: this company, the industry median and the industry's largest company.
export function PeerCompare({ ticker, d }: { ticker: string; d: Peers | null }) {
  if (!d || !d.available || !d.self || !d.median) return null;
  const leader = d.leaders?.[0];
  const selfTop = d.selfRank === 1;
  const others = (d.leaders ?? []).slice(1).map((p) => `${p.ticker} ${usd2(p.marketCap)}`).join("、");
  const gaps = topGaps(d);
  const cell = (row: PeerRow["metrics"] | undefined, c: (typeof peerCols)[number], self = false) => {
    const v = row?.[c.k];
    if (v == null) return <td key={c.k}>—</td>;
    const gap = self ? gaps.get(c.k) : undefined;
    if (gap == null) return <td key={c.k}>{c.fmt(v)}</td>;
    const better = c.better == null ? "" : (c.better === "up") === gap > 0 ? "，方向上更好" : "，方向上更差";
    return <td key={c.k}><span className="gap" title={`比行业中位${gap > 0 ? "高" : "低"} ${pct(Math.abs(gap))}${better}`}>{c.fmt(v)}</span></td>;
  };
  return (
    <div className="card peers">
      <div className="card-head">
        <strong>同行业对比</strong>
        <span title="只有通过 L1 质量层的公司才抓行情，所以同行是「同行业里质量也过关的公司」；少于 3 家时放宽到同板块">
          {d.level === "sector" ? "板块" : "行业"} {d.group} · 同行 {d.count} 家（均已通过 L1）· 本公司市值排第 {d.selfRank}{gaps.size ? " · 黄色 = 偏离中位最大的两项" : ""}
        </span>
      </div>
      <table className="peer-table">
        <thead>
          <tr>
            <th />
            {peerCols.map((c) => <th key={c.k} title={c.tip}>{c.label}</th>)}
          </tr>
        </thead>
        <tbody>
          <tr className="self">
            <th>{ticker}</th>
            {peerCols.map((c) => cell(d.self!.metrics, c, true))}
          </tr>
          <tr>
            <th title="同行的中位数，不受个别极端值影响">行业中位</th>
            {peerCols.map((c) => cell(d.median, c))}
          </tr>
          {leader && (
            <tr>
              <th title={`${selfTop ? "本公司是同行中市值最大的；这里是第二大。" : ""}市值最大的同行：${leader.name}（${usd2(leader.marketCap)}）${others ? `；其后：${others}` : ""}`}>
                <a href={`#/c/${leader.ticker}`}>{leader.ticker}</a> <small>{selfTop ? "第二大" : "头部"}</small>
              </th>
              {peerCols.map((c) => cell(leader.metrics, c))}
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

// ---------- judgement points ----------

interface Point {
  text: string;
  source: string;
}


// One column of points: three shown, clipped to three lines; click a point for its full text,
// or expand the column to see every point.
function PointCol({ title, tone, items, empty, more }: { title: string; tone: string; items: Point[]; empty?: string; more?: { label: string; onClick: () => void } }) {
  const [all, setAll] = useState(false);
  const [open, setOpen] = useState<Set<number>>(new Set());
  const shown = all ? items : items.slice(0, 3);
  const toggle = (i: number) => setOpen((s) => {
    const n = new Set(s);
    if (n.has(i)) n.delete(i);
    else n.add(i);
    return n;
  });
  return (
    <div className={`jcol ${tone}`}>
      <h4><i />{title}<small>{items.length}</small></h4>
      {items.length === 0 && <p className="muted small">{empty ?? "暂无"}</p>}
      {shown.map((p, i) => (
        <div key={i} className={`jpt ${all || open.has(i) ? "full" : ""}`} onClick={() => toggle(i)} role="button" tabIndex={0}
          onKeyDown={(e) => e.key === "Enter" && toggle(i)} title={open.has(i) ? "点击收起" : "点击看全文"}>
          <p>{p.text}</p>
          {p.source && <span className={`src ${p.source.includes("传闻") ? "rumor" : ""}`}>{p.source}</span>}
        </div>
      ))}
      {more ? (
        <button className="link small jmore" onClick={more.onClick}>{more.label}</button>
      ) : items.length > 3 && (
        <button className="link small jmore" onClick={() => setAll(!all)}>{all ? "收起" : `展开全部 ${items.length} 条`}</button>
      )}
    </div>
  );
}

type Cause = ReturnType<typeof rankedCauses>["hs"][number];

// Rumors (not company disclosures) only ever go to "to verify".
export function JudgementPoints({ e, ai, causes, sc, iv, onAllCauses }: {
  e: Evaluation;
  ai: AIPicture;
  causes: Cause[];
  sc: Scenarios | null;
  iv: number | null; // the safety-margin composite
  onAllCauses: () => void;
}) {
  const v = e.valuation;
  const dcf = e.analysis?.reverseDcf;
  const last = e.ttm?.at(-1) ?? e.years.at(-1);
  const trends = e.checks.filter((c) => c.layer === 1 && c.warn);
  const l1 = e.checks.filter((c) => c.layer === 1 && !/^(趋势|去劣)：/.test(c.name));
  const trendText = (c: Check) => `${c.name.replace(/^(趋势|去劣)：/, "").replace(/ − 5 年中位$/, "（对比 5 年中位）")} ${c.value}`;

  const likely: Point[] = causes
    .filter((h) => h.rank >= 2)
    .slice(0, 3)
    .map((h) => ({ text: `${h.label}：${h.evidence.join("；") || h.method}`, source: h.aiHigh ? `系统 + AI · ${causeLevelText[h.level]}` : `系统 · ${causeLevelText[h.level]}` }));
  const support: Point[] = [];
  const against: Point[] = [];
  const verify: Point[] = [];
  if (v?.multiple != null && v.percentile != null && v.percentile <= 0.2)
    support.push({ text: `P/FCF ${v.multiple.toFixed(1)}x，处于自身历史 ${pct(v.percentile)} 分位，中位 ${v.median?.toFixed(0)}x`, source: sc?.imputed ? "系统 · 估" : "系统" });
  if (dcf && dcf.histFcf != null && dcf.impliedGrowth < dcf.histFcf - 0.03)
    support.push({ text: `反向 DCF：价格隐含 FCF 每年 ${pct(dcf.impliedGrowth, 1)}、持续 ${dcf.years} 年；过去 5 年实际 ${dcf.histFcf >= 0 ? "+" : ""}${pct(dcf.histFcf, 1)}`, source: "专业指标" });
  if (last?.fcf != null && last.fcf > 0)
    support.push({ text: `${last.ttm ? "TTM" : "最近财年"} FCF ${usd2(e.fx ? last.fcf / e.fx : last.fcf)}；现金 ${usd2(e.fx && last.cash != null ? last.cash / e.fx : last.cash)}、有息负债 ${usd2(e.fx && last.debt != null ? last.debt / e.fx : last.debt)}`, source: "财报" });
  (e.hints ?? []).filter((h) => h.kind === "positive").forEach((h) => support.push({ text: h.text, source: "系统" }));
  ai.latest.forEach((d) => d.result.bull?.[0] && support.push({ text: d.result.bull[0], source: `AI · ${engineShort[d.engine]}` }));

  trends.forEach((c) => against.push({ text: trendText(c), source: "财报" }));
  // Trend hints repeat the trend checks above.
  (e.hints ?? []).filter((h) => h.kind === "internal" && !/较 5 年中位|收入增速从/.test(h.text)).forEach((h) => against.push({ text: h.text, source: "系统" }));
  ai.latest.forEach((d) => d.result.bear?.[0] && against.push({ text: d.result.bear[0], source: `AI · ${engineShort[d.engine]}` }));
  ai.pulse?.result.bear?.[0] && against.push({ text: ai.pulse.result.bear[0], source: "舆情" });

  if (sc?.imputed) verify.push({ text: "实际资本开支：近几年未在 XBRL 中单列，FCF 为估算", source: "数据" });
  ai.pulse?.result.rumors?.slice(0, 2).forEach((t) => verify.push({ text: t, source: "舆情 · 传闻" }));
  (ai.rollup?.result.checklist ?? ai.latest[0]?.result.dataIssues ?? []).slice(0, 2).forEach((t) => verify.push({ text: t, source: "AI" }));
  (e.hints ?? []).filter((h) => h.kind === "external").forEach((h) => verify.push({ text: h.text, source: "系统" }));

  // One item per source first (the three shown before expanding), so a column is not three AI
  // paraphrases of the same point.
  const pick = (xs: Point[]) => {
    const seen = new Set<string>();
    const first = xs.filter((p) => {
      const k = p.source.split(" · ")[0];
      if (seen.has(k)) return false;
      seen.add(k);
      return true;
    });
    return [...first, ...xs.filter((p) => !first.includes(p))];
  };

  // Conflicts between signals, each spelled out with the numbers behind it.
  const conflicts: string[] = [];
  const doubters = ai.latest.filter((d) => d.result.stance === "trap" || d.result.stance === "unclear");
  const aiWord = [...new Set(doubters.map((d) => stanceLabel[d.result.stance]))].join(" / ");
  const rev = latestRevGrowth(e);
  if (v && iv != null && iv > v.price * 1.2 && doubters.length) {
    const bear = sc?.bear != null ? `悲观情景内在价值 ${usd(sc.bear)}，${sc.bear >= v.price ? `仅比现价高 ${pct(sc.bear / v.price - 1)}` : `比现价低 ${pct(1 - sc.bear / v.price)}`}。` : "";
    conflicts.push(`安全边际显示现价低于内在价值 ${pct(1 - v.price / iv)}，AI 结论为「${aiWord}」。差异多半来自估值假设：基准情景起始增长 ${pct(sc?.baseGrowth ?? 0, 1)}${rev ? `，而最近一年收入增速 ${rev.g >= 0 ? "+" : ""}${pct(rev.g, 1)}` : ""}。${bear}`);
  }
  if (trends.length && e.stage >= 1)
    conflicts.push(`L1 质量 ${l1.filter((c) => c.pass).length}/${l1.length} 通过（按 5 年中位），但 ${trends.map(trendText).join("；")}。`);
  if (!conflicts.length && doubters.length && e.stage >= 3)
    conflicts.push(`系统把它筛为候选（价格错杀），但${doubters.length === ai.latest.length && ai.latest.length > 1 ? `${ai.latest.length} 个 AI 模型都` : ` ${doubters.map((d) => engineShort[d.engine]).join("、")} `}没有确认错杀（${aiWord}）。`);

  const cols = [
    { title: "最可能原因", tone: "info", all: likely, more: { label: "看全部 12 项排查", onClick: onAllCauses } },
    { title: "支持错杀", tone: "good", all: pick(support) },
    { title: "反对错杀", tone: "bad", all: pick(against) },
    { title: "待核实", tone: "warn", all: pick(verify) },
  ];
  return (
    <section className="card" aria-label="判断要点">
      <div className="card-head"><strong>判断要点</strong><span>每栏最多显示 3 条 · 汇总系统、财报、AI 与舆情 · 每条标注来源</span></div>
      {conflicts.length > 0 && (
        <div className="signal-conflict">
          {conflicts.map((c, i) => (
            <div key={i}><b>信号冲突</b><span>{c}</span></div>
          ))}
        </div>
      )}
      <div className="jpoints four">
        {cols.map((c) => (
          <PointCol key={c.title} title={c.title} tone={c.tone} items={c.all} more={c.more}
            empty={c.title === "最可能原因" ? "数据里没有特别突出的原因" : `暂无${ai.latest.length ? "" : "（AI 诊断后会补充）"}`} />
        ))}
      </div>
    </section>
  );
}

// ---------- falsifiers: parse thresholds from AI text, adopt into the judgement ----------

const metricWords: [RegExp, string][] = [
  [/毛利率/, "grossMargin"],
  [/(营业|经营)利润率/, "opMargin"],
  [/FCF\s*利润率|自由现金流利润率/, "fcfMargin"],
  [/收入(同比|增速|增长)/, "revenueGrowth"],
  [/FCF\s*收益率/, "fcfYield"],
  [/股价/, "price"],
  [/F\s*分数|Piotroski/i, "fscore"],
];
const reThreshold = /(低于|跌破|跌到|跌至|降至|降到|小于|少于|<|高于|超过|升破|大于|>)\s*约?\s*\$?\s*(\d+(?:\.\d+)?)\s*(%|美元)?/;

// parseFalsifiers finds every "metric … below 55%" in a sentence, in the order they appear.
export function parseFalsifiers(text: string): FalsifyRule[] {
  const found: { at: number; r: FalsifyRule }[] = [];
  for (const [re, metric] of metricWords) {
    const g = new RegExp(re.source, re.flags.includes("g") ? re.flags : re.flags + "g");
    for (const m of text.matchAll(g)) {
      const rest = text.slice(m.index! + m[0].length, m.index! + m[0].length + 24); // the threshold follows closely
      const t = reThreshold.exec(rest);
      if (!t) continue;
      const below = /低于|跌|降|小于|少于|</.test(t[1]);
      let value = Number(t[2]);
      if (t[3] === "%") value /= 100;
      else if (metric !== "price" && metric !== "fscore") continue; // a ratio needs a percent
      found.push({ at: m.index!, r: { metric, op: below ? "<" : ">", value, note: text.slice(0, 120) } });
    }
  }
  return found.sort((a, b) => a.at - b.at).map((f) => f.r).filter((r, i, xs) => xs.findIndex((x) => sameRule(x, r)) === i);
}

export const parseFalsifier = (text: string): FalsifyRule | null => parseFalsifiers(text)[0] ?? null;

export function fromFalsifier(f: Falsifier): FalsifyRule | null {
  if (!f.metric || !f.op) return parseFalsifier(f.text);
  return { metric: f.metric, op: f.op, value: f.value, note: f.text.slice(0, 120) };
}

export const sameRule = (a: FalsifyRule, b: FalsifyRule) => a.metric === b.metric && a.op === b.op && Math.abs(a.value - b.value) < 1e-9;

const metricLabel: Record<string, string> = {
  grossMargin: "毛利率", opMargin: "营业利润率", fcfMargin: "FCF 利润率", revenueGrowth: "收入同比", fcfVsAvg: "最新 FCF / 近期均值",
  price: "股价", drawdown: "较 5 年高点", fcfYield: "FCF 收益率", percentile: "估值分位", fscore: "F 分数",
};
const opSym = { "<": "<", "<=": "≤", ">": ">", ">=": "≥" } as const;
export const ruleText = (r: FalsifyRule) =>
  `${metricLabel[r.metric] ?? r.metric} ${opSym[r.op]} ${r.metric === "price" ? `$${r.value}` : r.metric === "fscore" ? r.value : `${+(r.value * 100).toFixed(2)}%`}`;

// ---------- AI diagnosis (brief) ----------

export function AIBrief({ ai, pic, rules, onToggleRule, children }: {
  ai: AI;
  pic: AIPicture;
  rules: FalsifyRule[];
  onToggleRule: (r: FalsifyRule) => void;
  children?: React.ReactNode; // the full per-model analysis, collapsed
}) {
  const head = pic.rollup ?? pic.latest[0];
  const open = pic.rollup ? pic.rollup.result.open : pic.latest.flatMap((d) => d.result.watch ?? []);
  const check = pic.rollup ? pic.rollup.result.checklist : pic.latest.flatMap((d) => d.result.falsify ?? []);
  const structured = [...(pic.rollup?.result.falsifiers ?? []), ...pic.latest.flatMap((d) => d.result.falsifiers ?? [])]
    .map((f) => ({ f, r: fromFalsifier(f) }))
    .filter((x): x is { f: Falsifier; r: FalsifyRule } => !!x.r);
  const jobs = ai.pending.filter((p) => p.kind !== "xpulse");
  const Adopt = ({ r }: { r: FalsifyRule }) => {
    const on = rules.some((x) => sameRule(x, r));
    return (
      <button className={`adopt ${on ? "on" : ""}`} onClick={() => onToggleRule(r)} title={on ? "再点一次取消采纳" : "加入右侧「证伪条件」，之后自动检查"}>
        {on ? `✓ 已采纳：${ruleText(r)}` : `+ 采纳为证伪条件（${ruleText(r)}）`}
      </button>
    );
  };
  const uniq = <T,>(xs: T[], key: (x: T) => string) => xs.filter((x, i) => xs.findIndex((y) => key(y) === key(x)) === i);
  const extra = uniq(structured, (x) => ruleText(x.r)).filter((x) => !check.slice(0, 5).some((t) => parseFalsifiers(t).some((p) => sameRule(p, x.r))));
  return (
    <div className="card aibrief">
      <div className="card-head">
        <strong>AI 诊断</strong>
        <span>{jobs.length ? <><i className="spinner" /> {jobs.map((j) => engineShort[j.engine]).join("、")} 诊断中</> : "第二意见，不替你下结论"}</span>
      </div>
      {!head && !jobs.length && <p className="profile-loading">还没有 AI 诊断。展开下方选一个模型诊断，或在首页用「AI 诊断」批量处理。</p>}
      {head && (
        <>
          <div className="aib-top">
            <div className="aib-stance">
              <b className={`st-${pic.stance}`}>{pic.stance ? stanceLabel[pic.stance] : "—"}</b>
              {pic.latest.length > 0 && <small>{pic.agree} / {pic.latest.length} 模型一致</small>}
            </div>
            <p>
              {head.result.summary}
              <span className="muted"> {pic.rollup ? "综合" : ""} {pic.latest.map((d) => `${engineShort[d.engine]}${d.model ? ` ${d.model}` : ""}`).join(" · ")} · {when(head.at)}</span>
            </p>
          </div>
          <div className="aib-cols">
            <div>
              <h4 className="warn">留给你判断的问题</h4>
              {open.slice(0, 4).map((t, i) => <p key={i} className="aib-item">{t}</p>)}
            </div>
            <div>
              <h4>行动前要核实</h4>
              {check.slice(0, 5).map((t, i) => (
                <div key={i} className="aib-item">
                  <p>{t}</p>
                  <div className="adopts">{parseFalsifiers(t).map((r, k) => <Adopt key={k} r={r} />)}</div>
                </div>
              ))}
              {extra.slice(0, 3).map((x, i) => (
                <div key={`s${i}`} className="aib-item">
                  <p>{x.f.text}</p>
                  <Adopt r={x.r} />
                </div>
              ))}
            </div>
          </div>
        </>
      )}
      <details className="aib-more">
        <summary>各模型详细分析与诊断记录（{(ai.items ?? []).filter((d) => d.kind === "diagnosis").length} 次）· 重新诊断</summary>
        {children}
      </details>
    </div>
  );
}

// ---------- market sentiment: attention + X pulse ----------

const tok = (n?: number) => (n == null ? "—" : n >= 1e4 ? `${(n / 1e4).toFixed(1)} 万` : `${n}`);

// How the X search ran: model, trigger, time, turns, tokens, cost, tools, the command line.
function GrokCall({ row }: { row: DiagnosisRow<XPulseResult> }) {
  const m = row.meta;
  const bits = [
    row.model || "默认模型",
    `${triggerLabel(row.trigger)}触发`,
    new Date(row.at).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }),
    `用时 ${(row.durationMs / 60000).toFixed(1)} 分钟`,
    m?.turns ? `${m.turns} 轮${m.maxTurns ? `（上限 ${m.maxTurns}）` : ""}` : "",
    m?.inputTokens ? `输入 ${tok(m.inputTokens)} / 输出 ${tok(m.outputTokens)} token` : "",
    m?.costUsd ? `约 $${m.costUsd.toFixed(2)}` : "",
  ].filter(Boolean);
  return (
    <div className="grok-call small muted">
      <b>调用细节</b> {bits.join(" · ")}
      <div>
        工具：{m?.tools?.length ? m.tools.join("、") : "X 搜索（x_keyword_search、x_semantic_search、x_user_search、x_thread_fetch）+ 网页搜索"}
        ；本机文件 / 终端工具全部禁用，不自动批准任何操作，在临时空目录运行。
      </div>
      {m?.command ? <code title="提示词与 JSON 结构以文件 / 省略号代替">{m.command}</code> : <div>这次搜索早于调用记录功能，轮数与用量未保存；下次搜索起会记录。</div>}
    </div>
  );
}

const sentimentLabel = { bullish: "偏多", bearish: "偏空", mixed: "多空分歧", neutral: "中性" } as const;
const compact = (v: number) => (v >= 1e9 ? `${(v / 1e9).toFixed(1)}B` : v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e4 ? `${(v / 1e3).toFixed(0)}K` : v.toFixed(0));

// One line for the folded sentiment card.
export function sentimentSummary(profile: Profile | null, pic: AIPicture, e: Evaluation) {
  const series = profile?.attention?.series ?? [];
  const vol = series.find((s) => s.key === "volume");
  const wiki = series.find((s) => s.key !== "volume");
  const p = pic.pulse?.result;
  const ratios = series.map((s) => s.ratio).filter((r): r is number => r != null);
  const cooling = ratios.length > 0 && ratios.every((r) => r < 0.85);
  const heating = ratios.some((r) => r >= 1.3);
  const bits = [
    vol?.ratio != null ? `成交量 ×${vol.ratio.toFixed(2)}` : "",
    wiki?.ratio != null ? `维基 ×${wiki.ratio.toFixed(2)}` : "",
    p ? `X ${sentimentLabel[p.sentiment] ?? p.sentiment} ${p.sentimentScore > 0 ? "+" : ""}${p.sentimentScore}` : "X 舆情未搜索",
    heating ? "关注度升温" : cooling ? ((e.valuation?.drawdown ?? 0) <= -0.4 ? "关注度降温，接近被遗忘" : "关注度降温") : "",
  ].filter(Boolean);
  const tag = p ? (p.sentimentScore <= -2 ? "偏空" : p.sentimentScore >= 2 ? "偏多" : sentimentLabel[p.sentiment] ?? "") : heating ? "升温" : cooling ? "降温" : "";
  const tone = p ? (p.sentimentScore <= -2 ? "bad" : p.sentimentScore >= 2 ? "good" : "warn") : "";
  return { text: bits.join(" · "), tag, tone };
}

export function MarketSentiment({ e, profile, ai, pic }: { e: Evaluation; profile: Profile | null; ai: AI; pic: AIPicture }) {
  const [status, setStatus] = useState<AIStatus | null>(null);
  useEffect(() => {
    aiApi.status().then(setStatus).catch(() => {});
  }, []);
  const grok = status?.engines.find((x) => x.name === "grok");
  const series = (profile?.attention?.series ?? []).filter((s) => s.key !== "wiki_zh" || !(profile?.attention?.series ?? []).some((x) => x.key === "wiki_en"));
  const p = pic.pulse?.result;
  const searching = ai.has("xpulse");
  const ratios = series.map((s) => s.ratio).filter((r): r is number => r != null);
  const cooling = ratios.length > 0 && ratios.every((r) => r < 0.85);
  const heating = ratios.some((r) => r >= 1.3);
  const dd = e.valuation?.drawdown ?? 0;
  const lead = heating
    ? "关注度在升温，多半有新闻事件：先看新闻再判断。"
    : cooling && dd <= -0.4
      ? "跌幅大、关注度在降温，接近「被市场遗忘」的阶段。"
      : cooling
        ? "关注度在降温。"
        : "关注度与此前相当。";
  return (
    <div className="card sentiment">
      <div className="card-head">
        <strong>市场情绪</strong>
        <span>
          {searching ? <><i className="spinner" /> Grok 搜索中</> : "近 30 天对比此前约 3 个月 · 观点不是事实"}
          {!searching && grok?.available && (
            <button className="link small" onClick={() => ai.request("xpulse", "grok")}>{p ? "重新搜索 X" : "搜索 X 舆情"}</button>
          )}
        </span>
      </div>
      <div className="sent-cells">
        {series.slice(0, 2).map((s) => (
          <div key={s.key} className="kpi">
            <span>{s.key === "volume" ? "成交量" : s.label.replace("维基百科浏览量", "维基浏览")}</span>
            <b>{s.ratio != null ? `×${s.ratio.toFixed(2)}` : "—"}</b>
            <small title={heat(s.ratio).label}>日均 {compact(s.recent)}，此前 {compact(s.base)}</small>
          </div>
        ))}
        {series.length === 0 && <div className="kpi"><span>关注度</span><b>—</b><small>{profile ? "没有取到数据" : "加载中…"}</small></div>}
        <div className="kpi">
          <span>X 舆情（Grok）</span>
          {p ? (
            <>
              <b className={p.sentimentScore <= -2 ? "neg" : p.sentimentScore >= 2 ? "pos" : ""}>{sentimentLabel[p.sentiment] ?? p.sentiment} {p.sentimentScore > 0 ? "+" : ""}{p.sentimentScore}</b>
              <small>{when(pic.pulse!.at)} · 股价 ${pic.pulse!.price.toFixed(2)}</small>
            </>
          ) : (
            <>
              <b>—</b>
              <small>{searching ? "搜索中…" : grok && !grok.available ? "Grok 未登录" : "还没有搜索"}</small>
            </>
          )}
        </div>
      </div>
      <p className="sent-lead">{lead}{p ? ` X 上的主线：${p.summary}` : ""}</p>
      <details className="sent-more">
        <summary>多空观点与待核实传闻{series.length ? " · 关注度走势" : ""}</summary>
        {p && (
          <div className="jpoints">
            {[{ t: "多方", tone: "good", xs: p.bull }, { t: "空方", tone: "bad", xs: p.bear }, { t: "流传中 · 待核实", tone: "warn", xs: p.rumors }].map((c) => (
              <PointCol key={c.t} title={c.t} tone={c.tone} items={(c.xs ?? []).map((text) => ({ text, source: "" }))} />
            ))}
          </div>
        )}
        {p && p.posts?.length > 0 && (
          <div className="xposts sent-posts">
            {p.posts.slice(0, 6).map((x, i) => (
              <a key={i} className={`xpost ${x.stance} ${x.suspect ? "suspect" : ""}`} href={x.url} target="_blank" rel="noreferrer"
                title={x.suspect ? `链接里的帖子 ID 对应 ${x.idDate ?? "未知日期"}，与声称的 ${x.date} 不符` : x.url}>
                <span className="nmeta">{x.author} · {x.date}{x.suspect && <b className="suspect-tag">链接可疑</b>}</span>
                <span>{x.text}</span>
              </a>
            ))}
          </div>
        )}
        {series.length > 0 && <div className="att-grid">{series.map((s) => <AttentionChart key={s.key} s={s} />)}</div>}
        <p className="small muted sent-foot">
          成交量看市场资金的关注，维基浏览量看大众的好奇心；X 舆情由 Grok 搜索，帖子链接已按帖子 ID 时间戳核对。
          {p?.coverage ? ` 搜索范围：${p.coverage}` : ""}
        </p>
        {pic.pulse && <GrokCall row={pic.pulse} />}
      </details>
    </div>
  );
}
