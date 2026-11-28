import type { ChatMessage, ChatRoomState, Session } from '../runtime/types';
import type { SignalMode } from './Signal';
export function memberFeedback(member: ChatRoomState['members'][number], room: ChatRoomState, sessions: readonly Session[], messages: readonly ChatMessage[], connected: boolean): { label: string; mode: SignalMode } {
  if (!connected) return { label: 'Offline · status unavailable', mode: 'offline' };
  const session = sessions.find(s => s.key === member.key);
  const paused = room.stopped || member.paused;
  if (session?.state === 'waiting') return { label: paused ? 'Needs your input · chat paused' : 'Needs your input', mode: 'waiting' };
  if (session?.state === 'working') return { label: paused ? 'Working · chat paused' : member.answering && !member.sent ? 'Working · reply pending' : 'Working', mode: 'active' };
  if (paused) return { label: 'Chat paused', mode: 'waiting' };
  if (member.answering && !member.sent) return { label: 'Received · reply pending', mode: 'waiting' };
  const read = member.read;
  const unread = messages.filter(m => read !== undefined && !m.note && (m.seq ?? 0) > read && (!m.to?.length || m.to.includes(member.key)));
  if (unread.length) return { label: `New message${unread.length === 1 ? '' : 's'} waiting to be received`, mode: 'waiting' };
  if (member.sent) return { label: 'Reply posted', mode: 'confirmed' };
  return { label: session ? 'Idle' : 'Activity not reported', mode: 'idle' };
}
