package profile

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"stockscreener/internal/fetch"
	"stockscreener/internal/yahoo"
)

// ---------- Wikidata → Chinese Wikipedia ----------

const (
	wdNYSE   = "wd:Q13677"
	wdNasdaq = "wd:Q82059"
)

// wikiTitles finds the company's Wikidata item by SEC CIK, or by its NYSE/Nasdaq ticker,
// and returns its Chinese and English Wikipedia article titles ("" when absent).
func wikiTitles(ctx context.Context, wc *fetch.Client, co Company) (zh, en string, err error) {
	ticker := strings.ReplaceAll(co.Ticker, `"`, "")
	q := fmt.Sprintf(`SELECT ?zh ?en WHERE {
  { ?item wdt:P5531 "%010d" } UNION { ?item p:P414 ?s . ?s ps:P414 ?ex ; pq:P249 "%s" . VALUES ?ex { %s %s } }
  OPTIONAL { ?zh schema:about ?item ; schema:isPartOf <https://zh.wikipedia.org/> . }
  OPTIONAL { ?en schema:about ?item ; schema:isPartOf <https://en.wikipedia.org/> . }
} LIMIT 5`, co.CIK, ticker, wdNYSE, wdNasdaq)
	body, err := wc.Get(ctx, "https://query.wikidata.org/sparql?format=json&query="+url.QueryEscape(q),
		fmt.Sprintf("profile/wikidata2-%d.json", co.CIK), 30*24*time.Hour)
	if err != nil {
		return "", "", err
	}
	var res struct {
		Results struct {
			Bindings []map[string]struct{ Value string } `json:"bindings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", "", err
	}
	title := func(page string) string {
		if page == "" {
			return ""
		}
		t, _ := url.PathUnescape(page[strings.LastIndex(page, "/")+1:])
		return t
	}
	for _, b := range res.Results.Bindings {
		if zh == "" {
			zh = title(b["zh"].Value)
		}
		if en == "" {
			en = title(b["en"].Value)
		}
	}
	return zh, en, nil
}

// wiki returns the intro of the company's Chinese Wikipedia article in Simplified Chinese.
func wiki(ctx context.Context, wc *fetch.Client, co Company) (*Wiki, error) {
	title, _, err := wikiTitles(ctx, wc, co)
	if err != nil || title == "" {
		return nil, err
	}
	api := "https://zh.wikipedia.org/w/api.php?action=query&prop=extracts&exintro=1&explaintext=1&variant=zh-cn&redirects=1&format=json&titles=" + url.QueryEscape(title)
	body, err := wc.Get(ctx, api, fmt.Sprintf("profile/zhwiki-%d.json", co.CIK), 30*24*time.Hour)
	if err != nil {
		return nil, err
	}
	var ex struct {
		Query struct {
			Pages map[string]struct {
				Title   string `json:"title"`
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &ex); err != nil {
		return nil, err
	}
	for _, p := range ex.Query.Pages {
		text := strings.TrimSpace(p.Extract)
		if r := []rune(text); len(r) > 900 {
			text = string(r[:900]) + "…"
		}
		if text != "" {
			return &Wiki{Title: p.Title, Text: text, URL: "https://zh.wikipedia.org/wiki/" + url.PathEscape(title) + "?variant=zh-cn"}, nil
		}
	}
	return nil, nil
}

// ---------- news ----------

type NewsItem struct {
	Title   string   `json:"title"`
	TitleEn string   `json:"titleEn,omitempty"` // original, when Title is a translation
	Link    string   `json:"link"`
	Source  string   `json:"source"`
	Time    string   `json:"time"` // RFC3339
	Type    string   `json:"type"` // 财报 | 评级 | 行业 | 公司
	Move    *float64 `json:"move"` // close-to-close return on the first trading day at/after publication
	MoveDay string   `json:"moveDay,omitempty"`
}

var reSuffix = regexp.MustCompile(`(?i)[,.]?\s+(inc|incorporated|corp|corporation|co|company|ltd|limited|plc|s\.?a|n\.?v|ag|se|holdings?|group|/[a-z]+/)\.?$`)

// searchName strips legal suffixes: "lululemon athletica inc." -> "lululemon athletica".
// Stripping stops before the name gets so short it is ambiguous ("PDD" is also a streamer).
func searchName(name string) string {
	n := strings.TrimSpace(name)
	for {
		m := strings.TrimSpace(reSuffix.ReplaceAllString(n, ""))
		if m == n || len(m) < 6 {
			break
		}
		n = m
	}
	if strings.HasPrefix(strings.ToLower(n), "the ") {
		n = n[4:]
	}
	return n
}

type rss struct {
	Channel struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
			Source  string `xml:"source"`
		} `xml:"item"`
	} `xml:"channel"`
}

func googleNews(ctx context.Context, web *fetch.Client, query, locale, key string) ([]NewsItem, error) {
	params := map[string]string{"zh": "hl=zh-CN&gl=CN&ceid=CN:zh-Hans", "en": "hl=en-US&gl=US&ceid=US:en"}[locale]
	u := "https://news.google.com/rss/search?q=" + url.QueryEscape(query+" when:60d") + "&" + params
	h := sha1.Sum([]byte(u))
	body, err := web.Get(ctx, u, "profile/news-"+key+"-"+hex.EncodeToString(h[:6])+".xml", 3*time.Hour)
	if err != nil {
		return nil, err
	}
	var feed rss
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	var out []NewsItem
	for _, it := range feed.Channel.Items {
		t, err := time.Parse(time.RFC1123, it.PubDate)
		if err != nil {
			t, _ = time.Parse(time.RFC1123Z, it.PubDate)
		}
		title := strings.TrimSpace(strings.TrimSuffix(it.Title, " - "+it.Source))
		out = append(out, NewsItem{Title: title, Link: it.Link, Source: it.Source, Time: t.UTC().Format(time.RFC3339)})
	}
	return out, nil
}

// ---------- quality filter ----------

var (
	// Quote pages, price converters, option-flow bots and personal check-in posts.
	reNoise     = regexp.MustCompile(`(?i)^\$|汇率|价格 \||期权|实时行情|股票价格|股价走势图|报价|打卡|price today|price chart|stock quote|options? (flow|activity)`)
	noiseSource = []string{"CoinMarketCap", "CryptoRank", "Moomoo", "雪球", "MarketBeat", "Stock Titan"}
	reNorm      = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

func noise(n NewsItem) bool {
	return reNoise.MatchString(n.Title) || slices.Contains(noiseSource, n.Source)
}

func bigrams(s string) map[string]bool {
	r := []rune(reNorm.ReplaceAllString(strings.ToLower(s), ""))
	out := map[string]bool{}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])] = true
	}
	return out
}

func similar(a, b map[string]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter)/float64(len(a)+len(b)-inter) >= 0.6
}

// clean drops low-quality sources and near-duplicate headlines (syndicated copies),
// returning at most limit items and how many were dropped.
func clean(items []NewsItem, limit int) ([]NewsItem, int) {
	var out []NewsItem
	var seen []map[string]bool
	hidden := 0
next:
	for _, it := range items {
		if noise(it) {
			hidden++
			continue
		}
		g := bigrams(it.Title)
		for _, s := range seen {
			if similar(g, s) {
				hidden++
				continue next
			}
		}
		seen = append(seen, g)
		if len(out) < limit {
			out = append(out, it)
		}
	}
	return out, hidden
}

// ---------- type & price reaction ----------

var newsTypes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"财报", regexp.MustCompile(`(?i)财报|业绩|营收|季度|季报|年报|指引|净利|利润|earnings|revenue|quarter|results|guidance|\bEPS\b|profit|sales`)},
	{"评级", regexp.MustCompile(`(?i)评级|目标价|上涨空间|买入|卖出|增持|减持|分析师|upside|upgrade|downgrade|rating|price target|analyst|overweight|underweight|outperform`)},
	{"行业", regexp.MustCompile(`(?i)行业|板块|同行|关税|监管|政策|sector|industry|peers|tariff|regulat|rivals?\b|competitors?`)},
}

func classify(title string) string {
	for _, t := range newsTypes {
		if t.re.MatchString(title) {
			return t.name
		}
	}
	return "公司"
}

var nyc, _ = time.LoadLocation("America/New_York")

// reaction finds the first session that could react to the story (same day if published
// before the 16:00 New York close, otherwise the next) and its close-to-close return.
func reaction(published string, bars []yahoo.Bar) (*float64, string) {
	t, err := time.Parse(time.RFC3339, published)
	if err != nil || nyc == nil || len(bars) < 2 {
		return nil, ""
	}
	t = t.In(nyc)
	day := t.Format(time.DateOnly)
	if t.Hour() >= 16 {
		day = t.AddDate(0, 0, 1).Format(time.DateOnly)
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].Date >= day {
			r := bars[i].Close/bars[i-1].Close - 1
			return &r, bars[i].Date
		}
	}
	return nil, ""
}

// news prefers Chinese-language coverage; small US companies rarely have any, so it
// falls back to English headlines with machine-translated titles.
func news(ctx context.Context, c Clients, co Company, zhName string) ([]NewsItem, string, int, error) {
	en := searchName(co.Name)
	q := fmt.Sprintf(`"%s"`, en)
	if zhName != "" && zhName != en {
		q = fmt.Sprintf(`"%s" OR "%s"`, en, zhName)
	}
	lang := "zh"
	raw, err := googleNews(ctx, c.Web, q, "zh", co.Ticker)
	items, hidden := clean(raw, 12)
	if err != nil || len(items) < 4 {
		raw, err = googleNews(ctx, c.Web, q+" stock", "en", co.Ticker)
		if err != nil {
			return nil, "", 0, err
		}
		items, hidden = clean(raw, 12)
		lang = "en"
		titles := make([]string, len(items))
		for i, it := range items {
			titles[i] = it.Title
		}
		if len(titles) > 0 {
			if t, err := translate(ctx, c.Web, strings.Join(titles, "\n")); err == nil {
				if parts := strings.Split(strings.TrimSpace(t), "\n"); len(parts) == len(items) {
					for i := range items {
						items[i].TitleEn, items[i].Title = items[i].Title, strings.TrimSpace(parts[i])
					}
				}
			}
		}
	}
	var bars []yahoo.Bar
	if c.Yahoo != nil {
		if ch, err := c.Yahoo.DailyChart(ctx, co.Ticker); err == nil {
			bars = ch.Bars
		}
	}
	for i := range items {
		items[i].Type = classify(items[i].TitleEn + " " + items[i].Title)
		items[i].Move, items[i].MoveDay = reaction(items[i].Time, bars)
	}
	return items, lang, hidden, nil
}
