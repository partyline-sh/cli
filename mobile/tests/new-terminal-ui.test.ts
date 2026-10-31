import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { Computer, Delivery } from '../src/runtime/types';
import { NewTerminalRow } from '../src/ui/NewTerminalRow';

const fixture = vi.hoisted(() => ({
  deliveries: [] as Delivery[], openShell: vi.fn(), selectView: vi.fn(),
  retryDelivery: vi.fn(), acknowledgeDelivery: vi.fn(), push: vi.fn(),
}));
vi.mock('../src/runtime', () => ({ usePartyline: () => fixture }));
vi.mock('expo-router', () => ({ router: { push: fixture.push } }));
vi.mock('react-native', () => ({
  Pressable: 'Pressable', Text: 'Text', View: 'View',
  Platform: { select: () => 'monospace' }, Alert: { alert: vi.fn() },
}));
const host: Computer = { id: 'mac', name: 'Mac', connection: 'connected', capabilities: ['shell_open'], relay: 'https://relay.example', pairedAt: 1 };
let rendered: ReactTestRenderer | undefined;
const render = async () => act(async () => {
  const element = createElement(NewTerminalRow, { computer: host });
  if (rendered) rendered.update(element); else rendered = create(element);
});
const open = () => rendered!.root.findByProps({ accessibilityLabel: 'New terminal on Mac' });
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.deliveries = [];
  host.capabilities = ['shell_open']; host.connection = 'connected';
  fixture.push.mockReset(); fixture.selectView.mockReset();
  fixture.openShell.mockReset().mockResolvedValue({ id: 'shell', computerId: 'mac', pane: 'shell', status: 'waiting' });
});
afterEach(async () => { await act(async () => rendered?.unmount()); rendered = undefined; });

it('opens the ordinary session route only after the computer confirms the shell', async () => {
  await render();
  expect(fixture.openShell).not.toHaveBeenCalled();
  await act(async () => open().props.onPress());
  expect(fixture.openShell).toHaveBeenCalledWith('mac');
  expect(fixture.push).not.toHaveBeenCalled();
  fixture.deliveries = [{ id: 'shell', computerId: 'mac', pane: 'shell', status: 'unknown' }];
  await render();
  expect(open().props.disabled).toBe(true);
  expect(fixture.push).not.toHaveBeenCalled();
  fixture.deliveries = [{ ...fixture.deliveries[0], status: 'confirmed', data: { pane: '%77' } }];
  await render();
  expect(fixture.selectView).toHaveBeenCalledWith('terminal');
  expect(fixture.push).toHaveBeenCalledExactlyOnceWith({ pathname: '/session/[computer]/[pane]', params: { computer: 'mac', pane: '77' } });
  await render();
  expect(fixture.push).toHaveBeenCalledTimes(1);
});
it('disables creation on old or disconnected computers', async () => {
  host.capabilities = []; await render(); expect(open().props.disabled).toBe(true);
  expect(JSON.stringify(rendered!.toJSON())).toContain('Update Partyline');
  host.capabilities = ['shell_open']; host.connection = 'offline';
  await render(); expect(open().props.disabled).toBe(true);
  expect(fixture.openShell).not.toHaveBeenCalled();
});
