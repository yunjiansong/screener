package diagnose

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/model"
	"stockscreener/internal/profile"
	"stockscreener/internal/screen"
	"stockscreener/internal/store"
)

// Result is one diagnosis. The JSON schema below mirrors it; every field is required so
// strict structured-output modes accept it.
type Result struct {
	Stance       string      `json:"stance"`     // mispriced | fair | trap | unclear
	Confidence   int         `json:"confidence"` // 1-5
	Summary      string      `json:"summary"`
	Worry        string      `json:"worry"`     // what the market is pricing in
	Temporary    string      `json:"temporary"` // why it may (or may not) be temporary / external
	Bull         []string    `json:"bull"`
	Bear         []string    `json:"bear"`
	Falsify      []string    `json:"falsify"` // facts that would prove the mispricing thesis wrong
	Watch        []string    `json:"watch"`   // what to monitor next
	ChangeVsLast string      `json:"changeVsLast"`
	DataIssues   []string    `json:"dataIssues"`
	Falsifiers   []Falsifier `json:"falsifiers"`
	FairValue    *FairValue  `json:"fairValue,omitempty"`
	Causes       []Cause     `json:"causes,omitempty"`
}

// Cause is the model's view on one standard hypothesis for why the stock is cheap.
type Cause struct {
	Key        string `json:"key"`
	Likelihood string `json:"likelihood"` // high | medium | low | unknown
	Reason     string `json:"reason"`
}

// CauseKeys are the hypotheses the detail page lists; the model rates each one.
var CauseKeys = []struct{ Key, Label string }{
	{"deterioration", "基本面正在恶化（收入、利润率、FCF 下滑）"},
	{"cyclical", "周期顶部利润（当前利润处于周期高位，将回落）"},
	{"leverage", "财务风险（负债、利息、偿债压力）"},
	{"accounting", "盈利质量 / 会计疑点（应计高、现金流跟不上利润、股权激励撑起 FCF）"},
	{"dilution", "股东被稀释（股本持续增加）"},
	{"disruption", "结构性颠覆 / 护城河受损（新技术、新对手、需求转移）"},
	{"slowdown", "增长放缓导致估值下修（从成长股变成熟股）"},
	{"derating", "估值从高位回落（之前太贵，业绩没坏）"},
	{"industry", "行业 / 板块整体下跌（宏观、利率、行业周期）"},
	{"company", "公司特有事件（管理层、产品、指引下调等，同行没跌这么多）"},
	{"event", "一次性 / 外部事件（诉讼、监管、关税、地缘政治、汇率）"},
	{"neglect", "被市场忽视（关注度低、流动性差、没人覆盖）"},
}

// FairValue is the model's own intrinsic-value estimate, per share in the same unit as the share price.
type FairValue struct {
	Low         float64 `json:"low"`
	Base        float64 `json:"base"`
	High        float64 `json:"high"`
	Method      string  `json:"method"`
	Assumptions string  `json:"assumptions"`
}

// Falsifier is a falsification condition the person can adopt with one click.
type Falsifier struct {
	Text   string  `json:"text"`
	Metric string  `json:"metric"`
	Op     string  `json:"op"`
	Value  float64 `json:"value"`
}

// Rollup summarizes several diagnoses of the same company.
type Rollup struct {
	Stance     string      `json:"stance"`
	Confidence int         `json:"confidence"`
	Summary    string      `json:"summary"`
	Trend      string      `json:"trend"`     // how the view moved over time and why
	Stable     []string    `json:"stable"`    // points every diagnosis agreed on
	Shifts     []string    `json:"shifts"`    // what changed between diagnoses
	Open       []string    `json:"open"`      // unresolved questions for the person
	Checklist  []string    `json:"checklist"` // what to verify before acting
	Falsifiers []Falsifier `json:"falsifiers"`
}

func strArr(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func objectSchema(props map[string]any) []byte {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": req, "properties": props})
	return b
}

var stanceEnum = map[string]any{"type": "string", "enum": []string{"mispriced", "fair", "trap", "unclear"},
	"description": "mispriced=错杀（价格被外部或暂时因素压低） fair=定价合理 trap=价值陷阱（下跌反映了真实恶化） unclear=数据不足以判断"}
var confidence = map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "description": "1=很不确定 5=很有把握"}

// falsifiersSchema: falsification conditions in a form the system can check automatically.
// metric/op are "none" when the condition is not one of the tracked metrics (Gemini's API
// rejects an empty string in an enum); normalizeFalsifiers turns "none" back into "".
func falsifiersSchema() map[string]any {
	keys := []string{noMetric}
	for _, d := range screen.FalsifyMetricDefs {
		keys = append(keys, d.Key)
	}
	return map[string]any{"type": "array", "description": "可量化的证伪条件（最多 4 条），能对应到下列指标的请填 metric/op/value，系统会自动盯",
		"items": json.RawMessage(objectSchema(map[string]any{
			"text":   map[string]any{"type": "string", "description": "一句话，例如「TTM 毛利率跌破 55%」"},
			"metric": map[string]any{"type": "string", "enum": keys, "description": metricHelp()},
			"op":     map[string]any{"type": "string", "enum": []string{noMetric, "<", "<=", ">", ">="}},
			"value":  map[string]any{"type": "number", "description": "阈值；比例用小数（55% 写 0.55），股价写美元，F 分数写整数；metric 为 none 时填 0"},
		}))}
}

const noMetric = "none"

// normalizeFalsifiers rewrites the schema's "none" placeholder to "" in metric/op, so stored
// results keep meaning "not a tracked metric" as an empty string.
func normalizeFalsifiers(js json.RawMessage) json.RawMessage {
	var top map[string]json.RawMessage
	if json.Unmarshal(js, &top) != nil || top["falsifiers"] == nil {
		return js
	}
	var fs []map[string]any
	if json.Unmarshal(top["falsifiers"], &fs) != nil {
		return js
	}
	for _, f := range fs {
		for _, k := range []string{"metric", "op"} {
			if f[k] == noMetric {
				f[k] = ""
			}
		}
	}
	b, err := json.Marshal(fs)
	if err != nil {
		return js
	}
	top["falsifiers"] = b
	if out, err := json.Marshal(top); err == nil {
		return out
	}
	return js
}

func causeKeyList() []string {
	out := make([]string, len(CauseKeys))
	for i, c := range CauseKeys {
		out[i] = c.Key
	}
	return out
}

func causeHelp() string {
	var b strings.Builder
	for _, c := range CauseKeys {
		fmt.Fprintf(&b, "%s=%s；", c.Key, c.Label)
	}
	return b.String()
}

func metricHelp() string {
	var b strings.Builder
	b.WriteString("对应的系统指标：")
	for _, d := range screen.FalsifyMetricDefs {
		fmt.Fprintf(&b, "%s=%s；", d.Key, d.Label)
	}
	b.WriteString("对应不上则 metric 和 op 都填 none")
	return b.String()
}

var diagnosisSchema = objectSchema(map[string]any{
	"falsifiers": falsifiersSchema(),
	"stance":     stanceEnum, "confidence": confidence,
	"summary":      map[string]any{"type": "string", "description": "一句话结论，60 字以内"},
	"worry":        map[string]any{"type": "string", "description": "市场在担心什么、价格里定价了什么"},
	"temporary":    map[string]any{"type": "string", "description": "这个担忧是暂时/外部的，还是公司内在恶化？给出理由"},
	"bull":         strArr("看多理由，每条一句，引用数据"),
	"bear":         strArr("看空理由，每条一句，引用数据"),
	"falsify":      strArr("什么事实出现就说明错杀判断错了（可观察、可验证）"),
	"watch":        strArr("接下来要盯的指标或事件，含时间点"),
	"changeVsLast": map[string]any{"type": "string", "description": "和上次诊断相比结论或依据的变化；没有上次诊断则写「首次诊断」"},
	"dataIssues":   strArr("数据里看起来可疑、缺失或需要人工核对的地方"),
	"causes": map[string]any{"type": "array", "description": "对每个可能原因各给一条判断（全部 12 个 key 都要出现）：" + causeHelp(),
		"items": json.RawMessage(objectSchema(map[string]any{
			"key":        map[string]any{"type": "string", "enum": causeKeyList()},
			"likelihood": map[string]any{"type": "string", "enum": []string{"high", "medium", "low", "unknown"}, "description": "high=很可能是主因 medium=有一定作用 low=不像 unknown=材料不足以判断"},
			"reason":     map[string]any{"type": "string", "description": "一句话依据，引用数据或公开信息，40 字以内"},
		}))},
	"fairValue": json.RawMessage(objectSchema(map[string]any{
		"low":         map[string]any{"type": "number", "description": "悲观情景的每股内在价值，和股价同一单位（美元/股）"},
		"base":        map[string]any{"type": "number", "description": "基准情景的每股内在价值"},
		"high":        map[string]any{"type": "number", "description": "乐观情景的每股内在价值"},
		"method":      map[string]any{"type": "string", "description": "用的估值方法，例如「两阶段 FCF 折现」「EPV」「同业倍数」，可组合"},
		"assumptions": map[string]any{"type": "string", "description": "关键假设：增速、折现率、倍数、FCF 起点等，以及为什么这么取，100 字以内"},
	})),
})

var rollupSchema = objectSchema(map[string]any{
	"falsifiers": falsifiersSchema(),
	"stance":     stanceEnum, "confidence": confidence,
	"summary":   map[string]any{"type": "string", "description": "综合所有诊断后的一句话结论，60 字以内"},
	"trend":     map[string]any{"type": "string", "description": "判断随时间如何变化、为什么变"},
	"stable":    strArr("历次诊断一致认同的要点"),
	"shifts":    strArr("历次诊断之间发生变化的要点，注明日期"),
	"open":      strArr("仍未解决、需要投资者自己判断的问题"),
	"checklist": strArr("采取行动前要核实的事项"),
})

const framework = `你在为一位个人投资者做「第二意见」。他的选股框架叫「凸性错杀」：
- 基本面硬：长期稳定的高质量现金流（FCF、毛利率稳定、ROIC 高、负债低、不过度稀释）。
- 价格被困：股价因为外部或暂时的原因大幅下跌，估值处于自身历史低位。
- 下行有限上行大：估值回到历史中位数的上涨空间远大于再跌的空间。
系统只负责筛出候选和线索，最终「是不是错杀」由他自己判断。你的任务是帮他把问题想清楚，而不是替他下决定。

要求：
1. 只依据下面给出的数据，以及你对这家公司和行业截至训练时的一般了解；凡是数据里没有、靠你记忆的事实，明确标注「据公开信息」并承认可能过时。不要编造数字。
2. 不要运行命令、不要读写文件、不要联网，直接根据材料回答。
3. 区分「市场担心的事」和「已经在财务数据里体现的事」。如果最近 12 个月（TTM）的 FCF、利润率已经明显恶化，要直说，这更像价值陷阱。
4. 正反两面都写，看空理由不能敷衍。证伪条件必须具体、可观察。
5. fairValue：用「每股估值输入」自己估一个每股内在价值区间（悲观 / 基准 / 乐观），方法和假设自选并写清楚；不要照抄系统的目标倍数，数据不够就把区间放宽并在假设里说明。
6. causes：股价低迷的原因有多种可能，逐个判断，不要只挑一个；数据和常识都判断不了的标 unknown，不要硬猜。industry 和 company 两项用「同行对比」里本公司与同行中位的跌幅差来判断。
7. 用简体中文回答，按给定 JSON 结构输出。这不构成投资建议。`

// promptData is what the model sees about the company; JSON keeps it unambiguous.
type promptData struct {
	Company    map[string]any   `json:"公司"`
	Screen     map[string]any   `json:"筛选结果,omitempty"`
	Valuation  map[string]any   `json:"估值,omitempty"`
	PriceTrail []map[string]any `json:"股价与估值倍数_每半年,omitempty"`
	Financials []map[string]any `json:"财务_报告币种"`
	Metrics    map[string]any   `json:"专业指标,omitempty"`
	Attention  []map[string]any `json:"关注度,omitempty"`
	News       []map[string]any `json:"近期新闻,omitempty"`
	Judgement  map[string]any   `json:"投资者自己的判断,omitempty"`
	Last       map[string]any   `json:"上次AI诊断,omitempty"`
	XPulse     map[string]any   `json:"X舆情_Grok搜索,omitempty"`
	Live       map[string]any   `json:"最新行情,omitempty"`
	Peers      map[string]any   `json:"同行对比,omitempty"`
}

// Optional, non-factual material a skill may ask for; the facts-only data leaves it out.
const (
	IncludeScreen    = "screen"    // the screen's layers, checks, hints, upside / downside / convexity
	IncludeJudgement = "judgement" // the investor's own judgement
	IncludeLastAI    = "lastAI"    // the latest AI diagnosis
	IncludeXPulse    = "xpulse"    // the latest X pulse (opinions on X)
)

var IncludeKeys = []string{IncludeScreen, IncludeJudgement, IncludeLastAI, IncludeXPulse}

// dataOpts: facts drops everything that carries the screen's stance or guidance (the
// framework, pass / fail verdicts, hints, target multiple and upside, good / bad grades, the
// peers' "how to use"); include adds back the optional material.
type dataOpts struct {
	facts   bool
	include map[string]bool
}

func (o dataOpts) has(k string) bool { return !o.facts || o.include[k] }

func r3(v float64) float64 { return float64(int64(v*1000+copysign(0.5, v))) / 1000 }

func copysign(a, b float64) float64 {
	if b < 0 {
		return -a
	}
	return a
}

func pv(p *float64) any {
	if p == nil {
		return nil
	}
	return r3(*p)
}

func yearRow(y model.Year) map[string]any {
	label := "财年 " + y.End
	if y.TTM {
		label = "TTM 截至 " + y.End
	}
	m := map[string]any{"期间": label}
	add := func(k string, v *float64) {
		if v != nil {
			m[k] = *v
		}
	}
	add("收入", y.Revenue)
	add("毛利", y.GrossProfit)
	add("营业利润", y.OpIncome)
	add("净利润", y.NetIncome)
	add("经营现金流", y.OCF)
	add("资本开支", y.Capex)
	add("FCF", y.FCF)
	add("现金及短投", y.Cash)
	add("有息负债", y.Debt)
	add("股东权益", y.Equity)
	add("稀释股数", y.Shares)
	add("股权激励SBC", y.SBC)
	add("回购", y.Buyback)
	add("分红", y.Dividends)
	if y.CapexImputed {
		m["注"] = "资本开支未披露，按历史比例估算"
	}
	return m
}

type liveQuote struct {
	Price    float64
	AsOf     string
	Drawdown float64
}

func buildPrompt(c config.Convexity, e *screen.Evaluation, p *profile.Profile, j *store.Judgement, last, pulse *store.Diagnosis, live *liveQuote, peers map[string]any, now time.Time) string {
	d := buildData(c, e, p, j, last, pulse, live, peers, dataOpts{})
	body, _ := json.MarshalIndent(d, "", " ")
	return framework + "\n\n今天是 " + now.Format(time.DateOnly) + "。金额单位为原始数值；比例为小数（0.25 = 25%）。\n\n<数据>\n" + string(body) + "\n</数据>\n"
}

func buildData(c config.Convexity, e *screen.Evaluation, p *profile.Profile, j *store.Judgement, last, pulse *store.Diagnosis, live *liveQuote, peers map[string]any, o dataOpts) promptData {
	if o.facts && peers != nil {
		cp := map[string]any{}
		for k, v := range peers {
			if k != "用途" {
				cp[k] = v
			}
		}
		peers = cp
	}
	d := promptData{
		Company: map[string]any{"代码": e.Ticker, "名称": e.Name, "交易所": e.Exchange, "行业": strings.Trim(e.Sector+" / "+e.Industry, " /"),
			"报告币种": e.Currency},
		Peers: peers,
	}
	if e.FX > 0 {
		d.Company["汇率"] = fmt.Sprintf("1 USD = %.4f %s（筛选时的最新汇率，估值换算用）", e.FX, e.Currency)
	}
	if o.has(IncludeScreen) {
		d.Screen = map[string]any{"通过层数": fmt.Sprintf("%d / 3", e.Stage)}
	}
	if p != nil {
		if p.Summary != "" {
			d.Company["简介"] = p.Summary
		}
		if p.Revenue != nil {
			for _, b := range p.Revenue.Breakdowns {
				var items []string
				for _, it := range b.Items {
					items = append(items, fmt.Sprintf("%s %.0f%%", it.Name, it.Share*100))
				}
				d.Company["收入构成_"+b.Title] = strings.Join(items, "，") + "（" + p.Revenue.PeriodEnd + "）"
			}
		}
		if p.Attention != nil {
			for _, s := range p.Attention.Series {
				row := map[string]any{"指标": s.Label, "近30天日均": r3(s.Recent), "此前日均": r3(s.Base)}
				if s.Ratio != nil {
					row["倍数"] = r3(*s.Ratio)
				}
				d.Attention = append(d.Attention, row)
			}
		}
		for i, n := range p.News {
			if i >= 20 {
				break
			}
			row := map[string]any{"时间": n.Time[:min(10, len(n.Time))], "类型": n.Type, "标题": n.Title, "来源": n.Source}
			if n.TitleEn != "" {
				row["原标题"] = n.TitleEn
			}
			if n.Move != nil {
				row["当日股价"] = fmt.Sprintf("%+.1f%%", *n.Move*100)
			}
			d.News = append(d.News, row)
		}
	}
	if d.Screen != nil {
		addScreen(&d, e)
	} else if len(e.Notes) > 0 {
		d.Company["数据说明"] = e.Notes
	}
	addValuation(&d, c, e, o)
	for _, y := range e.Years[max(0, len(e.Years)-6):] {
		d.Financials = append(d.Financials, yearRow(y))
	}
	for _, y := range e.TTM {
		d.Financials = append(d.Financials, yearRow(y))
	}
	addMetrics(&d, e, o)
	if j != nil && o.has(IncludeJudgement) {
		d.Judgement = map[string]any{"结论": map[string]string{"external": "外部错杀", "internal": "内部问题", "unsure": "不确定",
			"watch": "加入观察", "reject": "排除", "": "未下结论"}[j.Verdict], "市场在担心": j.Worry, "为何是暂时的": j.Why,
			"证伪条件": j.Falsify, "笔记": j.Note, "更新于": j.UpdatedAt}
	}
	if last != nil && o.has(IncludeLastAI) {
		var r Result
		_ = json.Unmarshal(last.Result, &r)
		d.Last = map[string]any{"日期": last.At[:10], "当时股价": last.Price, "当时数据口径": last.Basis, "结论": r.Stance,
			"信心": r.Confidence, "一句话": r.Summary, "证伪条件": r.Falsify}
	}
	if pulse != nil && o.has(IncludeXPulse) {
		var x XPulse
		_ = json.Unmarshal(pulse.Result, &x)
		d.XPulse = map[string]any{"日期": pulse.At[:10], "总结": x.Summary, "情绪": x.Sentiment, "情绪分": x.SentimentScore,
			"热度": x.Volume, "主要叙事": x.Narratives, "看多": x.Bull, "看空": x.Bear, "待核实传闻": x.Rumors,
			"说明": "这是 X 上的观点汇总，不是事实；只作为市场情绪的参考"}
	}
	if live != nil {
		d.Live = map[string]any{"日期": live.AsOf, "股价": live.Price, "距5年高点": r3(live.Drawdown),
			"说明": "筛选数据里的股价是上次筛选时的；这是最新收盘价"}
	}
	return d
}

func addScreen(d *promptData, e *screen.Evaluation) {
	var checks []string
	for _, c := range e.Checks {
		s := "通过"
		if !c.Pass {
			s = "未通过"
		} else if c.Near {
			s = "通过但临界"
		}
		checks = append(checks, fmt.Sprintf("L%d %s：%s（%s；规则 %s）", c.Layer, c.Name, s, c.Value, c.Rule))
	}
	d.Screen["检查项"] = checks
	var hints []string
	for _, h := range e.Hints {
		hints = append(hints, map[string]string{"external": "[外部因素]", "internal": "[内部问题]", "positive": "[正面]"}[h.Kind]+h.Text)
	}
	if len(hints) > 0 {
		d.Screen["系统线索"] = hints
	}
	if len(e.Notes) > 0 {
		d.Screen["数据说明"] = e.Notes
	}
}

func addValuation(d *promptData, c config.Convexity, e *screen.Evaluation, o dataOpts) {
	if v := e.Valuation; v != nil {
		d.Valuation = map[string]any{"口径": v.Basis, "股价": v.Price, "5年最高": v.High5y, "距5年高点": r3(v.Drawdown),
			"市值USD": v.MarketCapUSD, "正常化FCF_USD": v.NormFCFUSD, "FCF收益率": pv(v.FCFYield), "当前市值/正常化FCF": pv(v.Multiple),
			"倍数历史分位": pv(v.Percentile), "倍数历史中位数": pv(v.Median), "倍数历史10分位": pv(v.P10),
			"近期FCF保持率": pv(v.FCFRetention), "最新12个月FCF对比近期均值": pv(v.LatestFCFvsAvg),
			"收入保持率": pv(v.RevenueRetention), "5年高点以来正常化FCF变化": pv(v.FCFChangeSinceHigh), "估值历史月数": v.HistoryMonths}
		defs := "正常化FCF = 最近 3 个互不重叠的 12 个月期间（有季报时为 TTM，否则为财年）FCF 均值，按汇率折成美元。" +
			"近期FCF保持率 = 近期 3 期 FCF 均值 / 之前 3 个财年均值；最新12个月FCF对比近期均值 = 最新一期 / 近期 3 期均值；" +
			"收入保持率 = 最新一期收入 / 近期 3 期最高收入。"
		if o.has(IncludeScreen) {
			d.Valuation["目标倍数"], d.Valuation["上行空间"], d.Valuation["下行空间_压力情景"], d.Valuation["凸性_上行除以下行"] =
				pv(v.Target), pv(v.Upside), pv(v.Downside), pv(v.Convexity)
			defs += fmt.Sprintf("目标倍数 = min(历史中位数, %.0fx 上限)，上行空间 = 目标倍数/当前倍数 − 1，是倍数修复而非目标股价。"+
				"下行空间 = 倍数取 min(当前, 历史10分位) 且 FCF 再降 %.0f%% 的压力情景。", c.MaxTargetMultiple, c.FCFShock*100)
		}
		d.Valuation["口径说明"] = defs
		if v.MarketCapEstimated {
			d.Valuation["注"] = "市值按股价×股数估算"
		}
		for i := len(v.Series) - 1; i >= 0 && len(d.PriceTrail) < 12; i -= 6 {
			pt := v.Series[i]
			row := map[string]any{"月份": pt.Date[:min(7, len(pt.Date))], "股价": r3(pt.Price)}
			if pt.Multiple != nil {
				row["市值/FCF"] = r3(*pt.Multiple)
			}
			d.PriceTrail = append([]map[string]any{row}, d.PriceTrail...)
		}
	}
}

func addMetrics(d *promptData, e *screen.Evaluation, o dataOpts) {
	if a := e.Analysis; a != nil {
		d.Metrics = map[string]any{}
		for _, g := range a.Groups {
			var rows []string
			for _, m := range g.Metrics {
				if m.Value == nil {
					continue
				}
				tone := map[string]string{"good": "好", "warn": "临界", "bad": "差"}[m.Tone]
				if o.facts {
					tone = "" // the system's grade is a judgement
				}
				rows = append(rows, strings.TrimSpace(fmt.Sprintf("%s=%s（%s）%s", m.Name, fmtMetric(m), m.AsOf, tone)))
			}
			title := g.Title
			if o.facts { // "资本回报：护城河的直接证据" → "资本回报": the part after the colon is a reading
				title, _, _ = strings.Cut(title, "：")
			}
			d.Metrics[title] = rows
		}
		if f := a.FScore; f != nil {
			var miss []string
			for _, it := range f.Items {
				if !it.Pass {
					miss = append(miss, it.Name)
				}
			}
			d.Metrics["Piotroski_F"] = fmt.Sprintf("%d/%d（%s），未通过：%s", f.Value, f.Max, f.Period, strings.Join(miss, "、"))
		}
		if z := a.ZScore; z != nil {
			d.Metrics["Altman_Z"] = fmt.Sprintf("%.2f（%s）", z.Value, z.Zone)
		}
		if r := a.ReverseDCF; r != nil {
			d.Metrics["反向DCF"] = map[string]any{"当前价格隐含FCF年增速": r3(r.ImpliedGrowth), "年数": r.Years, "折现率": r.Discount,
				"永续增速": r.Terminal, "过去5年收入年增速": pv(r.HistRevenue), "过去5年FCF年增速": pv(r.HistFCF)}
		}
		if vi := a.Value; vi != nil {
			note := "按市值÷股价得到的流通股数折算（ADS 已按比例），供 fairValue 估算使用"
			if o.facts {
				note = "按市值÷股价得到的流通股数折算（ADS 已按比例）"
			}
			d.Metrics["每股估值输入_美元"] = map[string]any{"股价": r3(vi.Price), "正常化FCF每股": r3(vi.FCF), "最新12个月FCF每股": pv(vi.LatestFCF),
				"近期平均EPS": pv(vi.EPS), "最新12个月EPS": pv(vi.LatestEPS), "每股净资产": pv(vi.BookValue), "每股净现金": r3(vi.NetCash),
				"说明": note}
		}
	}
}

func fmtMetric(m screen.Metric) string {
	v := *m.Value
	switch m.Format {
	case "pct":
		return fmt.Sprintf("%.1f%%", v*100)
	case "x":
		return fmt.Sprintf("%.1fx", v)
	case "days":
		return fmt.Sprintf("%.0f天", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func buildRollupPrompt(ticker, name string, items []store.Diagnosis, now time.Time) string {
	var b strings.Builder
	b.WriteString(framework)
	fmt.Fprintf(&b, "\n\n今天是 %s。下面是对 %s（%s）在不同时间、不同触发原因下做的 %d 次诊断，按时间从早到晚排列。"+
		"诊断可能来自不同的 AI 模型（见「引擎」字段），也可能来自不同时间。请把它们汇总：\n"+
		"- 不同模型之间：哪些判断是共识，哪些有分歧；有分歧时，哪一方的论据更扎实、更有数据支持。\n"+
		"- 不同时间之间：哪些判断随新数据或股价变化而改变。\n"+
		"- 现在综合来看结论是什么，还有哪些问题要投资者自己判断。\n"+
		"不要简单取多数票：看论据质量，以及较新的诊断是否基于新的事实。在 stable / shifts 里注明是哪个模型或哪天的观点。\n\n<历次诊断>\n",
		now.Format(time.DateOnly), name, ticker, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		d := items[i]
		var r map[string]any
		_ = json.Unmarshal(d.Result, &r)
		row, _ := json.Marshal(map[string]any{"日期": d.At[:16], "引擎": strings.TrimSuffix(d.Engine+" / "+d.Model, " / "), "触发": d.Trigger,
			"股价": d.Price, "数据口径": d.Basis, "诊断": r})
		b.Write(row)
		b.WriteString("\n")
	}
	b.WriteString("</历次诊断>\n")
	return b.String()
}

// XPulse is what X (Twitter) has been saying about the company, gathered by Grok.
type XPulse struct {
	Summary        string   `json:"summary"`
	Sentiment      string   `json:"sentiment"`      // bullish | bearish | mixed | neutral
	SentimentScore int      `json:"sentimentScore"` // -5 … +5
	Volume         string   `json:"volume"`         // how much it is discussed vs usual, and the trend
	Narratives     []string `json:"narratives"`
	Bull           []string `json:"bull"`
	Bear           []string `json:"bear"`
	Posts          []XPost  `json:"posts"`
	Rumors         []string `json:"rumors"`   // claims circulating on X that need checking
	Coverage       string   `json:"coverage"` // what was searched, what could not be found
}

type XPost struct {
	Author string `json:"author"`
	Date   string `json:"date"`
	Text   string `json:"text"`
	URL    string `json:"url"`
	Stance string `json:"stance"` // bullish | bearish | neutral
	// Set by us, not the model: the date encoded in the post ID, and whether it contradicts
	// the stated date or the link is not a post link (a sign the link was made up).
	IDDate  string `json:"idDate,omitempty"`
	Suspect bool   `json:"suspect,omitempty"`
}

var reXPost = regexp.MustCompile(`^https?://(?:www\.)?(?:x|twitter)\.com/[A-Za-z0-9_]+/status/(\d{15,20})`)

// verifyPosts checks each link against the timestamp inside X's snowflake post ID.
func verifyPosts(posts []XPost) (suspect int) {
	for i := range posts {
		p := &posts[i]
		m := reXPost.FindStringSubmatch(p.URL)
		if m == nil {
			p.Suspect = true
			suspect++
			continue
		}
		id, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			p.Suspect = true
			suspect++
			continue
		}
		at := time.UnixMilli(int64(id>>22) + 1288834974657).UTC()
		p.IDDate = at.Format(time.DateOnly)
		if d, err := time.Parse(time.DateOnly, strings.TrimSpace(p.Date)[:min(10, len(strings.TrimSpace(p.Date)))]); err != nil ||
			math.Abs(at.Sub(d).Hours()) > 72 {
			p.Suspect = true
			suspect++
		}
	}
	return suspect
}

var sentimentEnum = []string{"bullish", "bearish", "mixed", "neutral"}

var xpulseSchema = objectSchema(map[string]any{
	"summary":        map[string]any{"type": "string", "description": "X 上近 30 天讨论的一句话总结，80 字以内"},
	"sentiment":      map[string]any{"type": "string", "enum": sentimentEnum},
	"sentimentScore": map[string]any{"type": "integer", "minimum": -5, "maximum": 5, "description": "-5 极度悲观 … +5 极度乐观"},
	"volume":         map[string]any{"type": "string", "description": "讨论热度：与平时相比多还是少，最近是升温还是降温，峰值在哪天、因为什么"},
	"narratives":     strArr("X 上的主要叙事 / 话题，按热度排序"),
	"bull":           strArr("X 上的主要看多论点"),
	"bear":           strArr("X 上的主要看空论点"),
	"posts": map[string]any{"type": "array", "description": "有代表性的帖子（最多 8 条，必须是真实搜到的，附链接）",
		"items": json.RawMessage(objectSchema(map[string]any{
			"author": map[string]any{"type": "string"}, "date": map[string]any{"type": "string", "description": "YYYY-MM-DD"},
			"text":   map[string]any{"type": "string", "description": "帖子要点，中文转述"},
			"url":    map[string]any{"type": "string"},
			"stance": map[string]any{"type": "string", "enum": []string{"bullish", "bearish", "neutral"}},
		}))},
	"rumors":   strArr("X 上流传、但需要核实的说法"),
	"coverage": map[string]any{"type": "string", "description": "实际搜了什么、覆盖哪段时间、有什么没搜到"},
})

func buildXPulsePrompt(e *screen.Evaluation, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "今天是 %s。请用你的 X（Twitter）搜索和网页搜索，调查最近 30 天 X 上关于 %s（股票代码 $%s，%s）的讨论。\n\n",
		now.Format(time.DateOnly), e.Name, e.Ticker, e.Exchange)
	if v := e.Valuation; v != nil {
		fmt.Fprintf(&b, "背景：股价 %.2f，距 5 年高点 %.0f%%。一位投资者在判断这次下跌是不是被「错杀」，想知道 X 上的人在担心什么、在期待什么。\n\n",
			v.Price, v.Drawdown*100)
	}
	b.WriteString(`要求：
1. 搜索 $` + e.Ticker + `、公司名、主要产品名，覆盖最近 30 天；优先看有实质内容的帖子（分析师、行业人士、财经账号、公司高管），忽略纯喊单和机器人。
2. 用 X 搜索工具（x_keyword_search / x_semantic_search / x_thread_fetch）直接搜 X，网页搜索只作补充。只写真实搜到的内容；帖子链接必须从搜索结果里原样复制，日期用帖子的实际发布日期（YYYY-MM-DD）；搜不到就说搜不到，绝不编造链接。系统会用帖子 ID 自动核对日期，对不上的会被标为可疑。
3. 区分「事实」和「观点 / 传闻」，传闻放到 rumors。
4. 讨论热度给出相对判断（例如「财报后三天讨论量是平时数倍，之后回落」）。
5. 用简体中文，最后只输出一个符合给定 JSON 结构的对象。这不构成投资建议。
`)
	return b.String()
}
