import type { ChatRoomState } from '../runtime/types';

/** Mirrors chatRoom.cap() on the computer: 0 or a value under 4 means the default of 16. */
export const DEFAULT_CHAT_LIMIT = 16;
const STEP = 4;
const MIN = 4;
const MAX = 1000000;

export interface ChatLimitView {
  run: number;
  limit: number | 'off';
  /** The limit is reached: delivery waits until you speak or allow more replies. */
  paused: boolean;
  /** You stopped the room with /stop. Raising the limit does not undo that. */
  stopped: boolean;
  /** At least three quarters of the limit is used. */
  near: boolean;
}

export function chatLimitView(room: ChatRoomState | undefined): ChatLimitView | undefined {
  if (!room || room.agentRun === undefined) return;
  const run = room.agentRun;
  const stopped = room.stopped;
  if (room.agentCap === -1) return { run, limit: 'off', paused: false, stopped, near: false };
  const limit = room.agentCap !== undefined && room.agentCap >= MIN ? room.agentCap : DEFAULT_CHAT_LIMIT;
  return { run, limit, paused: run >= limit, stopped, near: run >= limit - Math.floor(limit / 4) };
}

/**
 * The limit after pressing − or +. Raising a paused room allows STEP more replies from where the
 * agents are now, so one press always resumes delivery; lowering never goes under MIN. The phone
 * never turns the limit off.
 */
export function nextChatLimit(view: ChatLimitView, direction: 1 | -1): number | undefined {
  if (view.limit === 'off') return;
  if (direction > 0) return Math.min(MAX, Math.max(view.limit, view.run) + STEP);
  const lower = Math.max(MIN, view.limit - STEP);
  return lower === view.limit ? undefined : lower;
}

/** The meter's line: a manual /stop is not a limit pause, and + does not lift it. */
export function chatLimitLabel(view: ChatLimitView): string {
  if (view.stopped) return `Stopped with /stop · say anything or /go on the computer to resume`;
  if (view.limit === 'off') return `Agents ${view.run} since you spoke · limit off`;
  if (view.paused) return `Paused at ${view.limit} agent replies · say anything or press +`;
  return `Agents ${view.run}/${view.limit} since you spoke`;
}
