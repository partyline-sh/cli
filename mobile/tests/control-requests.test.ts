import { afterEach, expect, it, vi } from 'vitest';
import { ControlRequests } from '../src/runtime/control-requests';
import type { InnerMessage } from '../src/runtime/protocol/session';

afterEach(() => vi.useRealTimers());
it('keeps read/prepare replies scoped to the computer and never replays on disconnect', async () => {
  const sent: InnerMessage[] = [];
  const requests = new ControlRequests((_, message) => sent.push(message));
  const detail = requests.request('pc', 'loop_detail', 'abcdef');
  expect(requests.result('other', { type: 'input_result', id: sent[0].id, status: 'ok', data: 'wrong' })).toBe(false);
  expect(requests.result('pc', { type: 'input_result', id: sent[0].id, status: 'ok', data: { brief: 'full brief' } })).toBe(true);
  await expect(detail).resolves.toEqual({ brief: 'full brief' });
  const prepared = requests.request('pc', 'loop_prepare', 'abcdef', { action: 'reject' });
  const refused = expect(prepared).rejects.toThrow('disconnected');
  requests.disconnected('pc');
  await refused;
  expect(sent).toHaveLength(2);
  expect(requests.result('pc', { type: 'input_result', id: sent[1].id, status: 'ok' })).toBe(false);
  requests.dispose();
});
it('bounds pending reads and times out without sending again', async () => {
  vi.useFakeTimers();
  const send = vi.fn();
  const requests = new ControlRequests(send);
  const pending = Array.from({ length: 8 }, () => requests.request('pc', 'loop_detail', 'abcdef').catch(e => e.message));
  await expect(requests.request('pc', 'loop_detail', 'abcdef')).rejects.toThrow('Wait');
  await vi.advanceTimersByTimeAsync(60000);
  expect(await Promise.all(pending)).toEqual(Array(8).fill('Computer did not respond. Try again after it reconnects.'));
  expect(send).toHaveBeenCalledTimes(8);
  requests.dispose();
});
