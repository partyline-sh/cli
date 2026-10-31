import { describe, expect, it } from 'vitest';
import { fitTerminalFont, phoneTerminalViewport, terminalSnapshotText } from '../src/ui/terminal-layout';
import { TerminalResize } from '../src/runtime/terminal-resize';
import type { InnerMessage } from '../src/runtime/protocol/session';

describe('phone terminal geometry', () => {
  it('removes the capture delimiter without discarding real blank rows', () => {
    expect(terminalSnapshotText('first\nsecond\n').split('\n')).toHaveLength(2);
    expect(terminalSnapshotText('first\n\n\n')).toBe('first\n\n');
    expect(terminalSnapshotText('prompt>')).toBe('prompt>');
  });
  it('uses measured cells and the remaining keyboard-adjusted viewport', () => {
    expect(phoneTerminalViewport(393, 500, 7, 18)).toEqual({ columns: 52, rows: 26 });
    expect(phoneTerminalViewport(393, 230, 7, 18)).toEqual({ columns: 52, rows: 11 });
    expect(phoneTerminalViewport(844, 230, 7, 18)).toEqual({ columns: 117, rows: 11 });
  });
  it('waits for layout and bounds dimensions to the server contract', () => {
    expect(phoneTerminalViewport(0, 500, 7, 18)).toBeUndefined();
    expect(phoneTerminalViewport(393, 500, NaN, 18)).toBeUndefined();
    expect(phoneTerminalViewport(10000, 10000, 1, 1)).toEqual({ columns: 300, rows: 200 });
    expect(phoneTerminalViewport(100, 40, 10, 20)).toEqual({ columns: 20, rows: 5 });
  });
  it('fits the full grid using actual glyph width without enlarging narrow panes', () => {
    const font = fitTerminalFont(393, 160, 12, 8);
    expect(font * 8 / 12 * 160).toBeLessThanOrEqual(369.001);
    expect(fitTerminalFont(393, 30, 12, 8)).toBe(12);
  });
});

describe('terminal layout requests', () => {
  it('uses fresh request identities for renewals and ignores other computer replies', async () => {
    const sent: InnerMessage[] = [], rpc = new TerminalResize((_, m) => { sent.push(m); });
    const request = rpc.request('mac', '%21', 'phone', { columns: 52, rows: 26 });
    expect(sent[0]).toMatchObject({ type: 'resize', pane: '%21', data: { mode: 'phone', columns: 52, rows: 26 } });
    expect(rpc.result('other', { type: 'input_result', id: sent[0].id, status: 'ok' })).toBe(false);
    rpc.result('mac', { type: 'input_result', id: sent[0].id, status: 'ok' }); await request;
    const renewal = rpc.request('mac', '%21', 'phone', { columns: 52, rows: 26 });
    expect(sent[1].id).not.toBe(sent[0].id);
    rpc.result('mac', { type: 'input_result', id: sent[1].id, status: 'ok' }); await renewal;
  });
  it('refuses invalid dimensions before sending and does not replay after disconnect', async () => {
    const sent: InnerMessage[] = [], rpc = new TerminalResize((_, m) => { sent.push(m); });
    await expect(rpc.request('mac', '%21', 'phone', { columns: 3, rows: 26 })).rejects.toThrow('Invalid');
    expect(sent).toHaveLength(0);
    const request = rpc.request('mac', '%21', 'phone', { columns: 52, rows: 26 });
    rpc.disconnected('mac'); await expect(request).rejects.toThrow('disconnected');
    expect(sent).toHaveLength(1);
  });
});
