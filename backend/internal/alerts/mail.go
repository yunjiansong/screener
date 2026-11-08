package alerts

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SMTP struct {
	Host     string `json:"host"` // e.g. smtp.gmail.com
	Port     int    `json:"port"` // 587 (STARTTLS) or 465 (TLS)
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // or env SMTP_PASSWORD; never returned by the API
	From     string `json:"from"`
	To       string `json:"to"` // comma-separated
}

func (s SMTP) Configured() bool { return s.Host != "" && s.To != "" }

type Settings struct {
	Enabled       bool   `json:"enabled"`       // scheduled scans while `serve` runs
	IntervalHours int    `json:"intervalHours"` // between scheduled scans
	BaseURL       string `json:"baseUrl"`       // links in emails point here
	SMTP          SMTP   `json:"smtp"`
	Defaults      Rules  `json:"defaults"`
}

func DefaultSettings() Settings {
	return Settings{Enabled: true, IntervalHours: 6, BaseURL: "http://127.0.0.1:8090", SMTP: SMTP{Port: 587}, Defaults: DefaultRules()}
}

// LoadSettings overlays stored settings on the defaults; SMTP_PASSWORD in the environment wins.
func LoadSettings(raw json.RawMessage) Settings {
	s := DefaultSettings()
	if raw != nil {
		_ = json.Unmarshal(raw, &s)
	}
	if p := os.Getenv("SMTP_PASSWORD"); p != "" {
		s.SMTP.Password = p
	}
	if s.IntervalHours < 1 {
		s.IntervalHours = 6
	}
	return s
}

// Digest is one email: every company with at least one newly fired alert.
type Digest struct {
	At      time.Time
	BaseURL string
	Items   []DigestItem
}

type DigestItem struct {
	CIK          int
	Ticker, Name string
	Fired        []Condition
	Snap         Snapshot
}

var digestTmpl = template.Must(template.New("d").Funcs(template.FuncMap{
	"pct": func(p *float64) string {
		if p == nil {
			return "—"
		}
		return pct(*p)
	},
	"pctv": pct,
}).Parse(`<!doctype html><html><body style="margin:0;background:#f5f5f2;font-family:-apple-system,'PingFang SC','Helvetica Neue',sans-serif;color:#1d1c1a;">
<div style="max-width:640px;margin:0 auto;padding:24px 16px;">
<div style="font-size:18px;font-weight:650;margin-bottom:4px;">观察列表提醒</div>
<div style="font-size:12px;color:#97948d;margin-bottom:16px;">{{.At.Format "2006-01-02 15:04"}} · {{len .Items}} 家公司触发 · 提醒只是线索，是否行动由你判断</div>
{{range .Items}}
<div style="background:#fff;border:1px solid #e6e4df;border-radius:12px;padding:16px 18px;margin-bottom:12px;">
  <div style="display:flex;justify-content:space-between;align-items:baseline;">
    <div><a href="{{$.BaseURL}}/#/c/{{.Ticker}}" style="font-size:16px;font-weight:650;color:#2a62c9;text-decoration:none;">{{.Ticker}}</a>
    <span style="color:#6f6c66;font-size:13px;margin-left:6px;">{{.Name}}</span></div>
  </div>
  <ul style="margin:10px 0;padding-left:18px;font-size:14px;line-height:1.7;">
  {{range .Fired}}<li><b>{{.Label}}</b>：{{.Text}}</li>{{end}}
  </ul>
  <div style="font-size:12px;color:#6f6c66;border-top:1px solid #efede9;padding-top:8px;">
    现价 ${{printf "%.2f" .Snap.Price}}（{{.Snap.AsOf}}，单日 {{pct .Snap.DayChange}}）· 较5年高点 {{pctv .Snap.Drawdown}} · FCF 收益率 {{pct .Snap.FCFYield}} · 估值分位 {{pct .Snap.Percentile}} · 上行 {{pct .Snap.Upside}}
  </div>
</div>
{{end}}
<div style="font-size:11px;color:#a9a69f;">基本面数据来自最近一次全市场筛选（{{if .Items}}{{(index .Items 0).Snap.Basis}}{{end}}），价格来自 Yahoo（非官方接口）。在网页的「提醒设置」里可以调整阈值或关闭提醒。</div>
</div></body></html>`))

func (d Digest) Subject() string {
	var ts []string
	for _, it := range d.Items {
		ts = append(ts, it.Ticker)
	}
	return fmt.Sprintf("【选股提醒】%s 触发 %d 条", strings.Join(ts, "、"), d.count())
}

func (d Digest) count() int {
	n := 0
	for _, it := range d.Items {
		n += len(it.Fired)
	}
	return n
}

func (d Digest) HTML() (string, error) {
	var b bytes.Buffer
	err := digestTmpl.Execute(&b, d)
	return b.String(), err
}

// Deliver sends the email, or writes it to outboxDir when SMTP is not configured.
// It returns "sent" or "outbox: <path>".
func Deliver(s SMTP, outboxDir, subject, html string) (string, error) {
	if !s.Configured() {
		if err := os.MkdirAll(outboxDir, 0o755); err != nil {
			return "", err
		}
		path := filepath.Join(outboxDir, time.Now().Format("20060102-150405")+".html")
		return "outbox: " + path, os.WriteFile(path, []byte(html), 0o644)
	}
	return "sent", send(s, subject, html)
}

func send(s SMTP, subject, html string) error {
	from := s.From
	if from == "" {
		from = s.Username
	}
	var to []string
	for _, t := range strings.Split(s.To, ",") {
		if t = strings.TrimSpace(t); t != "" {
			to = append(to, t)
		}
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n",
		from, strings.Join(to, ", "), mime.BEncoding.Encode("UTF-8", subject), time.Now().Format(time.RFC1123Z))
	enc := base64.StdEncoding.EncodeToString([]byte(html))
	for len(enc) > 76 {
		msg.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	msg.WriteString(enc + "\r\n")

	addr := net.JoinHostPort(s.Host, fmt.Sprint(s.Port))
	var auth smtp.Auth
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}
	if s.Port != 465 {
		return smtp.SendMail(addr, auth, from, to, msg.Bytes()) // upgrades with STARTTLS when offered
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 20 * time.Second}, "tcp", addr, &tls.Config{ServerName: s.Host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
