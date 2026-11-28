import { expect, it } from 'vitest';
import type { LoopItem } from '../src/runtime/types';
import { loopProgress } from '../src/ui/loop-progress';
const loop = (patch: Partial<LoopItem> = {}): LoopItem => ({ id: 'a', computerId: 'mac', title: 'Task', section: 'In progress', state: 'building', ...patch });
it('animates only reported work and never fabricates completed preceding stages', () => {
  expect(loopProgress(loop(), true).stages.map(s => s.mark)).toEqual(['active', 'unreported', 'unreported']);
  expect(loopProgress(loop({ state: 'baseline' }), true).stages.map(s => s.mark)).toEqual(['unreported', 'active', 'unreported']);
  expect(loopProgress(loop({ state: 'checking', chips: [{ name: 'checks', mark: 'done' }, { name: 'claude', mark: 'reading' }] }), true).stages.map(s => s.mark)).toEqual(['unreported', 'done', 'active']);
});
it('stops cached and paused work, including a disconnected computer', () => {
  expect(loopProgress(loop(), false).moving).toBe(false);
  for (const state of ['failed', 'stopped', 'paused', 'awaiting_you', 'awaiting_approval']) expect(loopProgress(loop({ state }), true).moving).toBe(false);
  expect(loopProgress(loop({ section: 'Needs you' }), true).moving).toBe(false);
});
it('preserves unknown states and distinguishes finished, limited and unreported chips', () => {
  expect(loopProgress(loop({ state: 'future_state' }), true).stages.every(s => s.mark === 'unreported')).toBe(true);
  expect(loopProgress(loop({ state: 'checking', chips: [{ name: 'checks', mark: 'future_mark' }, { name: 'claude', mark: 'limited' }] }), true).stages.map(s => s.mark)).toEqual(['unreported', 'unreported', 'blocked']);
});
