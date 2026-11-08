package profile

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

var (
	reDrop    = regexp.MustCompile(`(?is)<(script|style|head|ix:header)[^>]*>.*?</(script|style|head|ix:header)>`)
	reBlock   = regexp.MustCompile(`(?i)</(p|div|tr|li|h[1-6]|table)>|<br\s*/?>`)
	reTag     = regexp.MustCompile(`<[^>]+>`)
	reSpaces  = regexp.MustCompile(`[ \t\r\f\v\x{00a0}\x{200b}]+`)
	reItem1   = regexp.MustCompile(`(?i)^item\s*1\s*[.:\-–—]?\s*(business|description of business)\.?$`)
	reItem1A  = regexp.MustCompile(`(?i)^item\s*1a\b`)
	reItem4   = regexp.MustCompile(`(?i)^item\s*4\s*[.:\-–—]?\s*information on the company\.?$`)
	reItem4A  = regexp.MustCompile(`(?i)^item\s*4a\b`)
	reBizOver = regexp.MustCompile(`(?i)^([a-z]\.\s*)?business overview\.?$`)
	reBoiler  = regexp.MustCompile(`(?i)(are|is) referred to as|we refer to|unless (otherwise indicated|the context)|forward-looking statements|^(please )?(see|refer to) (the )?(sections?|pages?|“|")`)
	reItemNum = regexp.MustCompile(`(?i)^item\s*\d+[a-z]?\.?$`)
)

// htmlLines flattens a filing into trimmed, non-empty text lines (one per block element).
func htmlLines(doc string) []string {
	doc = reDrop.ReplaceAllString(doc, "")
	doc = reBlock.ReplaceAllString(doc, "\n")
	doc = reTag.ReplaceAllString(doc, " ")
	doc = html.UnescapeString(doc)
	var out []string
	for _, l := range strings.Split(doc, "\n") {
		l = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
		if l == "" {
			continue
		}
		// Headings are often split across table cells: "Item 1." / "Business".
		if n := len(out); n > 0 && reItemNum.MatchString(out[n-1]) && len(l) < 60 {
			out[n-1] += " " + l
			continue
		}
		out = append(out, l)
	}
	return out
}

// section finds the heading that starts the real section (not its table-of-contents
// entry, which is followed within a few lines by the next item) and returns the lines after it.
func section(lines []string, heading, next *regexp.Regexp) []string {
	for i, l := range lines {
		if !heading.MatchString(l) {
			continue
		}
		toc := false
		for j := i + 1; j < len(lines) && j <= i+6; j++ {
			if len(lines[j]) >= 140 {
				break // prose: this is the real section
			}
			if next.MatchString(lines[j]) {
				toc = true
				break
			}
		}
		if !toc {
			return lines[i+1:]
		}
	}
	return nil
}

// paragraphs takes leading prose paragraphs (skipping subheadings) until limit characters or stop.
func paragraphs(lines []string, stop *regexp.Regexp, limit int) []string {
	var out []string
	n := 0
	for _, l := range lines {
		if stop != nil && stop.MatchString(l) {
			break
		}
		if len(l) < 140 || strings.Count(l, " ") < 15 || reBoiler.MatchString(l) {
			continue // headings, table cells, page numbers, definitions of "we"/"fiscal 2025"
		}
		out = append(out, l)
		if n += len(l); n >= limit || len(out) >= 5 {
			break
		}
	}
	return out
}

func overview(ctx context.Context, c Clients, f *filing) (*Overview, error) {
	if f.Form == "40-F" {
		return nil, fmt.Errorf("40-F 的业务介绍在附件（AIF）里，暂不解析")
	}
	body, err := c.SEC.Get(ctx, f.dir()+f.Primary, "profile/doc-"+f.Accession+".htm", 90*24*time.Hour)
	if err != nil {
		return nil, err
	}
	lines := htmlLines(string(body))
	var paras []string
	if f.Form == "20-F" {
		sec := section(lines, reItem4, reItem4A)
		if sec == nil { // some filers style the Item 4 heading so it doesn't survive flattening
			sec = section(lines, reBizOver, reItem4A)
		}
		if len(sec) > 600 {
			sec = sec[:600]
		}
		start := sec
		for i, l := range sec {
			if reBizOver.MatchString(l) {
				start = sec[i+1:]
				break
			}
		}
		paras = paragraphs(start, reItem4A, 2200)
	} else {
		paras = paragraphs(section(lines, reItem1, reItem1A), reItem1A, 2200)
	}
	if len(paras) == 0 {
		return nil, fmt.Errorf("未能在 %s 中定位业务介绍章节", f.Form)
	}
	orig := strings.Join(paras, "\n")
	ov := &Overview{Original: orig, Text: orig, Form: f.Form, FiledAt: f.FiledAt, URL: f.dir() + f.Primary}
	if zh, err := translate(ctx, c.Web, orig); err == nil && zh != "" {
		ov.Text, ov.Translated = zh, true
	}
	return ov, nil
}
