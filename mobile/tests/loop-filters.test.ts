import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { LoopItem } from '../src/runtime/types';
import { LoopsScreen } from '../src/ui/LoopsScreen';

const fixture = vi.hoisted(() => ({ loops: [] as LoopItem[], push: vi.fn() }));
vi.mock('expo-router', () => ({ router: { push: fixture.push } }));
vi.mock('../src/runtime', () => ({ usePartyline: () => fixture }));
vi.mock('react-native', () => ({
  Pressable: 'Pressable', ScrollView: 'ScrollView', Text: 'Text', View: 'View',
  StyleSheet: { create: (styles: unknown) => styles }, Platform: { select: () => 'monospace' },
}));
vi.mock('react-native-safe-area-context', () => ({ SafeAreaView: 'SafeAreaView' }));
vi.mock('../src/ui/parts', () => ({ Empty: 'Empty', LargeTitle: 'LargeTitle' }));
let rendered: ReactTestRenderer | undefined;
const render = async () => act(async () => {
  if (rendered) rendered.update(createElement(LoopsScreen)); else rendered = create(createElement(LoopsScreen));
});
const chip = (label: string) => rendered!.root.findByProps({ accessibilityLabel: label });
const press = async (label: string) => act(async () => chip(label).props.onPress());
const cards = () => rendered!.root.findAll(n => n.type === ('Pressable' as never) && n.props.accessible).map(n => n.props.accessibilityLabel);
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  fixture.push.mockReset();
  fixture.loops = [
    { computerId: 'mac', id: 'a', title: 'Build task', section: 'In progress', state: 'building' },
    { computerId: 'mac', id: 'b', title: 'Review task', section: 'In review', state: 'reviewing' },
    { computerId: 'mac', id: 'c', title: 'Finished task', section: 'Done', state: 'done' },
  ];
});
afterEach(async () => { await act(async () => rendered?.unmount()); rendered = undefined; });

describe('loop stage filters', () => {
  it('shows In progress and In review together and lets either be deselected', async () => {
    await render(); await press('In review');
    expect(chip('In progress').props.accessibilityState.checked).toBe(true);
    expect(chip('In review').props.accessibilityState.checked).toBe(true);
    expect(cards()).toEqual(['Build task, building', 'Review task, reviewing']);
    await press('In progress');
    expect(cards()).toEqual(['Review task, reviewing']);
    await press('In review');
    expect(cards()).toEqual([]);
    expect(rendered!.root.findByType('Empty' as never).props.children).toBe('Select one or more stages.');
  });

  it('keeps the chosen stages when a card moves into a different stage', async () => {
    await render(); await press('In review');
    fixture.loops = fixture.loops.map(l => l.id === 'a' ? { ...l, section: 'Done' } : l);
    await render();
    expect(chip('In progress').props.accessibilityState.checked).toBe(true);
    expect(chip('In review').props.accessibilityState.checked).toBe(true);
    expect(cards()).toEqual(['Review task, reviewing']);
  });

  it('selects every stage including new board columns and retains selected empty columns', async () => {
    fixture.loops.push({ computerId: 'mac', id: 'd', title: 'Custom task', section: 'Blocked externally', state: 'waiting' });
    await render(); await press('All stages');
    expect(cards()).toHaveLength(4);
    expect(chip('Blocked externally').props.accessibilityState.checked).toBe(true);
    fixture.loops.push({ computerId: 'mac', id: 'e', title: 'New stage task', section: 'New board stage', state: 'waiting' });
    await render();
    expect(cards()).toHaveLength(5);
    expect(chip('New board stage').props.accessibilityState.checked).toBe(true);
    await press('All stages');
    expect(chip('All stages').props.accessibilityState.checked).toBe(false);
    expect(cards()).toHaveLength(0);
    await press('Blocked externally');
    fixture.loops = []; await render();
    expect(chip('Blocked externally').props.accessibilityState.checked).toBe(true);
  });
});

it('opens the chosen computer and loop without confusing duplicate IDs across computers', async () => {
  fixture.loops = [
    { computerId: 'mac', id: 'same', title: 'Mac task', section: 'In progress', state: 'building' },
    { computerId: 'server', id: 'same', title: 'Server task', section: 'In progress', state: 'building' },
  ];
  await render();
  const items = rendered!.root.findAllByProps({ accessibilityHint: 'Open loop details' });
  await act(async () => items[1].props.onPress());
  expect(fixture.push).toHaveBeenCalledWith({ pathname: '/loop/[computer]/[id]', params: { computer: 'server', id: 'same' } });
});
