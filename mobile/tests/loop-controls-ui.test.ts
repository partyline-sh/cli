import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { LoopConfirmation, LoopDetail } from '../src/runtime/types';
import { FullLoopDetails } from '../src/ui/FullLoopDetails';

const fixture = vi.hoisted(() => ({
  computers: [{ id: 'mac', connection: 'connected', capabilities: ['loop_controls'] }],
  deliveries: [], readLoop: vi.fn(), prepareLoopAction: vi.fn(), confirmLoopAction: vi.fn(),
  retryDelivery: vi.fn(), acknowledgeDelivery: vi.fn(),
}));
vi.mock('../src/runtime', () => ({ usePartyline: () => fixture }));
vi.mock('react-native', () => ({
  Pressable: 'Pressable', TextInput: 'TextInput', Text: 'Text', View: 'View',
  StyleSheet: { create: (s: unknown) => s }, Platform: { select: () => 'monospace' },
  Alert: { alert: vi.fn() }, Linking: { openURL: vi.fn() },
}));
const detail: LoopDetail = {
  id: 'abcdef', title: 'Import fixes', state: 'awaiting_approval', revision: 3,
  brief: 'The complete acceptance criteria and verification plan.',
  history: 'Round 2: actual output and every finding.', dispute: {},
  actions: ['approve', 'reject', 'stop', 'delete'], deleteTarget: 'batch ffffff (3 steps)', pr: '',
};
let rendered: ReactTestRenderer | undefined;
const render = async () => act(async () => {
  const element = createElement(FullLoopDetails, { computer: 'mac', id: 'abcdef' });
  if (rendered) rendered.update(element); else rendered = create(element);
});
const content = () => JSON.stringify(rendered!.toJSON());
const button = (text: string) => rendered!.root.findAllByType('Pressable' as never).find(node => node.findAllByType('Text' as never).map(child => Array.isArray(child.props.children) ? child.props.children.join('') : String(child.props.children)).join('').includes(text))!;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.computers[0].connection = 'connected'; fixture.deliveries = [];
  fixture.readLoop.mockReset().mockResolvedValue(detail);
  fixture.prepareLoopAction.mockReset().mockImplementation(async (_computer, _id, action) => ({ token: 'one-use', code: '4321', revision: 3, action, detail } satisfies LoopConfirmation));
  fixture.confirmLoopAction.mockReset().mockResolvedValue({ id: 'decision', status: 'waiting' });
});
afterEach(async () => { await act(async () => rendered?.unmount()); rendered = undefined; });

it('reads full detail and requires both typed challenge and rejection note', async () => {
  await render();
  expect(fixture.readLoop).toHaveBeenCalledWith('mac', 'abcdef');
  expect(content()).toContain(detail.brief); expect(content()).toContain(detail.history);
  await act(async () => button('Request changes').props.onPress());
  expect(fixture.prepareLoopAction).toHaveBeenCalledWith('mac', 'abcdef', 'reject');
  expect(content()).toContain('4321');
  expect(button('Confirm request changes').props.disabled).toBe(true);
  const input = (label: string) => rendered!.root.findAllByType('TextInput' as never).find(node => node.props.accessibilityLabel === label)!;
  await act(async () => input('Four digit confirmation code').props.onChangeText('4321'));
  expect(button('Confirm request changes').props.disabled).toBe(true);
  await act(async () => input('Reason for requesting changes').props.onChangeText('Reduce scope'));
  expect(button('Confirm request changes').props.disabled).toBe(false);
  await act(async () => button('Confirm request changes').props.onPress());
  expect(fixture.confirmLoopAction).toHaveBeenCalledWith('mac', 'abcdef', expect.objectContaining({ token: 'one-use', revision: 3, action: 'reject' }), '4321', 'Reduce scope');
});
it('names the whole batch before deletion and disables it while disconnected', async () => {
  await render();
  await act(async () => button('Delete loop').props.onPress());
  expect(content()).toContain('Permanently removes batch ffffff (3 steps)');
  fixture.computers[0].connection = 'offline'; await render();
  expect(button('Confirm delete').props.disabled).toBe(true);
  expect(fixture.confirmLoopAction).not.toHaveBeenCalled();
});
it('shows a refused stale challenge and permits refresh without sending another effect', async () => {
  fixture.prepareLoopAction.mockRejectedValue(new Error('the loop changed since you read it'));
  await render();
  await act(async () => button('Approve brief').props.onPress());
  expect(content()).toContain('the loop changed');
  expect(fixture.confirmLoopAction).not.toHaveBeenCalled();
  await act(async () => button('Refresh').props.onPress());
  expect(fixture.readLoop).toHaveBeenCalledTimes(2);
});
