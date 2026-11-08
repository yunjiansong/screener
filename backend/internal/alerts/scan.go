package alerts

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"sync"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/profile"
	"stockscreener/internal/screen"
	"stockscreener/internal/store"
	"stockscreener/internal/yahoo"
)

type Scanner struct {
	st     *store.Store
	cfg    config.Config
	yahoo  *yahoo.Client
	pc     profile.Clients
	mu     sync.Mutex
	last   time.Time
	outbox string
	// OnFired, when set, is called for each company whose rules newly fired (e.g. to queue an AI diagnosis).
	OnFired func(cik int, ticker string, rules []string)
}

func New(cfg config.Config, st *store.Store, yc *yahoo.Client, pc profile.Clients) *Scanner {
	return &Scanner{st: st, cfg: cfg, yahoo: yc, pc: pc, outbox: filepath.Join(cfg.DataDir, "outbox")}
}

type Fired struct {
	Ticker string `json:"ticker"`
	Rule   string `json:"rule"`
	Text   string `json:"text"`
}

type Result struct {
	At        string   `json:"at"`
	Companies int      `json:"companies"`
	Fired     []Fired  `json:"fired"`
	Delivery  string   `json:"delivery"` // sent | outbox: path | "" when nothing fired
	Errors    []string `json:"errors"`
}

// Scan checks every watchlist company once and emails what newly fired.
func (s *Scanner) Scan(ctx context.Context) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	raw, err := s.st.AlertSettings()
	if err != nil {
		return nil, err
	}
	set := LoadSettings(raw)
	run, err := s.st.LatestDoneRun()
	if err != nil || run == nil {
		return nil, fmt.Errorf("还没有完成的筛选，先运行一次筛选")
	}
	items, err := s.st.Watchlist()
	if err != nil {
		return nil, err
	}
	res := &Result{At: time.Now().UTC().Format(time.RFC3339), Companies: len(items), Fired: []Fired{}}
	digest := Digest{At: time.Now(), BaseURL: set.BaseURL}
	for _, w := range items {
		item, err := s.check(ctx, set, run.ID, w)
		if err != nil {
			res.Errors = append(res.Errors, w.Ticker+": "+err.Error())
			continue
		}
		if len(item.Fired) > 0 {
			digest.Items = append(digest.Items, *item)
			for _, c := range item.Fired {
				res.Fired = append(res.Fired, Fired{w.Ticker, c.Label, c.Text})
			}
		}
	}
	if len(digest.Items) == 0 {
		return res, nil
	}
	if s.OnFired != nil {
		for _, it := range digest.Items {
			var rules []string
			for _, c := range it.Fired {
				rules = append(rules, c.Label)
			}
			s.OnFired(it.CIK, it.Ticker, rules)
		}
	}
	html, err := digest.HTML()
	if err != nil {
		return nil, err
	}
	delivery, err := Deliver(set.SMTP, s.outbox, digest.Subject(), html)
	if err != nil {
		delivery = "error: " + err.Error()
		res.Errors = append(res.Errors, "邮件发送失败："+err.Error())
	}
	res.Delivery = delivery
	for _, it := range digest.Items {
		for _, c := range it.Fired {
			_ = s.st.LogAlert(it.CIK, it.Ticker, c.Label, c.Text, delivery)
		}
	}
	return res, nil
}

func (s *Scanner) check(ctx context.Context, set Settings, runID int64, w store.WatchItem) (*DigestItem, error) {
	raw, err := s.st.DetailByCIK(runID, w.CIK)
	if err != nil || raw == nil {
		return nil, fmt.Errorf("不在最近一次筛选结果中")
	}
	var e screen.Evaluation
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	var over Rules
	_ = json.Unmarshal(w.Rules, &over)
	rules := set.Defaults.Merge(over)

	snap := Snapshot{Stage: e.Stage}
	if v := e.Valuation; v != nil {
		snap.Basis = v.Basis
	}
	if j, _ := s.st.Judgement(w.CIK); j != nil {
		snap.ReviewOn = j.ReviewOn
	}
	ch, err := s.yahoo.DailyChartFresh(ctx, w.Ticker, 20*time.Minute)
	if err != nil || len(ch.Bars) < 2 {
		snap.Error = "取不到最新股价"
		_ = s.st.SetSnapshot(w.CIK, snap)
		return nil, fmt.Errorf("取不到最新股价: %v", err)
	}
	bars := ch.Bars
	snap.AsOf, snap.Price = bars[len(bars)-1].Date, ch.Price
	if prev := bars[len(bars)-2].Close; prev > 0 {
		snap.DayChange = ptr(snap.Price/prev - 1)
	}
	if v := e.Valuation; v != nil && v.Price > 0 {
		var history []float64
		for i, p := range v.Series {
			if p.Multiple != nil && i < len(v.Series)-1 {
				history = append(history, *p.Multiple)
			}
		}
		snap.MarketCap, snap.Multiple, snap.FCFYield, snap.Percentile, snap.Upside, snap.Drawdown =
			rePrice(snap.Price, v.Price, v.MarketCapUSD, v.NormFCFUSD, v.High5y, s.cfg.Convexity.MaxTargetMultiple, v.Median, history)
		snap.High5y = math.Max(v.High5y, snap.Price)
	}

	conds := levelConditions(rules, snap)
	if on(rules.StageDrop) {
		conds = append(conds, Condition{Key: "stage_drop", Label: "跌出筛选层", Active: e.Stage < w.BaseStage,
			Text: fmt.Sprintf("加入观察时通过 %d 层，最新筛选只通过 %d 层（卡在：%s）", w.BaseStage, e.Stage, firstFail(e))})
	}
	if on(rules.NewQuarter) && snap.Basis != "" {
		conds = append(conds, Condition{Key: "new_quarter", Label: "新财报", Value: snap.Basis,
			Text: "财务数据更新为 " + snap.Basis + "，筛选结论可能变化"})
	}
	// Quantified falsification conditions from the judgement, on the live price.
	if j, _ := s.st.Judgement(w.CIK); j != nil && len(j.FalsifyRules) > 0 {
		m := screen.FalsifyMetrics(&e, snap.Price)
		hits := map[string]screen.FalsifyHit{}
		for _, h := range screen.EvalFalsify(j.FalsifyRules, m) {
			hits[fmt.Sprintf("%s%s%g", h.Rule.Metric, h.Rule.Op, h.Rule.Value)] = h
		}
		for _, fr := range j.FalsifyRules {
			k := fmt.Sprintf("%s%s%g", fr.Metric, fr.Op, fr.Value)
			h, hit := hits[k]
			conds = append(conds, Condition{Key: "falsify:" + k, Label: "证伪条件触发", Active: hit,
				Text: h.Text + "：重读你的判断，错杀的前提可能已经不成立"})
		}
	}
	if on(rules.Review) && snap.ReviewOn != "" {
		today := time.Now().Format(time.DateOnly)
		conds = append(conds, Condition{Key: "review", Label: "到期复查", Active: today >= snap.ReviewOn, Value: snap.ReviewOn,
			Text: "你设的下次复查日期 " + snap.ReviewOn + " 到了：重读你的判断，看证伪条件是否出现"})
	}
	if rules.NewsMove != nil {
		news, err := profile.NewsOnly(ctx, s.pc, profile.Company{CIK: e.CIK, Ticker: e.Ticker, Name: e.Name})
		if err != nil {
			log.Printf("alerts: news %s: %v", w.Ticker, err)
		}
		week := time.Now().AddDate(0, 0, -7).UTC().Format(time.RFC3339)
		for _, n := range news {
			if n.Move == nil || math.Abs(*n.Move) < *rules.NewsMove || n.Time < week {
				continue
			}
			h := sha1.Sum([]byte(n.Link))
			conds = append(conds, Condition{Key: "news:" + hex.EncodeToString(h[:6]), Label: "新闻引发异动", Active: true,
				Text: fmt.Sprintf("%s 股价 %+.1f%%：%s（%s）", n.MoveDay, *n.Move*100, n.Title, n.Source)})
		}
	}

	item := &DigestItem{CIK: w.CIK, Ticker: w.Ticker, Name: orName(w.Name, e.Name), Snap: snap}
	for _, c := range conds {
		prevActive, prevVal, err := s.st.AlertState(w.CIK, c.Key)
		if err != nil {
			return nil, err
		}
		var fire bool
		switch c.Key {
		case "new_quarter": // fires on change of basis, not on level; first sight just records it
			fire = prevVal != "" && prevVal != c.Value
		case "review": // one reminder per review date
			fire = c.Active && (!prevActive || prevVal != c.Value)
		default:
			fire = c.Active && !prevActive
		}
		if err := s.st.SetAlertState(w.CIK, c.Key, c.Active, c.Value); err != nil {
			return nil, err
		}
		if fire {
			item.Fired = append(item.Fired, c)
		}
	}
	_ = s.st.SetSnapshot(w.CIK, snap)
	return item, nil
}

func firstFail(e screen.Evaluation) string {
	for _, c := range e.Checks {
		if !c.Pass {
			return fmt.Sprintf("L%d %s", c.Layer, c.Name)
		}
	}
	return "—"
}

func orName(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Loop runs scheduled scans while ctx lives. The first scan comes two minutes after start.
func (s *Scanner) Loop(ctx context.Context) {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		raw, _ := s.st.AlertSettings()
		set := LoadSettings(raw)
		s.mu.Lock()
		last := s.last
		s.mu.Unlock()
		due := last.IsZero() && time.Since(start) >= 2*time.Minute || !last.IsZero() && time.Since(last) >= time.Duration(set.IntervalHours)*time.Hour
		if !set.Enabled || !due {
			continue
		}
		res, err := s.Scan(ctx)
		if err != nil {
			log.Println("alerts: scan failed:", err)
			continue
		}
		log.Printf("alerts: scanned %d companies, %d fired, %s", res.Companies, len(res.Fired), res.Delivery)
	}
}

// SendTest emails a sample so the SMTP settings can be checked.
func (s *Scanner) SendTest() (string, error) {
	raw, err := s.st.AlertSettings()
	if err != nil {
		return "", err
	}
	set := LoadSettings(raw)
	d := Digest{At: time.Now(), BaseURL: set.BaseURL, Items: []DigestItem{{Ticker: "TEST", Name: "测试邮件",
		Fired: []Condition{{Label: "测试", Text: "能收到这封邮件，说明提醒的邮件设置是对的"}},
		Snap:  Snapshot{AsOf: time.Now().Format(time.DateOnly), Price: 100, Drawdown: -0.3, Basis: "—"}}}}
	html, err := d.HTML()
	if err != nil {
		return "", err
	}
	return Deliver(set.SMTP, s.outbox, "【选股提醒】测试邮件", html)
}
