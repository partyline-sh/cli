import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { SessionScreen } from '../src/ui/SessionScreen';

const app = vi.hoisted(() => ({ loading: false, openLiveTerminal: undefined as undefined | (() => void), sessions: [] as { computerId: string; pane: string; title: string; engine?: string; state: string }[], computers: [{ id: 'mac', capabilities: [] as string[], connection: 'connected', name: 'Mac' }],
  panes: { 'mac:%1': { stale: false, transcriptSupported: false, terminal: { ansi: 'name     size\nnotes/   256B', columns: 80 } } }, sessionView: 'terminal' }));
vi.mock('../src/ui/LiveTerminal', () => ({ LiveTerminal: 'LiveTerminal' }));
vi.mock('../src/runtime', () => ({ usePartyline: () => app, paneKey: (a: string, b: string) => `${a}:${b}` }));
vi.mock('expo-router', () => ({ router: {}, useFocusEffect: () => {} }));
vi.mock('../src/ui/motion', () => ({ MotionGate: ({ children }: { children: unknown }) => children }));
vi.mock('react-native-safe-area-context', () => ({ SafeAreaView: 'SafeAreaView' }));
vi.mock('../src/ui/Composer', () => ({ Composer: 'Composer' }));
vi.mock('../src/ui/Messages', () => ({ Messages: 'Messages' }));
vi.mock('../src/ui/Terminal', () => ({ Terminal: 'Terminal', terminalSizes: 4 }));
vi.mock('../src/ui/TerminalLayoutControls', () => ({ TerminalLayoutControls: 'LayoutControls' }));
vi.mock('../src/ui/parts', () => ({ Empty: 'Empty' }));
vi.mock('react-native', () => ({ KeyboardAvoidingView: 'KeyboardAvoidingView', Pressable: 'Pressable', Text: 'Text', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { OS: 'ios', select: () => 'monospace' } }));
let tree: ReactTestRenderer;
let errors: ReturnType<typeof vi.spyOn>;
const render = async () => act(async () => {
  const e = createElement(SessionScreen, { computer: 'mac', pane: '1' });
  if (tree) tree.update(e); else tree = create(e);
});
beforeEach(() => { Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true }); app.sessions = []; app.computers[0].capabilities = []; app.openLiveTerminal = undefined; errors = vi.spyOn(console, 'error'); });
afterEach(async () => { await act(async () => tree?.unmount()); tree = undefined as never; errors.mockRestore(); });
it('uses compact shell controls for a known ordinary terminal and reveals display options on demand', async () => {
  app.sessions = [{ computerId: 'mac', pane: '%1', title: 'Terminal', engine: '', state: 'idle' }]; await render();
  expect(tree.root.findByType('Composer' as never).props).toMatchObject({ shell: true, placeholder: 'Command…' });
  expect(tree.root.findByType('Terminal' as never).props.showControls).toBe(false);
  expect(tree.root.findByType('LayoutControls' as never).props.collapsed).toBe(true);
  expect(errors.mock.calls.flat().join(' ')).not.toContain('Encountered two children with the same key');
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Terminal display options' }).props.onPress());
  expect(tree.root.findByType('Terminal' as never).props.showControls).toBe(true);
  expect(tree.root.findByType('LayoutControls' as never).props.collapsed).toBe(false);
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Terminal display options' }).props.onPress());
  expect(tree.root.findByType('LayoutControls' as never).props.collapsed).toBe(true);
});
it('keeps agent input unchanged, including before session metadata arrives', async () => {
  await render(); expect(tree.root.findByType('Composer' as never).props.shell).toBe(false);
  app.sessions = [{ computerId: 'mac', pane: '%1', title: 'Code review', engine: 'codex', state: 'idle' }]; await render();
  expect(tree.root.findByType('Composer' as never).props).toMatchObject({ shell: false, placeholder: 'Message codex…' });
  expect(tree.root.findByType('Terminal' as never).props.showControls).toBe(true);
  expect(tree.root.findAllByProps({ accessibilityLabel: 'Terminal display options' })).toHaveLength(0);
});

it('uses direct typing without the composer only for a negotiated native shell', async () => {
  app.sessions = [{ computerId:'mac',pane:'%1',title:'Terminal',engine:'',state:'idle' }];
  app.computers[0].capabilities=['live_terminal']; app.openLiveTerminal=()=>{};
  await render(); expect(tree.root.findAllByType('Composer' as never)).toHaveLength(0);
  expect(tree.root.findByType('LiveTerminal' as never).props).toMatchObject({computer:'mac',pane:'%1'});
  app.sessions[0].engine='codex'; await render();
  expect(tree.root.findAllByType('LiveTerminal' as never)).toHaveLength(0);
  expect(tree.root.findByType('Composer' as never).props.shell).toBe(false);
});
