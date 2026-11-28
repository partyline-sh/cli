import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { Composer } from '../src/ui/Composer';

const app = vi.hoisted(() => ({ platform: 'ios', drafts: {} as Record<string, string>, deliveries: [] as { id: string; computerId: string; pane: string; status: string; data?: unknown }[], computers: [{ id: 'mac', connection: 'connected', capabilities: ['input_feedback'] }], sessions: [],
  sendText: vi.fn(), sendKey: vi.fn(), sendChat: vi.fn(), paste: vi.fn(), setDraft: vi.fn(), retryDelivery: vi.fn(), retryEnter: vi.fn(), acknowledgeDelivery: vi.fn(), alert: vi.fn() }));
vi.mock('../src/runtime', () => ({ usePartyline: () => app, paneKey: (id: string, pane: string) => `${id}:${pane}` }));
vi.mock('../src/ui/useKeyboardVisible', () => ({ useKeyboardVisible: () => false }));
vi.mock('../src/ui/AttachmentPicker', () => ({ AttachmentPicker: 'AttachmentPicker' }));
vi.mock('../src/ui/Signal', () => ({ Signal: 'Signal' }));
vi.mock('../src/ui/ConnectionSignal', () => ({ ConnectionSignal: 'ConnectionSignal' }));
vi.mock('react-native', () => ({ Keyboard: {}, Alert: { alert: app.alert }, Pressable: 'Pressable', ScrollView: 'ScrollView', Text: 'Text', TextInput: 'TextInput', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { get OS() { return app.platform; }, select: () => 'monospace' } }));
let tree: ReactTestRenderer;
const render = async (shell = true) => act(async () => {
  const element = createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: shell ? 'Command…' : 'Message…', shell });
  if (tree) tree.update(element); else tree = create(element);
});
const input = () => tree.root.findByProps({ accessibilityLabel: 'Shell command, edited on this phone' });
const press = async (label: string) => act(async () => tree.root.findByProps({ accessibilityLabel: label }).props.onPress());
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.clearAllMocks(); app.drafts = {}; app.deliveries = []; app.platform = 'ios';
  for (const fn of [app.sendText, app.sendKey, app.paste, app.retryDelivery, app.retryEnter]) fn.mockResolvedValue({});
});
afterEach(async () => { await act(async () => tree?.unmount()); tree = undefined as never; });

it('buffers edits locally, submits the native current text once with Return, and keeps writing assistance off', async () => {
  await render();
  expect(input().props).toMatchObject({ autoCorrect: false, autoCapitalize: 'none', spellCheck: false, smartInsertDelete: false, autoComplete: 'off', submitBehavior: 'submit', returnKeyType: 'go', keyboardType: 'default' });
  await act(async () => input().props.onChangeText('echo "日本語"'));
  expect(app.setDraft).toHaveBeenCalledWith('mac', '%1', 'echo "日本語"');
  expect(app.sendText).not.toHaveBeenCalled();
  await act(async () => input().props.onSubmitEditing({ nativeEvent: { text: 'echo "日本語"' } }));
  expect(app.sendText).toHaveBeenCalledExactlyOnceWith('mac', '%1', 'echo "日本語"');
  expect(app.sendKey).not.toHaveBeenCalled();
  expect(tree.root.findAllByProps({ accessibilityLabel: 'Send' })).toHaveLength(0);
});
it('inserts without Enter, then sends Tab to the remote shell; empty Return submits the remote line', async () => {
  app.drafts = { 'mac:%1': 'git che' }; await render();
  await press('Insert command without pressing Return');
  expect(app.paste).toHaveBeenCalledExactlyOnceWith('mac', '%1', 'git che');
  expect(app.sendText).not.toHaveBeenCalled(); expect(app.sendKey).not.toHaveBeenCalled();
  app.drafts = {}; await render();
  await press('Send tab');
  expect(app.sendKey).toHaveBeenLastCalledWith('mac', '%1', 'Tab');
  await act(async () => input().props.onSubmitEditing({ nativeEvent: { text: '' } }));
  expect(app.sendKey).toHaveBeenLastCalledWith('mac', '%1', 'Enter');
});
it('requires a deliberate confirmation before running pasted multiple lines', async () => {
  await render();
  const text = 'printf one\nprintf two';
  await act(async () => input().props.onSubmitEditing({ nativeEvent: { text } }));
  expect(app.sendText).not.toHaveBeenCalled();
  const buttons = app.alert.mock.calls[0][2];
  expect(buttons[0].text).toBe('Cancel');
  await act(async () => buttons[1].onPress());
  expect(app.sendText).toHaveBeenCalledExactlyOnceWith('mac', '%1', text);
});
it('keeps pending Return from duplicating a command and leaves editing available', async () => {
  app.deliveries = [{ id: 'a', computerId: 'mac', pane: '%1', status: 'waiting' }]; await render();
  await act(async () => input().props.onSubmitEditing({ nativeEvent: { text: 'ls' } }));
  expect(app.sendText).not.toHaveBeenCalled(); expect(input().props.editable).not.toBe(false);
  expect(JSON.stringify(tree.toJSON())).toContain('Return was blocked by pending input confirmation');
});
it('explains an upload-blocked Return, then clears the explanation on a successful attempt', async () => {
  await render();
  await act(async () => tree.root.findByType('AttachmentPicker' as never).props.onBusy(true));
  await act(async () => input().props.onSubmitEditing({ nativeEvent: { text: 'pwd' } }));
  expect(app.sendText).not.toHaveBeenCalled();
  expect(JSON.stringify(tree.toJSON())).toContain('Return was blocked by the upload');
  await act(async () => tree.root.findByType('AttachmentPicker' as never).props.onBusy(false));
  await act(async () => input().props.onSubmitEditing({ nativeEvent: { text: 'pwd' } }));
  expect(app.sendText).toHaveBeenCalledExactlyOnceWith('mac', '%1', 'pwd');
  expect(JSON.stringify(tree.toJSON())).not.toContain('Return was blocked');
});
it('compacts ordinary confirmation but retains unknown-delivery retry confirmation', async () => {
  app.deliveries = [{ id: 'a', computerId: 'mac', pane: '%1', status: 'confirmed' }]; await render();
  expect(tree.root.findByProps({ accessibilityLabel: 'Delivered to terminal' }).props.children).toBe('✓');
  app.drafts = { 'mac:%1': 'a new command' }; await render();
  expect(tree.root.findAllByProps({ accessibilityLabel: 'Delivered to terminal' })).toHaveLength(0);
  app.deliveries = [{ ...app.deliveries[0], status: 'unknown' }]; await render();
  const retry = tree.root.findByProps({ children: 'Retry' }).parent!;
  await act(async () => retry.props.onPress());
  expect(app.retryDelivery).not.toHaveBeenCalled();
  await act(async () => app.alert.mock.calls[0][2][1].onPress());
  expect(app.retryDelivery).toHaveBeenCalledWith('a', true);
});
it('preserves the agent message field and its explicit Send action', async () => {
  app.drafts = { 'mac:%1': 'Review this' }; await render(false);
  expect(tree.root.findAllByProps({ accessibilityLabel: 'Shell command, edited on this phone' })).toHaveLength(0);
  await press('Send'); expect(app.sendText).toHaveBeenCalledExactlyOnceWith('mac', '%1', 'Review this');
});
it('submits Web Return without inserting a newline and leaves IME composition alone', async () => {
  app.platform = 'web'; await render();
  const preventDefault = vi.fn();
  await act(async () => input().props.onKeyPress({ nativeEvent: { key: 'Enter', isComposing: true }, preventDefault }));
  expect(preventDefault).not.toHaveBeenCalled(); expect(app.sendText).not.toHaveBeenCalled();
  await act(async () => input().props.onKeyPress({ nativeEvent: { key: 'Enter' }, currentTarget: { value: 'pwd' }, preventDefault }));
  expect(preventDefault).toHaveBeenCalledOnce(); expect(app.sendText).toHaveBeenCalledExactlyOnceWith('mac', '%1', 'pwd');
});
