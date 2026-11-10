// Types mirror the Go JSON (backend/internal/screen, store, pipeline).

export type Verdict = "external" | "internal" | "unsure" | "watch" | "reject";

export interface Judgement {
  cik: number;
  ticker: string;
  verdict: Verdict | ""; // "" = notes only, no conclusion yet
  note: string; // legacy free-form note
  worry: string;
  why: string;
  falsify: string;
  reviewOn: string;
  updatedAt: string;
  falsifyRules?: FalsifyRule[];
  snapshot?: { at: string; verdict: string; metrics?: Record<string, number>; multiple?: number; ai?: Stance };
}

export type JudgementInput = Pick<Judgement, "verdict" | "note" | "worry" | "why" | "falsify" | "reviewOn"> & { falsifyRules: FalsifyRule[] };

// Quantified falsification condition; ratios are fractions (0.55 = 55%).
export interface FalsifyRule {
  metric: string;
  op: "<" | "<=" | ">" | ">=";
  value: number;
  note?: string;
}

export interface FalsifyHit {
  rule: FalsifyRule;
  actual: number;
  text: string;
}

export interface MetricDef {
  Key: string;
  Label: string;
  Format: "pct" | "usd" | "x" | "num";
}

export interface HomeItem {
  cik: number;
  ticker: string;
  text: string;
}

export interface HomeData {
  rows: Record<number, { isNew?: boolean; falsified?: FalsifyHit[]; reviewDue?: string }>;
  falsified: HomeItem[];
  review: HomeItem[];
  newIn: HomeItem[];
  removed: HomeItem[];
  baseline: Run | null;
}

export interface EarningsDate {
  date: string;
  estimated: boolean;
  source: "nasdaq" | "sec";
  note?: string;
}

export interface Hint {
  kind: "external" | "internal" | "positive";
  text: string;
}

export interface Summary {
  cik: number;
  ticker: string;
  name: string;
  nameZh?: string;
  sector: string;
  industry: string;
  currency: string;
  stage: number;
  score: number;
  quality: number;
  marketCap: number | null;
  drawdown: number | null;
  fcfYield: number | null;
  percentile: number | null;
  upside: number | null;
  downside: number | null;
  convexity: number | null;
  failedCheck: string;
  hints: Hint[] | null;
  judgement?: Judgement;
  ai?: AIBrief; // latest AI results (rollup, per engine, X sentiment)
}

export interface Funnel {
  universe: number;
  withFundamentals: number;
  quality: number;
  dislocation: number;
  convexity: number;
  marketFetched: number;
  seconds: number;
}

export interface Run {
  id: number;
  startedAt: string;
  finishedAt: string | null;
  status: "running" | "done" | "failed";
  message: string;
  funnel: Funnel | Record<string, never>;
}

export interface Check {
  warn?: boolean; // trend warning: flags the layer, never fails it
  layer: number;
  name: string;
  pass: boolean;
  value: string;
  rule: string;
  near?: boolean; // passed, but close to the threshold
}

export interface Year {
  source?: "yahoo"; // TTM summed from Yahoo quarterly figures (foreign filers)
  end: string;
  revenue?: number;
  grossProfit?: number;
  opIncome?: number;
  netIncome?: number;
  ocf?: number;
  capex?: number;
  capexImputed?: boolean;
  ttm?: boolean;
  fcf?: number;
  shares?: number;
  cash?: number;
  debt?: number;
  equity?: number;
}

export interface Point {
  date: string;
  price: number;
  multiple?: number;
}

export interface Valuation {
  price: number;
  high5y: number;
  drawdown: number;
  marketCapUsd: number;
  marketCapEstimated: boolean;
  normFcfUsd: number;
  fcfYield: number | null;
  multiple: number | null;
  percentile: number | null;
  median: number | null;
  p10: number | null;
  target: number | null;
  upside: number | null;
  downside: number | null;
  convexity: number | null;
  historyMonths: number;
  latestFcfVsAvg: number | null;
  fcfChangeSinceHigh?: number | null;
  basis: string; // e.g. "TTM 截至 2026-08-28" or "财年截至 2025-12-31"
  series: Point[];
}

export interface Evaluation {
  cik: number;
  ticker: string;
  name: string;
  nameZh?: string;
  exchange: string;
  currency: string;
  taxonomy: string;
  years: Year[];
  ttm?: Year[];
  sector: string;
  industry: string;
  quality: {
    score: number;
    grossMargin?: number | null;
    opMargin?: number | null;
    fcfMargin?: number | null;
    revenueCagr?: number | null;
    shareChange?: number | null;
    netCash?: boolean;
  };
  valuation?: Valuation;
  checks: Check[];
  stage: number;
  score: number;
  hints: Hint[] | null;
  notes: string[] | null;
  analysis?: Analysis;
  fx?: number; // reporting-currency units per USD for the latest period (non-USD reporters)
}

export interface Metric {
  name: string;
  value: number | null;
  asOf: string;
  format: "pct" | "x" | "days" | "num";
  tone: "good" | "warn" | "bad" | "";
  note: string;
  series: { end: string; ttm?: boolean; v: number | null }[] | null;
}

export interface Analysis {
  groups: { title: string; metrics: Metric[] }[];
  fscore: { value: number; max: number; items: { name: string; pass: boolean }[]; period: string } | null;
  zscore: { value: number; zone: "safe" | "grey" | "distress" } | null;
  reverseDcf: { impliedGrowth: number; discount: number; terminal: number; years: number; histRevenue: number | null; histFcf: number | null; evUsd?: number; byMarketCap?: number | null } | null;
  value?: ValueInputs;
}

export interface AttentionSeries {
  key: "wiki_en" | "wiki_zh" | "volume";
  label: string;
  unit: string;
  source: string;
  url?: string;
  days: { date: string; v: number }[];
  recent: number;
  base: number;
  ratio: number | null;
  note?: string;
}

async function json<T>(res: Response): Promise<T> {
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  return res.json() as Promise<T>;
}

export const api = {
  status: () => fetch("/api/status").then((r) => json<{ latest: Run | null; lastDone: Run | null }>(r)),
  companies: (minStage: number) =>
    fetch(`/api/companies?minStage=${minStage}`).then((r) => json<{ run: Run | null; items: Summary[] }>(r)),
  company: (ticker: string) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}`)
      .then((r) => json<{ evaluation: Evaluation; judgement: Judgement | null; run: Run; watching: boolean; nameZh?: string }>(r))
      .then((r) => ({ ...r, evaluation: { ...r.evaluation, nameZh: r.nameZh || undefined } })),
  peers: (ticker: string) => fetch(`/api/companies/${encodeURIComponent(ticker)}/peers`).then((r) => json<Peers>(r)),
  startRun: async () => {
    const r = await fetch("/api/runs", { method: "POST" });
    if (!r.ok && r.status !== 409) throw new Error(await r.text());
  },
  saveJudgement: (cik: number, ticker: string, j: JudgementInput) =>
    fetch(`/api/judgements/${cik}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ticker, ...j }),
    }).then((r) => json<Judgement>(r)),
  deleteJudgement: (cik: number) => fetch(`/api/judgements/${cik}`, { method: "DELETE" }),
  home: () => fetch("/api/home").then((r) => json<HomeData>(r)),
  earnings: () => fetch("/api/earnings").then((r) => json<Record<string, EarningsDate>>(r)),
  falsifyMetrics: () => fetch("/api/falsify/metrics").then((r) => json<MetricDef[]>(r)),
  // Foreign filers: the latest four quarters from Yahoo, re-evaluated into the latest run.
  refreshQuarterly: (ticker: string) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}/quarterly`, { method: "POST" }).then((r) =>
      json<{ end: string; stage: number; prevStage: number }>(r),
    ),
  // The latest Yahoo price, re-evaluated into the latest run (financials unchanged).
  refreshPrice: (ticker: string) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}/price`, { method: "POST" }).then((r) =>
      json<{ price?: number; prevPrice?: number; stage: number; prevStage: number }>(r),
    ),
};

export const verdicts: Verdict[] = ["external", "internal", "unsure", "watch", "reject"];

export const verdictLabel: Record<Verdict, string> = {
  external: "外部原因·错杀",
  internal: "内部恶化",
  unsure: "说不清",
  watch: "加入观察",
  reject: "排除",
};

export const pct = (v: number | null | undefined, digits = 0) =>
  v == null ? "—" : `${(v * 100).toFixed(digits)}%`;

export const times = (v: number | null | undefined) => (v == null ? "—" : `${v.toFixed(1)}x`);

export function money(v: number | null | undefined, currency = "USD") {
  if (v == null) return "—";
  if (v === 0) return "0";
  const sign = v < 0 ? "-" : "";
  const a = Math.abs(v);
  const prefix = currency === "USD" ? "$" : "";
  const suffix = currency === "USD" ? "" : ` ${currency}`;
  if (a >= 1e12) return `${sign}${prefix}${(a / 1e12).toFixed(2)}T${suffix}`;
  if (a >= 1e9) return `${sign}${prefix}${(a / 1e9).toFixed(1)}B${suffix}`;
  return `${sign}${prefix}${(a / 1e6).toFixed(0)}M${suffix}`;
}

export const hintLabel = { external: "外部因素", internal: "内部恶化", positive: "价值背离" } as const;

// Values this extreme are almost always a data problem (unit, ADR ratio, missing capex).
export const looksAnomalous = (s: Pick<Summary, "marketCap" | "convexity" | "fcfYield">) =>
  (s.marketCap != null && s.marketCap < 50e6) || (s.convexity != null && s.convexity > 100) || (s.fcfYield != null && s.fcfYield > 1);

export interface Segment {
  name: string;
  nameEn: string;
  value: number;
  share: number;
}

export interface Breakdown {
  kind: "segment" | "product" | "geo";
  title: string;
  items: Segment[];
  overlap: boolean;
}

export interface NewsItem {
  title: string;
  titleEn?: string;
  link: string;
  source: string;
  time: string;
  type: "财报" | "评级" | "公司" | "行业";
  move: number | null; // first-session close-to-close return after publication
  moveDay?: string;
}

export interface Profile {
  overview: { text: string; original: string; translated: boolean; form: string; filedAt: string; url: string } | null;
  wiki: { title: string; text: string; url: string } | null;
  attention: { series: AttentionSeries[] | null } | null;
  revenue: { periodEnd: string; currency: string; total: number; breakdowns: Breakdown[] | null; form: string; url: string } | null;
  news: NewsItem[] | null;
  newsLang: "zh" | "en" | "";
  newsHidden: number;
  summary: string;
  errors: string[] | null;
  fetchedAt: string;
}

export const fetchProfile = (ticker: string) =>
  fetch(`/api/companies/${encodeURIComponent(ticker)}/profile`).then((r) => json<Profile>(r));

// ---------- watchlist & alerts ----------

export interface Rules {
  priceBelow?: number;
  priceAbove?: number;
  dailyDrop?: number;
  drawdown?: number;
  fcfYield?: number;
  multiple?: number;
  upside?: number;
  newsMove?: number;
  stageDrop?: boolean;
  newQuarter?: boolean;
  review?: boolean;
}

export interface Snapshot {
  asOf?: string;
  price?: number;
  dayChange?: number | null;
  high5y?: number;
  drawdown?: number;
  marketCap?: number;
  multiple?: number | null;
  fcfYield?: number | null;
  percentile?: number | null;
  upside?: number | null;
  stage?: number;
  basis?: string;
  reviewOn?: string;
  error?: string;
}

export interface WatchRow {
  cik: number;
  ticker: string;
  name: string;
  addedAt: string;
  baseStage: number;
  rules: Rules;
  effective: Rules;
  snapshot: Snapshot;
  scannedAt: string;
  judgement: Judgement | null;
  ai: DiagnosisRow<DiagnosisResult> | null; // latest successful AI diagnosis
}

export interface AlertSettings {
  enabled: boolean;
  intervalHours: number;
  baseUrl: string;
  smtp: { host: string; port: number; username: string; password?: string; from: string; to: string };
  defaults: Rules;
}

export interface ScanResult {
  at: string;
  companies: number;
  fired: { ticker: string; rule: string; text: string }[];
  delivery: string;
  errors: string[] | null;
}

export interface AlertLogRow {
  id: number;
  at: string;
  ticker: string;
  rule: string;
  message: string;
  delivery: string;
}

const send = (url: string, method: string, body?: unknown) =>
  fetch(url, { method, headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) }).then(async (r) => {
    if (!r.ok) throw new Error((await r.text()) || r.statusText);
    const text = await r.text(); // 202 / 204 come back with an empty body
    return text ? JSON.parse(text) : null;
  });

export const alertsApi = {
  watchlist: () => fetch("/api/watchlist").then((r) => json<{ items: WatchRow[]; defaults: Rules }>(r)),
  watch: (cik: number, ticker: string, rules?: Rules) => send(`/api/watchlist/${cik}`, "PUT", { ticker, rules }),
  unwatch: (cik: number) => send(`/api/watchlist/${cik}`, "DELETE"),
  settings: () => fetch("/api/alerts/settings").then((r) => json<{ settings: AlertSettings; hasPassword: boolean; passwordFromEnv: boolean }>(r)),
  saveSettings: (s: AlertSettings) => send("/api/alerts/settings", "PUT", s),
  scan: () => send("/api/alerts/scan", "POST") as Promise<ScanResult>,
  test: () => send("/api/alerts/test", "POST") as Promise<{ delivery: string }>,
  log: () => fetch("/api/alerts/log").then((r) => json<AlertLogRow[]>(r)),
};

// ---------- AI diagnosis ----------

export type Stance = "mispriced" | "fair" | "trap" | "unclear";
export type AIKind = "diagnosis" | "rollup" | "xpulse";
export type EngineName = "codex" | "claude" | "agy" | "grok";

export interface DiagnosisResult {
  stance: Stance;
  confidence: number;
  summary: string;
  worry: string;
  temporary: string;
  bull: string[];
  bear: string[];
  falsify: string[];
  watch: string[];
  changeVsLast: string;
  dataIssues: string[];
  falsifiers?: Falsifier[];
  fairValue?: FairValue;
  causes?: { key: string; likelihood: "high" | "medium" | "low" | "unknown"; reason: string }[];
}

// The model's own intrinsic-value estimate, per share in the share-price unit.
export interface FairValue {
  low: number;
  base: number;
  high: number;
  method: string;
  assumptions: string;
}

// A falsification condition from AI output; metric/op are "" when not a tracked metric.
export interface Falsifier {
  text: string;
  metric: string;
  op: "" | "<" | "<=" | ">" | ">=";
  value: number;
}

export interface RollupResult {
  stance: Stance;
  confidence: number;
  summary: string;
  trend: string;
  stable: string[];
  shifts: string[];
  open: string[];
  checklist: string[];
  falsifiers?: Falsifier[];
}

export interface XPost {
  author: string;
  date: string;
  text: string;
  url: string;
  stance: "bullish" | "bearish" | "neutral";
  idDate?: string; // date encoded in the post ID
  suspect?: boolean; // link is not a post link, or its ID date contradicts the stated date
}

export interface XPulseResult {
  summary: string;
  sentiment: "bullish" | "bearish" | "mixed" | "neutral";
  sentimentScore: number;
  volume: string;
  narratives: string[];
  bull: string[];
  bear: string[];
  posts: XPost[];
  rumors: string[];
  coverage: string;
}

export interface DiagnosisRow<R = unknown> {
  id: number;
  cik: number;
  ticker: string;
  at: string;
  kind: AIKind;
  trigger: string;
  engine: EngineName;
  model: string;
  basis: string;
  price: number;
  result: R;
  durationMs: number;
  error: string;
  meta?: CallMeta;
}

export interface AISettings {
  engine: EngineName;
  model: string;
  enabled: boolean;
  intervalDays: number;
  onAlert: boolean;
  autoRollup: boolean;
  maxPerDay: number;
  timeoutMinutes: number;
  xpulseEnabled: boolean;
  xpulseModel: string;
}

export interface EngineStatus {
  name: EngineName;
  label: string;
  path: string;
  available: boolean;
  detail: string;
}

export interface AIStatus {
  settings: AISettings;
  engines: EngineStatus[];
  queue: number;
  active: Partial<Record<EngineName, { ticker: string; kind: AIKind; trigger: string }>>;
  today: number;
  batch: BatchStatus | null;
}

export interface PendingJob {
  kind: AIKind;
  engine: EngineName;
  active: boolean;
}

export interface AIVerdict {
  stance: Stance;
  confidence: number;
  summary: string;
  at: string;
}

export interface AIBrief {
  rollup?: AIVerdict;
  engines?: Partial<Record<EngineName, AIVerdict>>;
  xpulse?: number;
}

export interface BatchStatus {
  started: string;
  finished?: string;
  engines: EngineName[];
  xpulse: boolean;
  rollup: boolean;
  companies: { cik: number; ticker: string; step: "waiting" | "xpulse" | "diagnosis" | "rollup" | "done"; errors?: string[] }[];
}

// Engines used only when the person triggers them (limited quota): never in batch / scheduled / automatic runs.
export const manualOnly: EngineName[] = ["claude"];
export const engineOrder: EngineName[] = ["codex", "claude", "agy", "grok"];
export const engineShort: Record<EngineName, string> = { codex: "ChatGPT", claude: "Claude", agy: "Gemini", grok: "Grok" };

export const stanceLabel: Record<Stance, string> = { mispriced: "错杀", fair: "定价合理", trap: "价值陷阱", unclear: "看不清" };
export const stanceChip: Record<Stance, string> = { mispriced: "positive", fair: "neutral", trap: "internal", unclear: "warn" };
export const triggerLabel = (t: string) =>
  t === "manual" ? "手动" : t === "schedule" ? "定期" : t === "auto" ? "自动汇总" : t.startsWith("alert:") ? `提醒触发：${t.slice(6)}` : t;

let statusCache: Promise<AIStatus> | null = null;

export const aiApi = {
  list: (ticker: string) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}/diagnoses`).then((r) =>
      json<{ items: DiagnosisRow[]; pending: PendingJob[]; engine: EngineName }>(r),
    ),
  request: (ticker: string, kind: AIKind, engine?: EngineName) =>
    send(`/api/companies/${encodeURIComponent(ticker)}/diagnoses?kind=${kind}${engine ? `&engine=${engine}` : ""}`, "POST"),
  prompt: (ticker: string) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}/diagnoses/prompt`).then(async (r) => {
      if (!r.ok) throw new Error(await r.text());
      return r.text();
    }),
  remove: (id: number) => send(`/api/diagnoses/${id}`, "DELETE"),
  status: (fresh = false) => {
    if (fresh || !statusCache) statusCache = fetch("/api/ai/status").then((r) => json<AIStatus>(r));
    return statusCache;
  },
  saveSettings: (s: AISettings) => send("/api/ai/settings", "PUT", s).then(() => { statusCache = null; }),
  batch: (body: { top?: number; tickers?: string[]; engines: EngineName[]; xpulse: boolean; rollup: boolean }) => send("/api/ai/batch", "POST", body),
};

export type PeerMetric = "grossMargin" | "opMargin" | "fcfMargin" | "revenueCagr" | "roic" | "fcfYield" | "multiple" | "drawdown";
export interface PeerRow {
  cik: number;
  ticker: string;
  name: string;
  marketCap: number;
  stage: number;
  metrics: Partial<Record<PeerMetric, number | null>>;
}
export interface Peers {
  available: boolean;
  reason?: string;
  group?: string;
  level?: "industry" | "sector";
  count?: number;
  selfRank?: number;
  self?: PeerRow;
  median?: Partial<Record<PeerMetric, number | null>>;
  leaders?: PeerRow[];
}

// How an AI CLI call ran, as the engine reported it (Grok / Claude report usage; Codex doesn't).
export interface CallMeta {
  command: string;
  search: boolean;
  tools?: string[];
  maxTurns?: number;
  turns?: number;
  modelCalls?: number;
  inputTokens?: number;
  outputTokens?: number;
  costUsd?: number;
}

// Per-share inputs for the intrinsic-value formulas (USD per traded share).
export interface ValueInputs {
  price: number;
  fcf: number;
  latestFcf: number | null;
  eps: number | null;
  latestEps: number | null;
  bookValue: number | null;
  netCash: number;
  histFcfGrowth: number | null;
  histRevGrowth: number | null;
  medianMultiple: number | null;
  p10Multiple: number | null;
}

export type ValueParams = Partial<{
  mos: number;
  discount: number;
  terminal: number;
  years: number;
  growth: number;
  aaa: number;
  multiple: number;
  fcfBase: "norm" | "latest";
  own: number;
  ownNote: string;
  off: string[];
}>;

export const valueApi = {
  get: (ticker: string) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}/value-params`).then((r) => json<{ company: ValueParams | null; default: ValueParams | null }>(r)),
  save: (ticker: string, p: ValueParams | null) => send(`/api/companies/${encodeURIComponent(ticker)}/value-params`, "PUT", p),
  saveDefault: (p: ValueParams | null) => send("/api/value-params/default", "PUT", p),
};

// ---------- skills: the person's own analysis templates, run over facts only ----------

export type IncludeKey = "screen" | "judgement" | "lastAI" | "xpulse";
export const includeLabel: Record<IncludeKey, { label: string; tip: string }> = {
  screen: { label: "系统筛选结论", tip: "通过层数、各项检查、系统线索、目标倍数与上行 / 下行 / 凸性：系统按「凸性错杀」框架得出的判断" },
  judgement: { label: "我的判断", tip: "你在右栏写的结论、担忧、理由和证伪条件" },
  lastAI: { label: "上次 AI 诊断", tip: "最近一次内置诊断的结论、信心和一句话" },
  xpulse: { label: "X 舆情", tip: "最近 10 天内 Grok 搜索的 X 观点汇总" },
};

export interface Skill {
  id: number;
  name: string;
  description: string;
  instructions: string;
  include: IncludeKey[];
  engine: EngineName | "";
  createdAt: string;
  updatedAt: string;
  runs: number;
}
export type SkillInput = Pick<Skill, "name" | "description" | "instructions" | "include" | "engine">;

export interface SkillRun {
  id: number;
  skillId: number;
  skillName: string;
  instructions: string;
  include: IncludeKey[];
  ticker: string;
  at: string;
  engine: EngineName;
  model: string;
  price: number;
  basis: string;
  output: string;
  durationMs: number;
  error: string;
  meta?: CallMeta;
}

export interface SkillPending {
  kind: "skill";
  engine: EngineName;
  active: boolean;
  skillId: number;
}

const textOf = async (r: Response) => {
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  return r.text();
};

export const skillApi = {
  list: () => fetch("/api/skills").then((r) => json<{ items: Skill[] }>(r)),
  create: (s: SkillInput) => send("/api/skills", "POST", s) as Promise<Skill>,
  update: (id: number, s: SkillInput) => send(`/api/skills/${id}`, "PUT", s) as Promise<Skill>,
  remove: (id: number) => send(`/api/skills/${id}`, "DELETE"),
  runs: (ticker: string) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}/skill-runs`).then((r) => json<{ items: SkillRun[]; pending: SkillPending[] | null; engine: EngineName }>(r)),
  run: (ticker: string, id: number, engine?: EngineName) =>
    send(`/api/companies/${encodeURIComponent(ticker)}/skills/${id}/run${engine ? `?engine=${engine}` : ""}`, "POST"),
  prompt: (ticker: string, id?: number) =>
    fetch(`/api/companies/${encodeURIComponent(ticker)}/skill-prompt${id ? `?skill=${id}` : ""}`).then(textOf),
  removeRun: (id: number) => send(`/api/skill-runs/${id}`, "DELETE"),
};
