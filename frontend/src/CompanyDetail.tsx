import { ThemeToggle } from "./theme";
import { useCallback, useEffect, useRef, useState } from "react";
import { alertsApi, api, Check, Evaluation, FalsifyRule, Judgement, JudgementInput, NewsItem, Verdict, hintLabel, money, pct, stanceLabel, verdictLabel, verdicts } from "./api";
import { LineChart } from "./LineChart";
import { CompanyProfile, News, useProfile } from "./Profile";
import { ProMetrics } from "./Metrics";
import { AIDiagnosisCard, useDiagnoses } from "./AI";
import { FalsifyRules } from "./Falsify";
import { AIBrief, AIPicture, JudgementPoints, Kpis, MarketSentiment, PeerCompare, aiPicture, ruleText, sameRule, sentimentSummary, usePeers } from "./DetailParts";
import { md } from "./Home";
import { SafetyMargin, mosSummary, scenarios, useValueParams, usd } from "./Value";
import { CheapCauses, rankedCauses } from "./Causes";
import { Analysis, Profile } from "./api";
import { SkillPanel, skillLine, useSkillRuns } from "./Skills";

const layerTitles: Record<number, string> = {
  1: "质量：基本面与护城河的痕迹",
  2: "价格错位：跌得多、估值低、基本面没坏",
  3: "凸性：下行有限、上行大",
};

interface Props {
  ticker: string;
  order: string[]; // tickers of the list the user came from, for J/K
  listLabel: string;
  onOpen: (t: string) => void;
  onBack: () => void;
  onSaved: (cik: number, j: Judgement | null) => void;
}

const emptyDraft: JudgementInput = { verdict: "", note: "", worry: "", why: "", falsify: "", reviewOn: "", falsifyRules: [] };

function toDraft(j: Judgement | null): JudgementInput {
  if (!j) return emptyDraft;
  // Older judgements only had a free-form note; surface it in "why" so it isn't lost.
  const why = j.why || (j.worry || j.falsify ? "" : j.note);
  return { verdict: j.verdict, note: why === j.note ? "" : j.note, worry: j.worry, why, falsify: j.falsify, reviewOn: j.reviewOn, falsifyRules: j.falsifyRules ?? [] };
}

const sameDraft = (a: JudgementInput, b: JudgementInput) => JSON.stringify(a) === JSON.stringify(b);

export function CompanyDetail({ ticker, order, listLabel, onOpen, onBack, onSaved }: Props) {
  const [e, setE] = useState<Evaluation | null>(null);
  const [judgement, setJudgement] = useState<Judgement | null>(null);
  const [watching, setWatching] = useState(false);
  const [draft, setDraft] = useState<JudgementInput>(emptyDraft);
  const [error, setError] = useState("");
  const [msg, setMsg] = useState("");
  const [saving, setSaving] = useState(false);
  const prof = useProfile(ticker);
  const ai = useDiagnoses(ticker);
  const pic = aiPicture(ai);
  const peers = usePeers(ticker);
  const vp = useValueParams(ticker);
  const sr = useSkillRuns(ticker);
  const [folds, setFolds] = useFolds();
  const [nextEarnings, setNextEarnings] = useState<string | null>(null);
  const [editRules, setEditRules] = useState(false);
  useEffect(() => {
    setNextEarnings(null);
    api.earnings().then((m) => setNextEarnings(m[ticker]?.date ?? null)).catch(() => {});
  }, [ticker]);
  const toggleRule = (r: FalsifyRule) =>
    setDraft((d) => ({ ...d, falsifyRules: d.falsifyRules.some((x) => sameRule(x, r)) ? d.falsifyRules.filter((x) => !sameRule(x, r)) : [...d.falsifyRules, r] }));

  useEffect(() => {
    setE(null);
    setError("");
    setMsg("");
    api
      .company(ticker)
      .then((r) => {
        setE(r.evaluation);
        setJudgement(r.judgement);
        setWatching(r.watching);
        setDraft(toDraft(r.judgement));
      })
      .catch((err) => setError(String(err.message ?? err)));
    scrollTo(0, 0);
  }, [ticker]);

  // Browser tab: "LULU · CareySon的视角的专家选股系统"; restored on leaving the page.
  useEffect(() => {
    const prev = document.title;
    document.title = `${ticker}${e?.nameZh ? ` ${e.nameZh}` : e?.name ? ` ${e.name}` : ""} · CareySon的视角的专家选股系统`;
    return () => {
      document.title = prev;
    };
  }, [ticker, e?.name, e?.nameZh]);

  const idx = order.indexOf(ticker);
  const at = (n: number) => order[(idx + n + order.length) % order.length];
  const step = useCallback((n: number) => order.length > 1 && onOpen(at(n)), [order, idx, onOpen]); // eslint-disable-line react-hooks/exhaustive-deps

  const pick = (v: Verdict) => setDraft((d) => ({ ...d, verdict: d.verdict === v ? "" : v }));

  const save = async (): Promise<boolean> => {
    if (!e) return false;
    setSaving(true);
    try {
      const blank = !draft.verdict && !(draft.note + draft.worry + draft.why + draft.falsify).trim() && draft.falsifyRules.length === 0;
      if (blank) {
        if (judgement) await api.deleteJudgement(e.cik);
        setJudgement(null);
        onSaved(e.cik, null);
        setMsg(judgement ? "已清除判断" : "");
      } else {
        const j = await api.saveJudgement(e.cik, e.ticker, draft);
        setJudgement(j);
        if (j.verdict === "watch") setWatching(true); // the server adds it to the watchlist
        onSaved(e.cik, j);
        setMsg("");
      }
      return true;
    } catch (err) {
      setMsg(String(err));
      return false;
    } finally {
      setSaving(false);
    }
  };
  const saveRef = useRef(save);
  saveRef.current = save;

  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
        ev.preventDefault();
        saveRef.current().then((ok) => ok && step(1));
        return;
      }
      const tag = (ev.target as HTMLElement).tagName;
      if (tag === "TEXTAREA" || tag === "INPUT" || ev.metaKey || ev.ctrlKey || ev.altKey) return;
      if (ev.key === "j" || ev.key === "J") step(1);
      else if (ev.key === "k" || ev.key === "K") step(-1);
      else if (ev.key === "Escape") onBack();
      else if (/^[1-5]$/.test(ev.key)) pick(verdicts[Number(ev.key) - 1]);
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [step, onBack]);

  // The panel's height follows its place on screen, so its button bar stays visible both
  // at the top of the page (panel below the KPI strip) and once it sticks.
  const sideRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const fit = () => {
      const el = sideRef.current;
      if (!el) return;
      const top = Math.max(68, el.getBoundingClientRect().top);
      el.style.maxHeight = `${Math.max(420, innerHeight - top - 16)}px`;
    };
    fit();
    addEventListener("scroll", fit, { passive: true });
    addEventListener("resize", fit);
    return () => {
      removeEventListener("scroll", fit);
      removeEventListener("resize", fit);
    };
  }, [e]);

  const dirty = !sameDraft(draft, toDraft(judgement));
  const savedLabel = dirty ? "有未保存的修改" : judgement ? `已保存 ${new Date(judgement.updatedAt).toLocaleString([], { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}` : "未保存";

  return (
    <>
      <div className="stickybar">
        <div className="in">
          <div>
            <button className="btn" onClick={onBack}>← 返回列表</button>
            <span className="muted">{idx >= 0 && order.length > 1 ? `${listLabel} · 第 ${idx + 1} / ${order.length} 家` : ""}</span>
          </div>
          <div className="navwrap">
            {order.length > 1 && (
              <div>
                <button className="btn" onClick={() => step(-1)}>‹ {at(-1)} <span className="kbd">K</span></button>
                <button className="btn" onClick={() => step(1)}>{at(1)} › <span className="kbd">J</span></button>
              </div>
            )}
            <ThemeToggle />
          </div>
        </div>
      </div>

      <div className="page" style={{ paddingTop: 22 }}>
        {error && <p className="error">{ticker}：{error}</p>}
        {!e && !error && <p className="muted">加载中…</p>}
        {e && (
          <>
            <Header e={e} judgement={judgement} />
            <EstimateBanner e={e} />
            {(() => {
              const v = e.analysis?.value;
              const sc = v ? scenarios(e, v, vp.saved) : null;
              const mos = v ? mosSummary(e, v, pic.latest, vp.saved) : null;
              const causes = rankedCauses(e, peers, prof.profile ?? null, pic.latest).hs;
              const openCauses = () => {
                setFolds((f) => ({ ...f, causes: true }));
                setTimeout(() => document.getElementById("fold-causes")?.scrollIntoView({ behavior: "smooth", block: "start" }), 0);
              };
              const fold = (k: string) => ({ open: !!folds[k], toggle: () => setFolds((f) => ({ ...f, [k]: !f[k] })) });
              return (
                <>
                  {e.valuation && <Kpis e={e} sc={sc} />}
                  <div className="body">
                    <div className="main">
                      <JudgementPoints e={e} ai={pic} causes={e.valuation ? causes : []} sc={sc} iv={mos?.iv ?? null} onAllCauses={openCauses} />
                      {e.valuation ? <Charts e={e} news={prof.profile?.news ?? []} /> : <div className="card empty">未通过质量层的公司不抓行情，没有股价与估值数据</div>}
                      <Evidence e={e} imputed={sc?.imputed ?? false} />
                      {e.valuation && <PeerCompare ticker={e.ticker} d={peers} />}
                      <Financials e={e} />
                      <div className="card folds">
                        <div className="folds-cap">以下默认折叠，每张只显示一行摘要 · 展开状态会记住</div>
                        {e.valuation && (() => {
                          const top = causes.filter((h) => h.rank >= 2).slice(0, 3);
                          const traps = top.filter((h) => h.kind === "trap").length;
                          return (
                            <Fold id="causes" title="原因排查" {...fold("causes")}
                              summary={top.length ? top.map((h) => h.label).join("、") : "没有特别突出的原因"}
                              tag={`${causes.filter((h) => h.level === "high").length} 项较可能 · 共 ${causes.length} 项`} tone={traps > top.length / 2 ? "bad" : ""}>
                              <CheapCauses e={e} profile={prof.profile ?? null} ai={pic.latest} peers={peers} />
                            </Fold>
                          );
                        })()}
                        <Fold id="news" title="近期新闻" {...fold("news")} {...newsLine(prof.profile)}>
                          <News profile={prof.profile} error={prof.error} />
                        </Fold>
                        <Fold id="mood" title="市场情绪" {...fold("mood")} summary={sentimentSummary(prof.profile ?? null, pic, e).text} tag={sentimentSummary(prof.profile ?? null, pic, e).tag} tone={sentimentSummary(prof.profile ?? null, pic, e).tone}>
                          <MarketSentiment e={e} profile={prof.profile} ai={ai} pic={pic} />
                        </Fold>
                        {e.analysis && (
                          <Fold id="pro" title="专业指标" {...fold("pro")} {...proLine(e.analysis)}>
                            <ProMetrics a={e.analysis} />
                          </Fold>
                        )}
                        {e.analysis?.value && mos && (
                          <Fold id="mos" title="安全边际明细" {...fold("mos")} summary={mos.text}
                            tag={mos.buy == null ? "" : mos.below ? `现价低于买入价 ${usd(mos.buy)}` : `买入价 ${usd(mos.buy)}`} tone={mos.below ? "good" : ""} est={sc?.imputed}>
                            <SafetyMargin e={e} v={e.analysis.value} ai={pic.latest} vp={vp} />
                          </Fold>
                        )}
                        <Fold id="skills" title="Skills 分析" {...fold("skills")} {...skillLine(sr)} tone={sr.pending.length ? "warn" : ""}>
                          <SkillPanel ticker={e.ticker} sr={sr} />
                        </Fold>
                        <Fold id="ai" title="AI 诊断" {...fold("ai")} {...aiLine(pic, ai.pending.filter((p) => p.kind !== "xpulse").length)}>
                          <AIBrief ai={ai} pic={pic} rules={draft.falsifyRules} onToggleRule={toggleRule}>
                            <AIDiagnosisCard ticker={e.ticker} ai={ai} />
                          </AIBrief>
                        </Fold>
                        <Fold id="ov" title="公司概况" {...fold("ov")} summary={firstSentence(prof.profile?.summary) || (prof.profile ? "没有找到业务简介" : "加载中…")} tag="">
                          <CompanyProfile profile={prof.profile} error={prof.error} defaultOpen />
                        </Fold>
                      </div>
                    </div>
              <aside className="card side" ref={sideRef}>
                <div className="sec head-sec">
                  <div className="l4">
                    <b>L4 你的判断</b>
                    <span>{savedLabel}</span>
                  </div>
                  <div className="q">是外部原因，还是公司自己变坏了？</div>
                </div>
                <div className="side-scroll">
                <div className="sec">
                  <div className="cap">系统线索 · 按类型合并</div>
                  <Signals e={e} />
                </div>
                <div className="sec">
                  <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
                    <div className="cap">结论</div>
                    <div className="decisions" role="radiogroup" aria-label="结论">
                      {verdicts.map((v, i) => (
                        <button key={v} role="radio" aria-checked={draft.verdict === v} className={`decision ${draft.verdict === v ? `on v-${v}` : ""}`} onClick={() => pick(v)}>
                          <small>{i + 1}</small>
                          {verdictLabel[v]}
                        </button>
                      ))}
                    </div>
                  </div>
                  <Field label="市场在担心什么？" placeholder="一句话写清空头逻辑" value={draft.worry} onChange={(worry) => setDraft({ ...draft, worry })} />
                  <Field label="为什么你认为是暂时的 / 外部的？" placeholder="证据，而不是感觉" value={draft.why} onChange={(why) => setDraft({ ...draft, why })} />
                  <div className="field">
                    <span className="field-row">什么事实出现就说明你错了？<small className="muted">可从 AI「行动前要核实」采纳</small></span>
                    {draft.falsifyRules.length > 0 && (
                      <div className="rtags">
                        {draft.falsifyRules.map((r, i) => (
                          <span key={i} className="rtag" title={r.note || "每次数据或股价更新时自动检查"}>
                            {ruleText(r)}
                            <button aria-label="删除" onClick={() => setDraft({ ...draft, falsifyRules: draft.falsifyRules.filter((_, k) => k !== i) })}>×</button>
                          </span>
                        ))}
                      </div>
                    )}
                    <textarea rows={2} placeholder="可观察、可证伪的条件" value={draft.falsify} onChange={(ev) => setDraft({ ...draft, falsify: ev.target.value })} />
                    {editRules ? (
                      <FalsifyRules rules={draft.falsifyRules} onChange={(falsifyRules) => setDraft({ ...draft, falsifyRules })} />
                    ) : (
                      <button className="link small" onClick={() => setEditRules(true)}>+ 添加 / 编辑量化条件（自动检查并提醒）</button>
                    )}
                  </div>
                  {draft.note && <Field label="旧笔记" value={draft.note} onChange={(note) => setDraft({ ...draft, note })} />}
                  <div className="field">
                    <span>下次复查</span>
                    <div className="review-row">
                      <input type="date" value={draft.reviewOn} onChange={(ev) => setDraft({ ...draft, reviewOn: ev.target.value })} />
                      {nextEarnings && (() => {
                        const after = new Date(Date.parse(nextEarnings) + 864e5).toLocaleDateString("sv");
                        return <button className={`quick ${draft.reviewOn === after ? "on" : ""}`} onClick={() => setDraft({ ...draft, reviewOn: after })} title={`下次财报预计 ${nextEarnings}，复查定在次日`}>财报后 {md(after)}</button>;
                      })()}
                    </div>
                  </div>
                  <label className="watchcheck">
                    <input type="checkbox" checked={watching} onChange={async (ev) => {
                      if (ev.target.checked) await alertsApi.watch(e.cik, e.ticker);
                      else await alertsApi.unwatch(e.cik);
                      setWatching(ev.target.checked);
                    }} />
                    <span>加入观察列表，复查日与证伪触发时邮件提醒{watching && <> · <a href="#/watch">提醒设置</a></>}</span>
                  </label>
                  {msg && <div className="msg">{msg}</div>}
                </div>
                </div>
                <div className="actions">
                  <button className="btn" disabled={saving} onClick={save}>保存</button>
                  <button className="btn primary" disabled={saving || order.length < 2} title="⌘/Ctrl + Enter" onClick={async () => (await saveRef.current()) && step(1)}>
                    保存并看下一家 →
                  </button>
                </div>
                <p className="snapnote">
                  保存时冻结当时股价{e.valuation ? ` $${e.valuation.price.toFixed(2)}` : ""}、P/FCF、TTM 利润率、AI 结论与证伪条件；复查日自动对比最新数据。
                  {judgement?.snapshot?.metrics?.price ? ` 上次保存：$${judgement.snapshot.metrics.price.toFixed(2)}${judgement.snapshot.ai ? ` · AI ${stanceLabel[judgement.snapshot.ai]}` : ""}` : ""}
                </p>
              </aside>
                  </div>
                </>
              );
            })()}
          </>
        )}
      </div>
    </>
  );
}

function Field({ label, placeholder, value, onChange }: { label: string; placeholder?: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="field">
      {label}
      <textarea rows={2} placeholder={placeholder} value={value} onChange={(ev) => onChange(ev.target.value)} />
    </label>
  );
}

function Header({ e, judgement }: { e: Evaluation; judgement: Judgement | null }) {
  const pill = (n: number, label: string) => {
    let state = e.stage >= n ? "pass" : e.stage === n - 1 ? "fail" : "skip";
    const warn = state === "pass" && e.checks.some((c) => c.layer === n && c.warn);
    if (warn) state = "warn";
    return (
      <div key={n} className={`pill ${state}`} title={warn ? e.checks.filter((c) => c.layer === n && c.warn).map((c) => `${c.name} ${c.value}`).join("\n") : undefined}>
        <span>{state === "pass" ? "✓" : state === "fail" ? "✕" : state === "warn" ? "!" : "–"}</span>
        <span>{label}{warn ? " · 趋势预警" : ""}</span>
      </div>
    );
  };
  return (
    <div className="dhead">
      <div>
        <div className="title">
          <b>{e.ticker}</b>
          {e.nameZh && <span className="zh">{e.nameZh}</span>}
          <span>{e.name}</span>
        </div>
        <div className="meta">
          {[e.sector, e.industry].filter(Boolean).join(" / ") || "行业未知"} · 报告币种 {e.currency}
          {e.fx ? `，估值统一换算为 USD（1 USD = ${e.fx.toFixed(2)} ${e.currency}，筛选时的最新汇率；历史 P/FCF 按当期汇率；年度财务保留原币）` : ""} · {e.taxonomy === "ifrs-full" ? "IFRS" : "US GAAP"} · 财务数据{" "}
          {e.ttm?.length ? `截至 ${e.ttm[e.ttm.length - 1].end}（季报）` : `截至 ${e.years[e.years.length - 1]?.end}（年报）`} ·{" "}
          <a href={`https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=${e.cik}&type=10-K`} target="_blank" rel="noreferrer">
            SEC 申报
          </a>
        </div>
      </div>
      <div className="pills">
        {pill(1, "L1 质量")}
        {pill(2, "L2 错位")}
        {pill(3, "L3 凸性")}
        {judgement?.verdict ? (
          <div className={`pill v-${judgement.verdict}`}><span>●</span><span>L4 {verdictLabel[judgement.verdict]}</span></div>
        ) : (
          <div className="pill l4"><span>○</span><span>L4 待你判断</span></div>
        )}
      </div>
    </div>
  );
}

function Charts({ e, news }: { e: Evaluation; news: NewsItem[] }) {
  const v = e.valuation!;
  const series = v.series;
  const last = series[series.length - 1]?.date ?? "";
  const fiveYearsAgo = `${Number(last.slice(0, 4)) - 5}${last.slice(4)}`;
  const from = Math.max(0, series.findIndex((p) => p.date >= fiveYearsAgo));
  // News whose first trading session moved the stock ≥ 5%, grouped by month.
  const byMonth = new Map<string, NewsItem[]>();
  for (const n of news) {
    if (n.move == null || Math.abs(n.move) < 0.05 || !n.moveDay) continue;
    const m = n.moveDay.slice(0, 7);
    byMonth.set(m, [...(byMonth.get(m) ?? []), n]);
  }
  const marks = [...byMonth].flatMap(([m, ns]) => {
    const i = series.findIndex((p) => p.date.startsWith(m));
    if (i < 0) return [];
    const big = ns.reduce((a, b) => (Math.abs(b.move!) > Math.abs(a.move!) ? b : a));
    return [{ index: i, label: pct(big.move, 1), move: Math.abs(big.move!), down: big.move! < 0, title: ns.map((n) => `${n.moveDay} ${pct(n.move, 1)}  ${n.title}`).join("\n") }];
  });
  // Only the biggest move gets a text label; the others would collide at the right edge.
  const labeled = marks.map(({ move: _m, ...m }) => ({ ...m, label: "" })); // dots only; hover for the headline
  return (
    <div className="card charts">
      <LineChart
        title="股价"
        note="月度 · 拆股复权 · 美元 · 点 = 涨跌 ≥5% 的新闻"
        width={460}
        height={190}
        data={series.map((p) => ({ date: p.date, value: p.price }))}
        format={(n) => `$${n >= 100 ? n.toFixed(0) : n.toFixed(n >= 10 ? 0 : 1)}`}
        refLines={[{ value: v.high5y, label: "5年高", fromIndex: from }]}
        marks={labeled}
      />
      <LineChart
        title="P/FCF 估值历史"
        note="对数坐标 · 市值 ÷ 近3年平均 FCF"
        width={460}
        height={190}
        log
        data={series.map((p) => ({ date: p.date, value: p.multiple != null && p.multiple > 0 ? p.multiple : undefined }))}
        format={(n) => `${n >= 10 ? n.toFixed(0) : n.toFixed(1)}x`}
        refLines={[...(v.median != null ? [{ value: v.median, label: "中位" }] : []), ...(v.p10 != null ? [{ value: v.p10, label: "P10" }] : [])]}
      />
    </div>
  );
}

type RowState = "p" | "w" | "f" | "n";
const icons: Record<RowState, string> = { p: "✓", w: "!", f: "✕", n: "–" };

function Evidence({ e, imputed }: { e: Evaluation; imputed: boolean }) {
  const [open, setOpen] = useState<Record<number, boolean>>({});
  return (
    <div className="card">
      <div className="card-head">
        <strong>筛选证据</strong>
        <span>趋势规则：毛利率、收入增速、营业利润率、FCF 利润率对比 5 年历史（只预警，不淘汰）· 折叠时只显示预警与未通过项</span>
      </div>
      {[1, 2, 3].map((n) => {
        const checks = e.checks.filter((c) => c.layer === n);
        const reached = e.stage >= n - 1;
        const state = (c: Check): RowState => (!reached ? "n" : !c.pass ? "f" : c.warn || c.near ? "w" : "p");
        const rows = checks.map((c) => ({ c, s: state(c) }));
        const fails = rows.filter((r) => r.s === "f").length;
        const trendWarns = rows.filter((r) => r.c.warn && r.s === "w").length;
        const warns = rows.filter((r) => r.s === "w").length - trendWarns;
        const isOpen = !!open[n];
        const shown = isOpen ? rows : rows.filter((r) => r.s === "f" || r.c.warn);
        const [summary, tone] = !checks.length
          ? ["未评估", "skip"]
          : !reached
            ? ["前一层未通过 · 仅供参考", "skip"]
            : fails
              ? [`${fails} 项未通过`, "fail"]
              : [`${checks.length}/${checks.length} 通过`, "pass"];
        return (
          <div key={n} className="group">
            <button aria-expanded={isOpen} onClick={() => setOpen((o) => ({ ...o, [n]: !o[n] }))} disabled={!checks.length}>
              <span className="chev">{isOpen ? "▾" : "▸"}</span>
              <span className="key">L{n}</span>
              <span className="gtitle">{layerTitles[n]}</span>
              {n === 3 && imputed && <em className="est" title="凸性用到的 FCF 含估算资本开支">估</em>}
              {trendWarns > 0 && reached && <span className="chip warn">{trendWarns} 项趋势预警</span>}
              {warns > 0 && reached && <span className="chip neutral">{warns} 项临界</span>}
              <span className={`sum ${tone}`}>{summary}</span>
            </button>
            {shown.length > 0 && (
              <div className="rows">
                {shown.map(({ c, s }) => (
                  <div key={c.name} className={`crow ${s}`}>
                    <span className="ic" aria-label={{ p: "通过", w: "临界", f: "未通过", n: "未计入" }[s]}>{icons[s]}</span>
                    <span className="lbl">{c.layer === 3 && c.name === "上行空间" && e.valuation?.target != null ? `上行空间（回到 ${e.valuation.target.toFixed(0)}x）` : c.name}{c.name.startsWith("趋势") && <small className="newrule">趋势规则</small>}</span>
                    <span className="val">{c.value}</span>
                    <span className="th">{c.rule}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function Signals({ e }: { e: Evaluation }) {
  const hints = e.hints ?? [];
  if (!hints.length) {
    const stop = e.checks.find((c) => !c.pass);
    return (
      <div className="signal">
        <span className="chip neutral">{e.stage >= 3 ? "无线索" : "未入选"}</span>
        <span>{e.stage >= 3 ? "系统没有发现明显线索，需要自己读财报和新闻" : `卡在 L${e.stage + 1} · ${stop?.name ?? ""}，系统未生成线索`}</span>
      </div>
    );
  }
  // One chip per kind; the texts under it.
  const kinds = [...new Set(hints.map((h) => h.kind))];
  return (
    <>
      {kinds.map((k) => (
        <div key={k} className="signal">
          <span className={`chip ${k}`}>{hintLabel[k]}</span>
          <span className="signal-texts">{hints.filter((h) => h.kind === k).map((h, i) => <span key={i}>{h.text}</span>)}</span>
        </div>
      ))}
    </>
  );
}

function Financials({ e }: { e: Evaluation }) {
  // Latest TTM (if a 10-Q is newer than the annual report) on top, then fiscal years.
  const [all, setAll] = useState(false);
  const latestTTM = e.ttm?.length ? e.ttm[e.ttm.length - 1] : null;
  const fys = [...e.years].reverse();
  const ys = [...(latestTTM ? [latestTTM] : []), ...(all ? fys : fys.slice(0, 6))];
  const m = (v?: number) => money(v, e.currency);
  const ratio = (a?: number, b?: number) => (a == null || b == null || b <= 0 ? "—" : pct(a / b, 1));
  const latestWeak = (e.valuation?.latestFcfVsAvg ?? 1) < 0.7;
  // Lenders and marketplaces with credit books push loan/deposit flows through operating cash flow.
  const fcfMarginOdd = ys.slice(0, 3).some((y) => y.fcf != null && y.revenue != null && y.revenue > 0 && y.fcf / y.revenue > 0.3);
  return (
    <div className="card" id="fin">
      <div className="card-head">
        <strong>年度财务</strong>
        <span>原币 {e.currency} · 来自 SEC XBRL{latestTTM ? " · TTM = 截至最新季报的滚动 12 个月；利润率黄标 = 低于 5 年中位" : " · 无季报，仅年报"}{ys.some((y) => y.capexImputed) ? " · 「估」= 未标注资本开支，按历史资本开支/收入估算" : ""}</span>
      </div>
      <div className="fin">
        <div className="inner">
          <div className="frow h">
            <span>财年末</span><span>收入</span><span>毛利率</span><span>营业利润率</span><span>经营现金流</span><span>资本开支</span>
            <span className="strong">FCF</span>
            <span>
              {fcfMarginOdd && (
                <span className="chip warn" title="近 3 年有 FCF 利润率 > 30%。含贷款 / 存款业务（如电商的金融科技板块）时，经营现金流会混入贷款与存款变动，FCF 可能被高估">
                  数据待核
                </span>
              )}{" "}
              FCF 利润率
            </span><span>现金+短投</span><span>有息负债</span>
          </div>
          {ys.map((y, i) => {
            const flag = (y.fcf != null && y.fcf < 0) || (i === 0 && latestWeak); // FCF cell only
            const F = ({ children }: { children: string }) => (flag ? <span className="flag">{children}</span> : <>{children}</>);
            return (
              <div key={`${y.ttm ? "ttm" : "fy"}-${y.end}`} className={`frow ${y.ttm ? "ttm-row" : ""}`}>
                <span>{y.ttm ? <b className="ttm" title={y.source === "yahoo" ? "滚动 12 个月：Yahoo 最近 4 个季度相加（该公司不提交 10-Q，季度业绩不在 SEC XBRL 里）" : "滚动 12 个月：最新年报 + 今年至今季报累计 − 去年同期累计"}>TTM {y.end.slice(0, 7)}{y.source === "yahoo" && <small className="src-tag">Yahoo</small>}</b> : y.end}</span>
                <span style={{ color: "var(--ink)" }}>{m(y.revenue)}</span>
                <span><span className={y.ttm && y.revenue != null && y.revenue > 0 && y.grossProfit != null && e.quality.grossMargin != null && y.grossProfit / y.revenue < e.quality.grossMargin ? "flag" : ""}>{ratio(y.grossProfit, y.revenue)}</span></span>
                <span><span className={y.ttm && y.revenue != null && y.revenue > 0 && y.opIncome != null && e.quality.opMargin != null && y.opIncome / y.revenue < e.quality.opMargin ? "flag" : ""}>{ratio(y.opIncome, y.revenue)}</span></span>
                <span>{m(y.ocf)}</span>
                <span>{y.capexImputed ? `估 ${m(y.ocf! - y.fcf!)}` : m(y.capex)}</span>
                <span className="strong"><F>{m(y.fcf)}</F></span>
                <span>{ratio(y.fcf, y.revenue)}</span>
                <span>{m(y.cash)}</span>
                <span>{m(y.debt)}</span>
              </div>
            );
          })}
        </div>
      </div>
      {fys.length > 6 && <button className="link small fin-more" onClick={() => setAll(!all)}>{all ? "只看近 6 年" : `显示全部 ${fys.length} 个财年`}</button>}
      {(e.notes ?? []).length > 0 && (
        <ul className="notes">
          {e.notes!.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ---------- folded cards ----------

const FOLD_KEY = "detail-folds";
function useFolds() {
  const [f, setF] = useState<Record<string, boolean>>(() => {
    try {
      return JSON.parse(localStorage.getItem(FOLD_KEY) ?? "{}");
    } catch {
      return {};
    }
  });
  useEffect(() => localStorage.setItem(FOLD_KEY, JSON.stringify(f)), [f]);
  return [f, setF] as const;
}

function Fold({ id, title, summary, tag, tone = "", est, open, toggle, children }: {
  id: string; title: string; summary: string; tag: string; tone?: string; est?: boolean; open: boolean; toggle: () => void; children: React.ReactNode;
}) {
  return (
    <div className={`fold ${open ? "open" : ""}`} id={`fold-${id}`}>
      <button className="fold-head" aria-expanded={open} onClick={toggle}>
        <span className="chev">{open ? "▾" : "▸"}</span>
        <b>{title}</b>
        <span className="fold-sum" title={summary}>{summary}</span>
        <span className={`fold-tag ${tone}`}>{est && <em className="est">估</em>}{tag}</span>
      </button>
      {open && <div className="fold-body">{children}</div>}
    </div>
  );
}

const firstSentence = (t?: string) => (t ?? "").split(/(?<=[。.!?！？])\s*/)[0].slice(0, 120);

function newsLine(p: Profile | null) {
  const ns = p?.news ?? [];
  if (!p) return { summary: "加载中…", tag: "" };
  if (!ns.length) return { summary: "没有近期新闻", tag: "" };
  const moved = ns.filter((n) => n.move != null && Math.abs(n.move) >= 0.03);
  const big = moved.reduce<typeof ns[number] | null>((a, b) => (!a || Math.abs(b.move!) > Math.abs(a.move!) ? b : a), null);
  return {
    summary: big ? `${big.title}（${pct(big.move, 1)}）` : ns[0].title,
    tag: `${ns.length} 条${moved.length ? ` · ${moved.length} 条有价格反应` : ""}`,
  };
}

function proLine(a: Analysis) {
  const roic = a.groups.flatMap((g) => g.metrics).find((m) => m.name === "ROIC");
  const accrual = a.groups.flatMap((g) => g.metrics).find((m) => m.name.includes("FCF / 净利润") || m.name.includes("现金转化"));
  const zone = { safe: "安全", grey: "灰区", distress: "危险" } as const;
  const bits = [
    a.fscore ? `Piotroski ${a.fscore.value}/${a.fscore.max}` : "",
    a.zscore ? `Altman Z ${a.zscore.value.toFixed(1)}（${zone[a.zscore.zone]}）` : "",
    accrual?.value != null ? `${accrual.name} ${pct(accrual.value)}` : "",
    roic?.value != null ? `ROIC ${pct(roic.value)}` : "",
  ].filter(Boolean);
  const f = a.fscore?.value;
  const [tag, tone] = f == null ? ["", ""] : f >= 7 ? ["基本面在改善", "good"] : f <= 3 ? ["基本面在恶化", "bad"] : ["中性", ""];
  return { summary: bits.join(" · ") || "数据不足", tag, tone };
}

function aiLine(pic: AIPicture, running: number) {
  const head = pic.rollup ?? pic.latest[0];
  if (!head) return { summary: running ? "诊断中…" : "还没有 AI 诊断", tag: running ? "诊断中" : "", tone: "" };
  const st = pic.stance;
  return {
    summary: `${pic.latest.length ? `${pic.agree}/${pic.latest.length} 模型一致 · ` : ""}${head.result.summary}`,
    tag: `${running ? "诊断中 · " : ""}${st ? stanceLabel[st] : "—"}`,
    tone: st === "mispriced" ? "good" : st === "trap" ? "bad" : st === "unclear" ? "warn" : "",
  };
}

// FCF built on estimated capex: say so once, at the top, and mark every number that uses it.
function EstimateBanner({ e }: { e: Evaluation }) {
  const ys = [...e.years.slice(-5), ...(e.ttm ?? []).slice(-1)].filter((y) => y.capexImputed);
  if (!ys.length) return null;
  const label = [...new Set(ys.map((y) => (y.ttm ? `TTM ${y.end.slice(0, 7)}` : y.end.slice(0, 4))))].join("、");
  return (
    <div className="est-banner">
      <em className="est">估</em>
      <span>{label} 的资本开支未在 XBRL 中单列，按该公司历史资本开支 / 收入中位数估算。凡依赖 FCF 的数字（P/FCF、反向 DCF、内在价值、凸性）均带「估」标记。</span>
      <a href="#fin" onClick={(ev) => { ev.preventDefault(); document.getElementById("fin")?.scrollIntoView({ behavior: "smooth" }); }}>看年度财务</a>
    </div>
  );
}
