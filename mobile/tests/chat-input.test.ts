import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, expect, it, vi } from 'vitest';
import { ChatInput } from '../src/ui/ChatInput';
import type { ChatRoomState } from '../src/runtime/types';
vi.mock('react-native', () => ({ Pressable: 'Pressable', ScrollView: 'ScrollView', Text: 'Text', TextInput: 'TextInput', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { select: () => 'monospace' } }));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let tree: ReactTestRenderer;
afterEach(async () => { await act(async () => tree?.unmount()); });
const room: ChatRoomState = { stopped: false, members: [{ key: 'codex', name: '#13 codex', engine: 'codex', answering: false, sent: false, paused: false }] };
it('inserts a tapped suggestion without invoking Send and dismissal preserves the draft', async () => {
  const change = vi.fn();
  await act(async () => { tree = create(createElement(ChatInput, { value: 'hi @co', onChangeText: change, placeholder: 'Message', style: {}, room })); });
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Insert @13, #13 codex' }).props.onPress());
  expect(change).toHaveBeenCalledExactlyOnceWith('hi @13 ');
  await act(async () => { tree.update(createElement(ChatInput, { value: 'hi @co', onChangeText: change, placeholder: 'Message', style: {}, room })); });
  // Restore the caret as the native selection event would after editing.
  await act(async () => tree.root.findByType('TextInput' as never).props.onSelectionChange({ nativeEvent: { selection: { start: 6, end: 6 } } }));
  change.mockClear();
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Dismiss mention suggestions' }).props.onPress());
  expect(change).not.toHaveBeenCalled(); expect(JSON.stringify(tree.toJSON())).not.toContain('Insert @13');
});
it('removes suggestions as soon as room metadata becomes unavailable', async () => {
  const props = { value: '@co', onChangeText: vi.fn(), placeholder: 'Message', style: {}, room };
  await act(async () => { tree = create(createElement(ChatInput, props)); });
  expect(JSON.stringify(tree.toJSON())).toContain('Insert @13');
  await act(async () => tree.update(createElement(ChatInput, { ...props, room: undefined })));
  expect(JSON.stringify(tree.toJSON())).not.toContain('Insert @13');
});
