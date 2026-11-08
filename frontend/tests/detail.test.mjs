import assert from 'node:assert/strict';
import { after, test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createServer } from 'vite';

const server = await createServer({ server: { middlewareMode: true }, appType: 'custom' });
after(() => server.close());
const { AIDiagnosisCard, XPulseCard } = await server.ssrLoadModule('/src/AI.tsx');
const { JudgementPoints } = await server.ssrLoadModule('/src/CompanyDetail.tsx');
const { ProMetrics, AttentionCard } = await server.ssrLoadModule('/src/Metrics.tsx');
const ai = (items) => ({ items, pending: [], error: '', has: () => false });
const diagnosis = {
  id: 1, trigger: 'manual', engine: 'codex', kind: 'diagnosis', at: '2026-11-07T00:00:00Z', durationMs: 0,
  result: { stance: 'trap', summary: '最新模型意见', confidence: 3, watch: [], falsify: [], dataIssues: [] },
};
const rollup = {
  ...diagnosis, id: 2, kind: 'rollup', at: '2026-11-06T00:00:00Z',
  result: { stance: 'mispriced', summary: '旧汇总结论', open: [], checklist: null },
};
const renderAI = (items) => renderToStaticMarkup(createElement(AIDiagnosisCard, { ticker: 'TEST', ai: ai(items) }));

test('new diagnosis supersedes an older rollup in the visible summary', () => {
  const html = renderAI([diagnosis, rollup]);
  const brief = html.split('<details class="ai-details">')[0];
  assert.match(brief, /最新模型意见/);
  assert.doesNotMatch(brief, /旧汇总结论/);
  assert.match(html, /各模型详细分析与诊断记录/);
  assert.doesNotMatch(html, /<details[^>]*\bopen/);
});

test('current rollup is used, and multiple engines may disagree', () => {
  const html = renderAI([{ ...rollup, at: '2026-11-08T00:00:00Z' }, diagnosis,
    { ...diagnosis, id: 3, engine: 'claude', result: { ...diagnosis.result, stance: 'fair' } }]);
  const brief = html.split('<details class="ai-details">')[0];
  assert.match(brief, /旧汇总结论/);
  assert.match(brief, /存在分歧/);
});

test('no AI or professional data remains renderable', () => {
  assert.match(renderAI([]), /还没有诊断/);
  assert.equal(renderToStaticMarkup(createElement(ProMetrics, {})), '');
  assert.match(renderToStaticMarkup(createElement(AttentionCard, { profile: null, error: 'offline' })), /offline/);
});

test('rumors stay inside the closed social evidence disclosure', () => {
  const html = renderToStaticMarkup(createElement(XPulseCard, { ticker: 'TEST', ai: ai([{
    ...diagnosis, kind: 'xpulse', result: { summary: '市场观点', sentiment: 'neutral', sentimentScore: 0,
      rumors: ['未证实的收购消息'], posts: [] },
  }]) }));
  assert.doesNotMatch(html.split('<details class="pulse-details">')[0], /未证实的收购消息/);
  assert.match(html, /未证实的收购消息/);
});


test('TTM margin conflict is factual, missing revenue is not treated as zero', () => {
  const e = { stage: 3, quality: { opMargin: 0.213 }, hints: [], notes: [],
    ttm: [{ end: '2026-08-02', opIncome: 178, revenue: 1000 }] };
  const render = (evaluation) => renderToStaticMarkup(createElement(JudgementPoints, { e: evaluation }));
  assert.match(render(e), /信号冲突/);
  assert.match(render(e), /17.8%/);
  assert.match(render(e), /21.3%/);
  assert.doesNotMatch(render({ ...e, ttm: [{ opIncome: 178 }] }), /信号冲突/);
  assert.doesNotMatch(render({ ...e, ttm: [] }), /信号冲突/);
  assert.doesNotMatch(render({ ...e, stage: 0 }), /信号冲突/);
});
