// Command screener finds high-quality US-listed companies whose price looks dislocated from
// their fundamentals, with asymmetric (convex) re-rating potential.
//
//	screener run            run a full screen and print the results
//	screener show TICKER    explain where a company stopped in the latest screen
//	screener profile TICKER print the business overview / revenue / news card as JSON
//	screener alerts         scan the watchlist once and email what fired
//	screener serve          serve the web UI and API (also scans the watchlist on a schedule)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"stockscreener/internal/api"
	"stockscreener/internal/config"
	"stockscreener/internal/pipeline"
	"stockscreener/internal/profile"
	"stockscreener/internal/screen"
	"stockscreener/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.json", "config file (optional; defaults apply)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: screener [-config file] run | show TICKER | profile TICKER | alerts | serve")
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal("config: ", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "screener.db"))
	if err != nil {
		log.Fatal("db: ", err)
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch flag.Arg(0) {
	case "run":
		f, err := pipeline.Run(ctx, cfg, st, func(m string) { log.Println(m) })
		if err != nil {
			log.Fatal(err)
		}
		printResults(st, f, cfg)
	case "show":
		if flag.NArg() < 2 {
			log.Fatal("usage: screener show TICKER")
		}
		show(st, strings.ToUpper(flag.Arg(1)))
	case "profile":
		if flag.NArg() < 2 {
			log.Fatal("usage: screener profile TICKER")
		}
		run, _ := st.LatestDoneRun()
		if run == nil {
			log.Fatal("还没有完成的筛选")
		}
		raw, _ := st.Detail(run.ID, strings.ToUpper(flag.Arg(1)))
		if raw == nil {
			log.Fatalf("%s 不在筛选范围内", flag.Arg(1))
		}
		var e screen.Evaluation
		_ = json.Unmarshal(raw, &e)
		pc := profile.NewClients(cfg.SECUserAgent, filepath.Join(cfg.DataDir, "cache"))
		out, _ := json.MarshalIndent(profile.Build(ctx, pc, profile.Company{CIK: e.CIK, Ticker: e.Ticker, Name: e.Name}), "", "  ")
		fmt.Println(string(out))
	case "alerts":
		res, err := api.New(ctx, cfg, st).Alerts.Scan(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("扫描 %d 家观察中的公司，触发 %d 条 %s\n", res.Companies, len(res.Fired), res.Delivery)
		for _, f := range res.Fired {
			fmt.Printf("  %-6s %s：%s\n", f.Ticker, f.Rule, f.Text)
		}
		for _, e := range res.Errors {
			fmt.Println("  错误:", e)
		}
	case "serve":
		log.Printf("http://%s", cfg.Listen)
		app := api.New(ctx, cfg, st)
		go app.Alerts.Loop(ctx) // scheduled watchlist scans
		go app.AI.Loop(ctx)     // AI diagnosis queue + scheduled diagnoses
		srv := &http.Server{Addr: cfg.Listen, Handler: app.Handler()}
		go func() { <-ctx.Done(); _ = srv.Close() }()
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func printResults(st *store.Store, f *pipeline.Funnel, cfg config.Config) {
	fmt.Printf("\n漏斗：上市运营公司 %d → 有财报数据 %d → 质量 %d → 错位 %d → 凸性 %d（取行情 %d 家，用时 %.0fs）\n\n",
		f.Universe, f.WithFundamentals, f.Quality, f.Dislocation, f.Convexity, f.MarketFetched, f.Seconds)
	run, _ := st.LatestDoneRun()
	rows, err := st.Summaries(run.ID, 2)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%-6s %-28s %-5s %6s %7s %6s %6s %7s %6s %5s\n", "代码", "名称", "层", "回撤", "FCF收益", "分位", "上行", "下行", "凸性", "质量")
	for i, r := range rows {
		if i >= 40 {
			break
		}
		fmt.Printf("%-6s %-28.28s L%-4d %6s %7s %6s %6s %7s %6s %5.0f\n", r.Ticker, r.Name, r.Stage,
			p(r.Drawdown), p(r.FCFYield), p(r.Percentile), p(r.Upside), p(r.Downside), x(r.Convexity), r.Quality)
	}
	fmt.Println("\n关注样本：")
	for _, t := range cfg.AlwaysInclude {
		showBrief(st, run.ID, t)
	}
}

func showBrief(st *store.Store, runID int64, ticker string) {
	raw, _ := st.Detail(runID, ticker)
	if raw == nil {
		fmt.Printf("  %-5s 不在筛选范围\n", ticker)
		return
	}
	var e screen.Evaluation
	_ = json.Unmarshal(raw, &e)
	stop := "全部通过"
	for _, c := range e.Checks {
		if !c.Pass {
			stop = fmt.Sprintf("卡在 L%d「%s」: %s（要求 %s）", c.Layer, c.Name, c.Value, c.Rule)
			break
		}
	}
	fmt.Printf("  %-5s 通过 %d 层，质量分 %.0f，%s\n", ticker, e.Stage, e.Quality.Score, stop)
}

func show(st *store.Store, ticker string) {
	run, err := st.LatestDoneRun()
	if err != nil || run == nil {
		log.Fatal("还没有完成的筛选，先运行 screener run")
	}
	raw, _ := st.Detail(run.ID, ticker)
	if raw == nil {
		log.Fatalf("%s 不在筛选范围内", ticker)
	}
	var e screen.Evaluation
	_ = json.Unmarshal(raw, &e)
	fmt.Printf("%s %s  报告币种 %s  %s / %s  通过 %d 层\n\n", e.Ticker, e.Name, e.Currency, e.Sector, e.Industry, e.Stage)
	for _, c := range e.Checks {
		mark := "✓"
		if !c.Pass {
			mark = "✗"
		}
		fmt.Printf("  L%d %s %-22s %-28s 要求 %s\n", c.Layer, mark, c.Name, c.Value, c.Rule)
	}
	for _, h := range e.Hints {
		fmt.Printf("  [%s] %s\n", h.Kind, h.Text)
	}
	for _, n := range e.Notes {
		fmt.Printf("  注: %s\n", n)
	}
	fmt.Println("\n  财年末        收入        FCF     营业利润率")
	for _, y := range e.Years {
		opm := "—"
		if y.OpIncome != nil && y.Revenue != nil && *y.Revenue > 0 {
			opm = fmt.Sprintf("%.1f%%", *y.OpIncome / *y.Revenue * 100)
		}
		fmt.Printf("  %s %10s %10s %8s\n", y.End, big(y.Revenue), big(y.FCF), opm)
	}
}

func p(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *v*100)
}

func x(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1fx", *v)
}

func big(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2fB", *v/1e9)
}
