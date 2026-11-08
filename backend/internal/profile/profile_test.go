package profile

import "testing"

func TestSearchName(t *testing.T) {
	for in, want := range map[string]string{
		"PDD Holdings Inc.":        "PDD Holdings",
		"lululemon athletica inc.": "lululemon athletica",
		"ADOBE INC.":               "ADOBE INC.",
		"Trip.com Group Ltd":       "Trip.com",
		"WillScot Holdings Corp":   "WillScot",
		"The Trade Desk, Inc.":     "Trade Desk",
	} {
		if got := searchName(in); got != want {
			t.Errorf("searchName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSection(t *testing.T) {
	lines := htmlLines(`<p>Item 1.</p><p>Business</p><p>Item 1A. Risk Factors</p><p>PART I</p><div>Item 1. Business</div>
<p>Acme makes widgets for industrial customers across North America and Europe, selling through distributors and a direct sales force that serves large accounts.</p>
<p>Item 1A. Risk Factors</p>`)
	got := paragraphs(section(lines, reItem1, reItem1A), reItem1A, 2000)
	if len(got) != 1 || got[0][:10] != "Acme makes" {
		t.Fatalf("got %q", got)
	}
}

func TestFitTotal(t *testing.T) {
	seg := func(n string, v float64) Segment { return Segment{NameEn: n, Value: v, Share: v / 100} }
	var overlap bool
	got := fitTotal(dedupe([]Segment{seg("Americas", 70), seg("United States", 57), seg("China", 18), seg("Other countries", 8), seg("Other Countries", 8), seg("Canada", 12)}), 100, &overlap)
	sum := 0.0
	for _, s := range got {
		sum += s.Value
	}
	if !overlap || sum > 100.0001 || got[0].NameEn != "Americas" || got[1].NameEn != "China" {
		t.Fatalf("overlap=%v sum=%v got=%+v", overlap, sum, got)
	}
	overlap = false
	got = fitTotal([]Segment{seg("A", 60), seg("B", 30)}, 100, &overlap)
	if overlap || len(got) != 3 || got[2].Name != "其他" {
		t.Fatalf("got %+v", got)
	}
}

func TestCleanNews(t *testing.T) {
	items := []NewsItem{
		{Title: "Lululemon shares fall after weak guidance", Source: "Reuters"},
		{Title: "Lululemon shares fall after weak guidance - update", Source: "Yahoo"},
		{Title: "LULU price today", Source: "CoinMarketCap"},
		{Title: "打卡 lulu", Source: "Moomoo"},
		{Title: "Analyst downgrades Lululemon", Source: "Barron's"},
	}
	got, hidden := clean(items, 10)
	if len(got) != 2 || hidden != 3 {
		t.Fatalf("got %d hidden %d: %+v", len(got), hidden, got)
	}
	if classify(got[0].Title) != "财报" || classify(got[1].Title) != "评级" {
		t.Fatalf("types %s %s", classify(got[0].Title), classify(got[1].Title))
	}
}
