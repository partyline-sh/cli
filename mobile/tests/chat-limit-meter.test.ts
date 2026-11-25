import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ChatLimitMeter } from '../src/ui/ChatLimitMeter';
import type { ChatRoomState, Computer, Delivery } from '../src/runtime/types';

const fixture = vi.hoisted(() => ({ app: { deliveries: [] as Delivery[], setChatLimit: vi.fn() as ((id: string, limit: number) => Promise<Delivery>) | undefined } }));
vi.mock('../src/runtime', () => ({ usePartyline: () => fixture.app }));
vi.mock('react-native', () => ({ Pressable: 'Pressable', Text: 'Text', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { OS: 'ios', select: () => 'monospace' } }));

const computer = (capabilities: string[]): Computer => ({ id: 'mac', name: 'Mac', connection: 'connected', capabilities } as unknown as Computer);
const room = (extra: Partial<ChatRoomState>): ChatRoomState => ({ stopped: false, members: [], agentRun: 16, agentCap: 16, ...extra });
let tree: ReactTestRenderer;
const render = async (host: Computer, r: ChatRoomState) => act(async () => {
  const element = createElement(ChatLimitMeter, { computer: host, room: r });
  if (tree) tree.update(element); else tree = create(element);
});
const text = () => JSON.stringify(tree.toJSON());
const plus = () => tree.root.findByProps({ accessibilityLabel: 'Allow more agent replies' });

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.app.deliveries = [];
  fixture.app.setChatLimit = vi.fn(async () => ({ id: 'd1', computerId: 'mac', pane: 'chat-limit', status: 'waiting' }) as Delivery);
});
afterEach(async () => { await act(async () => tree?.unmount()); tree = undefined as never; });

it('a paused room: + asks for four more replies, then shows the change as pending with the buttons off', async () => {
  await render(computer(['chat_limit']), room({}));
  expect(text()).toContain('Paused at 16 agent replies');
  await act(async () => plus().props.onPress());
  expect(fixture.app.setChatLimit).toHaveBeenCalledWith('mac', 20);
  fixture.app.deliveries = [{ id: 'd1', computerId: 'mac', pane: 'chat-limit', status: 'waiting' }];
  await render(computer(['chat_limit']), room({}));
  expect(text()).toContain('Changing the limit');
  expect(plus().props.disabled).toBe(true);
  await act(async () => plus().props.onPress());
  expect(fixture.app.setChatLimit).toHaveBeenCalledTimes(1);
});

it('a refusal or an uncertain result reaches the screen after the submit has resolved', async () => {
  fixture.app.deliveries = [{ id: 'd1', computerId: 'mac', pane: 'chat-limit', status: 'refused', reason: 'rate limit exceeded' }];
  await render(computer(['chat_limit']), room({ agentRun: 3 }));
  expect(text()).toContain('rate limit exceeded');
  expect(plus().props.disabled).toBe(false);
  fixture.app.deliveries = [{ id: 'd2', computerId: 'mac', pane: 'chat-limit', status: 'unknown' }];
  await render(computer(['chat_limit']), room({ agentRun: 3 }));
  expect(text()).toContain('may not have changed the limit');
  fixture.app.deliveries = [{ id: 'd3', computerId: 'mac', pane: 'chat-limit', status: 'confirmed' }];
  await render(computer(['chat_limit']), room({ agentRun: 3, agentCap: 20 }));
  expect(text()).toContain('Agents 3/20 since you spoke');
  expect(text()).not.toMatch(/Changing the limit|may not have/);
});

it('an older computer without chat_limit shows the count but no controls', async () => {
  await render(computer(['chat_send']), room({ agentRun: 5 }));
  expect(text()).toContain('Agents 5/16');
  expect(tree.root.findAllByProps({ accessibilityLabel: 'Allow more agent replies' })).toHaveLength(0);
});

it('a manual /stop is not described as a limit pause, and the limit being off has no controls', async () => {
  await render(computer(['chat_limit']), room({ stopped: true }));
  expect(text()).toContain('Stopped with /stop');
  expect(text()).not.toContain('press +');
  await render(computer(['chat_limit']), room({ agentCap: -1, agentRun: 40 }));
  expect(text()).toContain('limit off');
  expect(tree.root.findAllByProps({ accessibilityLabel: 'Allow more agent replies' })).toHaveLength(0);
});
