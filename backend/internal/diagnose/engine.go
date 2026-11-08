package diagnose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Engines run a locally installed, logged-in agent CLI in its non-interactive mode, so the
// diagnosis uses the person's own subscription instead of an API key:
//
//	codex exec  — OpenAI Codex CLI signed in with a ChatGPT account
//	claude -p   — Claude Code signed in with a Claude account
//	agy -p      — Google Antigravity CLI signed in with a Google account (Gemini models)
//	grok -p     — Grok CLI signed in with an X / xAI account; the only one used with search on,
//	              for the X (Twitter) pulse
//
// For diagnoses the agent gets all data in the prompt and no tools (or the most restrictive
// mode the CLI offers), running in an empty scratch directory.

var engineNames = map[string]string{"codex": "Codex（ChatGPT 订阅）", "claude": "Claude Code（Claude 订阅）",
	"agy": "Antigravity（Google 账号 · Gemini）", "grok": "Grok（X / xAI 账号）"}

// Engines in display order.
var Engines = []string{"codex", "claude", "agy", "grok"}

func validEngine(e string) bool { _, ok := engineNames[e]; return ok }

// findBinary looks on PATH, then in the usual per-user install locations (a server started
// by launchd may have a minimal PATH).
func findBinary(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{".local/bin", "." + name + "/bin", ".npm-global/bin", "bin"} {
		p := filepath.Join(home, dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	for _, p := range []string{"/opt/homebrew/bin/" + name, "/usr/local/bin/" + name} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

type EngineStatus struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Path      string `json:"path"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"` // version / login state, or why it is unavailable
}

func engineStatus(ctx context.Context, name string) EngineStatus {
	st := EngineStatus{Name: name, Label: engineNames[name], Path: findBinary(name)}
	if st.Path == "" {
		st.Detail = "没有找到 " + name + " 命令"
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ver, _ := exec.CommandContext(ctx, st.Path, "--version").CombinedOutput()
	st.Detail = strings.TrimSpace(string(ver))
	st.Available = true
	switch name {
	case "codex":
		out, err := exec.CommandContext(ctx, st.Path, "login", "status").CombinedOutput()
		login := strings.TrimSpace(string(out))
		if err != nil || !strings.Contains(strings.ToLower(login), "logged in") {
			st.Available = false
			st.Detail += " · 未登录：在终端运行 codex login，选择用 ChatGPT 账号登录"
		} else {
			st.Detail += " · " + login
		}
	case "grok": // "grok models" prints "You are not authenticated." when signed out
		out, _ := exec.CommandContext(ctx, st.Path, "models").CombinedOutput()
		if strings.Contains(strings.ToLower(string(out)), "not authenticated") {
			st.Available = false
			st.Detail += " · 未登录：在终端运行 grok login"
		}
	}
	return st
}

// Grok CLI built-in tools that touch the machine (from its own tool listing, v1.0.77).
const grokLocalTools = "run_terminal_command,read_file,search_replace,write,list_dir,grep,kill_command_or_subagent," +
	"todo_write,get_command_or_subagent_output,monitor,workflow,use_tool,enter_plan_mode,exit_plan_mode," +
	"ask_user_question,send_feedback,image_gen,image_edit,image_to_video,reference_to_video"

const grokSearchTools = "web_search,web_fetch,open_page,open_page_with_find,search_tool," +
	"x_keyword_search,x_semantic_search,x_user_search,x_thread_fetch"

type runOutput struct {
	JSON  json.RawMessage
	Model string
	Meta  CallMeta
}

// CallMeta records how a CLI call ran, for display ("Grok 搜索 12 轮 · 3.1 万 token · $0.21").
// Usage fields are whatever the engine's JSON envelope reports (Grok and Claude do; Codex doesn't).
type CallMeta struct {
	Command      string   `json:"command"`         // binary + flags, prompt and schema elided
	Search       bool     `json:"search"`          // web / X search allowed
	Tools        []string `json:"tools,omitempty"` // search tools offered
	MaxTurns     int      `json:"maxTurns,omitempty"`
	Turns        int      `json:"turns,omitempty"`
	ModelCalls   int      `json:"modelCalls,omitempty"`
	InputTokens  int      `json:"inputTokens,omitempty"`
	OutputTokens int      `json:"outputTokens,omitempty"`
	CostUSD      float64  `json:"costUsd,omitempty"`
}

// grokXTools are the X tools Grok's search turns can use (they run on xAI's side).
var grokXTools = []string{"x_keyword_search", "x_semantic_search", "x_user_search", "x_thread_fetch", "web_search", "web_fetch", "open_page"}

// usageOf reads turn / token / cost counts from a JSON envelope; missing fields stay zero.
func usageOf(b []byte, m *CallMeta) {
	var env struct {
		NumTurns int     `json:"num_turns"`
		Cost     float64 `json:"total_cost_usd"`
		Usage    struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
		ModelUsage map[string]struct {
			Calls int `json:"modelCalls"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal(bytes.TrimSpace(lastJSONLine(b)), &env) != nil {
		return
	}
	m.Turns, m.CostUSD, m.InputTokens, m.OutputTokens = env.NumTurns, env.Cost, env.Usage.In, env.Usage.Out
	for _, u := range env.ModelUsage {
		m.ModelCalls += u.Calls
	}
}

// commandLine shows the call without the (long) prompt and schema arguments.
func commandLine(engine string, args []string) string {
	out := []string{engine}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--json-schema", "--disallowed-tools":
			out = append(out, a, "…")
			i++
			continue
		case "-p":
			if engine == "agy" {
				out = append(out, a, "<提示词>")
				i++
				continue
			}
		}
		if strings.Contains(a, string(filepath.Separator)) {
			a = filepath.Base(a)
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}

// runOpts: Search lets the agent use web / X search (Grok's X pulse only).
type runOpts struct {
	Engine, Model string
	Search        bool
	Timeout       time.Duration
}

// run sends prompt to the engine and returns the JSON object matching schema.
func run(ctx context.Context, o runOpts, workDir, prompt string, schema []byte) (*runOutput, error) {
	bin := findBinary(o.Engine)
	if bin == "" {
		return nil, fmt.Errorf("没有找到 %s 命令", o.Engine)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(workDir, "run-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	outFile := filepath.Join(dir, "out.json")
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o644); err != nil {
		return nil, err
	}
	var args []string
	stdin := ""
	switch o.Engine {
	case "codex":
		schemaFile := filepath.Join(dir, "schema.json")
		if err := os.WriteFile(schemaFile, schema, 0o644); err != nil {
			return nil, err
		}
		args = []string{"exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only", "-C", dir,
			"--output-schema", schemaFile, "-o", outFile, "--color", "never"}
		if o.Model != "" {
			args = append(args, "-m", o.Model)
		}
		args, stdin = append(args, "-"), prompt
	case "claude":
		args = []string{"-p", "--output-format", "json", "--json-schema", string(schema), "--tools", "", "--no-session-persistence"}
		if o.Model != "" {
			args = append(args, "--model", o.Model)
		}
		stdin = prompt
	case "agy":
		args = []string{"-p", prompt, "--output-format", "json", "--json-schema", string(schema), "--sandbox",
			"--disable-slash-commands", "--print-timeout", o.Timeout.String()}
		if o.Model != "" {
			args = append(args, "--model", o.Model)
		}
	case "grok":
		// Grok's --tools allow-list silently falls back to *all* tools when a name is unknown, so
		// tools are removed by deny-list instead, and nothing is auto-approved: dontAsk denies
		// anything not explicitly allowed. The X search tools run on xAI's side.
		deny := grokLocalTools
		if !o.Search {
			deny += "," + grokSearchTools
		}
		args = []string{"--prompt-file", promptFile, "--output-format", "json", "--json-schema", string(schema),
			"--cwd", dir, "--no-subagents", "--no-plan", "--disallowed-tools", deny, "--permission-mode", "dontAsk"}
		if o.Search {
			args = append(args, "--allow", "web_search", "--allow", "web_fetch", "--allow", "open_page", "--max-turns", "40")
		} else {
			args = append(args, "--disable-web-search", "--max-turns", "4")
		}
		if o.Model != "" {
			args = append(args, "-m", o.Model)
		}
	default:
		return nil, fmt.Errorf("未知引擎 %q", o.Engine)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s 超过 %s 没有返回", o.Engine, o.Timeout)
		}
		// agy / grok print a JSON envelope even on failure; its "error" field is the useful part
		// (the tail of stdout is mostly the echoed schema).
		var env struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(stdout.Bytes(), &env) == nil && env.Error != "" {
			return nil, fmt.Errorf("%s 运行失败：%v：%s", o.Engine, err, tail(env.Error, 600))
		}
		return nil, fmt.Errorf("%s 运行失败：%v：%s", o.Engine, err, tail(stderr.String()+stdout.String(), 600))
	}

	out := &runOutput{Model: o.Model, Meta: CallMeta{Command: commandLine(o.Engine, args), Search: o.Search}}
	if o.Engine == "grok" {
		out.Meta.MaxTurns = 4
		if o.Search {
			out.Meta.MaxTurns, out.Meta.Tools = 40, grokXTools
		}
	}
	if o.Engine != "codex" {
		usageOf(stdout.Bytes(), &out.Meta)
	}
	if o.Engine == "codex" {
		b, err := os.ReadFile(outFile)
		if err != nil {
			return nil, fmt.Errorf("codex 没有输出结果：%s", tail(stderr.String()+stdout.String(), 600))
		}
		out.JSON = extractJSON(b)
		if out.Model == "" { // the run log header names the model, e.g. "model: gpt-…"
			for _, line := range strings.Split(stderr.String()+"\n"+stdout.String(), "\n") {
				if m, ok := strings.CutPrefix(strings.TrimSpace(line), "model: "); ok {
					out.Model = m
					break
				}
			}
		}
	} else {
		js, model, err := parseEnvelope(o.Engine, stdout.Bytes())
		if err != nil {
			return nil, err
		}
		out.JSON = js
		if out.Model == "" {
			out.Model = model
		}
	}
	if !json.Valid(out.JSON) {
		return nil, fmt.Errorf("%s 返回的不是有效 JSON：%s", o.Engine, tail(string(out.JSON), 400))
	}
	return out, nil
}

// parseEnvelope reads the JSON envelope printed by claude / agy / grok in json output mode:
// structured_output when present, otherwise the JSON inside the text answer.
func parseEnvelope(engine string, b []byte) (json.RawMessage, string, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(lastJSONLine(b)), &env); err != nil {
		return extractJSON(b), "", nil // plain text answer
	}
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(env[k], &v)
		return v
	}
	if str("type") == "error" || string(env["is_error"]) == "true" {
		msg := str("message") + str("result") + str("error")
		return nil, "", fmt.Errorf("%s 返回错误：%s", engine, tail(msg, 600))
	}
	if st := str("status"); st != "" && st != "SUCCESS" && st != "success" {
		return nil, "", fmt.Errorf("%s 状态 %s：%s", engine, st, tail(str("response")+str("error"), 600))
	}
	model := str("model")
	if model == "" {
		var mu map[string]any
		if json.Unmarshal(env["modelUsage"], &mu) == nil {
			for m := range mu {
				model = m
			}
		}
	}
	for _, k := range []string{"structured_output", "structuredOutput", "output"} {
		if v := bytes.TrimSpace(env[k]); len(v) > 0 && v[0] == '{' {
			return json.RawMessage(v), model, nil
		}
	}
	for _, k := range []string{"result", "response", "text", "content", "message"} {
		if s := str(k); s != "" {
			return extractJSON([]byte(s)), model, nil
		}
	}
	return nil, model, fmt.Errorf("%s 输出里没有找到结果：%s", engine, tail(string(b), 400))
}

// lastJSONLine returns the last line that looks like a JSON object (some CLIs log before it).
func lastJSONLine(b []byte) []byte {
	t := bytes.TrimSpace(b)
	if json.Valid(t) {
		return t
	}
	lines := bytes.Split(t, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 && l[0] == '{' && json.Valid(l) {
			return l
		}
	}
	return t
}

// extractJSON finds the answer object in text that may have code fences or prose around it, or
// several objects back to back (Grok sometimes emits drafts before the final one): the last
// complete top-level object wins.
func extractJSON(b []byte) json.RawMessage {
	s := strings.TrimSpace(string(b))
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	var last json.RawMessage
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var obj json.RawMessage
		if err := dec.Decode(&obj); err == nil && len(obj) > 0 && obj[0] == '{' {
			last = obj
			i += int(dec.InputOffset()) - 1
		}
	}
	if last != nil {
		return last
	}
	return json.RawMessage(s)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}
