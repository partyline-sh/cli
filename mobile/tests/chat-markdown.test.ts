import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, expect, it, vi } from 'vitest';
import { ChatMarkdown } from '../src/ui/ChatMarkdown';

vi.mock('react-native', () => ({ ScrollView: 'ScrollView', Text: 'Text', View: 'View', StyleSheet: { create: (s: unknown) => s }, Platform: { select: () => 'monospace' } }));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let tree: ReactTestRenderer;
afterEach(async () => { await act(async () => tree?.unmount()); });
const render = async (text: string) => { await act(async () => { tree = create(createElement(ChatMarkdown, { text })); }); return JSON.stringify(tree.toJSON()); };

it('renders emphasis, nested lists, code and tables as native nodes without markdown delimiters', async () => {
  const output = await render('**Ready** and `safe()`\n\n1. First\n   - Nested\n2. Second\n\n```ts\nconst x = 1;\n```\n\n| Item | State |\n|---|---|\n| Review | Passed |');
  expect(output).toContain('Ready'); expect(output).toContain('"fontWeight":"700"');
  const ready = tree.root.findAllByType('Text' as never).find(node => [node.props.children].flat(8).join('') === 'Ready');
  expect(ready?.props.style.fontWeight).toBe('700');
  expect(output).not.toContain('**Ready**'); expect(output).toContain('Nested');
  expect(output).toContain('1.'); expect(output).toContain('2.'); expect(output).toContain('const x = 1;');
  expect(output).toContain('Passed'); expect(output).not.toContain('|---|');
  expect(tree.root.findAllByType('ScrollView' as never)).toHaveLength(2);
});
it('does not create remote images, HTML or executable links', async () => {
  const output = await render('<script>alert(1)</script>\n\n![portrait](https://example.com/image.png)\n\n[click](javascript:alert(1))');
  expect(output).toContain('<script>alert(1)</script>'); expect(output).toContain('[image: portrait]');
  expect(tree.root.findAll(n => n.props.onPress || n.type === ('Image' as never) || n.type === ('WebView' as never))).toHaveLength(0);
});
it('preserves incomplete formatting and falls back intact for oversized messages', async () => {
  expect(await render('**unfinished `code')).toContain('unfinished');
  const text = 'large '.repeat(6000); await render(text);
  expect(tree.root.findByType('Text' as never).props.children).toBe(text);
});
