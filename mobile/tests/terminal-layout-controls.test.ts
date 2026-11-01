import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { TerminalLayoutControls } from '../src/ui/TerminalLayoutControls';

const fixture = vi.hoisted(() => ({ focused: true, resize: vi.fn<(...args: unknown[]) => Promise<void>>() }));
vi.mock('expo-router', () => ({ useIsFocused: () => fixture.focused }));
vi.mock('../src/runtime', () => ({ usePartyline: () => ({ computers: [{ id: 'mac', connection: 'connected', capabilities: ['terminal_resize'] }], resizeTerminal: fixture.resize }) }));
vi.mock('react-native', () => ({
  Pressable: 'Pressable', Text: 'Text', View: 'View',
  StyleSheet: { create: (styles: unknown) => styles }, Platform: { select: () => 'monospace' },
}));
let rendered: ReactTestRenderer | undefined;
const render = async (collapsed = false) => act(async () => {
  const element = createElement(TerminalLayoutControls, { computer: 'mac', pane: '%21', collapsed, viewport: { columns: 52, rows: 26 } });
  if (rendered) rendered.update(element); else rendered = create(element);
});
const advance = async (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const phone = () => rendered!.root.findByProps({ accessibilityLabel: 'Resize the shared terminal session' });
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.focused = true; fixture.resize.mockReset().mockResolvedValue(); vi.useFakeTimers();
});
afterEach(async () => { await act(async () => rendered?.unmount()); rendered = undefined; vi.useRealTimers(); });

it('never changes the Mac size merely by opening the terminal', async () => {
  await render(); await advance(30000);
  expect(fixture.resize).not.toHaveBeenCalled();
  expect(phone().props.accessibilityState.checked).toBe(false);
});
it('renews an explicit phone layout, then releases it and stops renewing when hidden', async () => {
  await render(); await act(async () => phone().props.onPress()); await advance(300);
  expect(fixture.resize).toHaveBeenCalledWith('mac', '%21', 'phone', { columns: 52, rows: 26 });
  await advance(10000); expect(fixture.resize).toHaveBeenCalledTimes(2);
  fixture.focused = false; await render();
  expect(fixture.resize).toHaveBeenLastCalledWith('mac', '%21', 'desktop');
  const count = fixture.resize.mock.calls.length;
  await advance(30000); expect(fixture.resize).toHaveBeenCalledTimes(count);
});
it('shows a receiver refusal and falls back to desktop mode', async () => {
  fixture.resize.mockRejectedValueOnce(new Error('This window is split'));
  await render(); await act(async () => phone().props.onPress()); await advance(300);
  expect(phone().props.accessibilityState.checked).toBe(false);
  expect(rendered!.root.findByProps({ accessibilityLiveRegion: 'assertive' }).props.children).toContain('split');
});
it('hides options without ending an active resize lease, retaining an explicit restore action', async () => {
  await render(); await act(async () => phone().props.onPress()); await advance(300);
  fixture.resize.mockClear();
  await render(true); await advance(10000);
  expect(fixture.resize).toHaveBeenCalledExactlyOnceWith('mac', '%21', 'phone', { columns: 52, rows: 26 });
  expect(rendered!.root.findAllByProps({ accessibilityLabel: 'Resize the shared terminal session' })).toHaveLength(0);
  await act(async () => rendered!.root.findByProps({ accessibilityLabel: 'Restore computer terminal size' }).props.onPress());
  expect(fixture.resize).toHaveBeenLastCalledWith('mac', '%21', 'desktop');
});
