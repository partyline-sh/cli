import type { PartylineState } from './types';
import { paneKey } from './types';

export function fixtureState(): PartylineState {
  const now = Date.now();
  return {
    mode: 'fixture', loading: false, tab: 'sessions', sessionView: 'messages', pairing: { status: 'idle' },
    computers: [
      { id: 'fixture-mbp', name: 'MacBook Pro', relay: 'https://studio-gateway.example.test', relayHosting: 'self-hosted', connection: 'connected', capabilities: ['input', 'chat_send', 'transcript'], lastReceivedAt: now, pairedAt: now - 86400000 },
      { id: 'fixture-mini', name: 'mac mini', relay: 'https://studio-gateway.example.test', relayHosting: 'self-hosted', connection: 'offline', capabilities: [], lastReceivedAt: now - 120000, pairedAt: now - 86400000 },
    ],
    sessions: [
      { id: 'fixture-pos', computerId: 'fixture-mbp', pane: '%1', title: 'ACR POS', engine: 'claude', state: 'waiting', project: 'acr-pos', lastLine: 'Waiting for your response', updatedAt: now },
      { id: 'fixture-fleet', computerId: 'fixture-mbp', pane: '%2', title: 'FLEET MANAGER', engine: 'codex', state: 'working', lastLine: 'Running go test ./...', updatedAt: now },
      { id: 'fixture-nav', computerId: 'fixture-mini', pane: '%1', title: 'Navigation refresh', engine: 'gemini', state: 'idle', lastLine: 'Waiting for a prompt', updatedAt: now - 120000 },
    ],
    panes: {
      [paneKey('fixture-mbp', '%1')]: {
        computerId: 'fixture-mbp', pane: '%1', transcriptSupported: true, stale: false,
        terminal: { ansi: '\u001b[38;5;209mclaude\u001b[0m\r\n\r\nRegression check passed. The receipt now shows the payment method and a confirmation for an already completed payment, so a retry does not create another charge.\r\n\r\n› Waiting for your response', columns: 80, rows: 24, receivedAt: now },
        transcript: { revision: 1, receivedAt: now, entries: [
          { id: 'fixture-1', role: 'user', text: 'Check the receipt printing change.', tools: [] },
          { id: 'fixture-2', role: 'assistant', text: 'The regression check passed. The receipt now includes the payment method.', tools: [{ name: 'go test', target: './pos/receipts', status: 'ok' }] },
        ] },
      },
    },
    loops: [{ id: 'fixture-pay', computerId: 'fixture-mbp', section: 'Needs you', title: 'Saved payment methods', state: 'dispute', repo: 'payments', detail: 'Final-card deletion requires a decision.', batch: 'step 2 of 3', round: 2, openP1: 1, dispute: 'Should deleting the final saved card require confirmation?', builder: 'codex', chips: [{ name: 'checks', mark: 'done' }, { name: 'claude', mark: 'reading' }], receivedAt: now }],
    chat: [{ id: 'fixture-chat-1', computerId: 'fixture-mbp', name: 'claude', text: 'The receipt change is ready for review.', at: new Date(now).toISOString(), own: false }],
    deliveries: [], drafts: {},
  };
}
