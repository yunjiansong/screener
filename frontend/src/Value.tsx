import { useEffect, useMemo, useState } from "react";
import { DiagnosisResult, DiagnosisRow, Evaluation, ValueInputs, ValueParams, engineShort, pct, valueApi } from "./api";

// Margin-of-safety price: intrinsic value per share from several standard formulas, the AI
// engines' own estimates and the person's own figure; buy price = value × (1 − margin of safety).
// The formulas run here so assumptions can be changed and seen at once; they are saved per
// company (or as defaults for all companies).

interface Full {
  mos: number;
  discount: number;
  terminal: number;
  years: number;
  growth: number;
  aaa: number;
  multiple: number | null;
  fcfBase: "norm" | "latest";
  own: number | null;
  ownNote: string;
  off: string[];
}

const BASE: Omit<Full, "growth" | "multiple"> = {
  mos: 0.3, discount: 0.1, terminal: 0.03, years: 10, aaa: 0.05, fcfBase: "norm", own: null, ownNote: "", off: ["grahamNumber"],
};

// Latest revenue growth: the latest 12 months against the 12 months a year earlier (TTM to TTM
// when there are quarterlies, else fiscal year to fiscal year), annualized when the gap isn't a year.
export function latestRevGrowth(e: Evaluation): { g: number; label: string } | null {
  const days = (a: string, b: string) => (Date.parse(a) - Date.parse(b)) / 864e5;
  const t = e.ttm ?? [];
  const last = t.at(-1) ?? e.years.at(-1);
  if (!last?.revenue) return null;
  const prev =
    t.length >= 2 ? t.at(-2) : [...e.years].reverse().find((y) => y.end !== last.end && days(last.end, y.end) >= 355 && y.revenue);
  if (!prev?.revenue || prev.revenue <= 0 || last.revenue <= 0) return null;
  const yrs = days(last.end, prev.end) / 365.25;
  if (yrs < 0.9 || yrs > 2.2) return null;
  return { g: (last.revenue / prev.revenue) ** (1 / yrs) - 1, label: `${prev.end.slice(0, 7)} → ${last.end.slice(0, 7)}` };
}

// Growth default: the lowest of 5-year FCF growth, the latest year's revenue growth and 8%, at least 0.
// Past growth over a lucky start year overstates the future; the latest year shows where it is now.
export function autoGrowth(v: ValueInputs, rev: number | null) {
  const xs = [v.histFcfGrowth, rev, 0.08].filter((x): x is number => x != null);
  return Math.max(0, Math.min(...xs));
}

function resolve(v: ValueInputs, target: number | null, rev: number | null, def: ValueParams | null, own: ValueParams | null): Full {
  return { ...BASE, growth: autoGrowth(v, rev), multiple: target ?? v.medianMultiple, ...stripNull(def), ...stripNull(own) } as Full;
}

function stripNull(p: ValueParams | null): ValueParams {
  if (!p) return {};
  return Object.fromEntries(Object.entries(p).filter(([, x]) => x != null)) as ValueParams;
}

interface Row {
  key: string;
  label: string;
  formula: string;
  value: number | null;
  range?: [number, number];
  note?: string;
  ai?: boolean;
}

// Growth starts at g and falls in a straight line to the terminal rate by year n.
function dcf(f: number, g: number, r: number, tg: number, n: number) {
  let total = 0;
  let x = f;
  for (let t = 1; t <= n; t++) {
    x *= 1 + (n > 1 ? g + ((tg - g) * (t - 1)) / (n - 1) : g);
    total += x / (1 + r) ** t;
  }
  return total + (x * (1 + tg)) / (r - tg) / (1 + r) ** n;
}

function rows(v: ValueInputs, p: Full, ai: DiagnosisRow<DiagnosisResult>[]): Row[] {
  const f = p.fcfBase === "latest" && v.latestFcf != null ? v.latestFcf : v.fcf;
  const fl = p.fcfBase === "latest" ? "最新 12 个月 FCF" : "正常化 FCF";
  const ok = f > 0;
  const out: Row[] = [
    {
      key: "dcf", label: "两阶段 DCF",
      formula: `${fl}/股 第 1 年增长 ${pct(p.growth, 1)}，${p.years} 年内线性降到永续 ${pct(p.terminal, 1)}，折现率 ${pct(p.discount, 1)}；加每股净现金`,
      value: ok && p.discount > p.terminal ? dcf(f, p.growth, p.discount, p.terminal, p.years) + v.netCash : null,
      note: ok ? undefined : "FCF 为负，不适用",
    },
    {
      key: "epv", label: "无增长价值（EPV）",
      formula: `${fl}/股 ÷ 折现率 ${pct(p.discount, 1)} + 每股净现金。假设永不增长，是保守的底线`,
      value: ok ? f / p.discount + v.netCash : null,
    },
    {
      key: "multiple", label: "历史倍数回归",
      formula: `${fl}/股 × P/FCF ${p.multiple != null ? `${p.multiple.toFixed(1)}x` : "—"}（默认为系统目标倍数 = min(自身历史中位, 上限)）`,
      value: ok && p.multiple != null ? f * p.multiple : null,
    },
    {
      key: "graham", label: "格雷厄姆公式",
      formula: `EPS × (8.5 + 2g) × 4.4 ÷ Y：EPS 取近期平均，g = ${(p.growth * 100).toFixed(1)}，Y = AAA 公司债收益率 ${(p.aaa * 100).toFixed(1)}`,
      value: v.eps != null && v.eps > 0 && p.aaa > 0 ? (v.eps * (8.5 + 2 * p.growth * 100) * 4.4) / (p.aaa * 100) : null,
      note: v.eps != null && v.eps <= 0 ? "EPS 为负，不适用" : undefined,
    },
    {
      key: "grahamNumber", label: "格雷厄姆数",
      formula: "√(22.5 × EPS × 每股净资产)：PE ≤ 15 且 PB ≤ 1.5 的上限价。轻资产公司净资产低，会严重低估，默认不计入",
      value: v.eps != null && v.bookValue != null && v.eps > 0 && v.bookValue > 0 ? Math.sqrt(22.5 * v.eps * v.bookValue) : null,
      note: v.bookValue != null && v.bookValue <= 0 ? "净资产为负，不适用" : undefined,
    },
  ];
  for (const d of ai) {
    const fv = d.result.fairValue;
    if (!fv || !(fv.base > 0)) continue;
    out.push({
      key: `ai:${d.engine}`, label: `AI · ${engineShort[d.engine]}`, ai: true,
      formula: `${fv.method}。${fv.assumptions}（${new Date(d.at).toLocaleDateString("zh-CN")} 诊断时股价 $${d.price.toFixed(2)}）`,
      value: fv.base, range: [fv.low, fv.high],
    });
  }
  if (p.own != null && p.own > 0) {
    out.push({ key: "own", label: "我的估计", formula: p.ownNote || "自己填写的每股内在价值", value: p.own });
  }
  return out;
}

const median = (xs: number[]) => {
  const s = [...xs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};

// Saved assumptions (this company's and the defaults), shared by the KPI strip and the table.
export type SavedParams = { company: ValueParams | null; default: ValueParams | null };
export function useValueParams(ticker: string) {
  const [saved, setSaved] = useState<SavedParams | null>(null);
  useEffect(() => {
    setSaved(null);
    valueApi.get(ticker).then(setSaved).catch(() => setSaved({ company: null, default: null }));
  }, [ticker]);
  return { saved, setSaved };
}

export interface Scenarios {
  bear: number | null;
  base: number | null;
  bull: number | null;
  bearGrowth: number;
  baseGrowth: number;
  bullMultiple: number | null;
  imputed: boolean;
}

// Three intrinsic-value scenarios per share: bear = the DCF at the latest actual revenue growth
// (never above 0), base = the DCF with the saved / default assumptions, bull = the historical
// multiple coming back (never below base).
export function scenarios(e: Evaluation, v: ValueInputs, saved: SavedParams | null): Scenarios {
  const rev = latestRevGrowth(e)?.g ?? null;
  const p = resolve(v, e.valuation?.target ?? null, rev, saved?.default ?? null, saved?.company ?? null);
  const f = p.fcfBase === "latest" && v.latestFcf != null ? v.latestFcf : v.fcf;
  const ok = f > 0 && p.discount > p.terminal;
  const bearGrowth = Math.min(0, rev ?? 0, v.histFcfGrowth ?? 0);
  const base = ok ? dcf(f, p.growth, p.discount, p.terminal, p.years) + v.netCash : null;
  const bear = ok ? dcf(f, bearGrowth, p.discount, p.terminal, p.years) + v.netCash : null;
  const mult = ok && p.multiple != null ? f * p.multiple : null;
  const bull = base != null && mult != null ? Math.max(base, mult) : (mult ?? base);
  const imputed = [...e.years.slice(-3), ...(e.ttm ?? []).slice(-1)].some((y) => y.capexImputed);
  return { bear: bear != null && base != null ? Math.min(bear, base) : bear, base, bull, bearGrowth, baseGrowth: p.growth, bullMultiple: p.multiple, imputed };
}

// The composite the table shows: median of the counted methods, and the buy price.
function composite(v: ValueInputs, p: Full, ai: DiagnosisRow<DiagnosisResult>[]) {
  const list = rows(v, p, ai);
  const used = list.filter((r) => r.value != null && r.value > 0 && !p.off.includes(r.key));
  const iv = used.length ? median(used.map((r) => r.value!)) : null;
  return { list, used, iv, buy: iv != null ? iv * (1 - p.mos) : null };
}

// One line for the folded card: methods, composite value, buy price, price against it.
export function mosSummary(e: Evaluation, v: ValueInputs, ai: DiagnosisRow<DiagnosisResult>[], saved: SavedParams | null) {
  const p = resolve(v, e.valuation?.target ?? null, latestRevGrowth(e)?.g ?? null, saved?.default ?? null, saved?.company ?? null);
  const { used, iv, buy } = composite(v, p, ai);
  const short: Record<string, string> = { dcf: "DCF", epv: "EPV", multiple: "历史倍数", graham: "格雷厄姆", grahamNumber: "格雷厄姆数", own: "我的估计" };
  return {
    text: `${used.length} 种方法 · ${used.map((r) => `${short[r.key] ?? r.label.replace("AI · ", "AI ")} ${usd(r.value)}`).join(" · ")}`,
    iv, buy, below: buy != null ? v.price <= buy : null,
  };
}

export const usd = (x: number | null | undefined) => (x == null || !isFinite(x) ? "—" : `$${x >= 1000 ? x.toFixed(0) : x.toFixed(2)}`);

export function SafetyMargin({ e, v, ai, vp }: { e: Evaluation; v: ValueInputs; ai: DiagnosisRow<DiagnosisResult>[]; vp: ReturnType<typeof useValueParams> }) {
  const ticker = e.ticker;
  const target = e.valuation?.target ?? null;
  const rev = useMemo(() => latestRevGrowth(e), [e]);
  const implied = e.analysis?.reverseDcf?.impliedGrowth ?? null;
  const { saved, setSaved } = vp;
  const [draft, setDraft] = useState<ValueParams>({});
  const [msg, setMsg] = useState("");
  useEffect(() => setDraft({}), [ticker]);

  const p = useMemo(() => resolve(v, target, rev?.g ?? null, saved?.default ?? null, { ...(saved?.company ?? {}), ...draft }), [v, target, rev, saved, draft]);
  const { list, used, iv, buy } = composite(v, p, ai);
  const gap = buy != null ? v.price / buy - 1 : null;
  const dirty = Object.keys(draft).length > 0;

  const set = (k: keyof ValueParams, x: unknown) => setDraft((d) => ({ ...d, [k]: x }));
  const toggle = (key: string) => set("off", p.off.includes(key) ? p.off.filter((x) => x !== key) : [...p.off, key]);
  const save = async (scope: "company" | "default" | "reset") => {
    try {
      if (scope === "reset") {
        await valueApi.save(ticker, null);
        setSaved((s) => ({ default: s?.default ?? null, company: null }));
        setDraft({});
        setMsg("已恢复默认假设");
        return;
      }
      const merged = { ...(saved?.company ?? {}), ...draft };
      if (scope === "company") {
        await valueApi.save(ticker, merged);
        setSaved((s) => ({ default: s?.default ?? null, company: merged }));
      } else {
        // Defaults hold the generic assumptions only; growth, multiple and own figure are per company.
        const { mos, discount, terminal, years, aaa, fcfBase, off } = p;
        const def = { mos, discount, terminal, years, aaa, fcfBase, off };
        await valueApi.saveDefault(def);
        const { growth, multiple, own, ownNote } = merged;
        const rest = Object.fromEntries(Object.entries({ growth, multiple, own, ownNote }).filter(([, x]) => x != null));
        await valueApi.save(ticker, Object.keys(rest).length ? rest : null);
        setSaved({ default: def, company: Object.keys(rest).length ? rest : null });
      }
      setDraft({});
      setMsg(scope === "company" ? "已保存为本公司假设" : "已设为所有公司的默认假设");
    } catch (e) {
      setMsg(`保存失败：${e instanceof Error ? e.message : e}`);
    }
  };

  const num = (k: keyof ValueParams, val: number | null, opts: { pct?: boolean; step?: number; min?: number; max?: number; suffix?: string }) => (
    <input
      type="number"
      step={opts.step ?? (opts.pct ? 0.5 : 1)}
      min={opts.min}
      max={opts.max}
      value={val == null ? "" : opts.pct ? +(val * 100).toFixed(2) : val}
      onChange={(e) => {
        const x = e.target.value === "" ? undefined : Number(e.target.value);
        set(k, x == null || !isFinite(x) ? undefined : opts.pct ? x / 100 : x);
      }}
    />
  );

  return (
    <div className="card mos">
      <div className="card-head">
        <strong>安全边际价格</strong>
        <span>安全边际 {pct(p.mos)} · 内在价值取已勾选方法的中位数{saved?.company ? " · 本公司自定假设" : ""}</span>
      </div>
      <div className="mos-sum">
        <div>
          <span>内在价值 / 股</span>
          <b>{usd(iv)}</b>
          <small>{used.length} 种方法的中位{used.length > 1 ? `，范围 ${usd(Math.min(...used.map((r) => r.value!)))}–${usd(Math.max(...used.map((r) => r.value!)))}` : ""}</small>
        </div>
        <div>
          <span>安全边际买入价</span>
          <b className="buy">{usd(buy)}</b>
          <small>内在价值 × (1 − {pct(p.mos)})</small>
        </div>
        <div>
          <span>现价</span>
          <b>{usd(v.price)}</b>
          <small className={gap == null ? "" : gap <= 0 ? "pos" : "neg"}>
            {gap == null ? "—" : gap <= 0 ? `已低于买入价 ${pct(-gap, 1)}` : `还需下跌 ${pct(1 - buy! / v.price, 1)} 才到买入价`}
          </small>
        </div>
      </div>
      <table className="mos-table">
        <thead>
          <tr>
            <th title="勾选的方法计入中位数">计入</th>
            <th>方法</th>
            <th>内在价值 / 股</th>
            <th>安全边际价</th>
            <th>现价相对内在价值</th>
          </tr>
        </thead>
        <tbody>
          {list.map((r) => {
            const off = p.off.includes(r.key);
            const valid = r.value != null && r.value > 0;
            return (
              <tr key={r.key} className={`${off || !valid ? "off" : ""} ${r.ai ? "ai" : ""}`}>
                <td><input type="checkbox" checked={!off && valid} disabled={!valid} onChange={() => toggle(r.key)} aria-label={`计入 ${r.label}`} /></td>
                <td title={r.formula}>
                  {r.label}
                  <small>{r.note ?? r.formula}</small>
                </td>
                <td>{valid ? usd(r.value) : "—"}{r.range && <small>{usd(r.range[0])}–{usd(r.range[1])}</small>}</td>
                <td>{valid ? usd(r.value! * (1 - p.mos)) : "—"}</td>
                <td className={valid ? (v.price < r.value! ? "pos" : "neg") : ""}>{valid ? `${v.price < r.value! ? "低" : "高"} ${pct(Math.abs(v.price / r.value! - 1))}` : ""}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {!ai.some((d) => d.result.fairValue) && (
        <p className="small muted">AI 估值：诊断时会让模型给出每股内在价值区间和假设；现有诊断早于这个功能，下次诊断后出现在表里。</p>
      )}
      <details className="mos-params">
        <summary>调整假设{dirty ? "（未保存）" : ""}</summary>
        <div className="mos-grid">
          <label>安全边际 %{num("mos", p.mos, { pct: true, step: 5, min: 0, max: 90 })}</label>
          <label>折现率 %{num("discount", p.discount, { pct: true, min: 1, max: 30 })}</label>
          <label>
            起始增长率 %
            {num("growth", p.growth, { pct: true, min: -20, max: 50 })}
            {implied != null && <small className={implied < p.growth ? "hint warn" : "hint"}>现价隐含 {implied >= 0 ? "+" : ""}{pct(implied, 1)} / 年</small>}
          </label>
          <label>高增长年数{num("years", p.years, { min: 1, max: 30 })}</label>
          <label>永续增长 %{num("terminal", p.terminal, { pct: true, min: 0, max: 5 })}</label>
          <label>AAA 债收益率 %{num("aaa", p.aaa, { pct: true, step: 0.1, min: 0.5, max: 15 })}</label>
          <label>目标 P/FCF{num("multiple", p.multiple, { step: 0.5, min: 1, max: 100 })}</label>
          <label>
            FCF 起点
            <select value={p.fcfBase} onChange={(e) => set("fcfBase", e.target.value)}>
              <option value="norm">正常化（近 3 期均值）</option>
              <option value="latest" disabled={v.latestFcf == null}>最新 12 个月</option>
            </select>
          </label>
          <label>我的内在价值 $/股{num("own", p.own, { step: 1, min: 0 })}</label>
          <label className="wide">
            我的依据
            <input type="text" value={p.ownNote} maxLength={200} placeholder="例如：同业 EV/EBIT 12x、分部估值…" onChange={(e) => set("ownNote", e.target.value)} />
          </label>
        </div>
        <p className="small muted">
          输入（美元 / 流通股，ADS 已折算）：正常化 FCF {usd(v.fcf)} · 最新 12 个月 FCF {usd(v.latestFcf)} · 近期平均 EPS {usd(v.eps)} · 每股净资产 {usd(v.bookValue)} · 每股净现金 {usd(v.netCash)} ·
          过去 5 年 FCF 年增速 {v.histFcfGrowth != null ? pct(v.histFcfGrowth, 1) : "—"} · 自身历史 P/FCF 中位 {v.medianMultiple != null ? `${v.medianMultiple.toFixed(1)}x` : "—"}（10 分位 {v.p10Multiple != null ? `${v.p10Multiple.toFixed(1)}x` : "—"}）。
          最近一年收入增速 {rev ? `${pct(rev.g, 1)}（${rev.label}）` : "—"}。
          起始增长率默认取「过去 5 年 FCF 增速、最近一年收入增速、8%」中最低的，不低于 0；DCF 里增长率逐年线性降到永续增速。
          {implied != null && ` 现价隐含的 FCF 增速是 ${pct(implied, 1)} / 年（反向 DCF，增速恒定 10 年、折现率 10%）：起始增长率高于它，说明你比市场乐观。`}
        </p>
        <div className="mos-actions">
          <button className="btn primary small" disabled={!dirty} onClick={() => save("company")}>保存为本公司假设</button>
          <button className="btn small" onClick={() => save("default")} title="安全边际、折现率、永续增长、年数、AAA 收益率、FCF 起点和勾选的方法作为所有公司的默认；增长率、倍数和我的估计只存本公司">设为默认</button>
          {saved?.company && <button className="link small" onClick={() => save("reset")}>恢复默认</button>}
          {dirty && <button className="link small" onClick={() => setDraft({})}>放弃修改</button>}
          {msg && <span className="small muted">{msg}</span>}
        </div>
      </details>
    </div>
  );
}
