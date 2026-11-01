import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { Composer } from '../src/ui/Composer';
const state = vi.hoisted(() => ({ drafts: {}, deliveries: [] as { id: string; computerId: string; pane: string; status: string; data?: unknown }[], computers: [{ id: 'mac', connection: 'connected', capabilities: ['input_feedback'] }], sessions: [] as unknown[], retryEnter: vi.fn() }));
vi.mock('../src/runtime', () => ({ usePartyline: () => state, paneKey: (id: string, pane: string) => `${id}:${pane}` }));
vi.mock('../src/ui/useKeyboardVisible', () => ({ useKeyboardVisible: () => false }));
vi.mock('../src/ui/AttachmentPicker', () => ({ AttachmentPicker: 'AttachmentPicker' }));
vi.mock('../src/ui/Signal', () => ({ Signal: 'Signal' }));
vi.mock('../src/ui/ConnectionSignal', () => ({ ConnectionSignal: 'ConnectionSignal' }));
vi.mock('react-native', () => ({ Keyboard: {}, Alert: {}, Pressable: 'Pressable', ScrollView: 'ScrollView', Text: 'Text', TextInput: 'TextInput', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { select: () => 'monospace' } }));
let tree: ReactTestRenderer;
beforeEach(() => { Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true }); state.retryEnter.mockReset().mockResolvedValue({}); });
afterEach(async () => { await act(async () => tree?.unmount()); });
it('keeps confirmed room delivery visible rather than hiding it', async () => {
  state.deliveries = [{ id: 'a', computerId: 'mac', pane: 'chat', status: 'confirmed' }];
  await act(async () => { tree = create(createElement(Composer, { computerId: 'mac', pane: 'chat', placeholder: 'Message' })); });
  expect(JSON.stringify(tree.toJSON())).toContain('Posted to room');
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
