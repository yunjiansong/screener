import { useEffect, useState } from "react";
import { FalsifyRule, MetricDef, api } from "./api";

let defsCache: Promise<MetricDef[]> | null = null;
const loadDefs = () => (defsCache ??= api.falsifyMetrics());

const ops: FalsifyRule["op"][] = ["<", "<=", ">", ">="];
const opLabel = { "<": "低于", "<=": "≤", ">": "高于", ">=": "≥" } as const;
const unit = (f?: MetricDef["Format"]) => (f === "pct" ? "%" : f === "usd" ? "$" : "");

// Editor for quantified falsification conditions. Percent metrics are typed as percentages
// and stored as fractions.
export function FalsifyRules({ rules, onChange }: { rules: FalsifyRule[]; onChange: (r: FalsifyRule[]) => void }) {
  const [defs, setDefs] = useState<MetricDef[]>([]);
  useEffect(() => {
    loadDefs().then(setDefs).catch(() => {});
  }, []);
  const def = (k: string) => defs.find((d) => d.Key === k);
  const toInput = (r: FalsifyRule) => (def(r.metric)?.Format === "pct" ? +(r.value * 100).toFixed(4) : r.value);
  const fromInput = (metric: string, v: number) => (def(metric)?.Format === "pct" ? v / 100 : v);
  const set = (i: number, r: FalsifyRule) => onChange(rules.map((x, k) => (k === i ? r : x)));
  return (
    <div className="frules">
      <div className="frules-head">
        <span>可量化的证伪条件</span>
        <small>每次数据或股价更新时自动检查，触发后出现在首页「证伪条件触发」并发提醒邮件</small>
      </div>
      {rules.map((r, i) => (
        <div key={i} className="frule">
          <select value={r.metric} aria-label="指标"
            onChange={(e) => set(i, { ...r, metric: e.target.value, value: fromInput(e.target.value, toInput(r)) })}>
            {defs.map((d) => <option key={d.Key} value={d.Key}>{d.Label}</option>)}
          </select>
          <select value={r.op} aria-label="比较" onChange={(e) => set(i, { ...r, op: e.target.value as FalsifyRule["op"] })}>
            {ops.map((o) => <option key={o} value={o}>{opLabel[o]}</option>)}
          </select>
          <span className="with-unit">
            {unit(def(r.metric)?.Format) === "$" && <i>$</i>}
            <input type="number" step="any" value={toInput(r)} aria-label="阈值"
              onChange={(e) => set(i, { ...r, value: fromInput(r.metric, Number(e.target.value)) })} />
            {unit(def(r.metric)?.Format) === "%" && <i>%</i>}
          </span>
          <button className="link danger" onClick={() => onChange(rules.filter((_, k) => k !== i))} aria-label="删除这条">✕</button>
        </div>
      ))}
      {rules.length < 10 && (
        <button className="link" onClick={() => onChange([...rules, { metric: "grossMargin", op: "<", value: 0.5 }])}>+ 添加条件</button>
      )}
    </div>
  );
}
