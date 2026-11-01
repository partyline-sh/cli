import { expect, it } from 'vitest';
import { agentDeliveryLabel, agentSessionAdapter, promptBaseline, simpleConversation } from '../src/runtime/agent-session';
import type { Delivery, PaneSnapshot, TranscriptEntry } from '../src/runtime/types';
const user = (text: string): TranscriptEntry => ({ id: `u:${text}`, role: 'user', text, tools: [] });
const answer = (text: string): TranscriptEntry => ({ id: `a:${text}`, role: 'assistant', text, tools: [] });
const snapshot = (entries: TranscriptEntry[], stale = false): PaneSnapshot => ({ computerId: 'mac', pane: '%1', stale, transcriptSupported: true, transcript: { entries, revision: 2, receivedAt: 100 } });
const sent = (previousMatches = 0): Delivery => ({ id: 'send', computerId: 'mac', pane: '%1', status: 'confirmed', prompt: { text: 'hello', previousMatches }, data: { inputDelivery: 'delivered' } });

it.each(['claude', 'codex'])('recognizes a fast %s reply without observing working', engine => {
  const adapter = agentSessionAdapter(engine);
  const pane = snapshot([user('hello'), answer('Hi')]);
  expect(agentDeliveryLabel(adapter, sent(), pane, false)).toBe('Answered');
  expect(simpleConversation(adapter, pane, [sent()]).entries).toHaveLength(2);
  expect(agentDeliveryLabel(adapter, sent(), pane, true)).toBe('Reply arriving…');
});
it('does not claim an answer from old text, an idle pane, stale data, tool-only output or another turn', () => {
  const adapter = agentSessionAdapter('claude');
  for (const pane of [snapshot([]), snapshot([user('hello'), answer('old')]), snapshot([user('hello'), user('other'), answer('other reply')]), snapshot([user('hello'), answer('')]), snapshot([user('hello'), answer('reply')], true)]) {
    const delivery = pane.transcript!.entries.some(e => e.text === 'old') ? sent(1) : sent();
    expect(agentDeliveryLabel(adapter, delivery, pane, false)).not.toBe('Answered');
  }
});
it('keeps repeated identical sends distinct before transcript echoes arrive', () => {
  const pane = snapshot([]);
  expect(promptBaseline(pane, 'hello', [sent()])).toBe(1);
  const prior = snapshot([user('hello'), answer('first')]);
  expect(agentDeliveryLabel(agentSessionAdapter('codex'), sent(1), prior, false)).not.toBe('Answered');
  expect(agentDeliveryLabel(agentSessionAdapter('codex'), sent(1), snapshot([...prior.transcript!.entries, user('hello'), answer('second')]), false)).toBe('Answered');
  expect(promptBaseline({ ...pane, stale: true }, 'hello')).toBeUndefined();
});
it('shows only confirmed own sends when native conversation is unavailable, never screen text', () => {
  const adapter = agentSessionAdapter('gemini');
  const pane: PaneSnapshot = { computerId: 'mac', pane: '%1', stale: false, transcriptSupported: false, terminal: { ansi: 'assistant? untrusted screen text', columns: 80, rows: 24, receivedAt: 1 } };
  const view = simpleConversation(adapter, pane, [sent(), { ...sent(), id: 'failed', status: 'refused' }]);
  expect(view.entries.map(e => [e.role, e.text])).toEqual([['user', 'hello']]);
  expect(view.notice).toContain('Replies for this engine appear in Session');
  expect(agentDeliveryLabel(adapter, sent(), pane, false)).toBe('Sent · replies in Session');
});
it('shows confirmed prompts while waiting for their transcript echo without duplicating them afterwards', () => {
  const adapter = agentSessionAdapter('claude');
  expect(simpleConversation(adapter, snapshot([]), [sent()]).entries.map(e => e.text)).toEqual(['hello']);
  expect(simpleConversation(adapter, snapshot([user('hello')]), [sent()]).entries.map(e => e.text)).toEqual(['hello']);
  expect(agentDeliveryLabel(adapter, { ...sent(), prompt: { text: 'hello' } }, snapshot([user('hello'), answer('old')]), false)).not.toBe('Answered');
});
