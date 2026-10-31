import type { LoopItem } from '../runtime/types';
export type StageMark = 'unreported' | 'active' | 'done' | 'blocked';
export type StageName = 'Build' | 'Checks' | 'Review';
export function loopProgress(loop: LoopItem, connected: boolean): { stages: { name: StageName; mark: StageMark }[]; moving: boolean } {
  const state = loop.state.toLowerCase();
  const stages: { name: StageName; mark: StageMark }[] = ['Build', 'Checks', 'Review'].map(name => ({ name: name as StageName, mark: 'unreported' }));
  const chips = loop.chips ?? [];
  const checks = chips.find(x => x.name === 'checks');
  const reviews = chips.filter(x => x.name !== 'checks');
  if (checks) stages[1].mark = checks.mark === 'done' ? 'done' : checks.mark === 'reading' ? 'active' : checks.mark === 'limited' ? 'blocked' : 'unreported';
  if (reviews.length) stages[2].mark = reviews.some(x => x.mark === 'limited') ? 'blocked' : reviews.some(x => x.mark === 'reading') ? 'active' : reviews.every(x => x.mark === 'done') ? 'done' : 'unreported';
  if (state === 'building' || state === 'revising') stages[0].mark = 'active';
  if (state === 'baseline' || state === 'checking' && !checks || state === 'running checks') stages[1].mark = 'active';
  if (['batch_review', 'scope_review', 'reviewing'].includes(state)) stages[2].mark = 'active';
  if (state === 'pr_ready') stages.forEach(stage => { stage.mark = 'done'; });
  // A stopped run or an old snapshot must never look like work is still happening.
  const moving = connected && !['failed', 'stopped', 'paused', 'awaiting_you', 'awaiting_approval', 'dispute'].includes(state) && loop.section !== 'Needs you';
  return { stages, moving };
}
