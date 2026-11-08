package diagnose

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"stockscreener/internal/config"
	"stockscreener/internal/model"
	"stockscreener/internal/screen"
	"stockscreener/internal/store"
)

func TestParseEnvelope(t *testing.T) {
	// Real agy -p --output-format json output.
	agy := `{"conversation_id":"x","status":"SUCCESS","response":"1+1等于2。\n{\"answer\":\"a\",\"n\":2}\n","structured_output":{"answer":"a","n":2},"usage":{}}`
	js, _, err := parseEnvelope("agy", []byte(agy))
	if err != nil || string(js) != `{"answer":"a","n":2}` {
		t.Fatalf("agy: %s %v", js, err)
	}
	// Real grok error when signed out.
	grok := `{"type":"error","message":"Not signed in. To authenticate without a browser, run:\n  grok login --device-code"}`
	if _, _, err := parseEnvelope("grok", []byte(grok)); err == nil || !strings.Contains(err.Error(), "Not signed in") {
		t.Fatalf("grok error not surfaced: %v", err)
	}
	// Claude: structured_output, model from modelUsage.
	cl := `{"type":"result","is_error":false,"result":"ok","structured_output":{"n":1},"modelUsage":{"claude-opus-5-5":{}}}`
	js, model, err := parseEnvelope("claude", []byte(cl))
	if err != nil || string(js) != `{"n":1}` || model != "claude-opus-5-5" {
		t.Fatalf("claude: %s %s %v", js, model, err)
	}
	// Text answer with a fenced JSON block, after log lines.
	txt := "log line\n{\"result\":\"```json\\n{\\\"n\\\":3}\\n```\"}"
	js, _, err = parseEnvelope("grok", []byte(txt))
	if err != nil || string(js) != `{"n":3}` {
		t.Fatalf("fenced: %s %v", js, err)
	}
}

// Strict structured-output modes need every property required and no extras, at every level.
func TestSchemasStrict(t *testing.T) {
	var check func(path string, m map[string]any)
	check = func(path string, m map[string]any) {
		if m["type"] == "object" {
			props, _ := m["properties"].(map[string]any)
			req, _ := m["required"].([]any)
			if m["additionalProperties"] != false || len(req) != len(props) {
				t.Errorf("%s: not strict (%d required of %d)", path, len(req), len(props))
			}
			for k, v := range props {
				check(path+"."+k, v.(map[string]any))
			}
		}
		if it, ok := m["items"].(map[string]any); ok {
			check(path+"[]", it)
		}
	}
	for name, s := range map[string][]byte{"diagnosis": diagnosisSchema, "rollup": rollupSchema, "xpulse": xpulseSchema} {
		var m map[string]any
		if err := json.Unmarshal(s, &m); err != nil {
			t.Fatal(name, err)
		}
		check(name, m)
	}
}

func TestVerifyPosts(t *testing.T) {
	posts := []XPost{
		{Date: "2026-11-07", URL: "https://x.com/febtheteen/status/2118930931948146893"},     // ID dated 2026-11-07
		{Date: "2026-11-07", URL: "https://x.com/StockSavvyShay/status/1986638921045123072"}, // ID dated 2025-11: made up
		{Date: "2026-11-07", URL: "https://example.com/post"},
	}
	if n := verifyPosts(posts); n != 2 || posts[0].Suspect || !posts[1].Suspect || !posts[2].Suspect {
		t.Fatalf("got %d suspect: %+v", n, posts)
	}
	if posts[0].IDDate != "2026-11-07" {
		t.Fatalf("id date %s", posts[0].IDDate)
	}
}

func TestExtractLastObject(t *testing.T) {
	got := string(extractJSON([]byte(`{"tool":"x","posts":[]}{"tool":"y","posts":["a"]}`)))
	if got != `{"tool":"y","posts":["a"]}` {
		t.Fatal(got)
	}
}

func TestClaudeManualOnly(t *testing.T) {
	r := &Runner{queues: map[string]chan job{}, queued: map[string]job{}, active: map[string]*job{}}
	for _, e := range Engines {
		r.queues[e] = make(chan job, 10)
	}
	r.settings = func() Settings { s := DefaultSettings(); s.Engine = "claude"; return s }
	for _, trig := range []string{"batch", "schedule", "alert:x", "auto"} {
		r.enqueue(job{CIK: 1, Ticker: "A", Kind: "diagnosis", Trigger: trig, Engine: "claude"})
		r.enqueue(job{CIK: 2, Ticker: "B", Kind: "diagnosis", Trigger: trig}) // default engine is claude
	}
	if n := len(r.queues["claude"]); n != 0 {
		t.Fatalf("%d automatic jobs went to claude", n)
	}
	r.enqueue(job{CIK: 3, Ticker: "C", Kind: "diagnosis", Trigger: "manual", Engine: "claude"})
	if n := len(r.queues["claude"]); n != 1 {
		t.Fatalf("manual claude job not queued (%d)", n)
	}
	if err := r.StartBatch(context.Background(), []BatchTarget{{1, "A"}}, []string{"codex", "claude"}, false, true); err == nil {
		t.Fatal("batch with claude accepted")
	}
}

func TestPromptIncludesPeers(t *testing.T) {
	e := &screen.Evaluation{Company: model.Company{Ticker: "X", Name: "X Corp"}}
	peers := map[string]any{"范围": "行业 Software，同行 5 家", "同行中位": map[string]any{"较5年高点": -0.2}}
	p := buildPrompt(config.Convexity{}, e, nil, nil, nil, nil, nil, peers, time.Now())
	if !strings.Contains(p, "同行对比") || !strings.Contains(p, "行业 Software") {
		t.Fatal("peer comparison missing from the prompt")
	}
	if strings.Contains(buildPrompt(config.Convexity{}, e, nil, nil, nil, nil, nil, nil, time.Now()), `"同行对比"`) {
		t.Fatal("empty peer comparison should be omitted")
	}
}

// Gemini's API rejects "" in an enum, so no schema may contain one.
func TestSchemasHaveNoEmptyEnum(t *testing.T) {
	for name, sc := range map[string]string{"diagnosis": string(diagnosisSchema), "rollup": string(rollupSchema), "xpulse": string(xpulseSchema)} {
		if strings.Contains(sc, `"enum":[""`) || strings.Contains(sc, `,""]`) || strings.Contains(sc, `,"",`) {
			t.Errorf("%s schema has an empty enum value", name)
		}
	}
}

func TestNormalizeFalsifiers(t *testing.T) {
	in := `{"summary":"x","falsifiers":[{"text":"a","metric":"none","op":"none","value":0},{"text":"b","metric":"grossMargin","op":"<","value":0.5}]}`
	var got struct {
		Summary    string
		Falsifiers []Falsifier
	}
	if err := json.Unmarshal(normalizeFalsifiers(json.RawMessage(in)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Summary != "x" || got.Falsifiers[0].Metric != "" || got.Falsifiers[0].Op != "" || got.Falsifiers[1].Metric != "grossMargin" || got.Falsifiers[1].Op != "<" {
		t.Fatalf("got %+v", got)
	}
}

// A skill sees facts only: none of the framework, verdicts, target / upside or grades, unless
// it opts into them; the skill's own instructions lead the prompt.
func TestSkillPromptIsFactsOnly(t *testing.T) {
	up, mult := 1.5, 8.0
	e := &screen.Evaluation{Company: model.Company{Ticker: "X", Name: "X Corp", Currency: "USD"}, Stage: 3,
		Checks:    []screen.Check{{Layer: 1, Name: "质量分", Pass: true, Value: "80", Rule: "≥ 60"}},
		Hints:     []screen.Hint{{Kind: "external", Text: "行业整体下跌"}},
		Valuation: &screen.Valuation{Price: 10, High5y: 20, Drawdown: -0.5, Multiple: &mult, Upside: &up, Target: &mult},
		Analysis: &screen.Analysis{Groups: []screen.MetricGroup{{Title: "资本回报", Metrics: []screen.Metric{
			{Name: "ROIC", Value: model.F(0.2), Format: "pct", Tone: "good", AsOf: "2025"}}}}},
	}
	j := &store.Judgement{Verdict: "watch", Worry: "关税"}
	peers := map[string]any{"范围": "行业 Software", "用途": "判断行业与公司因素"}
	r := &Runner{}
	in := &inputs{e: e, j: j, peers: peers}
	p := buildSkillPrompt(r, &store.Skill{Instructions: "用护城河框架分析"}, in, time.Now())
	if !strings.HasPrefix(p, "用护城河框架分析") {
		t.Fatal("skill instructions must lead the prompt")
	}
	for _, banned := range []string{"凸性错杀", "第二意见", "筛选结果", "上行空间", "目标倍数", "检查项", "系统线索", "（2025）好", "投资者自己的判断", "判断行业与公司因素"} {
		if strings.Contains(p, banned) {
			t.Errorf("facts-only prompt contains %q", banned)
		}
	}
	for _, want := range []string{"当前市值/正常化FCF", "ROIC=20.0%", "行业 Software"} {
		if !strings.Contains(p, want) {
			t.Errorf("facts-only prompt is missing %q", want)
		}
	}
	p = buildSkillPrompt(r, &store.Skill{Instructions: "x", Include: []string{IncludeScreen, IncludeJudgement}}, in, time.Now())
	for _, want := range []string{"筛选结果", "上行空间", "投资者自己的判断", "这些是观点而不是事实"} {
		if !strings.Contains(p, want) {
			t.Errorf("opted-in material %q missing", want)
		}
	}
}
