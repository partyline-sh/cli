import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { Composer } from '../src/ui/Composer';
const state = vi.hoisted(() => ({ drafts: {}, keyboardVisible: false, dismissKeyboard: vi.fn(), sendText: vi.fn(), sendKey: vi.fn(), deliveries: [] as { id: string; computerId: string; pane: string; status: string; data?: unknown; prompt?: { text: string } }[], computers: [{ id: 'mac', connection: 'connected', capabilities: ['input_feedback'] }], sessions: [] as unknown[], retryEnter: vi.fn() }));
vi.mock('../src/runtime', () => ({ usePartyline: () => state, paneKey: (id: string, pane: string) => `${id}:${pane}` }));
vi.mock('../src/ui/useKeyboardVisible', () => ({ useKeyboardVisible: () => state.keyboardVisible }));
vi.mock('../src/ui/AttachmentPicker', () => ({ AttachmentPicker: 'AttachmentPicker' }));
vi.mock('../src/ui/Signal', () => ({ Signal: 'Signal' }));
vi.mock('../src/ui/ConnectionSignal', () => ({ ConnectionSignal: 'ConnectionSignal' }));
vi.mock('react-native', () => ({ Keyboard: { dismiss: state.dismissKeyboard }, Alert: {}, Pressable: 'Pressable', ScrollView: 'ScrollView', Text: 'Text', TextInput: 'TextInput', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { select: () => 'monospace' } }));
let tree: ReactTestRenderer;
beforeEach(() => { Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true }); state.retryEnter.mockReset().mockResolvedValue({}); state.keyboardVisible = false; state.dismissKeyboard.mockReset(); state.sendText.mockReset(); state.sendKey.mockReset(); });
afterEach(async () => { await act(async () => tree?.unmount()); });
it('keeps confirmed room delivery visible rather than hiding it', async () => {
  state.deliveries = [{ id: 'a', computerId: 'mac', pane: 'chat', status: 'confirmed' }];
  await act(async () => { tree = create(createElement(Composer, { computerId: 'mac', pane: 'chat', placeholder: 'Message' })); });
  expect(JSON.stringify(tree.toJSON())).toContain('Posted to room');
});

it('Hide keyboard only dismisses the keyboard and sends no input or cancel command', async () => {
  state.deliveries = [];
  state.keyboardVisible = true;
  await act(async () => { tree = create(createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: 'Message' })); });
  expect(JSON.stringify(tree.toJSON())).toContain('Hide keyboard');
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Hide keyboard' }).props.onPress());
  expect(state.dismissKeyboard).toHaveBeenCalledExactlyOnceWith();
  expect(state.sendText).not.toHaveBeenCalled();
  expect(state.sendKey).not.toHaveBeenCalled();
});

it('labels confirmed agent input as sent to the session', async () => {
  state.deliveries = [{ id: 'sent', computerId: 'mac', pane: '%1', status: 'confirmed' }];
  await act(async () => { tree = create(createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: 'Message' })); });
  expect(JSON.stringify(tree.toJSON())).toContain('Sent to session');
});
it('Retry Enter references the original input and is unavailable on another room', async () => {
  state.deliveries = [{ id: 'a', computerId: 'mac', pane: '%1', status: 'confirmed', data: { inputDelivery: 'in_composer', retryInputId: 'a' } }];
  await act(async () => { tree = create(createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: 'Message' })); });
  const label = tree.root.findByProps({ children: 'Retry Enter' });
  await act(async () => label.parent!.props.onPress());
  expect(state.retryEnter).toHaveBeenCalledWith('mac', '%1', 'a');
  await act(async () => tree.update(createElement(Composer, { computerId: 'other', pane: '%1', placeholder: 'Message' })));
  expect(JSON.stringify(tree.toJSON())).not.toContain('Retry Enter');
});
it('shows reply evidence from the shared agent adapter instead of terminal delivery alone', async () => {
  state.deliveries = [{ id: 'fast', computerId: 'mac', pane: '%1', status: 'confirmed', data: { inputDelivery: 'in_composer', retryInputId: 'fast' } }];
  await act(async () => { tree = create(createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: 'Message', deliveryLabel: delivery => delivery.id === 'fast' ? 'Answered' : undefined })); });
  expect(JSON.stringify(tree.toJSON())).toContain('Answered');
  expect(JSON.stringify(tree.toJSON())).not.toContain('Delivered to terminal');
  expect(JSON.stringify(tree.toJSON())).not.toContain('Retry Enter');
  expect(tree.root.findByType('Signal' as never).props.mode).toBe('confirmed');
});

it('shows the outgoing message while sending and keeps it available after failure', async () => {
  state.deliveries = [{ id: 'pending', computerId: 'mac', pane: '%1', status: 'waiting', prompt: { text: 'My next instruction' } }];
  await act(async () => { tree = create(createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: 'Message' })); });
  expect(JSON.stringify(tree.toJSON())).toContain('Sending…');
  expect(tree.root.findByProps({ accessibilityLabel: 'Outgoing message' }).props.children).toBe('My next instruction');
  state.deliveries = [{ ...state.deliveries[0], status: 'not-sent' }];
  await act(async () => tree.update(createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: 'Message' })));
  expect(JSON.stringify(tree.toJSON())).toContain('Not sent');
  expect(tree.root.findByProps({ accessibilityLabel: 'Outgoing message' }).props.children).toBe('My next instruction');
  state.deliveries = [{ ...state.deliveries[0], status: 'confirmed' }];
  await act(async () => tree.update(createElement(Composer, { computerId: 'mac', pane: '%1', placeholder: 'Message' })));
  expect(tree.root.findAllByProps({ accessibilityLabel: 'Outgoing message' })).toHaveLength(0);
});
