// Package sec loads the listed-company universe and annual fundamentals from SEC EDGAR.
//
// Fundamentals come from the bulk companyfacts.zip archive (all filers, all taxonomies),
// so US-GAAP and IFRS filers in any reporting currency go through one parser.
package sec

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"stockscreener/internal/fetch"
	"stockscreener/internal/model"
)

const (
	tickersURL      = "https://www.sec.gov/files/company_tickers_exchange.json"
	companyFactsURL = "https://www.sec.gov/Archives/edgar/daily-index/xbrl/companyfacts.zip"
)

type Listing struct {
	CIK      int
	Ticker   string
	Name     string
	Exchange string
}

// Funds, trusts, SPACs and crypto vehicles have no operating fundamentals worth screening.
var (
	nonOperating = regexp.MustCompile(`(?i)\b(ETF|ETN|Fund|Trust|Acquisition|Bitcoin|Ethereum|Ether|Capital Corp)\b`)
	classShare   = regexp.MustCompile(`^[A-Z]+-[AB]$`)
)

// Listings returns one primary ticker per company on the given exchanges.
func Listings(ctx context.Context, c *fetch.Client, exchanges []string) ([]Listing, error) {
	body, err := c.Get(ctx, tickersURL, "sec/company_tickers_exchange.json", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Data [][]any `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse tickers: %w", err)
	}
	seen := map[int]bool{}
	var out []Listing
	for _, row := range raw.Data {
		cik, _ := row[0].(float64)
		name, _ := row[1].(string)
		ticker, _ := row[2].(string)
		exchange, _ := row[3].(string)
		if !slices.Contains(exchanges, exchange) || seen[int(cik)] || nonOperating.MatchString(name) {
			continue
		}
		// Preferreds, warrants and units carry suffixes; class shares like BRK-B are kept.
		if strings.Contains(ticker, "-") && !classShare.MatchString(ticker) {
			continue
		}
		seen[int(cik)] = true
		out = append(out, Listing{CIK: int(cik), Ticker: ticker, Name: name, Exchange: exchange})
	}
	return out, nil
}

// DownloadFacts fetches the bulk archive (≈1.4GB) at most once per maxAge.
func DownloadFacts(ctx context.Context, c *fetch.Client, maxAge time.Duration) (string, error) {
	return c.Download(ctx, companyFactsURL, "sec/companyfacts.zip", maxAge)
}

type spec struct {
	gaap, ifrs []string
	instant    bool
	shares     bool
	max        bool // take the largest candidate instead of the first by priority
	sumGAAP    bool // US-GAAP: add the candidates up (they are components, not alternatives)
}

var specs = map[string]spec{
	"revenue": {gaap: []string{"Revenues", "RevenueFromContractWithCustomerExcludingAssessedTax",
		"RevenueFromContractWithCustomerIncludingAssessedTax", "SalesRevenueNet", "SalesRevenueGoodsNet"},
		ifrs: []string{"Revenue", "RevenueFromContractsWithCustomers"}, max: true},
	"cogs":  {gaap: []string{"CostOfRevenue", "CostOfGoodsAndServicesSold", "CostOfGoodsSold"}, ifrs: []string{"CostOfSales"}},
	"gross": {gaap: []string{"GrossProfit"}, ifrs: []string{"GrossProfit"}},
	// Pre-tax income stands in where no operating line is tagged (common for pharma and foreign filers).
	"op_income": {gaap: []string{"OperatingIncomeLoss",
		"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
		"IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments"},
		ifrs: []string{"ProfitLossFromOperatingActivities", "ProfitLossBeforeTax"}},
	"net_income": {gaap: []string{"NetIncomeLoss", "ProfitLoss"}, ifrs: []string{"ProfitLossAttributableToOwnersOfParent", "ProfitLoss"}},
	"ocf": {gaap: []string{"NetCashProvidedByUsedInOperatingActivities", "NetCashProvidedByUsedInOperatingActivitiesContinuingOperations"},
		ifrs: []string{"CashFlowsFromUsedInOperatingActivities"}},
	// US-GAAP capex is the sum of its component tags (fleet, oil & gas, capitalized software
	// are tagged separately from PP&E), floored by the aggregate "productive assets" tag.
	"capex": {gaap: []string{"PaymentsToAcquirePropertyPlantAndEquipment", "PaymentsToAcquireOtherPropertyPlantAndEquipment",
		"PaymentsToAcquireEquipmentOnLease", "PaymentsToExploreAndDevelopOilAndGasProperties",
		"PaymentsToAcquireOilAndGasPropertyAndEquipment", "PaymentsToAcquireOilAndGasProperty",
		"PaymentsForCapitalImprovements", "PaymentsToDevelopSoftware", "PaymentsForSoftware"},
		ifrs:    []string{"PurchaseOfPropertyPlantAndEquipmentClassifiedAsInvestingActivities", "PurchaseOfPropertyPlantAndEquipment"},
		sumGAAP: true},
	"capex_agg": {gaap: []string{"PaymentsToAcquireProductiveAssets"}},
	"shares": {gaap: []string{"WeightedAverageNumberOfDilutedSharesOutstanding", "WeightedAverageNumberOfSharesOutstandingBasic"},
		ifrs: []string{"AdjustedWeightedAverageShares", "WeightedAverageShares"}, shares: true},
	"cash": {gaap: []string{"CashAndCashEquivalentsAtCarryingValue", "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents"},
		ifrs: []string{"CashAndCashEquivalents"}, instant: true},
	"st_invest": {gaap: []string{"ShortTermInvestments", "MarketableSecuritiesCurrent", "AvailableForSaleSecuritiesDebtSecuritiesCurrent"},
		ifrs: []string{"CurrentInvestments"}, instant: true, max: true},
	"debt_total": {gaap: []string{"DebtLongtermAndShorttermCombinedAmount"}, ifrs: []string{"Borrowings"}, instant: true},
	"lt_debt":    {gaap: []string{"LongTermDebt"}, instant: true},
	"lt_debt_nc": {gaap: []string{"LongTermDebtNoncurrent"}, ifrs: []string{"LongtermBorrowings"}, instant: true},
	"lt_debt_c":  {gaap: []string{"LongTermDebtCurrent"}, ifrs: []string{"CurrentPortionOfLongtermBorrowings"}, instant: true},
	"st_debt":    {gaap: []string{"ShortTermBorrowings", "CommercialPaper"}, ifrs: []string{"ShorttermBorrowings"}, instant: true},
	"debt_c":     {gaap: []string{"DebtCurrent"}, instant: true}, // current long-term debt + short-term borrowings
	// Totals that include finance (capital) leases. Only used when no lease-free tag is
	// filed at all (e.g. Comcast, Fiserv): a small lease share beats reading debt as zero.
	"debt_lease":      {gaap: []string{"DebtAndCapitalLeaseObligations"}, instant: true},
	"lt_debt_lease":   {gaap: []string{"LongTermDebtAndCapitalLeaseObligations"}, instant: true}, // noncurrent
	"lt_debt_lease_c": {gaap: []string{"LongTermDebtAndCapitalLeaseObligationsCurrent"}, instant: true},
	// ---- extra items for the professional metrics (not used by the screen rules) ----
	"sbc": {gaap: []string{"ShareBasedCompensation", "AllocatedShareBasedCompensationExpense"},
		ifrs: []string{"AdjustmentsForSharebasedPayments", "ExpenseFromSharebasedPaymentTransactionsWithEmployees"}},
	"da": {gaap: []string{"DepreciationDepletionAndAmortization", "DepreciationAmortizationAndAccretionNet", "DepreciationAndAmortization", "Depreciation"},
		ifrs: []string{"AdjustmentsForDepreciationAndAmortisationExpense", "DepreciationAndAmortisationExpense"}},
	"interest": {gaap: []string{"InterestExpense", "InterestExpenseNonoperating", "InterestExpenseDebt", "InterestAndDebtExpense"},
		ifrs: []string{"FinanceCosts", "InterestExpense"}},
	"rd":  {gaap: []string{"ResearchAndDevelopmentExpense", "ResearchAndDevelopmentExpenseExcludingAcquiredInProcessCost"}, ifrs: []string{"ResearchAndDevelopmentExpense"}},
	"sga": {gaap: []string{"SellingGeneralAndAdministrativeExpense"}, ifrs: []string{"SellingGeneralAndAdministrativeExpense"}},
	"buyback": {gaap: []string{"PaymentsForRepurchaseOfCommonStock", "PaymentsForRepurchaseOfEquity"},
		ifrs: []string{"PaymentsToAcquireOrRedeemEntitysShares"}},
	"dividends": {gaap: []string{"PaymentsOfDividends", "PaymentsOfDividendsCommonStock"},
		ifrs: []string{"DividendsPaidClassifiedAsFinancingActivities", "DividendsPaid"}},
	"tax":         {gaap: []string{"IncomeTaxExpenseBenefit"}, ifrs: []string{"IncomeTaxExpenseContinuingOperations"}},
	"pretax":      {gaap: []string{"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest", "IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments"}, ifrs: []string{"ProfitLossBeforeTax"}},
	"assets":      {gaap: []string{"Assets"}, ifrs: []string{"Assets"}, instant: true},
	"cur_assets":  {gaap: []string{"AssetsCurrent"}, ifrs: []string{"CurrentAssets"}, instant: true},
	"cur_liab":    {gaap: []string{"LiabilitiesCurrent"}, ifrs: []string{"CurrentLiabilities"}, instant: true},
	"liabilities": {gaap: []string{"Liabilities"}, ifrs: []string{"Liabilities"}, instant: true},
	"retained":    {gaap: []string{"RetainedEarningsAccumulatedDeficit"}, ifrs: []string{"RetainedEarnings"}, instant: true},
	"inventory":   {gaap: []string{"InventoryNet"}, ifrs: []string{"Inventories", "CurrentInventories"}, instant: true},
	"receivables": {gaap: []string{"AccountsReceivableNetCurrent", "ReceivablesNetCurrent"}, ifrs: []string{"TradeAndOtherCurrentReceivables", "CurrentTradeReceivables"}, instant: true},
	"goodwill":    {gaap: []string{"Goodwill"}, ifrs: []string{"Goodwill"}, instant: true},
	"intangibles": {gaap: []string{"IntangibleAssetsNetExcludingGoodwill", "FiniteLivedIntangibleAssetsNet"}, ifrs: []string{"IntangibleAssetsOtherThanGoodwill"}, instant: true},
	"equity": {gaap: []string{"StockholdersEquity", "StockholdersEquityIncludingPortionAttributableToNoncontrollingInterest"},
		ifrs: []string{"EquityAttributableToOwnersOfParent", "Equity"}, instant: true},
}

var annualForms = map[string]bool{"10-K": true, "10-K/A": true, "10-KT": true, "20-F": true, "20-F/A": true, "40-F": true, "40-F/A": true}

type fact struct {
	Start string  `json:"start"`
	End   string  `json:"end"`
	Val   float64 `json:"val"`
	Form  string  `json:"form"`
	Filed string  `json:"filed"`
}

type concept struct {
	Units map[string][]fact `json:"units"`
}

type factsFile struct {
	Facts map[string]map[string]json.RawMessage `json:"facts"`
}

// ParseFacts reads the archive and returns annual fundamentals for the given listings.
// Companies without usable annual cash-flow data are omitted.
func ParseFacts(path string, listings []Listing, maxYears int) (map[int]*model.Company, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer zr.Close()

	wanted := map[int]Listing{}
	for _, l := range listings {
		wanted[l.CIK] = l
	}
	jobs := make(chan *zip.File)
	var mu sync.Mutex
	out := map[int]*model.Company{}
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Go(func() {
			for f := range jobs {
				l := wanted[cikFromName(f.Name)]
				c, err := parseCompany(f, maxYears)
				if err != nil || c == nil {
					continue
				}
				c.CIK, c.Ticker, c.Name, c.Exchange = l.CIK, l.Ticker, l.Name, l.Exchange
				mu.Lock()
				out[l.CIK] = c
				mu.Unlock()
			}
		})
	}
	for _, f := range zr.File {
		if _, ok := wanted[cikFromName(f.Name)]; ok {
			jobs <- f
		}
	}
	close(jobs)
	wg.Wait()
	return out, nil
}

func cikFromName(name string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "CIK"), ".json"))
	return n
}

func parseCompany(f *zip.File, maxYears int) (*model.Company, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var ff factsFile
	if err := json.NewDecoder(rc).Decode(&ff); err != nil {
		return nil, err
	}
	return buildCompany(ff, maxYears), nil
}

type dated struct {
	val   float64
	filed string
}

func buildCompany(ff factsFile, maxYears int) *model.Company {
	taxonomy := "us-gaap"
	if !hasAnnual(ff.Facts[taxonomy], specs["ocf"].gaap) {
		taxonomy = "ifrs-full"
	}
	facts := ff.Facts[taxonomy]
	conceptsFor := func(s spec) []string {
		if taxonomy == "us-gaap" {
			return s.gaap
		}
		return s.ifrs
	}
	currency := pickCurrency(facts, conceptsFor(specs["ocf"]))
	if currency == "" {
		return nil
	}

	values := map[string]map[string]float64{} // field -> fiscal year end -> value
	for field, s := range specs {
		unit := currency
		if s.shares {
			unit = "shares"
		}
		merged := map[string]float64{}
		for _, name := range conceptsFor(s) {
			for end, v := range annualValues(decodeConcept(facts[name]), unit, s.instant) {
				old, ok := merged[end]
				switch {
				case !ok:
					merged[end] = v
				case s.sumGAAP && taxonomy == "us-gaap":
					merged[end] = old + v
				case s.max && v > old:
					merged[end] = v
				}
			}
		}
		values[field] = merged
	}

	ends := map[string]bool{}
	for end := range values["ocf"] {
		ends[end] = true
	}
	sorted := make([]string, 0, len(ends))
	for end := range ends {
		sorted = append(sorted, end)
	}
	slices.Sort(sorted)
	if len(sorted) > maxYears {
		sorted = sorted[len(sorted)-maxYears:]
	}

	get := func(field, end string) *float64 {
		if v, ok := values[field][end]; ok {
			return model.F(v)
		}
		return nil
	}
	years := make([]model.Year, 0, len(sorted))
	var capexRatios []float64
	for _, end := range sorted {
		y := model.Year{End: end, Revenue: get("revenue", end), OpIncome: get("op_income", end),
			NetIncome: get("net_income", end), OCF: get("ocf", end), Capex: maxOf(get("capex", end), get("capex_agg", end)),
			Shares: get("shares", end), Equity: get("equity", end)}
		y.GrossProfit = get("gross", end)
		if cogs := get("cogs", end); y.GrossProfit == nil && cogs != nil && y.Revenue != nil {
			y.GrossProfit = model.F(*y.Revenue - *cogs)
		}
		if y.Capex != nil && y.Revenue != nil && *y.Revenue > 0 {
			capexRatios = append(capexRatios, *y.Capex / *y.Revenue)
		}
		y.Cash = model.F(orZero(get("cash", end)) + orZero(get("st_invest", end)))
		y.Debt = model.F(debt(get, end))
		setExtras(&y, func(f string) *float64 { return get(f, end) })
		years = append(years, y)
	}
	// Some filers stop tagging capex for some years; impute from their own typical capex intensity
	// rather than silently treating it as zero.
	slices.Sort(capexRatios)
	for i := range years {
		y := &years[i]
		capex := orZero(y.Capex)
		if y.Capex == nil && len(capexRatios) > 0 && y.Revenue != nil {
			capex = capexRatios[len(capexRatios)/2] * *y.Revenue
			y.CapexImputed = true
		}
		y.FCF = model.F(*y.OCF - capex)
	}
	return &model.Company{Currency: currency, Taxonomy: taxonomy, Years: years,
		TTM: buildTTM(facts, conceptsFor, taxonomy, currency, values, years)}
}

// debt takes the largest of the alternative totals, since a missing component only ever
// understates. Interest-bearing debt excludes leases; lease-inclusive totals are the fallback
// for filers whose lease-free tags are missing or clearly partial.
func debt(get func(string, string) *float64, end string) float64 {
	long := math.Max(orZero(get("lt_debt", end)), orZero(get("lt_debt_nc", end))+orZero(get("lt_debt_c", end)))
	d := math.Max(orZero(get("debt_total", end)), long+orZero(get("st_debt", end)))
	if nc := get("lt_debt_nc", end); nc != nil {
		d = math.Max(d, *nc+orZero(get("debt_c", end)))
	}
	cur := math.Max(orZero(get("lt_debt_lease_c", end)), orZero(get("debt_c", end)))
	withLeases := math.Max(orZero(get("debt_lease", end)), orZero(get("lt_debt_lease", end))+cur)
	// Finance leases are a small share of debt; a lease-free figure under half the
	// lease-inclusive total is a partial tag (DPZ files only "other borrowings" as LongTermDebt).
	if d < withLeases*leasePartial {
		return withLeases
	}
	return d
}

const leasePartial = 0.5

func maxOf(a, b *float64) *float64 {
	if a == nil || (b != nil && *b > *a) {
		return b
	}
	return a
}

func orZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func decodeConcept(raw json.RawMessage) *concept {
	if raw == nil {
		return nil
	}
	var c concept
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return &c
}

func hasAnnual(facts map[string]json.RawMessage, names []string) bool {
	return pickCurrency(facts, names) != ""
}

// pickCurrency returns the unit with the most annual facts, so convenience USD translations
// that some foreign filers add for the latest year do not displace the reporting currency.
func pickCurrency(facts map[string]json.RawMessage, names []string) string {
	best, bestN := "", 0
	for _, name := range names {
		c := decodeConcept(facts[name])
		if c == nil {
			continue
		}
		for unit := range c.Units {
			if n := len(annualValues(c, unit, false)); n > bestN {
				best, bestN = unit, n
			}
		}
	}
	return best
}

// annualValues keys annual-form facts by period end; the most recently filed value wins so
// restatements replace originals.
func annualValues(cc *concept, unit string, instant bool) map[string]float64 {
	out := map[string]float64{}
	if cc == nil {
		return out
	}
	best := map[string]dated{}
	for _, f := range cc.Units[unit] {
		if !annualForms[f.Form] {
			continue
		}
		if instant != (f.Start == "") {
			continue
		}
		if !instant && !yearLong(f.Start, f.End) {
			continue
		}
		if old, ok := best[f.End]; !ok || f.Filed > old.filed {
			best[f.End] = dated{f.Val, f.Filed}
		}
	}
	for end, d := range best {
		out[end] = d.val
	}
	return out
}

func yearLong(start, end string) bool {
	s, err1 := time.Parse(time.DateOnly, start)
	e, err2 := time.Parse(time.DateOnly, end)
	if err1 != nil || err2 != nil {
		return false
	}
	days := e.Sub(s).Hours() / 24
	return days > 340 && days < 380
}

// setExtras fills the items only the professional metrics use; get returns a field's value
// for the period (flow) or at its end (balance sheet).
func setExtras(y *model.Year, get func(field string) *float64) {
	y.SBC, y.DA, y.Interest, y.RD, y.SGA = get("sbc"), get("da"), get("interest"), get("rd"), get("sga")
	y.Buyback, y.Dividends, y.Tax, y.Pretax = get("buyback"), get("dividends"), get("tax"), get("pretax")
	y.Assets, y.CurAssets, y.CurLiab, y.Retained = get("assets"), get("cur_assets"), get("cur_liab"), get("retained")
	y.Inventory, y.Receivables, y.Goodwill, y.Intangibles = get("inventory"), get("receivables"), get("goodwill"), get("intangibles")
	y.Liabilities = get("liabilities")
	if y.Liabilities == nil && y.Assets != nil && y.Equity != nil { // many filers tag only "liabilities and equity"
		y.Liabilities = model.F(*y.Assets - *y.Equity)
	}
}
