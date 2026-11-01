import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, expect, it, vi } from 'vitest';
import { AttachmentPicker } from '../src/ui/AttachmentPicker';
const fixture = vi.hoisted(() => ({ upload: vi.fn(), busy: vi.fn(), signal: undefined as AbortSignal | undefined }));
vi.mock('../src/runtime', () => ({ usePartyline: () => ({ computers: [{ id: 'mac', name: 'Mac', connection: 'connected', capabilities: ['uploads'] }], uploadAttachment: fixture.upload }) }));
vi.mock('expo-router', () => ({ useFocusEffect: () => {} }));
vi.mock('expo-document-picker', () => ({ getDocumentAsync: async () => ({ canceled: false, assets: [{ uri: 'file:///test.txt', name: 'test.txt', mimeType: 'text/plain', size: 4 }] }) }));
vi.mock('expo-image-picker', () => ({ launchImageLibraryAsync: vi.fn() }));
vi.mock('expo-file-system', () => ({ File: class { size = 4; bytes = async () => new Uint8Array([1, 2, 3, 4]); } }));
vi.mock('react-native', () => ({ Keyboard: { dismiss: vi.fn() }, Image: 'Image', Modal: 'Modal', Pressable: 'Pressable', Text: 'Text', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { OS: 'android', select: () => 'monospace' } }));
let tree: ReactTestRenderer;
const render = async () => act(async () => { tree = create(createElement(AttachmentPicker, { computerId: 'mac', pane: '%1', onBusy: fixture.busy, compact: true })); });
const choose = async () => {
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Attach a photo or file' }).props.onPress());
  await act(async () => tree.root.findByProps({ children: 'Choose a file' }).parent!.props.onPress());
};
afterEach(async () => { await act(async () => tree?.unmount()); vi.clearAllMocks(); });
it('keeps progress and cancellation available from the compact file key during an upload', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.upload.mockImplementation((_id, _pane, _file, _progress, signal: AbortSignal) => new Promise((_resolve, reject) => {
    fixture.signal = signal; signal.addEventListener('abort', () => reject(new Error('aborted')), { once: true });
  }));
  await render(); await choose();
  const key = tree.root.findByProps({ accessibilityLabel: 'Attachment upload progress and cancel' });
  expect(key.props.disabled).toBe(false);
  await act(async () => key.props.onPress());
  expect(tree.root.findByType('Modal' as never).props.visible).toBe(true);
  await act(async () => tree.root.findAllByProps({ children: 'Cancel' })[0].parent!.props.onPress());
  expect(fixture.signal?.aborted).toBe(true);
  expect(fixture.busy).toHaveBeenLastCalledWith(false);
});
it('describes successful attachments as a command draft without showing a chat Attach row', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.upload.mockResolvedValue({}); await render(); await choose();
  expect(JSON.stringify(tree.toJSON())).not.toContain('＋ Attach');
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Attach a photo or file' }).props.onPress());
  expect(JSON.stringify(tree.toJSON())).toContain('Attached to your command draft. Press Return when ready.');
});
