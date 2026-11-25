import { describe, expect, it } from 'vitest';
import { readChatRoom } from '../src/runtime/chat-room';
import { chatLimitView, nextChatLimit } from '../src/ui/chat-limit';

describe('chat reply limit', () => {
  it('reads the reply count and limit from the encrypted room snapshot', () => {
    expect(readChatRoom({ members: [], agent_run: 7, agent_cap: 20 })).toMatchObject({ agentRun: 7, agentCap: 20 });
    expect(readChatRoom({ members: [], agent_run: -1, agent_cap: 'x' })).toMatchObject({ agentRun: undefined, agentCap: undefined });
    expect(readChatRoom({ members: null, agent_run: 3, agent_cap: -1 })).toMatchObject({ agentRun: 3, agentCap: -1 });
  });

  it('mirrors the computer: an unset or too-small limit means 16, and -1 is off', () => {
    expect(chatLimitView({ stopped: false, members: [], agentRun: 2 })).toMatchObject({ limit: 16, paused: false, near: false });
    expect(chatLimitView({ stopped: false, members: [], agentRun: 12, agentCap: 0 })).toMatchObject({ limit: 16, near: true });
    expect(chatLimitView({ stopped: false, members: [], agentRun: 16, agentCap: 16 })).toMatchObject({ paused: true });
    expect(chatLimitView({ stopped: false, members: [], agentRun: 99, agentCap: -1 })).toMatchObject({ limit: 'off', paused: false });
    expect(chatLimitView({ stopped: false, members: [] })).toBeUndefined();
  });

  it('one + always resumes a paused room, − never goes under 4, and the phone never turns it off', () => {
    const paused = chatLimitView({ stopped: false, members: [], agentRun: 30, agentCap: 16 })!;
    expect(nextChatLimit(paused, 1)).toBe(34);
    const running = chatLimitView({ stopped: false, members: [], agentRun: 3, agentCap: 16 })!;
    expect(nextChatLimit(running, 1)).toBe(20);
    expect(nextChatLimit(running, -1)).toBe(12);
    const floor = chatLimitView({ stopped: false, members: [], agentRun: 0, agentCap: 4 })!;
    expect(nextChatLimit(floor, -1)).toBeUndefined();
    const off = chatLimitView({ stopped: false, members: [], agentRun: 5, agentCap: -1 })!;
    expect(nextChatLimit(off, 1)).toBeUndefined();
    expect(nextChatLimit(chatLimitView({ stopped: false, members: [], agentRun: 0, agentCap: 1000000 })!, 1)).toBe(1000000);
  });
});
