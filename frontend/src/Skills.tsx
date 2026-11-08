import { useCallback, useEffect, useState } from "react";
import { berkshireTemplates } from "./skillTemplates";
import {
  AIStatus,
  EngineName,
  IncludeKey,
  Skill,
  SkillInput,
  SkillPending,
  SkillRun,
  aiApi,
  engineOrder,
  engineShort,
  includeLabel,
  skillApi,
} from "./api";
import { when } from "./AI";
import { Markdown } from "./Markdown";

// Skills replace the built-in analysis: your own instructions, run over the company's facts
// only (no screening stance or guidance). Results are kept per company.

const includeKeys = Object.keys(includeLabel) as IncludeKey[];

const templates: { name: string; skill: SkillInput }[] = [
  { name: "空白", skill: { name: "", description: "", instructions: "", include: [], engine: "" } },
  {
    name: "护城河与资本配置",
    skill: {
      name: "护城河与资本配置",
      description: "像长期股东一样看：生意好不好、护城河在变宽还是变窄、管理层怎么花钱",
      engine: "",
      include: [],
      instructions: `你是一位只看长期的价值投资者。根据下面的数据分析这家公司：

1. 生意本身：靠什么赚钱，毛利率、ROIC 和 FCF 转化率说明了什么。
2. 护城河：属于哪一类（品牌、网络效应、转换成本、成本优势、规模），从数据趋势看在变宽还是变窄，证据是什么。
3. 资本配置：近几年的 FCF 用在了哪里（回购、分红、并购、再投资），回报如何，是否稀释股东。
4. 最大的风险：一条最可能让这门生意变差的因素。
5. 结论：用三句话总结，并给出一个你认为合理的每股价值区间和关键假设。

用 Markdown 输出，每部分一个小标题；引用数据时写出具体数字和期间。`,
    },
  },
  {
    name: "空头视角",
    skill: {
      name: "空头视角",
      description: "假设你在写做空报告：找出数据里最站得住的看空论据",
      engine: "",
      include: [],
      instructions: `假设你是一位做空研究员，要为这家公司写一份做空备忘录。只用下面的数据和你已知的公开信息：

1. 列出最有力的 3–5 条看空论据，每条附具体数据。
2. 指出财务数据里可疑或不可持续的地方（例如利润和现金流背离、一次性收益、资本开支不足）。
3. 说明什么事件会让空头逻辑失效。
4. 最后给出你对空头论据整体强度的评分（1–5）和一句理由。

用 Markdown 输出。凡是数据里没有、靠记忆的事实，标注「据公开信息」。`,
    },
  },
  {
    name: "市场预期拆解",
    skill: {
      name: "市场预期拆解",
      description: "现价隐含了什么预期，这些预期合不合理",
      engine: "",
      include: [],
      instructions: `从「现价隐含了什么」出发分析这家公司：

1. 用数据里的估值倍数、FCF 和反向 DCF，说明当前股价隐含的增长和利润率预期。
2. 和公司过去 5 年的实际表现、同行对比做比较：市场是更悲观还是更乐观，差在哪里。
3. 列出两三个能让预期修正的关键变量，各自需要看到什么数据。
4. 给出悲观 / 基准 / 乐观三种情景下的每股价值，写清假设。

用 Markdown 输出，表格优先。`,
    },
  },
  ...berkshireTemplates,
];

const emptyInput: SkillInput = templates[0].skill;
const toInput = (s: Skill): SkillInput => ({ name: s.name, description: s.description, instructions: s.instructions, include: s.include ?? [], engine: s.engine });
const same = (a: SkillInput, b: SkillInput) => JSON.stringify(a) === JSON.stringify(b);

function useEngines() {
  const [st, setSt] = useState<AIStatus | null>(null);
  useEffect(() => {
    aiApi.status().then(setSt).catch(() => {});
  }, []);
  return st;
}

function EngineSelect({ value, onChange, status, allowDefault, defaultLabel }: {
  value: EngineName | "";
  onChange: (e: EngineName | "") => void;
  status: AIStatus | null;
  allowDefault?: boolean;
  defaultLabel?: string;
}) {
  return (
    <select value={value} onChange={(ev) => onChange(ev.target.value as EngineName | "")}>
      {allowDefault && <option value="">{defaultLabel ?? "跟随 AI 设置"}</option>}
      {engineOrder.map((e) => {
        const s = status?.engines?.find((x) => x.name === e);
        return (
          <option key={e} value={e} disabled={s ? !s.available : false}>
            {engineShort[e]}{s && !s.available ? "（未登录）" : ""}
          </option>
        );
      })}
    </select>
  );
}

// ---------- the management page ----------

export function SkillsPage() {
  const [items, setItems] = useState<Skill[] | null>(null);
  const [sel, setSel] = useState<number | "new" | null>(null);
  const [draft, setDraft] = useState<SkillInput>(emptyInput);
  const [msg, setMsg] = useState("");
  const [confirmDel, setConfirmDel] = useState(false);
  const [preview, setPreview] = useState<{ ticker: string; text: string; loading: boolean }>({ ticker: "", text: "", loading: false });
  const status = useEngines();

  const load = useCallback(async (pick?: number) => {
    const r = await skillApi.list();
    setItems(r.items);
    if (pick != null) setSel(pick);
    else setSel((s) => (s == null && r.items.length ? r.items[0].id : s));
  }, []);
  useEffect(() => {
    load().catch((e) => setMsg(String(e.message ?? e)));
  }, [load]);

  const current = typeof sel === "number" ? items?.find((s) => s.id === sel) ?? null : null;
  useEffect(() => {
    setDraft(current ? toInput(current) : sel === "new" ? draft : emptyInput);
    setConfirmDel(false);
    setPreview((p) => ({ ...p, text: "" }));
  }, [sel, current?.updatedAt]); // eslint-disable-line react-hooks/exhaustive-deps

  const dirty = sel === "new" ? !same(draft, emptyInput) : current ? !same(draft, toInput(current)) : false;
  const startNew = (t: SkillInput) => {
    setSel("new");
    setDraft({ ...t });
    setMsg("");
  };
  const save = async () => {
    try {
      const saved = sel === "new" ? await skillApi.create(draft) : await skillApi.update(current!.id, draft);
      await load(saved.id);
      setMsg(`已保存 ${new Date().toLocaleTimeString()}`);
    } catch (e) {
      setMsg(`保存失败：${e instanceof Error ? e.message : e}`);
    }
  };
  const del = async () => {
    if (!current) return;
    try {
      await skillApi.remove(current.id);
      setSel(null);
      await load();
      setMsg(`已删除「${current.name}」；用它跑出的结果仍保留在各公司页面`);
    } catch (e) {
      setMsg(`删除失败：${e instanceof Error ? e.message : e}`);
    }
  };
  const runPreview = async () => {
    if (!current || !preview.ticker.trim()) return;
    setPreview((p) => ({ ...p, loading: true, text: "" }));
    try {
      const text = await skillApi.prompt(preview.ticker.trim().toUpperCase(), current.id);
      setPreview((p) => ({ ...p, loading: false, text }));
    } catch (e) {
      setPreview((p) => ({ ...p, loading: false, text: `预览失败：${e instanceof Error ? e.message : e}` }));
    }
  };

  return (
    <div className="page skills-page">
      <div className="card-head plain">
        <h1 className="ptitle">Skills</h1>
        <span className="muted">
          用你自己的分析方法替换内置诊断：提示词只给事实数据（财报、行情、估值统计、专业指标、同行、新闻），不带系统的「凸性错杀」立场、判定和评级；结果按公司保存。在公司详情页的「Skills 分析」里运行。
        </span>
      </div>
      <div className="skills-layout">
        <div className="card skills-list">
          <div className="skills-new">
            <span className="small muted">新建：</span>
            {templates.map((t) => (
              <button key={t.name} className="btn small" onClick={() => startNew(t.skill)}>{t.name}</button>
            ))}
          </div>
          {items == null && <p className="muted small pad">加载中…</p>}
          {items?.length === 0 && sel !== "new" && <p className="muted small pad">还没有 skill。从上面选一个模板开始，改成你自己的方法。</p>}
          {sel === "new" && (
            <button className="skill-item on">
              <b>{draft.name || "新 skill"}</b>
              <small>未保存</small>
            </button>
          )}
          {items?.map((s) => (
            <button key={s.id} className={`skill-item ${sel === s.id ? "on" : ""}`} onClick={() => setSel(s.id)}>
              <b>{s.name}</b>
              <small>{s.description || "（无说明）"}</small>
              <small className="muted">{s.engine ? engineShort[s.engine] : "跟随 AI 设置"} · 已保存 {s.runs} 次结果</small>
            </button>
          ))}
        </div>

        {(sel === "new" || current) ? (
          <div className="card skill-edit">
            <label className="field">
              名称
              <input type="text" value={draft.name} maxLength={60} placeholder="例如：护城河与资本配置" onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </label>
            <label className="field">
              说明（可选）
              <input type="text" value={draft.description} maxLength={300} placeholder="一句话说明这个 skill 看什么" onChange={(e) => setDraft({ ...draft, description: e.target.value })} />
            </label>
            <label className="field">
              <span className="field-row">分析指令<small className="muted">放在提示词最前面；后面系统会附上这家公司的事实数据</small></span>
              <textarea className="mono" rows={16} value={draft.instructions} placeholder="写下你希望模型怎么分析、输出什么结构……" onChange={(e) => setDraft({ ...draft, instructions: e.target.value })} />
            </label>
            <div className="field">
              <span className="field-row">附带材料<small className="muted">默认只给事实；勾选的是观点，提示词里会标明「这些是观点而不是事实」</small></span>
              <div className="include-row">
                {includeKeys.map((k) => (
                  <label key={k} className="check" title={includeLabel[k].tip}>
                    <input type="checkbox" checked={draft.include.includes(k)}
                      onChange={(e) => setDraft({ ...draft, include: e.target.checked ? [...draft.include, k] : draft.include.filter((x) => x !== k) })} />
                    {includeLabel[k].label}
                  </label>
                ))}
              </div>
            </div>
            <label className="field narrow">
              默认模型
              <EngineSelect value={draft.engine} onChange={(engine) => setDraft({ ...draft, engine })} status={status} allowDefault />
            </label>
            <div className="skill-actions">
              <button className="btn primary" disabled={!dirty || !draft.name.trim() || !draft.instructions.trim()} onClick={save}>保存</button>
              {dirty && <button className="link small" onClick={() => (sel === "new" ? setSel(items?.[0]?.id ?? null) : setDraft(toInput(current!)))}>放弃修改</button>}
              {current && (confirmDel ? (
                <>
                  <span className="small">删除「{current.name}」？已保存的结果会保留。</span>
                  <button className="btn small danger" onClick={del}>确认删除</button>
                  <button className="link small" onClick={() => setConfirmDel(false)}>取消</button>
                </>
              ) : (
                <button className="link small danger" onClick={() => setConfirmDel(true)}>删除</button>
              ))}
              {msg && <span className="small muted">{msg}</span>}
            </div>
            {current && (
              <details className="skill-preview">
                <summary>预览发送给模型的完整提示词</summary>
                <div className="preview-row">
                  <input type="text" placeholder="股票代码，如 PDD" value={preview.ticker} onChange={(e) => setPreview({ ...preview, ticker: e.target.value })}
                    onKeyDown={(e) => e.key === "Enter" && runPreview()} />
                  <button className="btn small" disabled={!preview.ticker.trim() || preview.loading} onClick={runPreview}>{preview.loading ? "生成中…" : "生成预览"}</button>
                  {dirty && <span className="small muted">预览按已保存的版本生成</span>}
                </div>
                {preview.text && <pre className="prompt-preview">{preview.text}</pre>}
              </details>
            )}
          </div>
        ) : (
          <div className="card skill-edit empty">{msg ? <p className="small muted">{msg}</p> : <p className="muted">选择左边的 skill 编辑，或从模板新建。</p>}</div>
        )}
      </div>
    </div>
  );
}

// ---------- the detail-page panel ----------

export function useSkillRuns(ticker: string) {
  const [items, setItems] = useState<SkillRun[] | null>(null);
  const [pending, setPending] = useState<SkillPending[]>([]);
  const load = useCallback(async () => {
    const r = await skillApi.runs(ticker);
    setItems(r.items);
    setPending(r.pending ?? []);
  }, [ticker]);
  useEffect(() => {
    setItems(null);
    setPending([]);
    load().catch(() => setItems([]));
  }, [load]);
  useEffect(() => {
    if (!pending.length) return;
    const t = setInterval(() => load().catch(() => {}), 5000);
    return () => clearInterval(t);
  }, [pending.length, load]);
  return { items, pending, load };
}

export function skillLine(sr: ReturnType<typeof useSkillRuns>) {
  const ok = (sr.items ?? []).filter((x) => !x.error);
  const last = ok[0];
  return {
    summary: sr.pending.length
      ? `${sr.pending.length} 个分析${sr.pending.some((p) => p.active) ? "进行中" : "排队中"}`
      : last
        ? `最近：${last.skillName} · ${engineShort[last.engine]} · ${when(last.at)}`
        : "用你自己的分析方法（只给事实数据）分析这家公司",
    tag: sr.pending.length ? "分析中" : ok.length ? `${ok.length} 条结果` : "",
  };
}

export function SkillPanel({ ticker, sr }: { ticker: string; sr: ReturnType<typeof useSkillRuns> }) {
  const [skills, setSkills] = useState<Skill[] | null>(null);
  const [pick, setPick] = useState<number | null>(null);
  const [engine, setEngine] = useState<EngineName | "">("");
  const [msg, setMsg] = useState("");
  const [del, setDel] = useState<number | null>(null);
  const status = useEngines();
  useEffect(() => {
    skillApi.list().then((r) => {
      setSkills(r.items);
      setPick((p) => p ?? r.items[0]?.id ?? null);
    }).catch(() => setSkills([]));
  }, []);
  const sk = skills?.find((s) => s.id === pick) ?? null;
  const defaultEngine = sk?.engine || status?.settings.engine || "codex";

  const run = async () => {
    if (!sk) return;
    setMsg("");
    try {
      await skillApi.run(ticker, sk.id, engine || undefined);
      await sr.load();
    } catch (e) {
      setMsg(e instanceof Error ? e.message : String(e));
    }
  };
  const copy = async (id?: number) => {
    setMsg("生成中…");
    try {
      await navigator.clipboard.writeText(await skillApi.prompt(ticker, id));
      setMsg(id ? "已复制：skill 指令 + 事实数据" : "已复制：只有事实数据");
    } catch (e) {
      setMsg(`复制失败：${e instanceof Error ? e.message : e}`);
    }
  };
  const remove = async (id: number) => {
    await skillApi.removeRun(id);
    setDel(null);
    await sr.load();
  };

  return (
    <div className="card skillpanel">
      <div className="card-head">
        <strong>Skills 分析</strong>
        <span>只给事实数据，不带系统立场 · <a href="#/skills">管理 Skills</a></span>
      </div>
      {skills?.length === 0 ? (
        <p className="pad muted">还没有 skill。<a href="#/skills">去新建一个</a>（有模板可选），之后就能在这里对当前股票运行。</p>
      ) : (
        <div className="skill-run-bar">
          <select value={pick ?? ""} onChange={(e) => setPick(Number(e.target.value))} aria-label="选择 skill">
            {skills?.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select>
          <EngineSelect value={engine} onChange={setEngine} status={status} allowDefault defaultLabel={`默认（${engineShort[defaultEngine as EngineName]}）`} />
          <button className="btn primary small" disabled={!sk} onClick={run}>运行</button>
          <button className="btn small" disabled={!sk} onClick={() => copy(sk?.id)} title="不调用本机 AI：把 skill 指令和事实数据复制到剪贴板，粘贴到任意聊天">复制提示词</button>
          <button className="link small" onClick={() => copy()} title="只复制事实数据，不含任何指令">只复制事实数据</button>
          {msg && <span className="small muted">{msg}</span>}
        </div>
      )}
      {sk?.description && <p className="pad small muted skill-desc">{sk.description}</p>}
      {sr.pending.length > 0 && (
        <div className="pad ai-running">
          {sr.pending.map((p, i) => (
            <span key={i} className="ai-job">
              <i className="spinner" /> {engineShort[p.engine]} · {skills?.find((s) => s.id === p.skillId)?.name ?? `skill ${p.skillId}`} {p.active ? "分析中" : "排队中"}
            </span>
          ))}
        </div>
      )}
      <div className="skill-results">
        {sr.items?.map((x, i) => (
          <details key={x.id} className={`skill-result ${x.error ? "failed" : ""}`} open={i === 0}>
            <summary>
              <b>{x.skillName}</b>
              <span className="muted small">
                {engineShort[x.engine]}{x.model ? ` ${x.model}` : ""} · {when(x.at)} · 当时股价 ${x.price.toFixed(2)} · 用时 {(x.durationMs / 60000).toFixed(1)} 分钟
              </span>
              {x.error && <span className="chip internal">失败</span>}
            </summary>
            {x.error ? <p className="error small">{x.error}</p> : <Markdown text={x.output} />}
            <div className="skill-result-foot">
              {!x.error && <button className="link small" onClick={() => navigator.clipboard.writeText(x.output)}>复制结果</button>}
              <details className="inline">
                <summary className="link small">当时的指令{x.include?.length ? ` · 附带 ${x.include.map((k) => includeLabel[k]?.label ?? k).join("、")}` : " · 只给事实"}</summary>
                <pre className="prompt-preview">{x.instructions}</pre>
              </details>
              {del === x.id ? (
                <>
                  <span className="small">删除这条结果？</span>
                  <button className="btn small danger" onClick={() => remove(x.id)}>确认删除</button>
                  <button className="link small" onClick={() => setDel(null)}>取消</button>
                </>
              ) : (
                <button className="link small danger" onClick={() => setDel(x.id)}>删除</button>
              )}
            </div>
          </details>
        ))}
        {sr.items?.length === 0 && skills && skills.length > 0 && !sr.pending.length && <p className="pad small muted">这家公司还没有 skill 分析结果。</p>}
      </div>
    </div>
  );
}
