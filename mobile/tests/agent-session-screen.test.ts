import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { AgentSessionScreen } from '../src/ui/AgentSessionScreen';
import type { Delivery, PaneSnapshot, Session } from '../src/runtime/types';
const fixture = vi.hoisted(() => ({ app: {
  sessions: [] as Session[], computers: [{ id: 'mac', name: 'Mac', connection: 'connected' }], panes: {} as Record<string, PaneSnapshot>,
  deliveries: [] as Delivery[], sessionView: 'messages' as 'messages' | 'terminal', selectView: vi.fn(),
} }));
vi.mock('../src/runtime', () => ({ usePartyline: () => fixture.app, paneKey: (a: string, b: string) => `${a}:${b}` }));
vi.mock('expo-router', () => ({ router: {} }));
vi.mock('../src/ui/motion', () => ({ MotionGate: ({ children }: { children: unknown }) => children }));
vi.mock('react-native-safe-area-context', () => ({ SafeAreaView: 'SafeAreaView' }));
vi.mock('../src/ui/Composer', () => ({ Composer: 'Composer' }));
vi.mock('../src/ui/Messages', () => ({ Messages: 'Messages' }));
vi.mock('../src/ui/Terminal', () => ({ Terminal: 'Terminal', terminalSizes: 4 }));
vi.mock('../src/ui/parts', () => ({ Empty: 'Empty' }));
vi.mock('react-native', () => ({ KeyboardAvoidingView: 'KeyboardAvoidingView', Pressable: 'Pressable', Text: 'Text', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { OS: 'ios', select: () => 'monospace' } }));
let tree: ReactTestRenderer;
const render = async () => act(async () => {
  const element = createElement(AgentSessionScreen, { computer: 'mac', pane: '%1' });
  if (tree) tree.update(element); else tree = create(element);
});
beforeEach(() => { Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true }); fixture.app.selectView.mockReset(); fixture.app.sessionView = 'messages'; fixture.app.deliveries = []; });
afterEach(async () => { await act(async () => tree?.unmount()); tree = undefined as never; });

it.each(['claude', 'codex', 'gemini'])('uses the same selector, composer and fixed wrapped Session for %s', async engine => {
  fixture.app.sessions = [{ id: 'one', computerId: 'mac', pane: '%1', engine, title: 'Project', state: 'idle' }];
  fixture.app.panes = { 'mac:%1': { computerId: 'mac', pane: '%1', stale: false, transcriptSupported: engine !== 'gemini',
    transcript: engine !== 'gemini' ? { entries: [], revision: 1, receivedAt: 1 } : undefined,
    terminal: { ansi: 'screen output', columns: 212, rows: 30, receivedAt: 1 } } };
  await render();
  expect(tree.root.findAllByProps({ accessibilityRole: 'tab' }).map(t => t.props.accessibilityLabel)).toEqual(['Simple', 'Session']);
  expect(tree.root.findByType('Composer' as never).props).toMatchObject({ computerId: 'mac', pane: '%1', placeholder: `Message ${engine}…` });
  if (engine === 'gemini') expect(JSON.stringify(tree.toJSON())).toContain('Replies for this engine appear in Session');
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Session' }).props.onPress());
  expect(fixture.app.selectView).toHaveBeenCalledExactlyOnceWith('terminal');
  fixture.app.sessionView = 'terminal'; await render();
  expect(tree.root.findByType('Terminal' as never).props).toMatchObject({ defaultDisplay: 'wrap', showControls: false, columns: 212 });
  expect(tree.root.findAllByType('Composer' as never)).toHaveLength(1);
  expect(JSON.stringify(tree.toJSON())).not.toMatch(/Exact grid|Fit width|Resize shared session|Computer size/);
});

it('renders a fast native reply in Simple and labels that same send Answered', async () => {
  fixture.app.sessions = [{ id: 'one', computerId: 'mac', pane: '%1', engine: 'claude', title: 'Project', state: 'idle' }];
  const delivery: Delivery = { id: 'd', computerId: 'mac', pane: '%1', status: 'confirmed', prompt: { text: 'test', previousMatches: 0 }, data: { inputDelivery: 'delivered' } };
  fixture.app.deliveries = [delivery];
  fixture.app.panes = { 'mac:%1': { computerId: 'mac', pane: '%1', stale: false, transcriptSupported: true, transcript: { entries: [], revision: 1, receivedAt: 1 } } };
  await render();
  expect(tree.root.findByType('Messages' as never).props.entries.map((e: { text: string }) => e.text)).toEqual(['test']);
  fixture.app.panes['mac:%1'].transcript = { entries: [{ id: 'u', role: 'user', text: 'test', tools: [] }, { id: 'a', role: 'assistant', text: 'Reply before one second', tools: [] }], revision: 2, receivedAt: 2 };
  await render();
  expect(tree.root.findByType('Messages' as never).props.entries.map((e: { text: string }) => e.text)).toEqual(['test', 'Reply before one second']);
  expect(tree.root.findByType('Composer' as never).props.deliveryLabel(delivery)).toBe('Answered');
});

it.each(['claude', 'codex'])('gives adopted %s the shared composer and hides another process receipts', async engine => {
  const target = `agent:${engine}:10:10:100`;
  fixture.app.sessions = [{ id: 'one', computerId: 'mac', pane: '%1', engine, title: 'Shell-launched agent', state: 'idle', detected: true, agentTarget: target }];
  fixture.app.panes = { 'mac:%1': { computerId: 'mac', pane: '%1', stale: false, transcriptSupported: false } };
  fixture.app.deliveries = [
    { id: 'old', computerId: 'mac', pane: '%1', target: 'agent:claude:9:9:99', status: 'confirmed', prompt: { text: 'Another process message', previousMatches: 0 } },
    { id: 'new', computerId: 'mac', pane: '%1', target, status: 'confirmed', prompt: { text: 'Current process message', previousMatches: 0 } },
  ];
  await render();
  expect(tree.root.findAllByProps({ accessibilityRole: 'tab' }).map(t => t.props.accessibilityLabel)).toEqual(['Simple', 'Session']);
  expect(tree.root.findByType('Messages' as never).props.entries.map((entry: { text: string }) => entry.text)).toEqual(['Current process message']);
  expect(tree.root.findByType('Composer' as never).props.placeholder).toBe(`Message ${engine}…`);
});
