import { fixtureState } from '../runtime/fixtures';
import { paneKey, type PartylineState } from '../runtime/types';

/** Fictional workspaces for native screenshots. No secrets, sockets or personal project names. */
export function storeState(): PartylineState {
  const state = fixtureState();
  const now = Date.now();
  state.computers = [
    { id: 'fixture-mbp', name: 'MacBook Pro', relay: 'https://relay.example.com', connection: 'connected', capabilities: ['input', 'chat_send', 'transcript'], pairedAt: now - 86400000, lastReceivedAt: now },
    { id: 'fixture-studio', name: 'Studio Mac', relay: 'https://relay.example.com', connection: 'connected', capabilities: ['input', 'chat_send', 'transcript'], pairedAt: now - 86400000, lastReceivedAt: now },
  ];
  state.sessions = [
    { id: 'demo-checkout', computerId: 'fixture-mbp', pane: '%21', title: 'Checkout flow', engine: 'claude', state: 'waiting', lastLine: 'Ready for your review.', updatedAt: now },
    { id: 'demo-design', computerId: 'fixture-studio', pane: '%4', title: 'Design system', engine: 'gemini', state: 'waiting', lastLine: 'Which direction should we take?', updatedAt: now - 40000 },
    { id: 'demo-api', computerId: 'fixture-mbp', pane: '%2', title: 'API authentication', engine: 'codex', state: 'working', lastLine: 'Checking token refresh and expiry.', updatedAt: now },
    { id: 'demo-search', computerId: 'fixture-mbp', pane: '%3', title: 'Search improvements', engine: 'claude', state: 'working', lastLine: 'Running the regression checks.', updatedAt: now },
    { id: 'demo-release', computerId: 'fixture-studio', pane: '%5', title: 'Release checks', engine: 'codex', state: 'working', lastLine: 'Verifying the production build.', updatedAt: now },
    { id: 'demo-docs', computerId: 'fixture-studio', pane: '%6', title: 'Documentation', engine: 'claude', state: 'idle', lastLine: 'Setup guide updated.', updatedAt: now - 120000 },
  ];
  state.panes = {
    [paneKey('fixture-mbp', '%21')]: {
      computerId: 'fixture-mbp', pane: '%21', stale: false, transcriptSupported: true,
      terminal: { receivedAt: now, columns: 80, rows: 28, ansi: [
        '\u001b[38;2;255;154;60mclaude\u001b[0m  ·  checkout-flow', '',
        '› Check the payment retry flow.', '',
        'I found the issue: a failed request',
        'could submit the payment twice.', '',
        '\u001b[38;2;74;222;128m✓\u001b[0m Added an idempotency key',
        '\u001b[38;2;74;222;128m✓\u001b[0m Kept the retry button available',
        '\u001b[38;2;74;222;128m✓\u001b[0m Added a regression check', '',
        '\u001b[38;2;168;162;158m$ npm test -- checkout\u001b[0m', '',
        '  \u001b[32m✓\u001b[0m failed request can be retried',
        '  \u001b[32m✓\u001b[0m duplicate payment is refused',
        '  \u001b[32m✓\u001b[0m successful payment is confirmed', '',
        '\u001b[32m3 checks passed\u001b[0m', '',
        'The change is ready for your review.', '', '› ',
      ].join('\r\n') },
      transcript: { revision: 1, receivedAt: now, entries: [
        { id: 'demo-message-1', role: 'user', text: 'Check the payment retry flow. A failed request should be safe to try again.', tools: [] },
        { id: 'demo-message-2', role: 'assistant', text: 'I found a path that could submit the same payment twice. I’m adding an idempotency key and a regression check.', tools: [{ name: 'Read', target: 'checkout/payment.ts', status: 'ok' }, { name: 'Edit', target: 'checkout/payment.ts', status: 'ok' }] },
        { id: 'demo-message-3', role: 'user', text: 'Keep the retry button available. Show a clear message if the payment already went through.', tools: [] },
        { id: 'demo-message-4', role: 'assistant', text: 'Done. Failed requests can be retried, duplicate payments are refused, and successful payments show a confirmation. All three regression checks pass.\n\nThe change is ready for your review.', tools: [{ name: 'npm test', target: 'checkout', status: 'ok' }] },
      ] },
    },
  };
  state.loops = [
    { id: '42', computerId: 'fixture-mbp', section: 'Needs you', title: 'Review the checkout retry flow', state: 'Awaiting your decision', repo: 'storefront', round: 3, detail: 'The checks pass. Choose how to handle a payment that has already completed.' },
    { id: '43', computerId: 'fixture-studio', section: 'Needs you', title: 'Choose the navigation direction', state: 'Review requested', repo: 'design-system', round: 2, detail: 'Two approaches are ready. The agents need your decision before the next round.' },
    { id: '44', computerId: 'fixture-mbp', section: 'In progress', title: 'Harden token refresh', state: 'Running checks', repo: 'api', round: 2 },
    { id: '45', computerId: 'fixture-studio', section: 'In review', title: 'Improve keyboard navigation', state: 'Reviewing', repo: 'dashboard', round: 3 },
    { id: '46', computerId: 'fixture-studio', section: 'Planned', title: 'Update the setup guide', state: 'Planned', repo: 'docs' },
  ];
  const at = (minute: number) => { const date = new Date(); date.setHours(9, minute, 0, 0); return date.toISOString(); };
  state.chat = [
    { id: 'demo-chat-1', computerId: 'fixture-mbp', name: '#12 claude', own: false, at: at(36), text: 'The checkout retry change is ready. The regression checks pass.' },
    { id: 'demo-chat-2', computerId: 'fixture-mbp', name: '#13 codex', own: false, at: at(37), text: 'I checked the API side. The idempotency key is preserved across retries.' },
    { id: 'demo-chat-3', computerId: 'fixture-mbp', name: 'you', own: true, at: at(38), text: 'Keep the retry button. Make the confirmation clear if the payment already went through.' },
    { id: 'demo-chat-4', computerId: 'fixture-mbp', name: '#12 claude', own: false, at: at(39), text: 'Updated. A completed payment shows its confirmation instead of creating another charge.' },
    { id: 'demo-chat-5', computerId: 'fixture-mbp', name: '#13 codex', own: false, at: at(40), text: 'Review passed. The change is waiting for your final check.' },
  ];
  return state;
}
