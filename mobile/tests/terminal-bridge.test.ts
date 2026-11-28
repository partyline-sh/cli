import { expect, it, vi } from 'vitest';
import { terminalBridgeMessage } from '../src/ui/stream-terminal/bridge';

it('rejects large bridge messages before parsing JSON', () => {
  const parse = vi.spyOn(JSON, 'parse');
  try {
    expect(() => terminalBridgeMessage(' '.repeat(12 * 1024 + 1))).toThrow('exceeded');
    expect(parse).not.toHaveBeenCalled();
  } finally { parse.mockRestore(); }
});
it('accepts maximum size terminal input and small control messages', () => {
  const data = 'A'.repeat(10924);
  expect(terminalBridgeMessage(JSON.stringify({ type: 'input', data }))).toEqual({ type: 'input', data });
  expect(terminalBridgeMessage('{"type":"loaded"}')).toEqual({ type: 'loaded' });
});
it.each(['null', '[]', '"text"', '{'])('rejects invalid bridge messages: %s', raw => {
  expect(() => terminalBridgeMessage(raw)).toThrow();
});
