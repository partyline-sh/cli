import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { Terminal } from '../src/ui/Terminal';
vi.mock('react-native', () => ({ Pressable: 'Pressable', ScrollView: 'ScrollView', Text: 'Text', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { select: () => 'monospace' } }));
let tree: ReactTestRenderer;
const scrollToEnd = vi.fn();
const frame = async () => act(async () => { await vi.advanceTimersByTimeAsync(20); });
const scroll = () => tree.root.findByProps({ testID: 'terminal-output' });
const event = (y = 0) => ({ nativeEvent: { contentOffset: { y }, contentSize: { height: 5000 }, layoutMeasurement: { height: 500 } } });
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true }); vi.useFakeTimers(); scrollToEnd.mockClear();
  vi.stubGlobal('requestAnimationFrame', (callback: () => void) => setTimeout(callback, 16));
  vi.stubGlobal('cancelAnimationFrame', (id: ReturnType<typeof setTimeout>) => clearTimeout(id));
});
afterEach(async () => { await act(async () => tree?.unmount()); tree = undefined as never; vi.useRealTimers(); vi.unstubAllGlobals(); });
const render = async (ansi: string) => act(async () => {
  const element = createElement(Terminal, { ansi, size: 1, columns: 212, defaultDisplay: 'wrap', showControls: false });
  if (tree) tree.update(element); else tree = create(element, { createNodeMock: node => (node.props as { testID?: string }).testID === 'terminal-output' ? { scrollToEnd } : null });
});
it('renders every wide terminal row separately, preserving the newest row on fresh load and updates', async () => {
  await render(Array.from({ length: 55 }, (_, i) => `ROW ${i}: ` + 'long desktop row '.repeat(15)).join('\n') + '\nLATEST');
  expect(tree.root.findByProps({ testID: 'terminal-row-55' }).findAllByType('Text' as never).at(-1)?.props.children).toBe('LATEST');
  await act(async () => { scroll().props.onScroll(event()); scroll().props.onLayout({ nativeEvent: { layout: { width: 393, height: 500 } } }); scroll().props.onContentSizeChange(); });
  await frame(); expect(scrollToEnd).toHaveBeenCalled();
  scrollToEnd.mockClear();
  await render('newest output');
  await act(async () => scroll().props.onContentSizeChange());
  await frame(); expect(scrollToEnd).toHaveBeenCalled();
});
it('lets a reader scroll up, keeps their place through new snapshots, and resumes following with Jump to latest', async () => {
  await render('old\nlatest');
  await act(async () => { scroll().props.onScrollBeginDrag(); scroll().props.onScroll(event()); scroll().props.onScrollEndDrag(event()); });
  await render('old\nnewer output');
  await act(async () => scroll().props.onContentSizeChange()); await frame();
  expect(scrollToEnd).not.toHaveBeenCalled();
  await act(async () => tree.root.findByProps({ accessibilityLabel: 'Jump to latest terminal output' }).props.onPress());
  await frame(); expect(scrollToEnd).toHaveBeenCalled();
});
