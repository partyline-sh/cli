import { afterEach, expect, it, vi } from 'vitest';
import { LiveTerminals, type LiveEvent, type LiveScreen } from '../src/runtime/live-terminal';
import type { InnerMessage } from '../src/runtime/protocol/session';
const screen: LiveScreen = { ansi: '$ ', typing: true, foreground: 'zsh', columns: 80, rows: 24, x: 2, y: 0, cursor: true, alternate: false, copy: false, acceptedSeq: 0, echoSeq: 0 };
const token = 'a'.repeat(48);
function setup() {
  vi.useFakeTimers();
  const sent: InnerMessage[] = [], events: LiveEvent[] = [];
  const live = new LiveTerminals((_, m) => sent.push(m));
  const handle = live.open('computer', '%3', event => events.push(event));
  const open = sent[0];
  const ready = () => live.message('computer', { type: 'live_result', pane: '%3', id: open.id, status: 'ok', data: { token } });
  return { live, handle, ready, sent, events };
}
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); });
it('does not persist or replay input when reconnecting', () => {
  const f = setup(); f.ready(); f.handle.send(1, 'text', 'test');
  f.live.disconnected('computer');
  expect(f.events.at(-1)).toMatchObject({ type: 'closed' });
  const before = f.sent.length;
  expect(() => f.handle.send(2, 'key', 'Enter')).toThrow();
  f.live.open('computer', '%3', () => {});
  expect(f.sent.slice(before).map(m => m.type)).toEqual(['live_open']);
  f.live.dispose();
});
it('tolerates a write ACK overtaking an older screen and ignores stale screen revisions', () => {
  const f = setup(); f.ready(); f.handle.send(1, 'text', 'x');
  f.live.message('computer', { type: 'live_result', pane: '%3', id: token, status: 'ok', revision: 1 });
  f.live.message('computer', { type: 'live_screen', pane: '%3', id: token, revision: 1, data: screen });
  expect(f.events.at(-1)).toMatchObject({ type: 'screen', screen: { acceptedSeq: 0 } });
  f.live.message('computer', { type: 'live_screen', pane: '%3', id: token, revision: 2, data: { ...screen, acceptedSeq: 1, echoSeq: 1 } });
  const count = f.events.length;
  f.live.message('computer', { type: 'live_screen', pane: '%3', id: token, revision: 1, data: screen });
  expect(f.events).toHaveLength(count); f.live.dispose();
});
it('discards sequence gaps, queue overflow and timed out input without retrying', () => {
  const f = setup(); f.ready(); f.handle.send(2, 'text', 'gap');
  expect(f.sent.filter(m => m.type === 'live_input')).toEqual([]);
  expect(f.events.at(-1)).toMatchObject({ type: 'closed' });
  const second = setup(); second.ready(); second.handle.send(1, 'key', 'Enter');
  vi.advanceTimersByTime(8500);
  expect(second.events.at(-1)).toMatchObject({ type: 'closed', reason: expect.stringContaining('not replayed') });
  expect(second.sent.filter(m => m.type === 'live_input')).toHaveLength(1);
});
it('ignores other pane/token frames and fails closed on regressing echo metadata', () => {
  const f = setup(); f.ready(); f.handle.send(1, 'text', 'x');
  f.live.message('computer', { type: 'live_screen', pane: '%4', id: token, revision: 1, data: screen });
  expect(f.events.at(-1)).toEqual({ type: 'ready' });
  f.live.message('computer', { type: 'live_screen', pane: '%3', id: 'other', revision: 1, data: screen });
  expect(f.events.at(-1)).toEqual({ type: 'ready' });
  f.live.message('computer', { type: 'live_screen', pane: '%3', id: token, revision: 1, data: { ...screen, acceptedSeq: 1, echoSeq: 1 } });
  f.live.message('computer', { type: 'live_screen', pane: '%3', id: token, revision: 2, data: { ...screen, acceptedSeq: 1, echoSeq: 0 } });
  expect(f.events.at(-1)).toMatchObject({ type: 'closed' });
});
