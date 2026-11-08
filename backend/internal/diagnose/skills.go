package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"stockscreener/internal/store"
)

// Skills replace the built-in analysis: the person's own instructions over the company's
// facts only – no "convexity mispricing" framework, no pass / fail verdicts, no system
// grades – unless the skill opts into that material. The answer is free-form Markdown.

var skillSchema = objectSchema(map[string]any{
	"markdown": map[string]any{"type": "string", "description": "完整的分析结果，Markdown 格式"},
})

func includeSet(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// SkillPrompt builds a skill's prompt for a company (also offered for copy-paste into a chat).
func (r *Runner) SkillPrompt(ctx context.Context, cik int, sk *store.Skill) (string, *inputs, error) {
	in, err := r.gather(ctx, cik)
	if err != nil {
		return "", nil, err
	}
	return buildSkillPrompt(r, sk, in, time.Now()), in, nil
}

// FactsPrompt is the facts-only data block alone, for pasting under one's own instructions.
func (r *Runner) FactsPrompt(ctx context.Context, cik int) (string, error) {
	in, err := r.gather(ctx, cik)
	if err != nil {
		return "", err
	}
	return factsBlock(r, in, nil, time.Now()), nil
}

func factsBlock(r *Runner, in *inputs, include []string, now time.Time) string {
	d := buildData(r.cfg.Convexity, in.e, in.p, in.j, in.last, in.pulse, in.live, in.peers, dataOpts{facts: true, include: includeSet(include)})
	body, _ := json.MarshalIndent(d, "", " ")
	var extra []string
	labels := map[string]string{IncludeScreen: "系统筛选结论", IncludeJudgement: "投资者自己的判断", IncludeLastAI: "上次 AI 诊断", IncludeXPulse: "X 舆情汇总"}
	for _, k := range IncludeKeys {
		if includeSet(include)[k] {
			extra = append(extra, labels[k])
		}
	}
	head := fmt.Sprintf("以下是 %s（%s）的数据：SEC 财报、Yahoo 行情、新闻与关注度，以及由这些数据直接计算的指标。今天是 %s。"+
		"财务金额为报告币种的原始数值，估值金额为美元；比例为小数（0.25 = 25%%）。", in.e.Ticker, in.e.Name, now.Format(time.DateOnly))
	if len(extra) > 0 {
		head += "另附带" + strings.Join(extra, "、") + "，这些是观点而不是事实。"
	}
	return head + "\n\n<数据>\n" + string(body) + "\n</数据>\n"
}

func buildSkillPrompt(r *Runner, sk *store.Skill, in *inputs, now time.Time) string {
	return strings.TrimSpace(sk.Instructions) + "\n\n---\n\n" + factsBlock(r, in, sk.Include, now) +
		"\n（运行说明：直接根据上面的材料和你已有的知识回答，不要运行命令、读写文件或联网。语言按上面指令的要求，未指定时用简体中文。结果以 Markdown 显示，不支持 LaTeX，公式请用纯文本写。）\n"
}

// EnqueueSkill queues a skill run (always a manual trigger). engine "" uses the skill's
// default engine, else the AI settings' engine.
func (r *Runner) EnqueueSkill(cik int, ticker string, skillID int64, engine string) bool {
	if !validEngine(engine) {
		if sk, err := r.st.Skill(skillID); err == nil && validEngine(sk.Engine) {
			engine = sk.Engine
		}
	}
	return r.enqueue(job{CIK: cik, Ticker: strings.ToUpper(ticker), Kind: "skill", Trigger: "manual", Engine: engine, SkillID: skillID})
}

func (r *Runner) runSkill(ctx context.Context, j job, o runOpts) error {
	sk, err := r.st.Skill(j.SkillID)
	if err != nil {
		return fmt.Errorf("skill %d：%w", j.SkillID, err)
	}
	x := store.SkillRun{SkillID: sk.ID, SkillName: sk.Name, Instructions: sk.Instructions, Include: sk.Include,
		CIK: j.CIK, Ticker: j.Ticker, Engine: o.Engine, Model: o.Model}
	start := time.Now()
	err = func() error {
		prompt, in, err := r.SkillPrompt(ctx, j.CIK, sk)
		if err != nil {
			return err
		}
		if v := in.e.Valuation; v != nil {
			x.Basis, x.Price = v.Basis, v.Price
		}
		if in.live != nil {
			x.Price = in.live.Price
		}
		out, err := run(ctx, o, r.work, prompt, skillSchema)
		if err != nil {
			return err
		}
		if out.Model != "" {
			x.Model = out.Model
		}
		if m, err := json.Marshal(out.Meta); err == nil {
			x.Meta = m
		}
		var res struct {
			Markdown string `json:"markdown"`
		}
		if err := json.Unmarshal(out.JSON, &res); err != nil || strings.TrimSpace(res.Markdown) == "" {
			x.Output = string(out.JSON)
			return fmt.Errorf("返回结果没有 markdown 字段")
		}
		x.Output = res.Markdown
		return nil
	}()
	x.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		x.Error = err.Error()
	}
	if _, serr := r.st.SaveSkillRun(x); serr != nil {
		return serr
	}
	return err
}
