package sec

import (
	"fmt"
	"os"
	"testing"
)

func TestSamplesFromEnv(t *testing.T) {
	path := os.Getenv("SAMPLES_ZIP")
	if path == "" {
		t.Skip("SAMPLES_ZIP not set")
	}
	ls := []Listing{{78003, "PFE", "Pfizer", "NYSE"}, {1397187, "LULU", "lulu", "Nasdaq"}, {1737806, "PDD", "PDD", "Nasdaq"}, {1549802, "JD", "JD", "Nasdaq"}, {353278, "NVO", "Novo", "NYSE"}, {796343, "ADBE", "Adobe", "Nasdaq"}}
	cs, err := ParseFacts(path, ls, 12)
	if err != nil {
		t.Fatal(err)
	}
	f := func(p *float64) string {
		if p == nil {
			return "     nil"
		}
		return fmt.Sprintf("%8.2f", *p/1e9)
	}
	for _, l := range ls {
		c := cs[l.CIK]
		if c == nil {
			t.Errorf("%s missing", l.Ticker)
			continue
		}
		fmt.Println(l.Ticker, c.Currency, c.Taxonomy)
		for _, y := range c.Years {
			fmt.Println(" ", y.End, "rev", f(y.Revenue), "gp", f(y.GrossProfit), "op", f(y.OpIncome), "ocf", f(y.OCF), "capex", f(y.Capex), "cash", f(y.Cash), "debt", f(y.Debt), "eq", f(y.Equity), "sh", f(y.Shares))
		}
		for _, y := range c.TTM {
			fmt.Println("  TTM", y.End, "rev", f(y.Revenue), "gp", f(y.GrossProfit), "op", f(y.OpIncome), "ocf", f(y.OCF), "capex", f(y.Capex), "fcf", f(y.FCF), "cash", f(y.Cash), "debt", f(y.Debt), "eq", f(y.Equity), "sh", f(y.Shares), y.CapexImputed)
		}
	}
}
