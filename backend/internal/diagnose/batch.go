package diagnose

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// A batch diagnoses several companies with several engines. Per company: the X pulse first
// (so every diagnosis can use it), then one diagnosis per engine in parallel, then a rollup
// that compares the engines. Companies flow through as a pipeline: while Grok searches X for
// the next company, the other engines diagnose the previous one.

type BatchCompany struct {
	CIK    int      `json:"cik"`
	Ticker string   `json:"ticker"`
	Step   string   `json:"step"` // waiting | xpulse | diagnosis | rollup | done
	Errors []string `json:"errors,omitempty"`
}

type BatchStatus struct {
	Started   string         `json:"started"`
	Finished  string         `json:"finished,omitempty"`
	Engines   []string       `json:"engines"`
	XPulse    bool           `json:"xpulse"`
	Rollup    bool           `json:"rollup"`
	Companies []BatchCompany `json:"companies"`
}

type BatchTarget struct {
	CIK    int
	Ticker string
}

// StartBatch runs in the background; it refuses while another batch is running.
func (r *Runner) StartBatch(ctx context.Context, targets []BatchTarget, engines []string, xpulse, rollup bool) error {
	var keep []string
	for _, e := range engines {
		if manualOnly[e] {
			return fmt.Errorf("%s 只在手动触发时使用，不能用于批量诊断", e)
		}
		if e != "grok" {
			keep = append(keep, e)
		}
	}
	if len(keep) == 0 {
		return fmt.Errorf("没有可用于批量诊断的模型")
	}
	engines = keep
	rollup = rollup && len(engines) > 1
	r.mu.Lock()
	if r.batch != nil && r.batch.Finished == "" {
		r.mu.Unlock()
		return fmt.Errorf("已有批量诊断在运行")
	}
	b := &BatchStatus{Started: time.Now().UTC().Format(time.RFC3339), Engines: engines, XPulse: xpulse, Rollup: rollup}
	for _, t := range targets {
		b.Companies = append(b.Companies, BatchCompany{CIK: t.CIK, Ticker: t.Ticker, Step: "waiting"})
	}
	r.batch = b
	r.mu.Unlock()

	set := func(i int, step string, err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if step != "" {
			b.Companies[i].Step = step
		}
		if err != nil {
			b.Companies[i].Errors = append(b.Companies[i].Errors, err.Error())
		}
	}
	go func() {
		// Companies enter the pipeline one by one at the X pulse stage (Grok is the slow one);
		// diagnoses and the rollup then proceed on their own goroutine.
		var wg sync.WaitGroup
		for i, t := range targets {
			if ctx.Err() != nil {
				break
			}
			if xpulse {
				set(i, "xpulse", nil)
				if err := r.runAndWait(ctx, job{CIK: t.CIK, Ticker: t.Ticker, Kind: "xpulse", Trigger: "batch"}); err != nil {
					set(i, "", fmt.Errorf("X 舆情：%v", err))
				}
			}
			set(i, "diagnosis", nil)
			wg.Add(1)
			go func(i int, t BatchTarget) {
				defer wg.Done()
				var dw sync.WaitGroup
				for _, e := range engines {
					dw.Add(1)
					go func(e string) {
						defer dw.Done()
						if err := r.runAndWait(ctx, job{CIK: t.CIK, Ticker: t.Ticker, Kind: "diagnosis", Trigger: "batch", Engine: e}); err != nil {
							set(i, "", fmt.Errorf("%s 诊断：%v", e, err))
						}
					}(e)
				}
				dw.Wait()
				if rollup {
					set(i, "rollup", nil)
					if err := r.runAndWait(ctx, job{CIK: t.CIK, Ticker: t.Ticker, Kind: "rollup", Trigger: "batch",
						Engine: r.autoEngine()}); err != nil {
						set(i, "", fmt.Errorf("汇总：%v", err))
					}
				}
				set(i, "done", nil)
			}(i, t)
		}
		wg.Wait()
		r.mu.Lock()
		b.Finished = time.Now().UTC().Format(time.RFC3339)
		r.mu.Unlock()
		log.Printf("diagnose: batch of %d companies finished", len(targets))
	}()
	return nil
}
