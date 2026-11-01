import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { expect, it, vi } from 'vitest';
import { Terminal } from '../src/ui/Terminal';
vi.mock('react-native', () => ({ Pressable: 'Pressable', ScrollView: 'ScrollView', Text: 'Text', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { select: () => 'monospace' } }));
it('keeps columns exact by default and retains a chosen mode while display controls are hidden', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  let tree: ReactTestRenderer;
  const props = { ansi: 'filename       size\nnotes/         256B', columns: 80, size: 1 };
  try {
    await act(async () => { tree = create(createElement(Terminal, props)); });
    expect(tree!.root.findAll(n => n.type === ('ScrollView' as never) && n.props.horizontal)).toHaveLength(1);
    await act(async () => tree!.root.findByProps({ accessibilityLabel: 'Wrap terminal text on this phone' }).props.onPress());
    await act(async () => tree!.update(createElement(Terminal, { ...props, showControls: false })));
    expect(tree!.root.findAll(n => n.type === ('ScrollView' as never) && n.props.horizontal)).toHaveLength(0);
    expect(tree!.root.findAllByProps({ accessibilityLabel: 'Exact terminal grid' })).toHaveLength(0);
  } finally { await act(async () => tree?.unmount()); }
});
it('adapts to arriving pane metadata until the reader chooses a display mode', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  let rendered: ReactTestRenderer;
  const props = { ansi: 'A long terminal row', columns: 120, size: 1 };
  const horizontal = () => rendered.root.findAll(n => n.type === ('ScrollView' as never) && n.props.horizontal).length;
  try {
    await act(async () => { rendered = create(createElement(Terminal, { ...props, defaultDisplay: 'wrap' })); });
    expect(horizontal()).toBe(0);
    await act(async () => { rendered.update(createElement(Terminal, { ...props, defaultDisplay: 'grid' })); });
    expect(horizontal()).toBe(1);
    await act(async () => rendered.root.findByProps({ accessibilityLabel: 'Wrap terminal text on this phone' }).props.onPress());
    expect(horizontal()).toBe(0);
    await act(async () => { rendered.update(createElement(Terminal, { ...props, defaultDisplay: 'grid' })); });
    expect(horizontal()).toBe(0);
  } finally {
    await act(async () => rendered?.unmount());
  }
});
