import { useEffect, useMemo, useState } from "react";
import {
  AIStatus,
  EarningsDate,
  EngineName,
  Funnel,
  HomeData,
  Judgement,
  Stance,
  Summary,
  aiApi,
  alertsApi,
  api,
  engineOrder,
  engineShort,
  hintLabel,
  looksAnomalous,
  money,
  pct,
  stanceLabel,
  verdictLabel,
} from "./api";

// ---------- shared helpers ----------

export const md = (d: string) => {
  const [, m, day] = d.split("-");
  return `${Number(m)}/${Number(day)}`;
};
const today = () => new Date().toLocaleDateString("sv"); // YYYY-MM-DD, local
const plusDays = (n: number) => new Date(Date.now() + n * 864e5).toLocaleDateString("sv");
export const within14 = (e?: EarningsDate) => !!e && e.date >= today() && e.date <= plusDays(14);

export interface Tab {
  minStage: number;
  label: string;
  short: string;
  desc: string;
  count: (f: Funnel) => number;
}

export const tabs: Tab[] = [
  { minStage: 0, label: "有财报数据", short: "有财报", desc: "所有有财报数据的公司，可搜索看它卡在哪一层", count: (f) => f.withFundamentals },
  { minStage: 1, label: "L1 质量", short: "L1 质量", desc: "基本面硬：FCF 持续为正、利润率达标、负债可控", count: (f) => f.quality },
  { minStage: 2, label: "L2 价格错位", short: "L2 错位", desc: "跌得多、估值低、基本面没坏", count: (f) => f.dislocation },
  { minStage: 3, label: "L3 凸性 · 候选", short: "L3 候选", desc: "三层全部通过：质量硬、价格错位、上下行不对称", count: (f) => f.convexity },
];

// ---------- AI conclusion per row ----------

const lean: Record<Stance, { label: string; cls: string }> = {
  mispriced: { label: "偏错杀", cls: "good" },
  trap: { label: "偏恶化", cls: "bad" },
  unclear: { label: "看不清", cls: "muted" },
  fair: { label: "定价合理", cls: "muted" },
};
const stepText = { xpulse: "搜索 X", diagnosis: "模型诊断", rollup: "综合各模型", skill: "Skill 分析" } as const;

export interface AIView {
  state: "none" | "queued" | "running" | "done";
  stance?: Stance;
  label: string;
  cls: string;
  sub: string;
  title: string;
  disagree: boolean;
}

export function aiView(r: Summary, status: AIStatus | null): AIView {
  const bc = status?.batch && !status.batch.finished ? status.batch.companies.find((c) => c.cik === r.cik) : undefined;
  const active = Object.values(status?.active ?? {}).find((j) => j?.ticker === r.ticker);
  if ((bc && bc.step !== "waiting" && bc.step !== "done") || active) {
    const step = (bc?.step ?? active?.kind) as keyof typeof stepText;
    return { state: "running", label: "诊断中", cls: "run", sub: stepText[step] ?? "", title: "AI 正在处理这家公司", disagree: false };
  }
  if (bc?.step === "waiting") return { state: "queued", label: "排队", cls: "queued", sub: "", title: "在 AI 批量诊断的队列里", disagree: false };
  const per = engineOrder.filter((e) => r.ai?.engines?.[e]).map((e) => [e, r.ai!.engines![e]!] as const);
  if (!r.ai || (!r.ai.rollup && per.length === 0)) return { state: "none", label: "—", cls: "muted", sub: "", title: "还没有 AI 诊断", disagree: false };
  const count: Record<string, number> = {};
  per.forEach(([, v]) => (count[v.stance] = (count[v.stance] ?? 0) + 1));
  const top = Object.entries(count).sort((a, b) => b[1] - a[1])[0]?.[0] as Stance | undefined;
  const stance = (r.ai.rollup?.stance ?? top)!;
  const agree = per.filter(([, v]) => v.stance === stance).length;
  const disagree = Object.keys(count).length > 1;
  const sub = per.length ? `${agree}/${per.length} ${disagree ? "分歧" : "一致"}` : "综合";
  const title = [
    r.ai.rollup ? `已综合各模型 · 综合：${stanceLabel[r.ai.rollup.stance]}（信心 ${r.ai.rollup.confidence}/5）${r.ai.rollup.summary}` : "",
    ...per.map(([e, v]) => `${engineShort[e as EngineName]}：${stanceLabel[v.stance]}（${v.confidence}/5）${v.summary}`),
    r.ai.xpulse != null ? `X 情绪分 ${r.ai.xpulse > 0 ? "+" : ""}${r.ai.xpulse}（-5…+5）` : "",
  ].filter(Boolean).join("\n");
  return { state: "done", stance, ...lean[stance], sub, title, disagree };
}

// Conflict: the system passed the company as a candidate, but AI does not see a mispricing
// the system's clues can explain — AI leans to deterioration, or cannot tell while the system
// raised no internal warning, or the models disagree with each other.
export function conflict(r: Summary, v: AIView): string | null {
  if (r.stage < 3 || v.state !== "done" || !v.stance) return null;
  const internal = (r.hints ?? []).some((h) => h.kind === "internal");
  if (v.stance === "trap") return "系统把它筛为候选（价格错杀），AI 却倾向「基本面在恶化」：先看 AI 的看空理由";
  if (v.stance === "unclear" && !internal) return "系统线索只有价值背离 / 外部因素，AI 却看不清：系统可能漏掉了内部问题";
  if (v.disagree) return "各 AI 模型的结论不一致：打开详情页对比分歧点";
  return null;
}

// ---------- to-do cards ----------

export type Focus = { kind: "falsified" | "review" | "new" | "earnings"; label: string } | { kind: "industry"; label: string; value: string };

export function TodoCards({ home, earnings, items, focus, onFocus }: {
  home: HomeData | null;
  earnings: Record<string, EarningsDate> | null;
  items: Summary[];
  focus: Focus | null;
  onFocus: (f: Focus | null) => void;
}) {
  const soon = useMemo(() => {
    if (!earnings) return [];
    return items
      .filter((i) => i.stage >= 3 && within14(earnings[i.ticker]))
      .sort((a, b) => earnings[a.ticker].date.localeCompare(earnings[b.ticker].date));
  }, [earnings, items]);
  const list = (xs: { ticker: string; text?: string }[], max: number, fmt: (x: { ticker: string; text?: string }) => string) =>
    xs.slice(0, max).map(fmt).join(" · ") + (xs.length > max ? ` 等 ${xs.length} 家` : "");
  const cards: { kind: Focus["kind"]; title: string; n: number | null; sub: string; tone: string; tip: string }[] = [
    {
      kind: "falsified", title: "证伪条件触发", tone: "red", n: home ? home.falsified.length : null,
      sub: !home ? "加载中…" : home.falsified.length ? list(home.falsified, 2, (x) => `${x.ticker} · ${x.text}`) : "没有触发 · 在详情页「我的判断」添加可量化的证伪条件",
      tip: "你在详情页写下的量化证伪条件（如「毛利率低于 55%」），被最新财报或股价击中",
    },
    {
      kind: "review", title: "到期复查", tone: "yellow", n: home ? home.review.length : null,
      sub: !home ? "加载中…" : home.review.length ? list(home.review, 3, (x) => `${x.ticker} · ${x.text}`) : "没有到期的复查",
      tip: "你设的下次复查日期已到：重读判断，看证伪条件是否出现",
    },
    {
      kind: "new", title: "新进候选", tone: "blue", n: home ? home.newIn.length : null,
      sub: !home ? "加载中…" : home.baseline
        ? `${home.newIn.length ? list(home.newIn, 3, (x) => x.ticker) + " 新进" : "没有新进"} · 移出 ${home.removed.length} 家${home.removed.length ? `（${list(home.removed, 3, (x) => x.ticker)}）` : ""}`
        : "历史批次的候选都相同，没有变化",
      tip: home?.baseline ? `对比 ${new Date(home.baseline.startedAt).toLocaleString()} 的筛选（最近一次候选名单不同的批次）` : "",
    },
    {
      kind: "earnings", title: "14 天内财报", tone: "grey", n: earnings ? soon.length : null,
      sub: !earnings ? "正在获取财报日期…" : soon.length ? list(soon.map((s) => ({ ticker: s.ticker, text: md(earnings[s.ticker].date) })), 4, (x) => `${x.ticker} ${x.text}`) : "未来两周没有候选公司发财报",
      tip: "日期来自 Nasdaq（Zacks 预估或公司确认），取不到时按 SEC 过去的业绩公告日推算",
    },
  ];
  return (
    <div className="todos">
      {cards.map((c) => {
        const on = focus?.kind === c.kind;
        return (
          <button key={c.kind} className={`todo ${c.tone} ${on ? "on" : ""} ${c.n ? "" : "empty"}`} title={c.tip} aria-pressed={on}
            disabled={!c.n && !on} onClick={() => onFocus(on ? null : { kind: c.kind, label: c.title } as Focus)}>
            <span className="todo-top"><b>{c.title}</b><span className="todo-n">{c.n ?? "…"}</span></span>
            <span className="todo-sub">{c.sub}</span>
          </button>
        );
      })}
    </div>
  );
}

// ---------- funnel as one segmented control ----------

export function FunnelSeg({ f, active, onPick }: { f: Funnel; active: Tab; onPick: (t: Tab) => void }) {
  let prev = f.universe;
  return (
    <div className="fseg" role="tablist" aria-label="漏斗层级">
      {tabs.map((t) => {
        const n = t.count(f);
        const rate = prev ? `${((n / prev) * 100).toFixed(1)}%` : "—";
        const from = prev;
        prev = n;
        return (
          <button key={t.minStage} role="tab" aria-selected={t === active} className={t === active ? "on" : ""} onClick={() => onPick(t)}
            title={`${t.label}：${t.desc}\n${n.toLocaleString()} / ${from.toLocaleString()} 家，留存 ${rate}${t.minStage === 0 ? `（数据源 ${f.universe.toLocaleString()} 家上市运营公司）` : ""}`}>
            {t.short} <b>{n.toLocaleString()}</b>
          </button>
        );
      })}
    </div>
  );
}

// ---------- industry distribution ----------

export function IndustryBar({ rows, focus, onFocus }: { rows: Summary[]; focus: Focus | null; onFocus: (f: Focus | null) => void }) {
  const counts = useMemo(() => {
    const m = new Map<string, number>();
    rows.forEach((r) => {
      const k = r.industry || r.sector || "行业未知";
      m.set(k, (m.get(k) ?? 0) + 1);
    });
    return [...m.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  }, [rows]);
  const [all, setAll] = useState(false);
  if (counts.length < 2) return null;
  const shown = all ? counts : counts.slice(0, 8);
  return (
    <div className="industries">
      <span className="muted">行业分布</span>
      {shown.map(([k, n]) => {
        const on = focus?.kind === "industry" && focus.value === k;
        return (
          <button key={k} className={`ind-btn ${on ? "on" : ""} ${n >= 3 ? "many" : ""}`} aria-pressed={on}
            title={n >= 3 ? "同一行业多家同时入选：可能是行业性的系统下跌（外部因素）" : undefined}
            onClick={() => onFocus(on ? null : { kind: "industry", label: k, value: k })}>
            {k} {n}
          </button>
        );
      })}
      {counts.length > 8 && <button className="link small" onClick={() => setAll(!all)}>{all ? "收起" : `还有 ${counts.length - 8} 个`}</button>}
    </div>
  );
}

// ---------- table row ----------

export function Row({ r, onOpen, selected, onSelect, home, earnings, ai }: {
  r: Summary;
  onOpen: () => void;
  selected: boolean;
  onSelect: (on: boolean) => void;
  home?: HomeData["rows"][number];
  earnings?: EarningsDate;
  ai: AIView;
}) {
  const isCand = r.stage >= 3;
  const anomaly = looksAnomalous(r);
  const kinds = [...new Set((r.hints ?? []).map((h) => h.kind))].sort((a, b) => ["internal", "positive", "external"].indexOf(a) - ["internal", "positive", "external"].indexOf(b));
  const q = r.quality;
  const clash = conflict(r, ai);
  const defaultDown = r.downside != null && Math.abs(r.downside + 0.2) < 0.005;
  return (
    <div className={`grid-row trow ${selected ? "sel" : ""}`} role="link" tabIndex={0} onClick={onOpen} onKeyDown={(e) => e.key === "Enter" && onOpen()}>
      <label className="ck" onClick={(e) => e.stopPropagation()}>
        <input type="checkbox" checked={selected} onChange={(e) => onSelect(e.target.checked)} aria-label={`选择 ${r.ticker}`} />
      </label>
      <div className="co">
        <div className="top">
          <b>{r.ticker}</b>
          <span className="nm" title={r.nameZh ? `${r.nameZh} · ${r.name}` : r.name}>{r.nameZh ? <><em className="zh">{r.nameZh}</em> {r.name}</> : r.name}</span>
          {home?.isNew && <span className="tag new" title="本次筛选新进入候选">新进</span>}
          {clash && <span className="tag clash" title={clash}>冲突</span>}
        </div>
        <div className="ind">{r.industry || r.sector || "行业未知"}</div>
      </div>
      <div className="r">{money(r.marketCap)}</div>
      <div className="barcell">
        {r.drawdown != null && (
          <div className="bar rtl" style={{ width: 44 }}>
            <div style={{ width: `${Math.min(100, -r.drawdown * 100)}%`, background: "var(--dd-bar)" }} />
          </div>
        )}
        <span style={{ width: 44, textAlign: "right" }}>{pct(r.drawdown)}</span>
      </div>
      <div className="r" style={r.fcfYield != null && r.fcfYield > 1 ? { color: "var(--warn-ink)" } : undefined}>{pct(r.fcfYield, 1)}</div>
      <div className="r">{pct(r.percentile)}</div>
      <div className="r cvx" title={r.upside != null ? `上行 ${pct(r.upside)} / 下行 ${pct(r.downside)}；凸性 = 上行 ÷ |下行|` : undefined}>
        <b>{r.convexity == null ? "—" : `${r.convexity.toFixed(1)}x`}</b>
        {r.upside != null && <small>{defaultDown ? `上行 ${pct(r.upside)}` : `${pct(r.upside)} / ${pct(r.downside)}`}</small>}
      </div>
      <div className="r" style={{ fontWeight: 600, color: q >= 90 ? "var(--good-ink)" : q < 60 ? "var(--warn-ink)" : undefined }}>{q.toFixed(0)}</div>
      <div className="clues">
        {isCand ? (
          kinds.map((k) => (
            <span key={k} className={`chip ${k}`} title={(r.hints ?? []).filter((h) => h.kind === k).map((h) => h.text).join("\n")}>
              {hintLabel[k]}
            </span>
          ))
        ) : (
          <div className="stuck">
            <div className="dots" aria-hidden>
              {[1, 2, 3].map((n) => (
                <i key={n} className={n <= r.stage ? "pass" : n === r.stage + 1 ? "fail" : ""} />
              ))}
            </div>
            <span>卡在 L{r.stage + 1} · {r.failedCheck}</span>
          </div>
        )}
        {anomaly && <span className="chip warn" title="市值 < $50M、凸性 > 100x 或 FCF 收益率 > 100%，通常是数据问题">数据待核</span>}
      </div>
      <div className={`aicell ${ai.cls}`} title={ai.title}>
        <b>{ai.label}</b>
        {ai.sub && <small>{ai.sub}</small>}
      </div>
      <div className="r">
        {earnings ? (
          <span className={`edate ${within14(earnings) ? "soon" : ""}`}
            title={`${earnings.date}${earnings.estimated ? "（预计）" : "（公司已确认）"} · ${earnings.source === "nasdaq" ? "Nasdaq / Zacks" : earnings.note ?? "SEC 推算"}`}>
            {md(earnings.date)}{earnings.estimated ? "" : " ✓"}
          </span>
        ) : (
          <span className="muted">{isCand ? "—" : ""}</span>
        )}
      </div>
      <div>
        <VerdictCell j={r.judgement} home={home} isCand={isCand} />
      </div>
    </div>
  );
}

function VerdictCell({ j, home, isCand }: { j?: Judgement; home?: HomeData["rows"][number]; isCand: boolean }) {
  if (home?.falsified?.length) return <span className="vchip v-falsified" title={home.falsified.map((h) => h.text).join("\n")}>证伪触发</span>;
  if (home?.reviewDue) return <span className="vchip v-review" title={`复查日 ${home.reviewDue}`}>复查到期</span>;
  if (j?.verdict) return <span className={`vchip v-${j.verdict}`}>{verdictLabel[j.verdict]}</span>;
  if (j) return <span className="vchip v-note">有笔记</span>;
  return <span className="muted small">{isCand ? "未判断" : "—"}</span>;
}

// ---------- bulk actions ----------

export function BulkBar({ selected, items, onClear, onDone }: {
  selected: Set<number>;
  items: Summary[];
  onClear: () => void;
  onDone: (msg: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const rows = items.filter((i) => selected.has(i.cik));
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClear();
    addEventListener("keydown", k);
    return () => removeEventListener("keydown", k);
  }, [onClear]);
  if (rows.length === 0) return null;
  const run = async (what: string, f: (r: Summary) => Promise<unknown>) => {
    setBusy(true);
    const errs: string[] = [];
    for (const r of rows) {
      try {
        await f(r);
      } catch (e) {
        errs.push(`${r.ticker}：${(e as Error).message}`);
      }
    }
    setBusy(false);
    onDone(errs.length ? `${what}：${rows.length - errs.length} 家成功，失败 ${errs.join("；")}` : `${what}：${rows.length} 家`);
  };
  const reject = (r: Summary) => {
    const j = r.judgement;
    return api.saveJudgement(r.cik, r.ticker, {
      verdict: "reject", note: j?.note ?? "", worry: j?.worry ?? "", why: j?.why ?? "", falsify: j?.falsify ?? "",
      reviewOn: j?.reviewOn ?? "", falsifyRules: j?.falsifyRules ?? [],
    });
  };
  const rediagnose = async () => {
    setBusy(true);
    try {
      await aiApi.batch({ tickers: rows.map((r) => r.ticker), engines: ["codex", "agy"], xpulse: false, rollup: true });
      onDone(`已排队 AI 诊断：${rows.map((r) => r.ticker).join("、")}（ChatGPT + Gemini，完成后综合）`);
    } catch (e) {
      onDone(`AI 诊断没有开始：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="bulkbar" role="toolbar" aria-label="批量操作">
      <span>已选 <b>{rows.length}</b> 家</span>
      <i className="sep" />
      <button disabled={busy} onClick={() => run("已加入观察", (r) => alertsApi.watch(r.cik, r.ticker))}>加入观察</button>
      <button disabled={busy} onClick={() => run("已标为排除", reject)}>排除</button>
      <button className="primary" disabled={busy} onClick={rediagnose} title="用 ChatGPT + Gemini 重新诊断并综合（不用 Claude、不搜 X）">重新 AI 诊断</button>
      <button className="ghost" disabled={busy} onClick={onClear}>取消</button>
    </div>
  );
}
