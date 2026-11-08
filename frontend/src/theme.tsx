import { useEffect, useState } from "react";

// 主题：跟随系统 / 浅色 / 深色。选择存在 localStorage，index.html 的内联脚本在
// 首屏前先套用一次，避免刷新时闪一下另一种颜色。
export type ThemeMode = "system" | "light" | "dark";
const KEY = "theme";
const mq = window.matchMedia("(prefers-color-scheme: dark)");

function stored(): ThemeMode {
  const v = localStorage.getItem(KEY);
  return v === "light" || v === "dark" ? v : "system";
}

function apply(mode: ThemeMode) {
  const dark = mode === "dark" || (mode === "system" && mq.matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}

export function ThemeToggle() {
  const [mode, setMode] = useState<ThemeMode>(stored);
  useEffect(() => {
    apply(mode);
    if (mode === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, mode);
    if (mode !== "system") return;
    const on = () => apply("system");
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [mode]);
  const opts: [ThemeMode, string][] = [["system", "跟随系统"], ["light", "浅色"], ["dark", "深色"]];
  return (
    <div className="seg theme" role="group" aria-label="主题">
      {opts.map(([k, label]) => (
        <button key={k} className={mode === k ? "on" : ""} aria-pressed={mode === k} onClick={() => setMode(k)}>{label}</button>
      ))}
    </div>
  );
}
