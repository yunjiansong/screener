import { useEffect, useState } from "react";
import { Profile, fetchProfile, money } from "./api";

export function useProfile(ticker: string) {
  const [p, setP] = useState<Profile | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    setP(null);
    setError("");
    fetchProfile(ticker)
      .then((r) => live && setP(r))
      .catch((e) => live && setError(String(e.message ?? e)));
    return () => {
      live = false;
    };
  }, [ticker]);
  return { profile: p, error };
}

const loadingText = "正在从 SEC 年报、维基百科获取…（首次查看约 10–30 秒，之后走缓存）";

// Overview: what the company does (translated business section of the latest annual
// report, plus a Chinese Wikipedia intro) and where revenue comes from.
export function CompanyProfile({ profile, error, defaultOpen = false }: { profile: Profile | null; error: string; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const [original, setOriginal] = useState(false);
  const [kind, setKind] = useState(0);
  const head = (sub: string) => (
    <button className="collapse-head" aria-expanded={open} onClick={() => setOpen(!open)}>
      <span>
        <i className="chev">{open ? "▾" : "▸"}</i>
        <b>公司概况</b>
      </span>
      <span className="small muted">{sub}</span>
    </button>
  );
  if (error) return <div className="card">{head("")}<p className="profile-loading">公司概况获取失败：{error}</p></div>;
  if (!profile) return <div className="card">{head("")}<p className="profile-loading">{loadingText}</p></div>;
  const { overview: ov, wiki, revenue: rv } = profile;
  const breakdowns = rv?.breakdowns ?? [];
  const b = breakdowns[Math.min(kind, breakdowns.length - 1)];
  const partErrors = (profile.errors ?? []).filter((e) => !e.startsWith("新闻"));
  return (
    <div className="card">
      {head(ov ? `${ov.form} 业务章节 · 默认折叠` : "")}
      <div className="profile">
        <p className="summary">{profile.summary || "没有找到业务简介。"}</p>
        {open && (
          <>
            {wiki && !profile.summary.startsWith(wiki.text.slice(0, 20)) && (
              <p className="wiki">
                {wiki.text} <a href={wiki.url} target="_blank" rel="noreferrer" className="small">维基百科</a>
              </p>
            )}
            {ov && (
              <div className="overview">
                {(original ? ov.original : ov.text).split("\n").map((para, i) => (
                  <p key={i}>{para}</p>
                ))}
                <div className="row-links">
                  {ov.translated && <button className="link" onClick={() => setOriginal(!original)}>{original ? "看中文" : "看英文原文"}</button>}
                  <a className="small" href={ov.url} target="_blank" rel="noreferrer">{ov.form} 原文（{ov.filedAt} 提交）</a>
                </div>
              </div>
            )}
            <div className="rev">
              <div className="rev-head">
                <b>收入构成</b>
                {rv && <span className="muted small">财年截至 {rv.periodEnd} · 总收入 {money(rv.total, rv.currency)} · 来自 XBRL</span>}
                {breakdowns.length > 1 && (
                  <div className="seg" role="tablist" aria-label="收入拆分口径">
                    {breakdowns.map((x, i) => (
                      <button key={x.kind} role="tab" aria-selected={b === x} className={b === x ? "on" : ""} onClick={() => setKind(i)}>
                        {x.title}
                      </button>
                    ))}
                  </div>
                )}
              </div>
              {b ? (
                <>
                  <div className="rev-rows">
                    {b.items.map((s) => (
                      <div key={s.nameEn} className="rev-row" title={`${s.nameEn}：${money(s.value, rv!.currency)}，占总收入 ${(s.share * 100).toFixed(1)}%`}>
                        <span className="rname">
                          {s.name}
                          {s.name !== s.nameEn && <small>{s.nameEn}</small>}
                        </span>
                        <span className="rbar"><i style={{ width: `${Math.min(100, s.share * 100)}%` }} /></span>
                        <span className="rval">{money(s.value, rv!.currency)}</span>
                        <span className="rshare">{(s.share * 100).toFixed(1)}%</span>
                      </div>
                    ))}
                  </div>
                  {b.overlap && <p className="small muted">年报的分项里含小计（如"美洲"与"美国"），这里只保留互不重叠的前 3 项，其余并入"其他"。</p>}
                </>
              ) : (
                <p className="muted small">{rv ? "年报 XBRL 没有按分部 / 产品 / 地区拆分收入。" : "没有取到收入拆分。"}</p>
              )}
            </div>
            {partErrors.length > 0 && <p className="small muted">部分内容未取到：{partErrors.join("；")}</p>}
          </>
        )}
      </div>
    </div>
  );
}

const ago = (iso: string) => {
  const d = (Date.now() - new Date(iso).getTime()) / 864e5;
  return d < 1 ? "今天" : d < 2 ? "昨天" : d < 30 ? `${Math.floor(d)} 天前` : new Date(iso).toLocaleDateString();
};

const movePct = (m: number) => `${m > 0 ? "+" : ""}${(m * 100).toFixed(1)}%`;

export function News({ profile, error }: { profile: Profile | null; error: string }) {
  const [filter, setFilter] = useState<"all" | "move">("all");
  const [more, setMore] = useState(false);
  const newsErr = (profile?.errors ?? []).find((e) => e.startsWith("新闻"));
  const all = [...(profile?.news ?? [])].sort((a, b) => b.time.localeCompare(a.time));
  const items = filter === "move" ? all.filter((n) => n.move != null && Math.abs(n.move) >= 0.05) : all;
  return (
    <div className="card">
      <div className="card-head news-head">
        <strong>近期新闻</strong>
        <div>
          <span>
            近 60 天{profile ? ` · 已过滤 ${profile.newsHidden} 条低质量来源` : ""}
            {profile?.newsLang === "en" ? " · 中文报道太少，标题译自英文" : ""}
          </span>
          <div className="seg small-seg" role="radiogroup" aria-label="新闻筛选" title="有价格反应：新闻后首个交易日涨跌幅绝对值 ≥5%">
            {([["all", "全部"], ["move", "有价格反应"]] as const).map(([k, l]) => (
              <button key={k} role="radio" aria-checked={filter === k} className={filter === k ? "on" : ""} onClick={() => setFilter(k)}>
                {l}
              </button>
            ))}
          </div>
        </div>
      </div>
      {!profile && !error && <p className="profile-loading">正在获取新闻…（首次约 10–30 秒）</p>}
      {error && <p className="profile-loading">新闻获取失败：{error}</p>}
      {profile && !items.length && <p className="profile-loading">{filter === "move" ? "近 60 天没有首个交易日涨跌幅 ≥5% 的新闻" : newsErr ?? "近 60 天没有找到相关报道"}</p>}
      <div className="news">
        {(more ? items : items.slice(0, 8)).map((n) => (
          <a key={n.link} href={n.link} target="_blank" rel="noreferrer" title={n.titleEn}>
            <span className={`ntype t-${n.type}`}>{n.type}</span>
            <span className="ntitle"><span>{n.title}</span></span>
            <span className={`nmove ${n.move == null || Math.abs(n.move) < 0.02 ? "" : n.move > 0 ? "up" : "down"}`} title={n.moveDay ? `${n.moveDay} 收盘较前一交易日` : undefined}>
              {n.move != null && Math.abs(n.move) >= 0.02 ? movePct(n.move) : ""}
            </span>
            <span className="nmeta">{n.source} · {ago(n.time)}</span>
          </a>
        ))}
      </div>
      {items.length > 8 && <button className="link small fin-more" onClick={() => setMore(!more)}>{more ? "收起" : `再显示 ${items.length - 8} 条`}</button>}
    </div>
  );
}
