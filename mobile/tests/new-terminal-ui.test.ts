import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { Computer, Delivery } from '../src/runtime/types';
import { NewTerminalRow } from '../src/ui/NewTerminalRow';
vi.mock('../src/ui/Signal', () => ({ Signal: 'Signal' }));
vi.mock('../src/ui/ConnectionSignal', () => ({ ConnectionSignal: 'ConnectionSignal' }));
vi.mock('../src/ui/LoopProgress', () => ({ LoopProgress: 'LoopProgress' }));

const fixture = vi.hoisted(() => ({
  openTerminalStream: undefined as undefined|(()=>void), phoneTerminals: [] as {id:string;computerId:string;title:string}[], deliveries: [] as Delivery[], openShell: vi.fn(), selectView: vi.fn(),
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
  fixture.deliveries = [];fixture.openTerminalStream=undefined;fixture.phoneTerminals=[];
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

it('opens the phone-owned streaming route without creating a desktop shell',async()=>{
 host.capabilities=['term_stream'];fixture.openTerminalStream=()=>{};
 await render();await act(async()=>open().props.onPress());
 expect(fixture.openShell).not.toHaveBeenCalled();expect(fixture.push).toHaveBeenCalledWith({pathname:'/terminal/[computer]/[session]',params:{computer:'mac',session:'new'}});
});

it('does not let an old unknown legacy request block the new stream',async()=>{
 host.capabilities=['term_stream'];fixture.openTerminalStream=()=>{};
 fixture.deliveries=[{id:'old',computerId:'mac',pane:'shell',status:'unknown'}];
 await render();expect(open().props.disabled).toBe(false);
});
