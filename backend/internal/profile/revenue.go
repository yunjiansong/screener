package profile

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Revenue struct {
	PeriodEnd  string      `json:"periodEnd"`
	Currency   string      `json:"currency"`
	Total      float64     `json:"total"`
	Breakdowns []Breakdown `json:"breakdowns"`
	Form       string      `json:"form"`
	URL        string      `json:"url"`
}

type Breakdown struct {
	Kind    string    `json:"kind"`  // segment | product | geo
	Title   string    `json:"title"` // 按业务分部 …
	Items   []Segment `json:"items"`
	Overlap bool      `json:"overlap"` // members included subtotals; only non-overlapping top items are kept
}

type Segment struct {
	Name   string  `json:"name"`   // Chinese when translation worked
	NameEn string  `json:"nameEn"` // as labeled in the filing
	Value  float64 `json:"value"`
	Share  float64 `json:"share"` // of total revenue
}

var revenueConcepts = map[string]bool{
	"Revenues": true, "RevenueFromContractWithCustomerExcludingAssessedTax": true,
	"RevenueFromContractWithCustomerIncludingAssessedTax": true, "SalesRevenueNet": true,
	"Revenue": true, "RevenueFromContractsWithCustomers": true,
}

var axes = []struct {
	kind, title string
	names       []string
}{
	{"segment", "按业务分部", []string{"StatementBusinessSegmentsAxis", "SegmentsAxis"}},
	{"product", "按产品 / 服务", []string{"ProductOrServiceAxis", "ProductsAndServicesAxis"}},
	{"geo", "按地区", []string{"StatementGeographicalAxis", "GeographicalAreasAxis"}},
}

type xcontext struct {
	start, end string
	dims       map[string]string // axis local name -> member QName
}

type xfact struct {
	concept, ctx, unit string
	value              float64
}

func local(qname string) string {
	if i := strings.LastIndexByte(qname, ':'); i >= 0 {
		return qname[i+1:]
	}
	return qname
}

// parseInstance streams an XBRL instance, keeping contexts, units and revenue facts.
func parseInstance(r io.Reader) (map[string]*xcontext, map[string]string, []xfact, error) {
	ctxs := map[string]*xcontext{}
	units := map[string]string{}
	var facts []xfact
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var cur *xcontext
	var curUnit, dim, text string
	var inFact *xfact
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			text = ""
			attr := func(name string) string {
				for _, a := range t.Attr {
					if a.Name.Local == name {
						return a.Value
					}
				}
				return ""
			}
			switch t.Name.Local {
			case "context":
				cur = &xcontext{dims: map[string]string{}}
				ctxs[attr("id")] = cur
			case "explicitMember":
				dim = local(attr("dimension"))
			case "unit":
				curUnit = attr("id")
			default:
				ns := t.Name.Space
				if revenueConcepts[t.Name.Local] && (strings.Contains(ns, "us-gaap") || strings.Contains(ns, "ifrs")) && attr("contextRef") != "" {
					inFact = &xfact{concept: t.Name.Local, ctx: attr("contextRef"), unit: attr("unitRef")}
				}
			}
		case xml.CharData:
			text += string(t)
		case xml.EndElement:
			s := strings.TrimSpace(text)
			switch t.Name.Local {
			case "context":
				cur = nil
			case "startDate":
				if cur != nil {
					cur.start = s
				}
			case "endDate":
				if cur != nil {
					cur.end = s
				}
			case "explicitMember":
				if cur != nil {
					cur.dims[dim] = s
				}
			case "measure":
				if curUnit != "" && s != "" {
					units[curUnit] = local(s)
				}
			case "unit":
				curUnit = ""
			default:
				if inFact != nil && t.Name.Local == inFact.concept {
					if v, err := strconv.ParseFloat(s, 64); err == nil {
						inFact.value = v
						facts = append(facts, *inFact)
					}
					inFact = nil
				}
			}
		}
	}
	return ctxs, units, facts, nil
}

func days(start, end string) int {
	a, err1 := time.Parse(time.DateOnly, start)
	b, err2 := time.Parse(time.DateOnly, end)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(b.Sub(a).Hours() / 24)
}

// axisMember returns the member when the context is sliced on exactly one of the
// wanted axes (optionally alongside "operating segments" on the consolidation axis).
func axisMember(c *xcontext, names []string) string {
	member := ""
	for axis, m := range c.dims {
		switch {
		case slices.Contains(names, axis):
			if member != "" {
				return ""
			}
			member = m
		case axis == "ConsolidationItemsAxis" && local(m) == "OperatingSegmentsMember":
		default:
			return ""
		}
	}
	return member
}

func revenue(ctx context.Context, c Clients, f *filing) (*Revenue, error) {
	idx, err := c.SEC.Get(ctx, f.dir()+"index.json", "profile/idx-"+f.Accession+".json", 365*24*time.Hour)
	if err != nil {
		return nil, err
	}
	var dir struct {
		Directory struct {
			Item []struct{ Name string } `json:"item"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(idx, &dir); err != nil {
		return nil, err
	}
	var instance, labels string
	for _, it := range dir.Directory.Item {
		n := it.Name
		switch {
		case strings.HasSuffix(n, "_htm.xml"):
			instance = n
		case strings.HasSuffix(n, "_lab.xml"):
			labels = n
		case instance == "" && strings.HasSuffix(n, ".xml") && n != "FilingSummary.xml" &&
			!strings.HasSuffix(n, "_cal.xml") && !strings.HasSuffix(n, "_def.xml") && !strings.HasSuffix(n, "_pre.xml"):
			instance = n
		}
	}
	if instance == "" {
		return nil, fmt.Errorf("该年报没有 XBRL 数据")
	}
	body, err := c.SEC.Get(ctx, f.dir()+instance, "profile/xbrl-"+f.Accession+".xml", 365*24*time.Hour)
	if err != nil {
		return nil, err
	}
	ctxs, units, facts, err := parseInstance(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// The fiscal year: latest end date among undimensioned annual revenue facts.
	rv := &Revenue{Form: f.Form, URL: f.dir()}
	for _, fc := range facts {
		cx := ctxs[fc.ctx]
		if cx == nil || len(cx.dims) > 0 || days(cx.start, cx.end) < 340 || days(cx.start, cx.end) > 380 {
			continue
		}
		if cx.end > rv.PeriodEnd || (cx.end == rv.PeriodEnd && fc.value > rv.Total) {
			rv.PeriodEnd, rv.Total, rv.Currency = cx.end, fc.value, units[fc.unit]
		}
	}
	if rv.Total <= 0 {
		return nil, fmt.Errorf("年报 XBRL 中没有年度收入")
	}

	var all []*Breakdown
	for _, ax := range axes {
		vals := map[string]float64{}
		for _, fc := range facts {
			cx := ctxs[fc.ctx]
			if cx == nil || cx.end != rv.PeriodEnd || days(cx.start, cx.end) < 340 || units[fc.unit] != rv.Currency {
				continue
			}
			if m := axisMember(cx, ax.names); m != "" && fc.value > vals[m] {
				vals[m] = fc.value
			}
		}
		b := &Breakdown{Kind: ax.kind, Title: ax.title}
		for m, v := range vals {
			if v <= 0 || v >= rv.Total*0.999 {
				continue // aggregates restate the total
			}
			b.Items = append(b.Items, Segment{NameEn: m, Value: v, Share: v / rv.Total})
		}
		if len(b.Items) < 2 {
			continue
		}
		slices.SortFunc(b.Items, func(a, b Segment) int { return cmp.Compare(b.Value, a.Value) })
		all = append(all, b)
	}
	if len(all) == 0 {
		return rv, nil
	}

	names := memberLabels(ctx, c, f, labels)
	var en []string
	for _, b := range all {
		for i := range b.Items {
			b.Items[i].NameEn = labelFor(b.Items[i].NameEn, names)
			en = append(en, b.Items[i].NameEn)
		}
	}
	zh := en
	if t, err := translate(ctx, c.Web, strings.Join(en, "\n")); err == nil {
		if parts := strings.Split(strings.TrimSpace(t), "\n"); len(parts) == len(en) {
			zh = parts
		}
	}
	k := 0
	for _, b := range all {
		for i := range b.Items {
			b.Items[i].Name = strings.TrimSpace(zh[k])
			k++
		}
		b.Items = fitTotal(dedupe(b.Items), rv.Total, &b.Overlap)
		if len(b.Items) >= 2 {
			rv.Breakdowns = append(rv.Breakdowns, *b)
		}
	}
	return rv, nil
}

// memberLabels maps element ids (prefix_Local) to their standard label from the label linkbase.
func memberLabels(ctx context.Context, c Clients, f *filing, file string) map[string]string {
	out := map[string]string{}
	if file == "" {
		return out
	}
	body, err := c.SEC.Get(ctx, f.dir()+file, "profile/lab-"+f.Accession+".xml", 365*24*time.Hour)
	if err != nil {
		return out
	}
	locs := map[string]string{}   // xlink:label -> element id
	texts := map[string]string{}  // xlink:label -> text (standard/terse roles only)
	arcs := map[string][]string{} // loc label -> resource labels
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	var curLabel, curRole, text string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			get := func(n string) string {
				for _, a := range t.Attr {
					if a.Name.Local == n {
						return a.Value
					}
				}
				return ""
			}
			switch t.Name.Local {
			case "loc":
				if h := get("href"); strings.Contains(h, "#") {
					locs[get("label")] = h[strings.LastIndexByte(h, '#')+1:]
				}
			case "label":
				curLabel, curRole, text = get("label"), get("role"), ""
			case "labelArc":
				arcs[get("from")] = append(arcs[get("from")], get("to"))
			}
		case xml.CharData:
			text += string(t)
		case xml.EndElement:
			if t.Name.Local == "label" && curLabel != "" {
				if strings.HasSuffix(curRole, "/terseLabel") || (strings.HasSuffix(curRole, "/label") && texts[curLabel] == "") {
					texts[curLabel] = strings.TrimSpace(text)
				}
				curLabel = ""
			}
		}
	}
	for loc, id := range locs {
		for _, to := range arcs[loc] {
			if s := texts[to]; s != "" {
				out[id] = s
			}
		}
	}
	return out
}

var (
	reMemberSuffix = regexp.MustCompile(`\s*\[Member\]$`)
	reCamel        = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

func labelFor(qname string, labels map[string]string) string {
	if s := labels[strings.Replace(qname, ":", "_", 1)]; s != "" {
		return reMemberSuffix.ReplaceAllString(s, "")
	}
	name := strings.TrimSuffix(local(qname), "Member")
	if strings.HasPrefix(qname, "country:") {
		return name // ISO code, e.g. US, CN
	}
	return reCamel.ReplaceAllString(name, "$1 $2")
}

var reNameKey = regexp.MustCompile(`[^a-z0-9]+`)

// dedupe merges members whose labels differ only in case or punctuation
// ("Other Countries" / "Other countries"); they restate the same amount, so keep the larger.
func dedupe(items []Segment) []Segment {
	idx := map[string]int{}
	var out []Segment
	for _, it := range items {
		key := reNameKey.ReplaceAllString(strings.ToLower(it.NameEn), "")
		if i, ok := idx[key]; ok {
			if it.Value > out[i].Value {
				out[i] = it
			}
			continue
		}
		idx[key] = len(out)
		out = append(out, it)
	}
	slices.SortFunc(out, func(a, b Segment) int { return cmp.Compare(b.Value, a.Value) })
	return out
}

// fitTotal keeps the shares summing to at most 100%. When members overlap (a region and a
// country inside it), it keeps the largest three that fit together and folds the rest into
// "其他". Otherwise it keeps the top nine and adds the unallocated remainder as "其他".
func fitTotal(items []Segment, total float64, overlap *bool) []Segment {
	sum := 0.0
	for _, it := range items {
		sum += it.Value
	}
	var out []Segment
	used := 0.0
	if sum > total*1.005 {
		*overlap = true
		for _, it := range items {
			if used+it.Value <= total*1.005 {
				out = append(out, it)
				used += it.Value
				if len(out) == 3 {
					break
				}
			}
		}
	} else {
		for _, it := range items {
			if len(out) == 9 {
				break
			}
			out = append(out, it)
			used += it.Value
		}
	}
	if rest := total - used; rest > total*0.005 {
		out = append(out, Segment{Name: "其他", NameEn: "Other / unallocated", Value: rest, Share: rest / total})
	}
	return out
}
