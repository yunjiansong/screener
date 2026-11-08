import { Fragment, ReactNode } from "react";

// A small Markdown renderer for skill results: headings, lists, quotes, code blocks, tables,
// rules, bold / italic / code / links. It builds React elements (never HTML strings), so
// model output can't inject markup.

const INLINE = /(\*\*[^*]+\*\*|__[^_]+__|`[^`]+`|\[[^\]]+\]\([^)\s]+\)|\*[^*\s][^*]*\*)/g;

function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let k = 0;
  for (const m of text.matchAll(INLINE)) {
    if (m.index! > last) out.push(text.slice(last, m.index));
    const t = m[0];
    if (t.startsWith("**") || t.startsWith("__")) out.push(<strong key={k++}>{inline(t.slice(2, -2))}</strong>);
    else if (t.startsWith("`")) out.push(<code key={k++}>{t.slice(1, -1)}</code>);
    else if (t.startsWith("[")) {
      const [, label, url] = t.match(/^\[([^\]]+)\]\(([^)\s]+)\)$/)!;
      out.push(/^https?:\/\//.test(url) ? <a key={k++} href={url} target="_blank" rel="noreferrer noopener">{label}</a> : label);
    } else out.push(<em key={k++}>{inline(t.slice(1, -1))}</em>);
    last = m.index! + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

const cells = (line: string) => line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());

export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const blocks: ReactNode[] = [];
  let i = 0;
  let k = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    if (line.startsWith("```")) {
      const body: string[] = [];
      for (i++; i < lines.length && !lines[i].startsWith("```"); i++) body.push(lines[i]);
      i++;
      blocks.push(<pre key={k++}><code>{body.join("\n")}</code></pre>);
      continue;
    }
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      const level = Math.min(6, h[1].length + 2); // # → h3: the card already has a title
      const Tag = `h${level}` as "h3";
      blocks.push(<Tag key={k++}>{inline(h[2])}</Tag>);
      i++;
      continue;
    }
    if (/^(-{3,}|\*{3,}|_{3,})\s*$/.test(line.trim())) {
      blocks.push(<hr key={k++} />);
      i++;
      continue;
    }
    if (line.trim().startsWith("|") && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1])) {
      const head = cells(line);
      const rows: string[][] = [];
      for (i += 2; i < lines.length && lines[i].trim().startsWith("|"); i++) rows.push(cells(lines[i]));
      blocks.push(
        <div key={k++} className="md-table">
          <table>
            <thead><tr>{head.map((c, j) => <th key={j}>{inline(c)}</th>)}</tr></thead>
            <tbody>{rows.map((r, ri) => <tr key={ri}>{r.map((c, j) => <td key={j}>{inline(c)}</td>)}</tr>)}</tbody>
          </table>
        </div>,
      );
      continue;
    }
    if (line.startsWith(">")) {
      const body: string[] = [];
      for (; i < lines.length && lines[i].startsWith(">"); i++) body.push(lines[i].replace(/^>\s?/, ""));
      blocks.push(<blockquote key={k++}><Markdown text={body.join("\n")} /></blockquote>);
      continue;
    }
    const li = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
    if (li.test(line)) {
      const ordered = /\d/.test(line.match(li)![2]);
      const items: { depth: number; text: string }[] = [];
      for (; i < lines.length; i++) {
        const m = lines[i].match(li);
        if (m) items.push({ depth: m[1].length >= 2 ? 1 : 0, text: m[3] });
        else if (lines[i].trim() && /^\s{2,}/.test(lines[i]) && items.length) items[items.length - 1].text += " " + lines[i].trim();
        else break;
      }
      const L = ordered ? "ol" : "ul";
      blocks.push(
        <L key={k++}>
          {items.map((it, j) => (
            <li key={j} className={it.depth ? "sub" : ""}>{inline(it.text)}</li>
          ))}
        </L>,
      );
      continue;
    }
    const para: string[] = [];
    for (; i < lines.length && lines[i].trim() && !/^(#{1,6}\s|```|>|\s*([-*+]|\d+[.)])\s|\s*\|)/.test(lines[i]); i++) para.push(lines[i].trim());
    if (!para.length) {
      // a line no block rule took (e.g. a lone "|"): show it as text rather than loop
      para.push(lines[i].trim());
      i++;
    }
    blocks.push(<p key={k++}>{para.map((p, j) => <Fragment key={j}>{j > 0 && <br />}{inline(p)}</Fragment>)}</p>);
  }
  return <div className="md">{blocks}</div>;
}
