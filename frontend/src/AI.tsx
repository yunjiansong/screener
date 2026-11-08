import { useCallback, useEffect, useState } from "react";
import {
  AIKind,
  AIBrief,
  AISettings,
  AIStatus,
  AIVerdict,
  DiagnosisResult,
  DiagnosisRow,
  EngineName,
  PendingJob,
  RollupResult,
  Stance,
  engineOrder,
  engineShort,
  manualOnly,
  XPulseResult,
  aiApi,
  stanceChip,
  stanceLabel,
  triggerLabel,
} from "./api";

export const when = (at: string) => new Date(at).toLocaleString(undefined, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
const day = (at: string) => new Date(at).toLocaleDateString();
const secs = (ms: number) => (ms >= 60000 ? `${(ms / 60000).toFixed(1)} 分钟` : `${Math.round(ms / 1000)} 秒`);

function Dots({ n, max = 5 }: { n: number; max?: number }) {
  return (
    <span className="dots" aria-label={`信心 ${n} / ${max}`} title={`信心 ${n} / ${max}`}>
      {Array.from({ length: max }, (_, i) => <i key={i} className={i < n ? "on" : ""} />)}
    </span>
  );
}

export function List({ title, items, tone }: { title: string; items?: string[]; tone?: "good" | "bad" | "warn" }) {
  if (!items?.length) return null;
  return (
    <div className={`ai-list ${tone ?? ""}`}>
      <h4>{title}</h4>
      <ul>{items.map((t, i) => <li key={i}>{t}</li>)}</ul>
    </div>
  );
}

function Meta({ d }: { d: DiagnosisRow }) {
  return (
    <div className="ai-meta">
      {when(d.at)} · {triggerLabel(d.trigger)} · {d.engine}
      {d.model ? ` / ${d.model}` : ""} · 用时 {secs(d.durationMs)}
      {d.price ? ` · 当时股价 $${d.price.toFixed(2)}` : ""}
      {d.basis ? ` · ${d.basis}` : ""}
    </div>
  );
}

// Shared state for a company's AI history: loads, polls while something is queued, requests runs.
export function useDiagnoses(ticker: string) {
  const [items, setItems] = useState<DiagnosisRow[] | null>(null);
  const [pending, setPending] = useState<PendingJob[]>([]);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    aiApi
      .list(ticker)
      .then((r) => {
        setItems(r.items);
        setPending(r.pending ?? []);
      })
      .catch((e) => setError(String(e.message ?? e)));
  }, [ticker]);
  useEffect(load, [load]);
  useEffect(() => {
    if (!pending.length) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [pending.length, load]);
  const request = async (kind: AIKind, engine?: EngineName) => {
    setError("");
    try {
      await aiApi.request(ticker, kind, engine);
      setPending((p) => [...p, { kind, engine: engine ?? "codex", active: false }]);
    } catch (e) {
      setError(String((e as Error).message));
    }
  };
  const remove = async (id: number) => {
    await aiApi.remove(id);
    load();
  };
  const has = (kind: AIKind, engine?: EngineName) => pending.some((p) => p.kind === kind && (!engine || p.engine === engine));
  return { items, pending, error, request, remove, has };
}

export function useStatus() {
  const [status, setStatus] = useState<AIStatus | null>(null);
  useEffect(() => {
    aiApi.status().then(setStatus).catch(() => setStatus(null));
  }, []);
  return status;
}

function Running({ jobs }: { jobs: PendingJob[] }) {
  if (!jobs.length) return null;
  const what = { diagnosis: "诊断", rollup: "汇总", xpulse: "搜索 X" } as const;
  return (
    <div className="ai-running">
      <span className="spinner" />
      <span>
        {jobs.map((j, i) => (
          <span key={i} className="ai-job">{engineShort[j.engine]} {what[j.kind]}{j.active ? "中" : "排队中"}</span>
        ))}
        <span className="muted">· 每项通常 1–5 分钟，可以离开本页，结果会自动保存</span>
      </span>
    </div>
  );
}

function DiagnosisView({ d }: { d: DiagnosisRow<DiagnosisResult> }) {
  const r = d.result;
  return (
    <div className="ai-view">
      <div className="ai-verdict">
        <span className={`chip ${stanceChip[r.stance] ?? "neutral"}`}>{stanceLabel[r.stance] ?? r.stance}</span>
        <Dots n={r.confidence} />
        <b>{r.summary}</b>
      </div>
      <Meta d={d} />
      <div className="ai-two">
        <div className="ai-para"><h4>市场在担心什么</h4><p>{r.worry}</p></div>
        <div className="ai-para"><h4>是暂时 / 外部的吗</h4><p>{r.temporary}</p></div>
      </div>
      <div className="ai-two">
        <List title="看多理由" items={r.bull} tone="good" />
        <List title="看空理由" items={r.bear} tone="bad" />
      </div>
      <div className="ai-two">
        <List title="证伪条件" items={r.falsify} />
        <List title="接下来盯什么" items={r.watch} />
      </div>
      {r.changeVsLast && r.changeVsLast !== "首次诊断" && <div className="ai-para"><h4>与上次相比</h4><p>{r.changeVsLast}</p></div>}
      <List title="数据疑点（建议人工核对）" items={r.dataIssues} tone="warn" />
    </div>
  );
}

function RollupView({ d }: { d: DiagnosisRow<RollupResult> }) {
  const r = d.result;
  return (
    <div className="ai-view rollup">
      <div className="ai-verdict">
        <span className="ai-tag">历次汇总</span>
        <span className={`chip ${stanceChip[r.stance] ?? "neutral"}`}>{stanceLabel[r.stance] ?? r.stance}</span>
        <Dots n={r.confidence} />
        <b>{r.summary}</b>
      </div>
      <Meta d={d} />
      <div className="ai-para"><h4>判断如何变化</h4><p>{r.trend}</p></div>
      <div className="ai-two">
        <List title="始终一致的判断" items={r.stable} />
        <List title="发生变化的地方" items={r.shifts} />
      </div>
      <div className="ai-two">
        <List title="留给你判断的问题" items={r.open} tone="warn" />
        <List title="行动前要核实" items={r.checklist} />
      </div>
    </div>
  );
}

function EnginePicker({ status, value, onChange, exclude }: { status: AIStatus | null; value: EngineName; onChange: (e: EngineName) => void; exclude?: EngineName[] }) {
  const engines = (status?.engines ?? []).filter((e) => !exclude?.includes(e.name));
  return (
    <select className="engine" value={value} onChange={(e) => onChange(e.target.value as EngineName)} aria-label="AI 引擎">
      {engines.length === 0 && <option value={value}>{value}</option>}
      {engines.map((e) => (
        <option key={e.name} value={e.name} disabled={!e.available}>
          {e.label}{e.available ? "" : "（不可用）"}{manualOnly.includes(e.name) ? " · 仅手动" : ""}{status?.settings.engine === e.name ? " · 默认" : ""}
        </option>
      ))}
    </select>
  );
}

export function AIDiagnosisCard({ ticker, ai }: { ticker: string; ai: ReturnType<typeof useDiagnoses> }) {
  const { items, pending, error, request, remove, has } = ai;
  const status = useStatus();
  const [engine, setEngine] = useState<EngineName>("codex");
  const [tab, setTab] = useState<EngineName | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [copyMsg, setCopyMsg] = useState("");
  useEffect(() => {
    if (status) setEngine(status.settings.engine);
  }, [status]);
  useEffect(() => {
    setSelected(null);
    setTab(null);
  }, [ticker]);

  const diags = (items ?? []).filter((d) => d.kind === "diagnosis") as DiagnosisRow<DiagnosisResult>[];
  const ok = diags.filter((d) => !d.error);
  const rollup = (items ?? []).find((d) => d.kind === "rollup" && !d.error) as DiagnosisRow<RollupResult> | undefined;
  const lastErr = (items ?? []).find((d) => d.kind !== "xpulse");
  // Latest successful diagnosis per engine, in the fixed engine order.
  const latest = engineOrder.map((e) => ok.find((d) => d.engine === e)).filter(Boolean) as DiagnosisRow<DiagnosisResult>[];
  const picked = selected != null ? diags.find((d) => d.id === selected) : undefined;
  const curTab = picked?.engine ?? tab ?? latest[0]?.engine;
  const shown = picked && !picked.error ? picked : latest.find((d) => d.engine === curTab);
  const engineOK = status?.engines.find((e) => e.name === engine)?.available ?? true;
  const newerThanRollup = rollup ? ok.filter((d) => d.at > rollup.at).length : 0;
  const jobs = pending.filter((p) => p.kind !== "xpulse");

  const brief = newerThanRollup === 0 && rollup ? rollup : latest.reduce<DiagnosisRow<DiagnosisResult> | undefined>((a, b) => !a || b.at > a.at ? b : a, undefined);
  const tally = latest.reduce<Record<string, number>>((m, d) => ({ ...m, [d.result.stance]: (m[d.result.stance] ?? 0) + 1 }), {});

  const copyPrompt = async () => {
    setCopyMsg("正在整理数据…");
    try {
      const text = await aiApi.prompt(ticker);
      await navigator.clipboard.writeText(text);
      setCopyMsg(`已复制（${Math.round(text.length / 100) / 10}k 字），可粘贴到 ChatGPT / Gemini 等网页`);
    } catch (e) {
      setCopyMsg("复制失败：" + String((e as Error).message));
    }
  };

  return (
    <div className="card">
      <div className="card-head">
        <strong>AI 诊断</strong>
        <span>多个模型给第二意见，不替你下结论 · 每次结果都会保存</span>
      </div>
      <div className="ai-bar">
        <EnginePicker status={status} value={engine} onChange={setEngine} exclude={["grok"]} />
        <button className="btn primary" disabled={has("diagnosis", engine) || !engineOK} onClick={() => request("diagnosis", engine)}>
          {has("diagnosis", engine) ? "诊断中…" : ok.some((d) => d.engine === engine) ? "用它重新诊断" : "用它诊断"}
        </button>
        <button className="btn" disabled={ok.length < 2 || has("rollup")} onClick={() => request("rollup", engine)}
          title={ok.length < 2 ? "至少 2 次成功的诊断才能汇总" : "让 AI 对比各模型与历次诊断，给出综合判断"}>
          {has("rollup") ? "汇总中…" : `综合各模型（${ok.length}）`}
        </button>
        <button className="btn" onClick={copyPrompt} title="不调用本机 AI，把数据整理成提示词复制到剪贴板">复制提示词</button>
        {copyMsg && <span className="small muted">{copyMsg}</span>}
      </div>
      {error && <p className="error">{error}</p>}
      <Running jobs={jobs} />
      {lastErr?.error && !jobs.length && <p className="error small ai-err">最近一次{lastErr.kind === "rollup" ? "汇总" : "诊断"}失败（{engineShort[lastErr.engine]}，{when(lastErr.at)}）：{lastErr.error}</p>}
      {items && !ok.length && !jobs.length && (
        <p className="profile-loading">
          还没有诊断。选一个模型点「用它诊断」：筛选证据、财务、专业指标、新闻、关注度、X 舆情和你的判断会一起交给它。
          可以换几个模型各诊断一次，再点「综合各模型」看共识和分歧。
        </p>
      )}

      {brief && <section className="ai-sec">
        <div className="ai-verdict"><span className={`chip ${stanceChip[brief.result.stance] ?? "neutral"}`}>{stanceLabel[brief.result.stance]}</span><b>{brief.result.summary}</b></div>
        <p className="small muted">{latest.length} 个模型 · {Object.entries(tally).map(([k, n]) => `${stanceLabel[k as Stance]} ${n}`).join(" / ")}{latest.length > 1 ? Object.keys(tally).length === 1 ? " · 结论一致" : " · 存在分歧" : ""}</p>
        <Meta d={brief} />
        {newerThanRollup > 0 && <p className="small muted">汇总后有新诊断；当前展示最新模型意见，可重新综合。</p>}
        <div className="ai-two">
          <List title="留给你判断的问题" items={(brief.kind === "rollup" ? (brief.result as RollupResult).open : (brief.result as DiagnosisResult).watch)?.slice(0, 4)} tone="warn" />
          <List title="行动前要核实" items={(brief.kind === "rollup" ? ((brief.result as RollupResult).checklist ?? []) : [...((brief.result as DiagnosisResult).falsify ?? []), ...((brief.result as DiagnosisResult).dataIssues ?? [])]).slice(0, 5)} />
        </div>
      </section>}
      <details className="ai-details"><summary>各模型详细分析与诊断记录（{diags.length} 次）</summary>
      {rollup && (
        <section className="ai-sec">
          <h3 className="ai-h">① 综合判断{newerThanRollup > 0 && <span className="chip warn">之后又有 {newerThanRollup} 次新诊断，可重新综合</span>}</h3>
          <RollupView d={rollup} />
        </section>
      )}

      {latest.length > 0 && (
        <section className="ai-sec">
          <h3 className="ai-h">
            {rollup ? "②" : "①"} 各模型结论
            <span className="muted small">
              {latest.length} 个模型：{Object.entries(tally).map(([k, n]) => `${stanceLabel[k as keyof typeof stanceLabel] ?? k} ${n}`).join(" · ")}
              {Object.keys(tally).length === 1 && latest.length > 1 ? "（一致）" : Object.keys(tally).length > 1 ? "（有分歧）" : ""}
            </span>
          </h3>
          <div className="ai-compare">
            {latest.map((d) => (
              <button key={d.engine} className={`ai-crow ${curTab === d.engine ? "sel" : ""}`} onClick={() => { setTab(d.engine); setSelected(null); }}>
                <span className="ai-eng">{engineShort[d.engine]}<small>{d.model || "默认模型"}</small></span>
                <span className={`chip ${stanceChip[d.result.stance] ?? "neutral"}`}>{stanceLabel[d.result.stance] ?? d.result.stance}</span>
                <Dots n={d.result.confidence} />
                <span className="ai-sum">{d.result.summary}</span>
                <span className="nmeta">{day(d.at)}</span>
              </button>
            ))}
          </div>
        </section>
      )}

      {shown && (
        <section className="ai-sec">
          <h3 className="ai-h">
            {rollup ? "③" : "②"} 详细分析
            <span className="ai-tabs" role="tablist">
              {latest.map((d) => (
                <button key={d.engine} role="tab" aria-selected={curTab === d.engine && !picked} className={curTab === d.engine && !picked ? "on" : ""}
                  onClick={() => { setTab(d.engine); setSelected(null); }}>
                  {engineShort[d.engine]}
                </button>
              ))}
              {picked && <span className="chip neutral">历史：{when(picked.at)}</span>}
            </span>
          </h3>
          <DiagnosisView d={shown} />
        </section>
      )}

      {diags.length > 0 && (
        <details className="ai-history">
          <summary>全部诊断记录（{diags.length}）<span className="muted small"> · 点一行查看那次的完整结果</span></summary>
          <div className="ai-trail" aria-label="结论走势">
            {[...ok].reverse().map((d) => (
              <span key={d.id} className={`chip ${stanceChip[d.result.stance] ?? "neutral"}`} title={`${when(d.at)} ${engineShort[d.engine]}：${d.result.summary}`}>
                {day(d.at)} {engineShort[d.engine]} {stanceLabel[d.result.stance]}
              </span>
            ))}
          </div>
          {diags.map((d) => (
            <div key={d.id} className={`ai-hrow ${shown?.id === d.id ? "sel" : ""}`} onClick={() => !d.error && setSelected(d.id)}>
              <span className="nmeta">{when(d.at)}</span>
              <span className="nmeta">{triggerLabel(d.trigger)}</span>
              <span className="nmeta">{engineShort[d.engine]}</span>
              {d.error ? (
                <span className="bad-ink small">失败：{d.error.slice(0, 120)}</span>
              ) : (
                <span className="small">
                  <span className={`chip ${stanceChip[d.result.stance] ?? "neutral"}`}>{stanceLabel[d.result.stance]}</span> {d.result.summary}
                </span>
              )}
              <span className="nmeta">{d.price ? `$${d.price.toFixed(2)}` : ""}</span>
              <button className="link danger" onClick={(e) => { e.stopPropagation(); if (confirm("删除这条诊断记录？")) remove(d.id); }}>删除</button>
            </div>
          ))}
        </details>
      )}
      </details>
    </div>
  );
}

const sentimentLabel = { bullish: "偏多", bearish: "偏空", mixed: "多空分歧", neutral: "中性" } as const;

export function XPulseCard({ ai }: { ai: ReturnType<typeof useDiagnoses> }) {
  const { items, error, request, has } = ai;
  const status = useStatus();
  const grok = status?.engines.find((e) => e.name === "grok");
  const pulses = (items ?? []).filter((d) => d.kind === "xpulse");
  const ok = pulses.filter((d) => !d.error) as DiagnosisRow<XPulseResult>[];
  const [sel, setSel] = useState<number | null>(null);
  const d = ok.find((p) => p.id === sel) ?? ok[0];
  const last = pulses[0];
  const r = d?.result;
  return (
    <div className="card">
      <div className="card-head">
        <strong>X 舆情</strong>
        <span>{has("xpulse") ? <><i className="spinner" /> 搜索中 / 排队中</> : "由 Grok 搜索 X 近 30 天的讨论 · 观点不是事实"}</span>
      </div>
      <div className="ai-bar">
        <button className="btn primary" disabled={!grok?.available || has("xpulse")} onClick={() => request("xpulse", "grok")}>
          {has("xpulse") ? "搜索中…" : ok.length ? "重新搜索" : "搜索 X 舆情"}
        </button>
        {ok.length > 1 && (
          <select value={d?.id} onChange={(e) => setSel(Number(e.target.value))} aria-label="历史记录">
            {ok.map((p) => <option key={p.id} value={p.id}>{day(p.at)} · {sentimentLabel[p.result.sentiment]} {p.result.sentimentScore > 0 ? "+" : ""}{p.result.sentimentScore}</option>)}
          </select>
        )}
        {grok && !grok.available && <span className="small muted">{grok.detail.includes("未登录") ? <>Grok 还没登录：在终端运行 <code>grok login</code> 后刷新本页。</> : grok.detail}</span>}
      </div>
      {error && <p className="error">{error}</p>}
      {last?.error && !has("xpulse") && <p className="error small">最近一次搜索失败（{when(last.at)}）：{last.error}</p>}
      {items && !ok.length && !has("xpulse") && (
        <p className="profile-loading">
          X 没有对个人开放的免费接口，这里借助 Grok 自带的 X 搜索。系统会用帖子 ID 里的时间戳核对每条链接，对不上的标为「链接可疑」。结果会保存，也会作为「市场情绪」交给 AI 诊断参考（10 天内的有效）。
        </p>
      )}
      {d && r && (
        <div className="ai-view">
          <div className="ai-verdict">
            <span className={`chip ${r.sentimentScore >= 2 ? "positive" : r.sentimentScore <= -2 ? "internal" : "neutral"}`}>{sentimentLabel[r.sentiment] ?? r.sentiment}</span>
            <span className="sent-bar" title={`情绪分 ${r.sentimentScore}（-5 … +5）`}><i style={{ left: `${((r.sentimentScore + 5) / 10) * 100}%` }} /></span>
            <b>{r.summary}</b>
          </div>
          <Meta d={d} />
          <details className="pulse-details"><summary>多空观点、待核实传闻与搜索范围</summary>
          <div className="ai-para"><h4>讨论热度</h4><p>{r.volume}</p></div>
          <List title="主要叙事" items={r.narratives} />
          <div className="ai-two">
            <List title="X 上的看多论点" items={r.bull} tone="good" />
            <List title="X 上的看空论点" items={r.bear} tone="bad" />
          </div>
          {r.posts?.length > 0 && (
            <div className="ai-list">
              <h4>代表性帖子</h4>
              <div className="xposts">
                {r.posts.map((p, i) => (
                  <a key={i} className={`xpost ${p.stance} ${p.suspect ? "suspect" : ""}`} href={p.url} target="_blank" rel="noreferrer"
                    title={p.suspect ? `链接里的帖子 ID 对应 ${p.idDate ?? "未知日期"}，与声称的 ${p.date} 不符：可能是 AI 编造或记错的链接` : p.url}>
                    <span className="nmeta">{p.author} · {p.date}{p.suspect && <b className="suspect-tag">链接可疑</b>}</span>
                    <span>{p.text}</span>
                  </a>
                ))}
              </div>
            </div>
          )}
          <List title="流传中、需要核实的说法" items={r.rumors} tone="warn" />
          {r.coverage && <p className="small muted">搜索范围：{r.coverage}</p>}
          </details>
        </div>
      )}
    </div>
  );
}

// Settings block for the alerts settings page.
export function AISettingsCard() {
  const [status, setStatus] = useState<AIStatus | null>(null);
  const [s, setS] = useState<AISettings | null>(null);
  const [msg, setMsg] = useState("");
  const load = useCallback(() => {
    aiApi.status(true).then((st) => {
      setStatus(st);
      setS(st.settings);
    });
  }, []);
  useEffect(load, [load]);
  if (!s || !status) return <div className="card"><div className="card-head"><strong>AI 诊断</strong><span>正在检查本机 AI 命令…</span></div></div>;
  const save = async () => {
    try {
      await aiApi.saveSettings(s);
      setMsg("已保存");
      load();
    } catch (e) {
      setMsg(String((e as Error).message));
    }
  };
  const grok = status.engines.find((e) => e.name === "grok");
  return (
    <div className="card">
      <div className="card-head"><strong>AI 诊断</strong><span>调用本机已登录的 AI 命令行，用你的订阅额度，不需要 API key</span></div>
      <div className="form">
        <div className="engines">
          {status.engines.map((e) => (
            <div key={e.name} className={`engine-row ${e.available ? "" : "off"}`}>
              <b>{e.available ? "✓" : "✕"} {e.label}</b>
              <span className="small muted">{e.detail}</span>
            </div>
          ))}
        </div>
        <label className="frm inline"><span>默认引擎</span>
          <select value={s.engine} onChange={(e) => setS({ ...s, engine: e.target.value as EngineName })}>
            {status.engines.map((e) => <option key={e.name} value={e.name}>{e.label}{manualOnly.includes(e.name) ? "（仅手动；自动任务改用 Codex）" : ""}</option>)}
          </select></label>
        <label className="frm"><span>模型（可选）</span><input value={s.model} placeholder="留空用该命令行的默认模型" onChange={(e) => setS({ ...s, model: e.target.value.trim() })} /></label>
        <label className="frm inline"><span>定期诊断观察列表</span>
          <select value={s.enabled ? "on" : "off"} onChange={(e) => setS({ ...s, enabled: e.target.value === "on" })}>
            <option value="on">开</option><option value="off">关</option>
          </select></label>
        <label className="frm inline"><span>每隔</span>
          <span className="with-unit"><input type="number" min={1} value={s.intervalDays} onChange={(e) => setS({ ...s, intervalDays: Number(e.target.value) })} /><i>天</i></span></label>
        <label className="frm inline"><span>提醒触发后诊断</span>
          <select value={s.onAlert ? "on" : "off"} onChange={(e) => setS({ ...s, onAlert: e.target.value === "on" })}>
            <option value="on">开（该公司的提醒一触发就诊断一次）</option><option value="off">关</option>
          </select></label>
        <label className="frm inline"><span>自动汇总</span>
          <select value={s.autoRollup ? "on" : "off"} onChange={(e) => setS({ ...s, autoRollup: e.target.value === "on" })}>
            <option value="off">关（在详情页手动汇总）</option><option value="on">开（每次新诊断后刷新汇总）</option>
          </select></label>
        <label className="frm inline"><span>X 舆情（Grok）</span>
          <select value={s.xpulseEnabled ? "on" : "off"} onChange={(e) => setS({ ...s, xpulseEnabled: e.target.value === "on" })} disabled={!grok?.available && !s.xpulseEnabled}>
            <option value="off">关</option><option value="on">开（定期诊断前先搜一次 X）</option>
          </select></label>
        <label className="frm inline"><span>每天自动运行上限</span>
          <span className="with-unit"><input type="number" min={1} value={s.maxPerDay} onChange={(e) => setS({ ...s, maxPerDay: Number(e.target.value) })} /><i>次</i></span></label>
        <label className="frm inline"><span>单次超时</span>
          <span className="with-unit"><input type="number" min={1} value={s.timeoutMinutes} onChange={(e) => setS({ ...s, timeoutMinutes: Number(e.target.value) })} /><i>分钟</i></span></label>
        <p className="small muted">
          Claude 额度有限：只在你于详情页手动选择时使用，定期、提醒触发、自动汇总和批量都不会用它。
          诊断时 AI 只拿到页面上的数据，在空的临时目录里以只读 / 无工具模式运行，不会改动你的文件；只有 X 舆情允许 Grok 联网搜索。
          自动运行会消耗订阅额度，上限按「当天所有诊断次数」计算。今天已运行 {status.today} 次{status.queue ? `，队列中 ${status.queue} 个` : ""}。
        </p>
        <div className="row-links">
          <button className="btn primary" onClick={save}>保存</button>
          {msg && <span className="small muted">{msg}</span>}
        </div>
      </div>
    </div>
  );
}

// ---------- list page ----------

const majority = (vs: AIVerdict[]) => {
  const n: Record<string, number> = {};
  vs.forEach((v) => (n[v.stance] = (n[v.stance] ?? 0) + 1));
  return Object.entries(n).sort((a, b) => b[1] - a[1])[0]?.[0] as Stance | undefined;
};

// Compact AI digest for a list row: the rollup (or majority) stance, one dot per engine.
export function AIChip({ ai }: { ai?: AIBrief }) {
  const per = engineOrder.filter((e) => ai?.engines?.[e]).map((e) => [e, ai!.engines![e]!] as const);
  if (!ai || (!ai.rollup && per.length === 0)) return null;
  const stance = ai.rollup?.stance ?? majority(per.map(([, v]) => v));
  if (!stance) return null;
  const title = [
    ai.rollup ? `综合：${stanceLabel[ai.rollup.stance]}（信心 ${ai.rollup.confidence}/5）${ai.rollup.summary}` : "",
    ...per.map(([e, v]) => `${engineShort[e]}：${stanceLabel[v.stance]}（${v.confidence}/5）${v.summary}`),
    ai.xpulse != null ? `X 情绪分：${ai.xpulse > 0 ? "+" : ""}${ai.xpulse}` : "",
  ].filter(Boolean).join("\n");
  return (
    <span className={`chip aichip ${stanceChip[stance]}`} title={title}>
      AI·{stanceLabel[stance]}
      <span className="aidots">{per.map(([e, v]) => <i key={e} className={`s-${v.stance}`} />)}</span>
    </span>
  );
}

const stepLabel = { waiting: "排队", xpulse: "搜 X", diagnosis: "诊断中", rollup: "汇总中", done: "完成" } as const;

// Batch control + progress on the list page.
export function AIBatchBar({ onProgress }: { onProgress: () => void }) {
  const [status, setStatus] = useState<AIStatus | null>(null);
  const [open, setOpen] = useState(false);
  const [msg, setMsg] = useState("");
  const [engines, setEngines] = useState<EngineName[]>(["codex", "agy"]);
  const [top, setTop] = useState(10);
  const [xpulse, setXpulse] = useState(true);
  const refresh = useCallback(() => aiApi.status(true).then(setStatus).catch(() => {}), []);
  useEffect(() => {
    refresh();
  }, [refresh]);
  const b = status?.batch;
  const running = !!b && !b.finished;
  const doneCount = b?.companies.filter((c) => c.step === "done").length ?? 0;
  useEffect(() => {
    if (!running) return;
    const t = setInterval(refresh, 10000);
    return () => clearInterval(t);
  }, [running, refresh]);
  useEffect(() => {
    if (b) onProgress(); // a company finished: reload the list so its AI chip appears
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doneCount, b?.finished]);

  const start = async () => {
    setMsg("");
    try {
      await aiApi.batch({ top, engines, xpulse, rollup: engines.length > 1 });
      setOpen(false);
      refresh();
    } catch (e) {
      setMsg(String((e as Error).message));
    }
  };
  const avail = (status?.engines ?? []).filter((e) => e.available && e.name !== "grok" && !manualOnly.includes(e.name));
  return (
    <div className="aibatch">
      <div className="aibatch-head">
        <b>AI 批量诊断</b>
        {b ? (
          <span className="small muted">
            {running ? "进行中" : `完成于 ${new Date(b.finished!).toLocaleString()}`} · {doneCount}/{b.companies.length} 家 ·
            {" "}{b.engines.map((e) => engineShort[e]).join(" / ")}{b.xpulse ? " + Grok 搜 X" : ""}{b.rollup ? " + 综合" : ""}
          </span>
        ) : (
          <span className="small muted">让多个模型对高分候选逐一诊断，结果出现在列表「线索」列和各公司详情页</span>
        )}
        {!running && <button className="btn" onClick={() => setOpen(!open)}>{open ? "收起" : b ? "再来一批" : "开始"}</button>}
      </div>
      {running && b && (
        <div className="aibatch-steps">
          {b.companies.map((c) => (
            <span key={c.cik} className={`bstep ${c.step}`} title={c.errors?.join("\n")}>
              {c.ticker} · {stepLabel[c.step]}{c.errors?.length ? " ⚠" : ""}
            </span>
          ))}
        </div>
      )}
      {!running && b && b.companies.some((c) => c.errors?.length) && (
        <p className="small error">部分失败：{b.companies.filter((c) => c.errors?.length).map((c) => `${c.ticker}（${c.errors!.join("；").slice(0, 120)}）`).join("，")}</p>
      )}
      {open && !running && (
        <div className="aibatch-form">
          <label className="frm inline"><span>候选前</span>
            <span className="with-unit"><input type="number" min={1} max={50} value={top} onChange={(e) => setTop(Number(e.target.value))} /><i>家（按评分）</i></span></label>
          <span className="small">模型：</span>
          {avail.map((e) => (
            <label key={e.name} className="small check">
              <input type="checkbox" checked={engines.includes(e.name)} onChange={(ev) => setEngines(ev.target.checked ? [...engines, e.name] : engines.filter((x) => x !== e.name))} />
              {engineShort[e.name]}
            </label>
          ))}
          <label className="small check"><input type="checkbox" checked={xpulse} onChange={(e) => setXpulse(e.target.checked)} />先用 Grok 搜 X 舆情（每家约 5 分钟）</label>
          <button className="btn primary" disabled={!engines.length} onClick={start}>开始</button>
          {msg && <span className="small error">{msg}</span>}
          <span className="small muted">多个模型时会自动加一步「综合」。Claude 额度有限，只在详情页手动使用，不参与批量。批量任务不计入每日自动上限。</span>
        </div>
      )}
    </div>
  );
}

// Top-bar capsule for AI batches: progress at a glance, details and a new batch in a popover.
export function AIBatchCapsule({ status, onRefresh }: { status: AIStatus | null; onRefresh: () => void }) {
  const [open, setOpen] = useState(false);
  const [msg, setMsg] = useState("");
  const [engines, setEngines] = useState<EngineName[]>(["codex", "agy"]);
  const [top, setTop] = useState(10);
  const [xpulse, setXpulse] = useState(true);
  const [probe, setProbe] = useState<AIStatus | null>(null); // with engine availability, fetched when opened
  useEffect(() => {
    if (open && !probe) aiApi.status(true).then(setProbe).catch(() => {});
  }, [open, probe]);
  useEffect(() => {
    if (!open) return;
    const k = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    addEventListener("keydown", k);
    return () => removeEventListener("keydown", k);
  }, [open]);
  const b = status?.batch;
  const running = !!b && !b.finished;
  const done = b?.companies.filter((c) => c.step === "done").length ?? 0;
  const queue = status?.queue ?? 0;
  const label = running ? `AI 诊断 ${done} / ${b!.companies.length} · 进行中` : queue ? `AI 队列 ${queue}` : "AI 诊断";
  const avail = (probe?.engines ?? []).filter((e) => e.available && e.name !== "grok" && !manualOnly.includes(e.name));
  const start = async () => {
    setMsg("");
    try {
      await aiApi.batch({ top, engines, xpulse, rollup: engines.length > 1 });
      onRefresh();
    } catch (e) {
      setMsg(String((e as Error).message));
    }
  };
  return (
    <div className="aicap-wrap">
      <button className={`aicap ${running || queue ? "on" : ""}`} onClick={() => setOpen(!open)} aria-expanded={open}>
        {(running || queue > 0) && <i className="pulse" />}
        {label}
      </button>
      {open && (
        <>
          <div className="pop-mask" onClick={() => setOpen(false)} />
          <div className="aicap-pop" role="dialog" aria-label="AI 批量诊断">
            <div className="pop-head">
              <b>AI 批量诊断</b>
              {b && <span className="small muted">{running ? "进行中" : `完成于 ${new Date(b.finished!).toLocaleString()}`} · {b.engines.map((e) => engineShort[e]).join(" / ")}{b.xpulse ? " + Grok 搜 X" : ""}{b.rollup ? " + 综合" : ""}</span>}
            </div>
            {b && (
              <div className="pop-steps">
                {b.companies.map((c) => (
                  <div key={c.cik} className={`pstep ${c.step}`}>
                    <b>{c.ticker}</b>
                    <span>{stepLabel[c.step]}</span>
                    {c.errors?.length ? <small className="bad-ink" title={c.errors.join("\n")}>⚠ {c.errors[0].slice(0, 60)}</small> : null}
                  </div>
                ))}
              </div>
            )}
            {Object.entries(status?.active ?? {}).filter(([, j]) => j).length > 0 && (
              <p className="small muted">正在运行：{Object.entries(status!.active).filter(([, j]) => j).map(([e, j]) => `${engineShort[e as EngineName]} · ${j!.ticker}`).join("，")}</p>
            )}
            {!running && (
              <div className="pop-form">
                <label className="frm inline"><span>按评分前</span>
                  <span className="with-unit"><input type="number" min={1} max={50} value={top} onChange={(e) => setTop(Number(e.target.value))} /><i>家候选</i></span></label>
                <div className="checks">
                  {probe ? avail.map((e) => (
                    <label key={e.name} className="small check">
                      <input type="checkbox" checked={engines.includes(e.name)} onChange={(ev) => setEngines(ev.target.checked ? [...engines, e.name] : engines.filter((x) => x !== e.name))} />
                      {engineShort[e.name]}
                    </label>
                  )) : <span className="small muted">检查本机 AI 命令…</span>}
                  <label className="small check"><input type="checkbox" checked={xpulse} onChange={(e) => setXpulse(e.target.checked)} />先用 Grok 搜 X（每家约 5 分钟）</label>
                </div>
                <div className="row-links">
                  <button className="btn primary" disabled={!engines.length} onClick={start}>开始</button>
                  {msg && <span className="small error">{msg}</span>}
                </div>
                <p className="small muted">多个模型时自动加一步「综合」。Claude 额度有限，只在详情页手动使用。</p>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}

// Polls the light AI status: often while something runs, rarely otherwise.
export function useAIStatus() {
  const [status, setStatus] = useState<AIStatus | null>(null);
  const refresh = useCallback(() => {
    fetch("/api/ai/status?light=1").then((r) => (r.ok ? r.json() : null)).then((s) => s && setStatus(s)).catch(() => {});
  }, []);
  const busy = !!status && ((status.batch && !status.batch.finished) || status.queue > 0);
  useEffect(() => {
    refresh();
    const t = setInterval(refresh, busy ? 8000 : 60000);
    return () => clearInterval(t);
  }, [busy, refresh]);
  return { status, refresh };
}
