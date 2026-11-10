package sec

import "testing"

func TestDebtFallbacks(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		tags map[string]*float64
		want float64
	}{
		{"noncurrent + current portion + short-term", map[string]*float64{"lt_debt_nc": f(80), "lt_debt_c": f(5), "st_debt": f(3)}, 88},
		{"noncurrent + DebtCurrent", map[string]*float64{"lt_debt_nc": f(80), "debt_c": f(9)}, 89},
		// Comcast-style: only lease-inclusive tags plus DebtCurrent.
		{"lease-inclusive fallback", map[string]*float64{"lt_debt_lease": f(92.979), "debt_c": f(5.958)}, 98.937},
		{"lease-inclusive total", map[string]*float64{"debt_lease": f(98.9), "lt_debt_lease": f(92.979), "debt_c": f(5.958)}, 98.937},
		// A lease-free figure wins even when lease-inclusive tags are larger.
		{"lease-free preferred", map[string]*float64{"lt_debt": f(50), "debt_lease": f(70)}, 50},
		// DPZ-style: LongTermDebt holds only a sliver of "other borrowings".
		{"partial lease-free tag", map[string]*float64{"lt_debt": f(0.0146), "debt_lease": f(4.8168)}, 4.8168},
		{"nothing filed", map[string]*float64{}, 0},
	}
	for _, c := range cases {
		got := debt(func(k, _ string) *float64 { return c.tags[k] }, "2025-12-31")
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: debt = %v, want %v", c.name, got, c.want)
		}
	}
}
