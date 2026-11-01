import { createElement, useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { MotionGate, MotionProvider, useSignalMotion } from '../src/ui/motion';
const mock = vi.hoisted(() => ({
  start: vi.fn(), stop: vi.fn(), reset: vi.fn(), timing: vi.fn(),
  preference: undefined as undefined | ((value: boolean) => void),
  activity: undefined as undefined | ((value: string) => void),
  blur: undefined as undefined | (() => void),
  removePreference: vi.fn(), removeActivity: vi.fn(),
  read: vi.fn<() => Promise<boolean>>(),
}));
vi.mock('react-native', () => ({
  AccessibilityInfo: { isReduceMotionEnabled: mock.read, addEventListener: (_: string, fn: (v: boolean) => void) => { mock.preference = fn; return { remove: mock.removePreference }; } },
  AppState: { currentState: 'active', addEventListener: (_: string, fn: (v: string) => void) => { mock.activity = fn; return { remove: mock.removeActivity }; } },
  Animated: { Value: class { setValue = mock.reset; }, timing: mock.timing, loop: () => ({ start: mock.start, stop: mock.stop }) }, Easing: { linear: 'linear' },
}));
vi.mock('expo-router', () => ({ useFocusEffect: (fn: () => () => void) => useEffect(() => { mock.blur = fn(); return mock.blur; }, [fn]) }));
function Probe({ active }: { active: boolean }) { useSignalMotion(active); return null; }
let tree: ReactTestRenderer;
const render = async (active = true) => act(async () => {
  const el = createElement(MotionProvider, null, createElement(Probe, { active }));
  if (tree) tree.update(el); else tree = create(el);
});
beforeEach(() => { Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true }); vi.clearAllMocks(); mock.read.mockResolvedValue(false); });
afterEach(async () => { await act(async () => tree?.unmount()); tree = undefined as unknown as ReactTestRenderer; });
it('stops on reduced motion, background, route blur and settled state', async () => {
  await render(); expect(mock.start).toHaveBeenCalledOnce();
  expect(mock.timing.mock.calls[0][1]).toMatchObject({ useNativeDriver: true, isInteraction: false });
  await act(async () => mock.preference!(true)); expect(mock.stop).toHaveBeenCalledTimes(1);
  await act(async () => mock.preference!(false)); expect(mock.start).toHaveBeenCalledTimes(2);
  await act(async () => mock.activity!('background')); expect(mock.stop).toHaveBeenCalledTimes(2);
  await act(async () => mock.activity!('active')); expect(mock.start).toHaveBeenCalledTimes(3);
  await render(false); expect(mock.stop).toHaveBeenCalledTimes(3);
  await render(true); expect(mock.start).toHaveBeenCalledTimes(4);
  await act(async () => mock.blur!()); expect(mock.stop).toHaveBeenCalledTimes(4);
});
it('cannot start from a late accessibility reply after unmount', async () => {
  let resolve!: (value: boolean) => void;
  mock.read.mockImplementation(() => new Promise(r => { resolve = r; }));
  await render(); expect(mock.start).not.toHaveBeenCalled();
  await act(async () => tree.unmount());
  await act(async () => resolve(false)); expect(mock.start).not.toHaveBeenCalled();
  expect(mock.removePreference).toHaveBeenCalled(); expect(mock.removeActivity).toHaveBeenCalled();
});
it('a preference-change event wins over an older pending preference query', async () => {
  let resolve!: (value: boolean) => void;
  mock.read.mockImplementation(() => new Promise(r => { resolve = r; }));
  await render(); await act(async () => mock.preference!(true));
  await act(async () => resolve(false)); expect(mock.start).not.toHaveBeenCalled();
});

it('stops all scoped motion when a session snapshot becomes stale', async () => {
  const scope = (active: boolean) => createElement(MotionProvider, null, createElement(MotionGate, { active }, createElement(Probe, { active: true })));
  await act(async () => { tree = create(scope(true)); }); expect(mock.start).toHaveBeenCalledOnce();
  await act(async () => tree.update(scope(false))); expect(mock.stop).toHaveBeenCalledOnce();
  await act(async () => tree.update(scope(true))); expect(mock.start).toHaveBeenCalledTimes(2);
});
