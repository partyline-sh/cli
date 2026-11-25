import type { ChatRoomState } from './types';
/** Optional room metadata is already supplied by the computer's encrypted chat snapshot. */
export function readChatRoom(value: unknown): ChatRoomState | undefined {
  if (!value || typeof value !== 'object') return;
  const room = value as Record<string, unknown>;
  const count = (v: unknown, min: number) => Number.isSafeInteger(v) && Number(v) >= min ? Number(v) : undefined;
  const limits = { agentRun: count(room.agent_run, 0), agentCap: count(room.agent_cap, -1) };
  if (room.members === null) return { stopped: room.stopped === true, ...limits, members: [] };
  if (!Array.isArray(room.members)) return;
  return {
    stopped: room.stopped === true,
    ...limits,
    members: room.members.flatMap(value => {
      if (!value || typeof value !== 'object') return [];
      const member = value as Record<string, unknown>;
      if (typeof member.key !== 'string' || !member.key || typeof member.name !== 'string') return [];
      return [{ key: member.key, name: member.name, engine: typeof member.engine === 'string' ? member.engine : '',
        read: Number.isSafeInteger(member.read) && Number(member.read) >= 0 ? Number(member.read) : undefined,
        answering: member.answering === true, sent: member.sent === true, paused: member.paused === true }];
    }),
  };
}
