import { useState } from "react";
import { DiagnosisResult, DiagnosisRow, Evaluation, Metric, Peers, Profile, Year, engineShort, pct } from "./api";
import { latestRevGrowth } from "./Value";

// Why is it cheap? The standard checklist of reasons a stock trades low, each tested against the
// data the system has (trend, Piotroski F, Altman Z, accruals, peers, price-drop attribution,
// attention, news) and rated by every AI diagnosis. Several reasons usually apply at once: the
// card shows them as possibilities, ranked, with the evidence – it does not pick one.

type Level = "high" | "medium" | "low" | "unknown";
type Kind = "trap" | "repair" | "neutral";

interface Hypo {
  key: string;
  label: string;
  kind: Kind;
  method: string; // the standard check behind it
  level: Level;
  evidence: string[];
}

const levelText: Record<Level, string> = { high: "较可能", medium: "有迹象", low: "不像", unknown: "看不出" };
const levelRank: Record<Level, number> = { high: 3, medium: 2, unknown: 1, low: 0 };
const kindText: Record<Kind, string> = { trap: "陷阱型", repair: "可修复", neutral: "中性" };
const kindTip: Record<Kind, string> = {
  trap: "下跌反映了真实的价值损失，便宜可能是陷阱",
  repair: "外部或暂时的原因，基本面没坏，价格有修复的可能",
  neutral: "价格回落有道理，但不一定意味着价值受损",
};

const opm = (y?: Year) => (y?.opIncome != null && y.revenue ? y.opIncome / y.revenue : null);
const gpm = (y?: Year) => (y?.grossProfit != null && y.revenue ? y.grossProfit / y.revenue : null);
const pp = (x: number) => `${x >= 0 ? "+" : ""}${(x * 100).toFixed(1)}pp`;
const sgn = (x: number, d = 0) => `${x >= 0 ? "+" : ""}${pct(x, d)}`;

function metricOf(e: Evaluation, name: string): Metric | undefined {
  for (const g of e.analysis?.groups ?? []) {
    const m = g.metrics.find((x) => x.name === name);
    if (m) return m;
  }
}

// level from strong / mild signal counts
const grade = (strong: number, mild: number): Level => (strong >= 2 || (strong >= 1 && mild >= 1) ? "high" : strong + mild > 0 ? "medium" : "low");

const CYCLICAL_SECTORS = ["Energy", "Basic Materials"];
const CYCLICAL_WORDS = /semiconductor|auto|steel|chemical|oil|gas|airline|homebuild|shipping|marine|mining|metal|copper|gold|aluminum|lumber|paper|trucking|railroad|building products|construction|travel|lodging|resort|casino|recreational|furnish/i;
const EVENT_WORDS = /lawsuit|sue[sd]?\b|investigat|probe|subpoena|SEC\b|DOJ|FTC|antitrust|tariff|sanction|ban\b|recall|FDA|warning letter|delist|short seller|fraud|restat|诉讼|起诉|调查|监管|关税|制裁|禁令|召回|退市|做空|欺诈|重述|反垄断/i;

export function hypotheses(e: Evaluation, peers: Peers | null, profile: Profile | null): Hypo[] {
  const v = e.valuation;
  const last = e.ttm?.at(-1) ?? e.years.at(-1);
  const q = e.quality;
  const rev = latestRevGrowth(e);
  const dcf = e.analysis?.reverseDcf;
  const out: Hypo[] = [];
  const add = (h: Omit<Hypo, "evidence" | "level"> & { level: Level; evidence: (string | false | null | undefined)[] }) =>
    out.push({ ...h, evidence: h.evidence.filter(Boolean) as string[] });

  // 1. deterioration
  {
    let s = 0, m = 0;
    const ev: string[] = [];
    const om = opm(last);
    if (om != null && q.opMargin != null) {
      const d = om - q.opMargin;
      if (d < -0.05) s++; else if (d < -0.03) m++;
      ev.push(`最新 12 个月营业利润率 ${pct(om, 1)}，5 年中位 ${pct(q.opMargin, 1)}（${pp(d)}）`);
    }
    if (v?.latestFcfVsAvg != null) {
      if (v.latestFcfVsAvg < 0.7) s++; else if (v.latestFcfVsAvg < 0.85) m++;
      ev.push(`最新 12 个月 FCF 是近期均值的 ${pct(v.latestFcfVsAvg)}`);
    }
    if (rev) {
      if (rev.g < -0.05) s++; else if (rev.g < 0) m++;
      ev.push(`收入同比 ${sgn(rev.g, 1)}`);
    }
    const f = e.analysis?.fscore;
    if (f) {
      if (f.value <= 3) s++; else if (f.value <= 5) m++;
      ev.push(`Piotroski F 分数 ${f.value}/${f.max}`);
    }
    add({ key: "deterioration", label: "基本面正在恶化", kind: "trap", method: "利润率趋势 + FCF 保持 + Piotroski F 分数", level: ev.length ? grade(s, m) : "unknown", evidence: ev });
  }

  // 2. cyclical peak
  {
    const cyc = CYCLICAL_SECTORS.includes(e.sector) || CYCLICAL_WORDS.test(e.industry);
    const om = opm(last);
    const d = om != null && q.opMargin != null ? om - q.opMargin : null;
    add({
      key: "cyclical", label: "周期顶部利润", kind: "trap", method: "行业周期属性 + 利润率相对历史的位置",
      level: !cyc ? "low" : d != null && d > 0.03 ? "high" : "medium",
      evidence: [
        cyc ? `${e.industry || e.sector} 属周期性行业` : `${e.industry || e.sector || "该行业"} 一般不算强周期`,
        cyc && d != null && (d > 0.03 ? `利润率比 5 年中位高 ${pp(d)}，可能处在周期高位，「便宜」是因为利润会回落` : `利润率比 5 年中位 ${pp(d)}，不像周期顶部`),
      ],
    });
  }

  // 3. leverage
  {
    let s = 0, m = 0;
    const ev: string[] = [];
    const z = e.analysis?.zscore;
    if (z) {
      if (z.zone === "distress") s++; else if (z.zone === "grey") m++;
      ev.push(`Altman Z ${z.value.toFixed(2)}（${z.zone === "safe" ? "安全区" : z.zone === "grey" ? "灰色区" : "困境区"}）`);
    }
    const nd = metricOf(e, "净债务 / EBITDA")?.value;
    if (nd != null) {
      if (nd > 3) s++; else if (nd > 2) m++;
      ev.push(nd < 0 ? "净现金" : `净债务 / EBITDA ${nd.toFixed(1)}x`);
    }
    const ic = metricOf(e, "利息保障倍数")?.value;
    if (ic != null && ic < 8) {
      if (ic < 3) s++; else m++;
      ev.push(`利息保障倍数 ${ic.toFixed(1)}x`);
    }
    // Net cash outweighs a grey Z-score (Z is built for manufacturers and penalizes asset-light firms).
    let lv: Level = ev.length ? grade(s, m) : "unknown";
    if (nd != null && nd < 0 && s === 0 && lv === "medium") {
      lv = "low";
      ev.push("有净现金，Z 分数偏低不构成偿债压力");
    }
    add({ key: "leverage", label: "财务风险", kind: "trap", method: "Altman Z + 净债务 / EBITDA + 利息保障", level: lv, evidence: ev });
  }

  // 4. earnings quality / accounting
  {
    let s = 0, m = 0;
    const ev: string[] = [];
    const acc = metricOf(e, "应计比率")?.value;
    if (acc != null) {
      if (acc > 0.1) s++; else if (acc > 0.05) m++;
      ev.push(`应计比率 ${pct(acc, 1)}${acc > 0.05 ? "（利润里现金成分偏低）" : ""}`);
    }
    const conv = metricOf(e, "FCF / 净利润")?.value;
    if (conv != null) {
      if (conv < 0.5) s++; else if (conv < 0.7) m++;
      ev.push(`FCF / 净利润 ${pct(conv)}`);
    }
    const sbc = metricOf(e, "股权激励 / 收入")?.value;
    if (sbc != null && sbc > 0.05) {
      if (sbc > 0.1) s++; else m++;
      ev.push(`股权激励占收入 ${pct(sbc, 1)}，FCF 没扣这部分成本`);
    }
    const dso = metricOf(e, "应收账款周转天数")?.series?.filter((x) => x.v != null) ?? [];
    if (dso.length >= 4) {
      const a = dso[dso.length - 4].v!, b = dso[dso.length - 1].v!;
      if (a > 0 && b / a > 1.3) {
        m++;
        ev.push(`应收账款周转天数 ${a.toFixed(0)} → ${b.toFixed(0)} 天`);
      }
    }
    const imputed = [...e.years.slice(-5), ...(e.ttm ?? [])].filter((y) => y.capexImputed).length;
    if (imputed) ev.push(`${imputed} 期资本开支未披露、按历史比例估算`);
    add({ key: "accounting", label: "盈利质量 / 会计疑点", kind: "trap", method: "Sloan 应计异常 + 现金转化率 + 股权激励", level: ev.length ? grade(s, m) : "unknown", evidence: ev });
  }

  // 5. dilution
  {
    const yoy = metricOf(e, "股本变化（同比）")?.value;
    const five = q.shareChange;
    let s = 0, m = 0;
    if (yoy != null) { if (yoy > 0.03) s++; else if (yoy > 0.01) m++; }
    if (five != null) { if (five > 0.15) s++; else if (five > 0.05) m++; }
    add({
      key: "dilution", label: "股东被稀释", kind: "trap", method: "股本变化",
      level: yoy == null && five == null ? "unknown" : grade(s, m),
      evidence: [yoy != null && `股本同比 ${sgn(yoy, 1)}`, five != null && `5 年累计 ${sgn(five, 1)}`],
    });
  }

  // 6. disruption: not measurable; a falling gross margin with slowing sales is the only hint
  {
    const g = gpm(last);
    const d = g != null && q.grossMargin != null ? g - q.grossMargin : null;
    const weak = d != null && d < -0.03 && rev != null && rev.g < (dcf?.histRevenue ?? 0.05) / 2;
    add({
      key: "disruption", label: "结构性颠覆 / 护城河受损", kind: "trap", method: "毛利率趋势 + 增速变化（定性为主，看 AI 与新闻）",
      level: weak ? "medium" : "unknown",
      evidence: [
        d != null && `毛利率 ${pct(g!, 1)}，5 年中位 ${pct(q.grossMargin!, 1)}（${pp(d)}）`,
        weak ? "毛利率下滑同时增长放缓，定价权可能在减弱" : "数据看不出，需要判断竞争格局和需求变化",
      ],
    });
  }

  // 7. growth slowdown → de-rating
  {
    const h = dcf?.histRevenue ?? q.revenueCagr ?? null;
    const level: Level = !rev || h == null ? "unknown" : h >= 0.1 && rev.g < h / 2 ? "high" : rev.g < h - 0.05 ? "medium" : "low";
    add({
      key: "slowdown", label: "增长放缓，估值下修", kind: "neutral", method: "期望投资：反向 DCF 隐含增速 vs 历史增速",
      level,
      evidence: [
        rev && h != null && `收入增速从 5 年年均 ${pct(h, 1)} 降到最近一年 ${sgn(rev.g, 1)}`,
        dcf && `现价隐含 FCF 增速 ${sgn(dcf.impliedGrowth, 1)} / 年，过去 5 年 FCF ${dcf.histFcf != null ? sgn(dcf.histFcf, 1) : "—"} / 年`,
      ],
    });
  }

  // 8. de-rating from a high multiple: split the drop since the 5-year high into multiple × FCF
  {
    const all = v?.series ?? [];
    const cut = all.length ? new Date(Date.parse(all[all.length - 1].date) - 5 * 365.25 * 864e5).toISOString().slice(0, 10) : "";
    const s = all.filter((x) => x.date >= cut); // same window as the 5-year high
    const top = s.reduce<(typeof s)[number] | null>((a, x) => (!a || x.price > a.price ? x : a), null);
    const then = top?.multiple;
    const now = v?.multiple;
    if (v && then != null && now != null && then > 0 && now > 0 && v.drawdown < -0.05) {
      const mult = now / then - 1;
      const fcf = (1 + v.drawdown) / (now / then) - 1;
      const share = Math.log(now / then) / Math.log(1 + v.drawdown);
      add({
        key: "derating", label: "估值从高位回落", kind: "neutral", method: "跌幅拆解：股价变化 = 估值倍数变化 × FCF 变化",
        level: v.drawdown > -0.15 ? "low" : share >= 0.8 && fcf > -0.1 ? "high" : share >= 0.5 ? "medium" : "low",
        evidence: [
          `5 年高点（${top!.date.slice(0, 7)}）P/FCF ${then.toFixed(0)}x → 现在 ${now.toFixed(0)}x（${sgn(mult)}），同期每股 FCF 约 ${sgn(fcf)}`,
          `${share >= 1 ? "倍数收缩比股价跌得还多（FCF 在增长），下跌全部来自估值" : `跌幅约 ${pct(Math.max(0, share))} 来自估值收缩，其余来自 FCF 下降`}${v.median != null && then > v.median * 1.5 ? `；高点时倍数是历史中位 ${v.median.toFixed(0)}x 的 ${(then / v.median).toFixed(1)} 倍` : ""}`,
        ],
      });
    } else {
      add({ key: "derating", label: "估值从高位回落", kind: "neutral", method: "跌幅拆解：股价变化 = 估值倍数变化 × FCF 变化", level: v && v.drawdown >= -0.05 ? "low" : "unknown", evidence: [v && v.drawdown >= -0.05 ? "股价接近 5 年高点" : "缺少高点时的估值倍数"] });
    }
  }

  // 9 / 10. industry-wide vs company-specific: relative to peers
  {
    const med = peers?.available ? peers.median?.drawdown : null;
    const dd = v?.drawdown;
    const grp = peers?.group ? `${peers.level === "sector" ? "板块" : "行业"} ${peers.group}，${peers.count} 家` : "";
    if (med != null && dd != null) {
      const gap = dd - med;
      add({
        key: "industry", label: "行业 / 板块整体下跌", kind: "repair", method: "同业相对表现（同行中位跌幅）",
        level: med <= -0.3 && gap > -0.15 ? "high" : med <= -0.2 ? "medium" : "low",
        evidence: [`同行较 5 年高点中位 ${pct(med)}，本公司 ${pct(dd)}（${grp}，均通过 L1）`],
      });
      add({
        key: "company", label: "公司特有问题", kind: "neutral", method: "同业相对表现（比同行多跌多少）",
        level: gap <= -0.25 ? "high" : gap <= -0.15 ? "medium" : "low",
        evidence: [`比同行中位多跌 ${pct(-gap)}${gap <= -0.15 ? "，市场在为这家公司的特有问题定价，要找到是什么" : ""}`],
      });
    } else {
      for (const [key, label, kind] of [["industry", "行业 / 板块整体下跌", "repair"], ["company", "公司特有问题", "neutral"]] as const)
        add({ key, label, kind, method: "同业相对表现", level: "unknown", evidence: [peers ? peers.reason ?? "同行不足" : "同行数据加载中…"] });
    }
  }

  // 11. one-off / external event: China ADR discount, legal / regulatory words in recent news
  {
    const china = ["CNY", "HKD"].includes(e.currency);
    const news = (profile?.news ?? []).filter((n) => EVENT_WORDS.test(`${n.title} ${n.titleEn ?? ""}`)).slice(0, 2);
    add({
      key: "event", label: "一次性 / 外部事件", kind: "repair", method: "新闻关键词（诉讼、监管、关税、召回…）+ 地缘折价",
      level: news.length >= 2 || (china && news.length) ? "high" : news.length || china ? "medium" : profile ? "low" : "unknown",
      evidence: [
        china && "中国公司：监管、地缘政治、审计与退市风险带来长期折价",
        ...news.map((n) => `新闻：${n.title}${n.move != null ? `（次日 ${sgn(n.move, 1)}）` : ""}`),
        !china && !news.length && (profile ? "近期新闻里没有诉讼、监管、关税类关键词" : "新闻加载中…"),
      ],
    });
  }

  // 12. neglect: attention cooling, small size
  {
    const ser = profile?.attention?.series ?? [];
    const ratios = ser.map((x) => x.ratio).filter((r): r is number => r != null);
    const cool = ratios.length > 0 && ratios.every((r) => r < 0.85);
    const small = (v?.marketCapUsd ?? Infinity) < 5e9;
    add({
      key: "neglect", label: "被市场忽视", kind: "repair", method: "关注度（成交量、维基浏览）+ 市值规模",
      level: !ratios.length ? (small ? "medium" : "unknown") : cool && small ? "high" : cool || small ? "medium" : "low",
      evidence: [
        ratios.length ? `近 30 天关注度是此前的 ${ratios.map((r) => `×${r.toFixed(2)}`).join(" / ")}（${ser.map((s) => (s.key === "volume" ? "成交量" : "维基")).join(" / ")}）` : "",
        v && `市值 ${v.marketCapUsd >= 1e9 ? `$${(v.marketCapUsd / 1e9).toFixed(1)}B` : `$${(v.marketCapUsd / 1e6).toFixed(0)}M`}${small ? "，小盘股覆盖少" : ""}`,
      ],
    });
  }
  return out;
}

const aiLevel = (d: DiagnosisRow<DiagnosisResult>, key: string) => d.result.causes?.find((c) => c.key === key);

// Every hypothesis, ranked by the stronger of the system's and the AI engines' judgement.
export function rankedCauses(e: Evaluation, peers: Peers | null, profile: Profile | null, ai: DiagnosisRow<DiagnosisResult>[]) {
  const withAI = ai.filter((d) => d.result.causes?.length);
  const hs = hypotheses(e, peers, profile).map((h) => {
    const votes = withAI.map((d) => aiLevel(d, h.key)?.likelihood).filter(Boolean) as Level[];
    const top = votes.reduce((a, x) => Math.max(a, levelRank[x]), -1);
    return { ...h, rank: Math.max(levelRank[h.level], top) + levelRank[h.level] * 0.1, aiHigh: votes.filter((x) => x === "high").length };
  });
  return { hs: hs.sort((a, b) => b.rank - a.rank), withAI };
}

export const causeLevelText = levelText;

export function CheapCauses({ e, profile, ai, peers }: { e: Evaluation; profile: Profile | null; ai: DiagnosisRow<DiagnosisResult>[]; peers: Peers | null }) {
  const [all, setAll] = useState(false);
  const { hs, withAI } = rankedCauses(e, peers, profile, ai);
  const shown = all ? hs : hs.filter((h) => h.rank >= 2);
  const likely = hs.filter((h) => h.level === "high");
  const traps = likely.filter((h) => h.kind === "trap").length;
  const repairs = likely.filter((h) => h.kind === "repair").length;

  return (
    <div className="card causes">
      <div className="card-head">
        <strong>股价低迷的可能原因</strong>
        <span title="一家公司往往几个原因同时存在：这里列出的是可能性，按系统和 AI 的判断排序，不是结论">
          12 项排查 · 系统按数据判断{withAI.length ? ` · ${withAI.map((d) => engineShort[d.engine]).join("、")} 逐项判断` : ""}
        </span>
      </div>
      <p className="causes-lead">
        {likely.length
          ? <>系统看最可能：{likely.map((h) => h.label).join("、")}。{traps > repairs ? <b className="neg">陷阱型原因占多数，便宜可能有道理。</b> : repairs > traps ? <b className="pos">可修复的外部原因为主。</b> : traps ? "陷阱型和可修复的原因都有，要分清主次。" : ""}</>
          : "数据里没有特别突出的原因，需要结合新闻和 AI 看。"}
      </p>
      <div className="causes-list">
        {shown.map((h) => (
          <div key={h.key} className={`cause lv-${h.level}`}>
            <div className="cause-head">
              <span className={`kind ${h.kind}`} title={kindTip[h.kind]}>{kindText[h.kind]}</span>
              <b title={h.method}>{h.label}</b>
              <span className={`lv lv-${h.level}`} title={`系统判断 · 方法：${h.method}`}>{levelText[h.level]}</span>
              {withAI.map((d) => {
                const c = aiLevel(d, h.key);
                return c ? <span key={d.engine} className={`lv ai lv-${c.likelihood}`} title={`${engineShort[d.engine]}：${c.reason}`}>{engineShort[d.engine]} {levelText[c.likelihood]}</span> : null;
              })}
            </div>
            <div className="cause-ev">
              {h.evidence.join("；")}
              {withAI.map((d) => {
                const c = aiLevel(d, h.key);
                return c && c.likelihood !== "low" && c.likelihood !== "unknown" ? <div key={d.engine} className="cause-ai">{engineShort[d.engine]}：{c.reason}</div> : null;
              })}
            </div>
          </div>
        ))}
      </div>
      <div className="causes-foot">
        {hs.length > shown.length && <button className="link small" onClick={() => setAll(true)}>其余 {hs.length - shown.length} 项（不像 / 看不出）</button>}
        {all && <button className="link small" onClick={() => setAll(false)}>收起</button>}
        {!withAI.length && <span className="small muted">AI 逐项判断会在下次诊断后出现；定性的原因（颠覆、事件）主要靠它和新闻。</span>}
      </div>
    </div>
  );
}
