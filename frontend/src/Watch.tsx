import { useCallback, useEffect, useState } from "react";
import { AlertLogRow, AlertSettings, Rules, ScanResult, WatchRow, alertsApi, pct, stanceChip, stanceLabel, verdictLabel } from "./api";
import { AISettingsCard } from "./AI";

// ---------- rule fields ----------

type Kind = "price" | "pct" | "times" | "bool";
interface FieldDef {
  key: keyof Rules;
  label: string;
  kind: Kind;
  hint: string;
  perCompanyOnly?: boolean;
}

const fields: FieldDef[] = [
  { key: "priceBelow", label: "股价 ≤", kind: "price", hint: "你的买入区间，美元", perCompanyOnly: true },
  { key: "priceAbove", label: "股价 ≥", kind: "price", hint: "涨回这里再看一眼", perCompanyOnly: true },
  { key: "dailyDrop", label: "单日跌幅 ≤", kind: "pct", hint: "如 -7 表示单日跌 7% 以上" },
  { key: "drawdown", label: "较5年高点 ≤", kind: "pct", hint: "如 -50" },
  { key: "fcfYield", label: "FCF 收益率 ≥", kind: "pct", hint: "如 10" },
  { key: "multiple", label: "P/FCF ≤", kind: "times", hint: "倍数，如 12" },
  { key: "upside", label: "估值修复上行 ≥", kind: "pct", hint: "如 150" },
  { key: "newsMove", label: "新闻引发涨跌 ≥", kind: "pct", hint: "新闻发布后首个交易日 |涨跌|，如 8" },
  { key: "stageDrop", label: "跌出筛选层", kind: "bool", hint: "最新筛选比加入时少通过一层" },
  { key: "newQuarter", label: "新财报", kind: "bool", hint: "出了新季报/年报，数据口径更新" },
  { key: "review", label: "到期复查", kind: "bool", hint: "到了你在判断里写的下次复查日期" },
];

const toInput = (f: FieldDef, v: number | boolean | undefined) =>
  v == null || typeof v === "boolean" ? "" : f.kind === "pct" ? String(+(v * 100).toFixed(2)) : String(v);
const fromInput = (f: FieldDef, s: string): number | undefined => {
  if (s.trim() === "") return undefined;
  const n = Number(s);
  if (!isFinite(n)) return undefined;
  return f.kind === "pct" ? n / 100 : n;
};
export const describe = (f: FieldDef, v: number | boolean | undefined) => {
  if (v == null || v === false) return "";
  if (v === true) return f.label;
  if (f.kind === "price") return `${f.label} $${v}`;
  if (f.kind === "times") return `${f.label} ${v}x`;
  return `${f.label} ${(Number(v) * 100).toFixed(0)}%`;
};

export function rulesSummary(r: Rules) {
  return fields.map((f) => describe(f, r[f.key])).filter(Boolean);
}

// RulesForm edits either the defaults (blank = off) or a company's overrides
// (blank = use default, 0 = turn this default off).
function RulesForm({ value, onChange, defaults }: { value: Rules; onChange: (r: Rules) => void; defaults?: Rules }) {
  const overrides = defaults != null;
  return (
    <div className="rules">
      {fields
        .filter((f) => overrides || !f.perCompanyOnly)
        .map((f) => {
          const def = defaults?.[f.key];
          if (f.kind === "bool") {
            const v = value[f.key] as boolean | undefined;
            const sel = v == null ? (overrides ? "default" : "off") : v ? "on" : "off";
            return (
              <label key={f.key} className="rule" title={f.hint}>
                <span>{f.label}</span>
                <select
                  value={sel}
                  onChange={(e) => {
                    const s = e.target.value;
                    onChange({ ...value, [f.key]: s === "default" ? undefined : s === "on" });
                  }}
                >
                  {overrides && <option value="default">默认（{def ? "开" : "关"}）</option>}
                  <option value="on">开</option>
                  <option value="off">关</option>
                </select>
                <small>{f.hint}</small>
              </label>
            );
          }
          return (
            <label key={f.key} className="rule" title={f.hint}>
              <span>{f.label}</span>
              <span className="with-unit">
                {f.kind === "price" && <i>$</i>}
                <input
                  inputMode="decimal"
                  value={toInput(f, value[f.key] as number | undefined)}
                  placeholder={overrides ? (def != null ? `默认 ${toInput(f, def as number)}` : "不提醒") : "不提醒"}
                  onChange={(e) => onChange({ ...value, [f.key]: fromInput(f, e.target.value) })}
                />
                {f.kind === "pct" && <i>%</i>}
                {f.kind === "times" && <i>x</i>}
              </span>
              <small>{f.hint}{overrides && f.kind !== "price" ? "；填 0 关闭默认" : ""}</small>
            </label>
          );
        })}
    </div>
  );
}

// ---------- watchlist page ----------

export function WatchlistPage({ onOpen }: { onOpen: (t: string) => void }) {
  const [rows, setRows] = useState<WatchRow[] | null>(null);
  const [defaults, setDefaults] = useState<Rules>({});
  const [editing, setEditing] = useState<number | null>(null);
  const [draft, setDraft] = useState<Rules>({});
  const [scan, setScan] = useState<ScanResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    alertsApi
      .watchlist()
      .then((r) => {
        setRows(r.items);
        setDefaults(r.defaults);
      })
      .catch((e) => setError(String(e.message ?? e)));
  }, []);
  useEffect(load, [load]);

  const runScan = async () => {
    setBusy(true);
    setError("");
    try {
      setScan(await alertsApi.scan());
      load();
    } catch (e) {
      setError(String((e as Error).message));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page">
      <div className="toolbar">
        <div className="left">
          <h2 className="ptitle">观察列表</h2>
          <span className="muted">在详情页把结论设为「加入观察」，或点「加入观察列表」，公司就会出现在这里。</span>
        </div>
        <button className="btn primary" disabled={busy} onClick={runScan}>{busy ? "扫描中…" : "立即扫描"}</button>
      </div>
      {error && <p className="error">{error}</p>}
      {scan && <ScanBanner r={scan} />}
      <div className="card table">
        <div className="inner" style={{ minWidth: 1240 }}>
          <div className="wrow thead">
            <span>公司</span><span className="r">现价</span><span className="r">单日</span><span className="r">较5年高点</span>
            <span className="r">FCF 收益率</span><span className="r">估值分位</span><span className="r">上行</span>
            <span>判断 / 复查</span><span>AI 最近诊断</span><span>提醒规则</span><span />
          </div>
          {rows?.map((w) => {
            const s = w.snapshot ?? {};
            return (
              <div key={w.cik} className="wgroup">
                <div className="wrow trow" onClick={() => onOpen(w.ticker)} role="link" tabIndex={0} onKeyDown={(e) => e.key === "Enter" && onOpen(w.ticker)}>
                  <div className="co">
                    <div className="top"><b>{w.ticker}</b><span>{w.name}</span></div>
                    <div className="ind">{s.error ? s.error : w.scannedAt ? `扫描于 ${new Date(w.scannedAt).toLocaleString()}` : "还没扫描过"}</div>
                  </div>
                  <span className="r">{s.price != null ? `$${s.price.toFixed(2)}` : "—"}</span>
                  <span className="r" style={{ color: s.dayChange != null && s.dayChange <= -0.05 ? "var(--bad-ink)" : undefined }}>{pct(s.dayChange, 1)}</span>
                  <span className="r">{pct(s.drawdown)}</span>
                  <span className="r">{pct(s.fcfYield, 1)}</span>
                  <span className="r">{pct(s.percentile)}</span>
                  <span className="r" style={{ color: "var(--good-ink)" }}>{pct(s.upside)}</span>
                  <span className="small">
                    {w.judgement?.verdict ? verdictLabel[w.judgement.verdict] : "—"}
                    {w.judgement?.reviewOn && <span className="muted"> · {w.judgement.reviewOn} 复查</span>}
                  </span>
                  <span className="small" title={w.ai ? `${new Date(w.ai.at).toLocaleString()} · ${w.ai.result.summary}` : ""}>
                    {w.ai ? <><span className={`chip ${stanceChip[w.ai.result.stance] ?? "neutral"}`}>{stanceLabel[w.ai.result.stance]}</span> <span className="muted">{new Date(w.ai.at).toLocaleDateString()}</span></> : <span className="muted">—</span>}
                  </span>
                  <span className="clues">
                    {rulesSummary(w.effective).map((t) => <span key={t} className="chip neutral">{t}</span>)}
                  </span>
                  <span className="actions-inline" onClick={(e) => e.stopPropagation()}>
                    <button className="link" onClick={() => { setEditing(editing === w.cik ? null : w.cik); setDraft(w.rules ?? {}); }}>
                      {editing === w.cik ? "收起" : "编辑提醒"}
                    </button>
                    <button className="link danger" onClick={async () => { await alertsApi.unwatch(w.cik); load(); }}>移除</button>
                  </span>
                </div>
                {editing === w.cik && (
                  <div className="wedit">
                    <RulesForm value={draft} onChange={setDraft} defaults={defaults} />
                    <div className="row-links">
                      <button className="btn primary" onClick={async () => { await alertsApi.watch(w.cik, w.ticker, draft); setEditing(null); load(); }}>保存提醒</button>
                      <button className="btn" onClick={() => setDraft({})}>全部用默认</button>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
          {rows && rows.length === 0 && <div className="empty">观察列表是空的。打开一家候选公司，结论选「加入观察」即可。</div>}
          {!rows && !error && <div className="empty">加载中…</div>}
        </div>
      </div>
      <p className="foot">价格来自 Yahoo 最新日线；估值指标 = 最近一次全市场筛选的正常化 FCF 按最新价格重算。提醒只在条件「刚满足」时发一次，条件消失后再次满足才会再发。</p>
    </div>
  );
}

function ScanBanner({ r }: { r: ScanResult }) {
  return (
    <div className="card banner">
      <b>扫描了 {r.companies} 家，新触发 {r.fired.length} 条</b>
      {r.delivery && <span className="muted small">{r.delivery.startsWith("outbox") ? `未配置邮件，已存为本地文件：${r.delivery.slice(8)}` : r.delivery === "sent" ? "邮件已发送" : r.delivery}</span>}
      {r.fired.map((f, i) => <div key={i} className="small">· {f.ticker} {f.rule}：{f.text}</div>)}
      {(r.errors ?? []).map((e) => <div key={e} className="small error">{e}</div>)}
    </div>
  );
}

// ---------- settings page ----------

export function AlertSettingsPage() {
  const [s, setS] = useState<AlertSettings | null>(null);
  const [meta, setMeta] = useState({ hasPassword: false, passwordFromEnv: false });
  const [logs, setLogs] = useState<AlertLogRow[]>([]);
  const [msg, setMsg] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    alertsApi.settings().then((r) => {
      setS(r.settings);
      setMeta({ hasPassword: r.hasPassword, passwordFromEnv: r.passwordFromEnv });
    });
    alertsApi.log().then(setLogs);
  }, []);
  useEffect(load, [load]);
  if (!s) return <div className="page"><p className="muted">加载中…</p></div>;

  const smtp = (k: keyof AlertSettings["smtp"], v: string | number) => setS({ ...s, smtp: { ...s.smtp, [k]: v } });
  const save = async () => {
    setBusy(true);
    try {
      await alertsApi.saveSettings(s);
      setMsg("已保存");
      load();
    } catch (e) {
      setMsg(String((e as Error).message));
    } finally {
      setBusy(false);
    }
  };
  const test = async () => {
    setBusy(true);
    setMsg("发送中…");
    try {
      await alertsApi.saveSettings(s);
      const r = await alertsApi.test();
      setMsg(r.delivery === "sent" ? `测试邮件已发到 ${s.smtp.to}` : `未配置 SMTP，测试邮件已存为本地文件：${r.delivery.slice(8)}`);
    } catch (e) {
      setMsg(String((e as Error).message));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page">
      <h2 className="ptitle">提醒设置</h2>
      <div className="settings-grid">
        <div className="card">
          <div className="card-head"><strong>扫描与邮件</strong><span>保存后立即生效</span></div>
          <div className="form">
            <label className="frm inline"><span>定时扫描</span>
              <select value={s.enabled ? "on" : "off"} onChange={(e) => setS({ ...s, enabled: e.target.value === "on" })}>
                <option value="on">开（网页服务运行时）</option><option value="off">关</option>
              </select></label>
            <label className="frm inline"><span>每隔</span>
              <span className="with-unit"><input type="number" min={1} value={s.intervalHours} onChange={(e) => setS({ ...s, intervalHours: Number(e.target.value) })} /><i>小时</i></span></label>
            <label className="frm"><span>收件人</span><input value={s.smtp.to} placeholder="you@example.com，多个用逗号分隔" onChange={(e) => smtp("to", e.target.value)} /></label>
            <label className="frm"><span>SMTP 服务器</span><input value={s.smtp.host} placeholder="smtp.gmail.com" onChange={(e) => smtp("host", e.target.value)} /></label>
            <label className="frm inline"><span>端口</span><input type="number" value={s.smtp.port} onChange={(e) => smtp("port", Number(e.target.value))} /></label>
            <label className="frm"><span>登录账号</span><input value={s.smtp.username} placeholder="通常就是发件邮箱" onChange={(e) => smtp("username", e.target.value)} /></label>
            <label className="frm"><span>密码 / 应用专用密码</span>
              <input type="password" value={s.smtp.password ?? ""} placeholder={meta.passwordFromEnv ? "已从环境变量 SMTP_PASSWORD 读取" : meta.hasPassword ? "已保存（留空不修改）" : "Gmail 需要「应用专用密码」"} onChange={(e) => smtp("password", e.target.value)} /></label>
            <label className="frm"><span>发件人（可选）</span><input value={s.smtp.from} placeholder="默认同登录账号" onChange={(e) => smtp("from", e.target.value)} /></label>
            <label className="frm"><span>邮件里的链接</span><input value={s.baseUrl} onChange={(e) => setS({ ...s, baseUrl: e.target.value })} /></label>
            <p className="small muted">密码保存在本机 data/screener.db（明文）。更稳妥的做法是不在这里填，改用环境变量 SMTP_PASSWORD 启动服务。没配置 SMTP 时，提醒邮件会存到 data/outbox/ 方便预览。</p>
            <div className="row-links">
              <button className="btn primary" disabled={busy} onClick={save}>保存</button>
              <button className="btn" disabled={busy} onClick={test}>发送测试邮件</button>
              {msg && <span className="small muted">{msg}</span>}
            </div>
          </div>
        </div>
        <div className="card">
          <div className="card-head"><strong>默认提醒阈值</strong><span>对观察列表里所有公司生效，可在每家公司单独覆盖</span></div>
          <div className="form">
            <RulesForm value={s.defaults} onChange={(defaults) => setS({ ...s, defaults })} />
            <div className="row-links"><button className="btn primary" disabled={busy} onClick={save}>保存</button></div>
          </div>
        </div>
        <AISettingsCard />
      </div>
      <div className="card">
        <div className="card-head"><strong>提醒记录</strong><span>最近 200 条</span></div>
        <div className="news">
          {logs.map((l) => (
            <div key={l.id} className="logrow">
              <span className="nmeta">{new Date(l.at).toLocaleString()}</span>
              <b>{l.ticker}</b>
              <span>{l.rule}：{l.message}</span>
              <span className="nmeta">{l.delivery.startsWith("outbox") ? "本地文件" : l.delivery === "sent" ? "已发送" : l.delivery}</span>
            </div>
          ))}
          {logs.length === 0 && <p className="profile-loading">还没有触发过提醒。</p>}
        </div>
      </div>
    </div>
  );
}

