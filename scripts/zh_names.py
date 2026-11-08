#!/usr/bin/env python3
"""One-off: fill company_zh (cik -> Chinese name) from Wikidata.

Match by SEC CIK (Wikidata P5531) first, then by an active NYSE / Nasdaq ticker (P414 + P249).
Simplified labels are preferred; traditional ones are converted with OpenCC (t2s) when it is
importable. Labels without any Chinese character (e.g. "Adobe") are skipped.

usage: python3 scripts/zh_names.py data/screener.db [by_cik.csv by_ticker.csv]
  (the optional CSVs are saved results of the two queries, columns key/cik|ticker, lang, label)
"""
import csv, io, sqlite3, sys, urllib.parse, urllib.request

UA = "stock-screener-personal/0.1 (one-off Chinese name lookup)"
PREF = ["zh-cn", "zh-hans", "zh-sg", "zh", "zh-hk", "zh-tw", "zh-hant", "zh-mo"]
LANGS = ",".join(f'"{l}"' for l in PREF)
BY_CIK = f"""SELECT ?key ?lang ?label WHERE {{
  ?item wdt:P5531 ?key . ?item rdfs:label ?label .
  FILTER(LANG(?label) IN ({LANGS})) BIND(LANG(?label) AS ?lang) }}"""
BY_TICKER = f"""SELECT ?key ?lang ?label WHERE {{
  VALUES ?ex {{ wd:Q13677 wd:Q82059 }}
  ?item p:P414 ?st . ?st ps:P414 ?ex ; pq:P249 ?key .
  FILTER NOT EXISTS {{ ?st pq:P582 ?end }}
  ?item rdfs:label ?label .
  FILTER(LANG(?label) IN ({LANGS}))
  OPTIONAL {{ ?item rdfs:label ?en . FILTER(LANG(?en) = "en") }}
  BIND(LANG(?label) AS ?lang) }}"""

STOP = {"inc", "incorporated", "corp", "corporation", "co", "company", "ltd", "limited", "plc", "holdings", "holding",
        "group", "sa", "ag", "nv", "se", "the", "of", "and", "lp", "llc", "sab", "de", "cv", "as", "a", "s", "aktiengesellschaft", "spa", "bv", "kk"}


def tokens(name):
    import re
    return [t for t in re.findall(r"[a-z0-9]+", name.lower()) if t not in STOP]


def same_company(en, sec_name):
    """A ticker can be reused by another company, so the Wikidata English label has to agree."""
    a, b = tokens(en), tokens(sec_name)
    if not a or not b:
        return False
    # The Wikidata name must carry the whole SEC name (e.g. "Mercer" alone is the consultancy,
    # not Mercer International), or both are the same single word.
    return a == b or "".join(a).startswith("".join(b))

# Well-known names Wikidata lacks or labels oddly (applied last, source "manual").
MANUAL = {
    "PDD": "拼多多",
    "BZ": "BOSS直聘",
    "EDU": "新东方",
    "CRM": "赛富时",
    "INTU": "财捷",
    "BABA": "阿里巴巴",
    "JD": "京东",
    "BIDU": "百度",
    "NTES": "网易",
    "TME": "腾讯音乐",
    "BILI": "哔哩哔哩",
    "YUMC": "百胜中国",
    "NVO": "诺和诺德",
}

# Wrong matches seen on review (same ticker and English name, different company).
SKIP = {"EQT"}  # Wikidata's EQT label is the Swedish EQT AB (殷拓), not NYSE: EQT Corp

try:
    import opencc
    t2s = opencc.OpenCC("t2s").convert
except ImportError:
    t2s = lambda s: s


def has_han(s):
    return any("一" <= ch <= "鿿" for ch in s)


def query(q, path=None):
    if path:
        text = open(path, encoding="utf-8").read()
    else:
        url = "https://query.wikidata.org/sparql?" + urllib.parse.urlencode({"query": q})
        req = urllib.request.Request(url, headers={"Accept": "text/csv", "User-Agent": UA})
        with urllib.request.urlopen(req, timeout=120) as r:
            text = r.read().decode()
    best = {}
    for row in csv.DictReader(io.StringIO(text)):
        row["key"] = row.get("key") or row.get("cik") or row.get("ticker") or ""
        label = row["label"].strip()
        if not has_han(label) or row["lang"] not in PREF:
            continue
        rank = PREF.index(row["lang"])
        key = row["key"].strip().upper()
        if key not in best or rank < best[key][0]:
            best[key] = (rank, label, row.get("en") or "")
    return {k: (t2s(v[1]), v[2]) for k, v in best.items()}


def main(db_path, cik_csv=None, ticker_csv=None):
    by_cik = {int(k): v for k, v in query(BY_CIK, cik_csv).items() if k.isdigit()}
    by_ticker = query(BY_TICKER, ticker_csv)
    db = sqlite3.connect(db_path, timeout=30)
    db.execute("""CREATE TABLE IF NOT EXISTS company_zh (
        cik INTEGER PRIMARY KEY, ticker TEXT NOT NULL, name_zh TEXT NOT NULL, source TEXT NOT NULL)""")
    run = db.execute("SELECT MAX(id) FROM runs WHERE status='done'").fetchone()[0]
    rows = db.execute("SELECT cik, ticker, json_extract(summary_json, '$.name') FROM evaluations WHERE run_id=?", (run,)).fetchall()
    db.execute("DELETE FROM company_zh WHERE source LIKE 'wikidata:%'")
    n = {"cik": 0, "ticker": 0}
    for cik, ticker, sec_name in rows:
        name, src = by_cik.get(cik, (None, ""))[0], "wikidata:cik"
        if not name:
            name, en = by_ticker.get(ticker.upper(), (None, ""))
            src = "wikidata:ticker"
            if name and not same_company(en, sec_name or ""):
                name = None
        if not name or ticker.upper() in SKIP:
            continue
        n[src.split(":")[1]] += 1
        db.execute("INSERT OR REPLACE INTO company_zh(cik, ticker, name_zh, source) VALUES(?,?,?,?)", (cik, ticker, name, src))
    tick = {t.upper(): c for c, t, _ in rows}
    for t, name in MANUAL.items():
        if t in tick:
            db.execute("INSERT OR REPLACE INTO company_zh(cik, ticker, name_zh, source) VALUES(?,?,?,?)", (tick[t], t, name, "manual"))
    db.commit()
    print(f"{len(rows)} 家公司，匹配到中文名 {sum(n.values())} 家（按 CIK {n['cik']}，按代码 {n['ticker']}）")


if __name__ == "__main__":
    a = sys.argv[1:]
    main(a[0] if a else "data/screener.db", *(a[1:3]))
