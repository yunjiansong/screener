import { useCallback, useEffect, useMemo, useState } from "react";
import { api, EarningsDate, Funnel, HomeData, Judgement, Run, Summary } from "./api";
import { CompanyDetail } from "./CompanyDetail";
import { AlertSettingsPage, WatchlistPage } from "./Watch";
import { AIBatchCapsule, useAIStatus } from "./AI";
import { ThemeToggle } from "./theme";
import { SkillsPage } from "./Skills";
import { BulkBar, Focus, FunnelSeg, IndustryBar, Row, Tab, TodoCards, aiView, tabs, within14 } from "./Home";

type SortKey = "score" | "marketCap" | "drawdown" | "fcfYield" | "percentile" | "convexity" | "quality" | "earnings";
type JFilter = "all" | "todo" | "done";

// Natural first direction per column: -1 = largest first.
const firstDir: Record<SortKey, 1 | -1> = { score: -1, marketCap: -1, drawdown: 1, fcfYield: -1, percentile: 1, convexity: -1, quality: -1, earnings: 1 };

const SCORE_TIP = "综合分 = 质量分 × 0.5 + 5 × min(凸性, 10)，满分 100；默认按它排序（先按通过层数）";

const columns: { key: SortKey | null; label: string; right?: boolean; title?: string }[] = [
  { key: "score", label: "公司 · 综合分", title: SCORE_TIP },
  { key: "marketCap", label: "市值", right: true },
  { key: "drawdown", label: "较5年高点", right: true },
  { key: "fcfYield", label: "FCF 收益率", right: true },
  { key: "percentile", label: "估值分位", right: true },
  { key: "convexity", label: "凸性", right: true, title: "凸性 = 上行 ÷ |下行|；下行多为压力情景 −20%，不是 −20% 时第二行会写出来" },
  { key: "quality", label: "质量", right: true },
  { key: null, label: "系统线索" },
  { key: null, label: "AI 结论", title: "综合结论（没有综合时取多数）；副行是各模型一致程度，或正在进行的步骤" },
  { key: "earnings", label: "下次财报", right: true, title: "黄底 = 14 天内；✓ = 公司已确认，其余为预计" },
  { key: null, label: "我的判断" },
];

const MAX_ROWS = 300;

type Route = { page: "list" | "watch" | "alerts" | "skills" | "detail"; ticker?: string };

function useHashRoute(): [Route, (hash: string) => void] {
  const read = (): Route => {
    const h = location.hash;
    const m = h.match(/^#\/c\/(.+)$/);
    if (m) return { page: "detail", ticker: decodeURIComponent(m[1]) };
    if (h === "#/watch") return { page: "watch" };
    if (h === "#/alerts") return { page: "alerts" };
    if (h === "#/skills") return { page: "skills" };
    return { page: "list" };
  };
  const [route, setRoute] = useState(read);
  useEffect(() => {
    const on = () => setRoute(read());
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  const go = useCallback((hash: string) => {
    location.hash = hash;
  }, []);
  return [route, go];
}

function Nav({ page, go }: { page: Route["page"]; go: (h: string) => void }) {
  const items: [Route["page"], string, string][] = [["list", "候选筛选", ""], ["watch", "观察列表", "#/watch"], ["alerts", "提醒设置", "#/alerts"], ["skills", "Skills", "#/skills"]];
  return (
    <div className="navwrap">
      <nav className="nav seg" aria-label="页面">
        {items.map(([k, label, h]) => (
          <button key={k} className={page === k ? "on" : ""} aria-current={page === k} onClick={() => go(h)}>{label}</button>
        ))}
      </nav>
      <ThemeToggle />
    </div>
  );
}

export function App() {
  const [items, setItems] = useState<Summary[]>([]);
  const [run, setRun] = useState<Run | null>(null);
  const [latest, setLatest] = useState<Run | null>(null);
  const [tab, setTab] = useState(tabs[3]);
  const [query, setQuery] = useState("");
  const [jf, setJf] = useState<JFilter>("all");
  const [sort, setSort] = useState<{ key: SortKey; dir: 1 | -1 }>({ key: "score", dir: -1 });
  const [error, setError] = useState("");
  const [home, setHome] = useState<HomeData | null>(null);
  const [earnings, setEarnings] = useState<Record<string, EarningsDate> | null>(null);
  const [focus, setFocus] = useState<Focus | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [toast, setToast] = useState("");
  const { status: aiStatus, refresh: refreshAI } = useAIStatus();
  const [route, go] = useHashRoute();
  const ticker = route.page === "detail" ? route.ticker! : null;
  const [back, setBack] = useState("");
  const setTicker = useCallback(
    (t: string | null) => {
      if (t) {
        if (route.page !== "detail") setBack(location.hash);
        go(`#/c/${encodeURIComponent(t)}`);
      } else go(back);
    },
    [go, back, route.page],
  );

  // The whole universe (~5k summaries, ~2MB) is loaded once; tabs and filters are client-side.
  const load = useCallback(async () => {
    try {
      const [list, status] = await Promise.all([api.companies(0), api.status()]);
      setItems(list.items);
      setRun(list.run);
      setLatest(status.latest);
      setError("");
      api.home().then(setHome).catch(() => {});
    } catch (e) {
      setError(String(e));
    }
  }, []);
  useEffect(() => {
    load();
    api.earnings().then(setEarnings).catch(() => setEarnings({}));
  }, [load]);

  // When an AI batch makes progress, reload so finished companies show their conclusion.
  const doneAI = aiStatus?.batch?.companies.filter((c) => c.step === "done").length ?? 0;
  useEffect(() => {
    if (doneAI) load();
  }, [doneAI, load]);

  const running = latest?.status === "running";
  useEffect(() => {
    if (!running) return;
    const id = setInterval(async () => {
      const s = await api.status();
      setLatest(s.latest);
      if (s.latest?.status !== "running") load();
    }, 3000);
    return () => clearInterval(id);
  }, [running, load]);

  const startRun = async () => {
    try {
      await api.startRun();
      setLatest((await api.status()).latest);
    } catch (e) {
      setError(String(e));
    }
  };

  const onSaved = useCallback((cik: number, j: Judgement | null) => {
    setItems((xs) => xs.map((x) => (x.cik === cik ? { ...x, judgement: j ?? undefined } : x)));
    api.home().then(setHome).catch(() => {});
  }, []);

  // Stage tab + search; a to-do card focus spans all stages (a triggered falsification may be
  // on a company that has since dropped out of L3).
  const base = useMemo(() => {
    const q = query.trim().toLowerCase();
    const card = focus && focus.kind !== "industry";
    let r = items.filter((i) => (card || i.stage >= tab.minStage) && (!q || i.ticker.toLowerCase().includes(q) || i.name.toLowerCase().includes(q) || (i.nameZh ?? "").includes(q)));
    if (jf === "todo") r = r.filter((i) => i.stage >= 3 && !i.judgement);
    if (jf === "done") r = r.filter((i) => i.judgement);
    if (focus?.kind === "falsified") r = r.filter((i) => home?.rows[i.cik]?.falsified?.length);
    if (focus?.kind === "review") r = r.filter((i) => home?.rows[i.cik]?.reviewDue);
    if (focus?.kind === "new") r = r.filter((i) => home?.rows[i.cik]?.isNew);
    if (focus?.kind === "earnings") r = r.filter((i) => i.stage >= 3 && within14(earnings?.[i.ticker]));
    return r;
  }, [items, tab, query, jf, focus, home, earnings]);

  const rows = useMemo(() => {
    let r = focus?.kind === "industry" ? base.filter((i) => (i.industry || i.sector || "行业未知") === focus.value) : base;
    if (sort.key === "score") return sort.dir === -1 ? r : [...r].reverse(); // server order: stage, then score
    const val = (i: Summary): number | null => {
      if (sort.key === "quality") return i.quality;
      if (sort.key === "earnings") {
        const d = earnings?.[i.ticker]?.date;
        return d ? Date.parse(d) : null;
      }
      return i[sort.key as "marketCap" | "drawdown" | "fcfYield" | "percentile" | "convexity"];
    };
    r = [...r];
    return r.sort((a, b) => {
      const va = val(a), vb = val(b);
      if (va == null) return vb == null ? 0 : 1;
      if (vb == null) return -1;
      return (va - vb) * sort.dir;
    });
  }, [base, focus, sort, earnings]);

  const order = useMemo(() => rows.map((r) => r.ticker), [rows]);
  const funnel = run?.funnel && "universe" in run.funnel ? (run.funnel as Funnel) : null;
  const cands = items.filter((i) => i.stage >= 3);
  const judged = cands.filter((i) => i.judgement).length;

  if (route.page === "watch" || route.page === "alerts" || route.page === "skills")
    return (
      <>
        <div className="topnav"><div className="in"><b>CareySon的视角的专家选股系统</b><Nav page={route.page} go={go} /></div></div>
        {route.page === "watch" ? <WatchlistPage onOpen={setTicker} /> : route.page === "skills" ? <SkillsPage /> : <AlertSettingsPage />}
      </>
    );

  if (ticker)
    return (
      <CompanyDetail
        key={ticker}
        ticker={ticker}
        order={order.includes(ticker) ? order : [ticker]}
        listLabel={tab.label}
        onOpen={setTicker}
        onBack={() => setTicker(null)}
        onSaved={onSaved}
      />
    );

  const visible = rows.slice(0, MAX_ROWS);
  const allOn = visible.length > 0 && visible.every((r) => selected.has(r.cik));
  const toggle = (cik: number, on: boolean) =>
    setSelected((s) => {
      const n = new Set(s);
      if (on) n.add(cik);
      else n.delete(cik);
      return n;
    });
  const lastRun = run ? new Date(run.finishedAt ?? run.startedAt) : null;

  return (
    <div className="page home">
      <header className="hbar">
        <div className="brand" title="基本面硬 · 价格被困 · 下行有限上行大。系统只给候选和线索，是否错杀由你判断。">
          <span className="eyebrow">CAREYSON · VALUE INVESTING</span>
          <span className="verse">质坚于内，价困于时。众弃非贱，反者道动。以数辨真，以静候复。</span>
          <span className="verse coda">器穷其象，断在于心。</span>
        </div>
        <Nav page="list" go={go} />
        <div className="hbar-right">
          <AIBatchCapsule status={aiStatus} onRefresh={refreshAI} />
          {running ? (
            <span className="running">● 筛选中：{latest?.message}</span>
          ) : (
            <>
              <span className="small muted" title={lastRun ? lastRun.toLocaleString() : ""}>
                {lastRun ? `上次筛选 ${lastRun.getMonth() + 1}/${lastRun.getDate()} ${lastRun.toTimeString().slice(0, 5)}` : "尚未筛选"}
                {latest?.status === "failed" && ` · 最近一次失败：${latest.message}`}
              </span>
              <button className="btn" onClick={startRun}>重新筛选</button>
            </>
          )}
        </div>
      </header>

      {error && <p className="error">{error}</p>}
      <TodoCards home={home} earnings={earnings} items={items} focus={focus?.kind === "industry" ? null : focus} onFocus={(f) => { setFocus(f); setSelected(new Set()); }} />

      <div className="filterline">
        {funnel && <FunnelSeg f={funnel} active={tab} onPick={(t: Tab) => { setTab(t); if (focus?.kind === "industry") setFocus(null); }} />}
        <input className="search" placeholder="搜索代码或名称，如 JD" value={query} onChange={(e) => setQuery(e.target.value)} />
        <div className="seg small-seg" role="radiogroup" aria-label="按判断筛选">
          {([["all", "全部"], ["todo", "未判断"], ["done", "已判断"]] as const).map(([k, l]) => (
            <button key={k} role="radio" aria-checked={jf === k} className={jf === k ? "on" : ""} onClick={() => setJf(k)}>{l}</button>
          ))}
        </div>
        <div className="progress" title="L3 候选中你已下结论或写了笔记的比例">
          <div className="track"><div style={{ width: `${cands.length ? (judged / cands.length) * 100 : 0}%` }} /></div>
          <span>已判断 {judged} / {cands.length}</span>
        </div>
      </div>

      <IndustryBar rows={base} focus={focus} onFocus={setFocus} />

      {(focus || rows.length !== base.length) && focus && (
        <div className="focusline">
          <span>筛选：</span>
          <button className="fchip" onClick={() => setFocus(null)}>{focus.label} <b>×</b></button>
          <span className="muted">{rows.length} 家{focus.kind !== "industry" ? "（跨所有层级）" : ""}</span>
        </div>
      )}

      <div className="card table">
        <div className="inner">
          <div className="grid-row thead">
            <label className="ck" title="全选当前列表">
              <input type="checkbox" checked={allOn} aria-label="全选"
                onChange={(e) => setSelected(e.target.checked ? new Set(visible.map((r) => r.cik)) : new Set())} />
            </label>
            {columns.map((c) => {
              const on = c.key != null && sort.key === c.key;
              return (
                <button
                  key={c.label}
                  title={c.title}
                  className={`th ${c.right ? "r" : ""} ${c.key ? "sortable" : ""} ${on ? "on" : ""}`}
                  disabled={!c.key}
                  onClick={() => c.key && setSort((s) => ({ key: c.key!, dir: s.key === c.key ? (-s.dir as 1 | -1) : firstDir[c.key!] }))}
                >
                  <span>{c.label}</span>
                  {on && <small>{sort.dir < 0 ? "↓" : "↑"}</small>}
                </button>
              );
            })}
          </div>
          {visible.map((r) => (
            <Row key={r.cik} r={r} onOpen={() => setTicker(r.ticker)} selected={selected.has(r.cik)} onSelect={(on) => toggle(r.cik, on)}
              home={home?.rows[r.cik]} earnings={earnings?.[r.ticker]} ai={aiView(r, aiStatus)} />
          ))}
          {rows.length === 0 && <div className="empty">{items.length ? "没有匹配的公司" : "还没有筛选结果，点「重新筛选」"}</div>}
          {rows.length > MAX_ROWS && <div className="empty">仅显示前 {MAX_ROWS} 家，用搜索缩小范围</div>}
        </div>
      </div>
      <div className="foot">点击行进入详情 · 详情页按 J / K 切换上下一家，1–5 选结论，Esc 返回 · 勾选多行可批量操作</div>

      <BulkBar selected={selected} items={items} onClear={() => setSelected(new Set())}
        onDone={(m) => { setToast(m); setSelected(new Set()); load(); refreshAI(); setTimeout(() => setToast(""), 6000); }} />
      {toast && <div className="toast" role="status">{toast}</div>}
    </div>
  );
}

