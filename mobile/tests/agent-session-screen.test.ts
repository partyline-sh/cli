import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { AgentSessionScreen } from '../src/ui/AgentSessionScreen';
import type { Delivery, PaneSnapshot, Session } from '../src/runtime/types';
const fixture = vi.hoisted(() => ({ app: {
  sessions: [] as Session[], computers: [{ id: 'mac', name: 'Mac', connection: 'connected' }], panes: {} as Record<string, PaneSnapshot>,
  deliveries: [] as Delivery[], sessionView: 'messages' as 'messages' | 'terminal', selectView: vi.fn(), refreshSession: vi.fn(),
} }));
vi.mock('../src/runtime', () => ({ usePartyline: () => fixture.app, paneKey: (a: string, b: string) => `${a}:${b}` }));
vi.mock('expo-router', () => ({ router: {} }));
vi.mock('../src/ui/motion', () => ({ MotionGate: ({ children }: { children: unknown }) => children }));
vi.mock('react-native-safe-area-context', () => ({ SafeAreaView: 'SafeAreaView' }));
vi.mock('../src/ui/Signal', () => ({ Signal: 'Signal' }));
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

it.each(['claude', 'codex', 'gemini', 'opencode', 'goose', 'antigravity', 'llm', 'prime-agent'])('opens %s directly in the wrapped terminal with one composer', async engine => {
  fixture.app.sessions = [{ id: 'one', computerId: 'mac', pane: '%1', engine, title: 'Project', state: 'idle' }];
  fixture.app.sessionView = 'messages';
  fixture.app.panes = { 'mac:%1': { computerId: 'mac', pane: '%1', stale: false, transcriptSupported: false,
    terminal: { ansi: 'latest agent reply', columns: 212, rows: 30, receivedAt: 1 } } };
  await render();
  expect(tree.root.findAllByProps({ accessibilityRole: 'tab' })).toHaveLength(0);
  expect(tree.root.findAllByType('Messages' as never)).toHaveLength(0);
  expect(tree.root.findByType('Terminal' as never).props).toMatchObject({ ansi: 'latest agent reply', defaultDisplay: 'wrap', showControls: false, columns: 212 });
  expect(tree.root.findAllByType('Composer' as never)).toHaveLength(1);
  expect(tree.root.findByType('Composer' as never).props).toMatchObject({ computerId: 'mac', pane: '%1', placeholder: `Message ${engine}…` });
  expect(fixture.app.selectView).not.toHaveBeenCalled();
  expect(JSON.stringify(tree.toJSON())).not.toMatch(/Conversation is not available|Exact grid|Resize shared session/);
});

it('shows new terminal output without waiting for a transcript and preserves text-size control', async () => {
  fixture.app.sessions = [{ id: 'one', computerId: 'mac', pane: '%1', engine: 'codex', title: 'Project', state: 'working', detected: true, agentTarget: 'agent:codex:10:10:100' }];
  fixture.app.panes = { 'mac:%1': { computerId: 'mac', pane: '%1', stale: false, transcriptSupported: false,
    terminal: { ansi: 'working', columns: 120, rows: 40, receivedAt: 1 } } };
  await render();
  fixture.app.panes['mac:%1'].terminal = { ansi: 'completed reply', columns: 120, rows: 40, receivedAt: 2 };
  await render();
  expect(tree.root.findByType('Terminal' as never).props.ansi).toBe('completed reply');
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Text size' }).props.onPress());
  expect(tree.root.findByType('Terminal' as never).props.size).toBe(2);
  expect(tree.root.findByType('Composer' as never).props.deliveryLabel).toBeUndefined();
});

it('shows snapshot refresh separately from agent activity, reports a delay and allows retry', async () => {
  fixture.app.sessions = [{ id: 'one', computerId: 'mac', pane: '%1', engine: 'codex', title: 'Project', state: 'idle' }];
  fixture.app.panes = { 'mac:%1': { computerId: 'mac', pane: '%1', stale: true, transcriptSupported: false,
    terminal: { ansi: 'previous snapshot', columns: 212, rows: 56, receivedAt: 1 } } };
  await render();
  expect(JSON.stringify(tree.toJSON())).toContain('Refreshing session…');
  expect(tree.root.findByType('Signal' as never).props.mode).toBe('active');
  expect(tree.root.findByProps({ accessibilityLabel: 'Refresh session output' }).props.disabled).toBe(true);
  fixture.app.panes['mac:%1'].refreshError = 'Session refresh is taking longer than expected.';
  await render();
  expect(JSON.stringify(tree.toJSON())).toContain('taking longer than expected');
  expect(JSON.stringify(tree.toJSON())).toContain('showing output from');
  const retry = tree.root.findByProps({ accessibilityLabel: 'Refresh session output' });
  expect(retry.props.disabled).toBe(false);
  await act(async () => retry.props.onPress());
  expect(fixture.app.refreshSession).toHaveBeenCalledWith('mac', '%1');
  fixture.app.panes['mac:%1'].stale = false;
  fixture.app.panes['mac:%1'].refreshError = undefined;
  await render();
  expect(JSON.stringify(tree.toJSON())).toContain('Updated ');
  expect(JSON.stringify(tree.toJSON())).not.toContain('Refreshing session…');
});
