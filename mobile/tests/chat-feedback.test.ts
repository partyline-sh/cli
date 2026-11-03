import { expect, it } from 'vitest';
import { readChatRoom } from '../src/runtime/chat-room';
import { memberFeedback } from '../src/ui/chat-feedback';
import type { ChatMessage, ChatRoomState, Session } from '../src/runtime/types';
const member = { key: 'a', name: '#12 claude', engine: 'claude', read: 2, answering: false, sent: false, paused: false };
const room: ChatRoomState = { stopped: false, members: [member] };
const session: Session = { id: 's', key: 'a', computerId: 'mac', pane: '%1', title: 'Agent', engine: 'claude', state: 'working' };
const message = (seq: number, patch: Partial<ChatMessage> = {}): ChatMessage => ({ id: String(seq), computerId: 'mac', name: 'you', own: true, text: 'hello', at: '', seq, ...patch });
it('does not turn a receipt alone into an invented thinking indicator', () => {
  expect(memberFeedback({ ...member, answering: true }, room, [], [], true)).toEqual({ label: 'Received · reply pending', mode: 'waiting' });
  expect(memberFeedback({ ...member, answering: true }, room, [session], [], true)).toEqual({ label: 'Working · reply pending', mode: 'active' });
  expect(memberFeedback(member, room, [], [], true).mode).toBe('idle');
});
it('honors scoped recipients, notes, and read positions', () => {
  const messages = [message(1), message(3, { to: ['other'] }), message(4, { note: true }), message(5, { to: ['a'] })];
  expect(memberFeedback(member, room, [], messages, true).label).toBe('New message waiting to be received');
  expect(memberFeedback({ ...member, read: undefined }, room, [], messages, true).label).toBe('Activity not reported');
});
it('distinguishes paused chat from an agent still working, and refuses stale activity offline', () => {
  expect(memberFeedback(member, { ...room, stopped: true }, [session], [], true)).toEqual({ label: 'Working · chat paused', mode: 'active' });
  expect(memberFeedback({ ...member, paused: true }, room, [], [], true).label).toBe('Chat paused');
  expect(memberFeedback(member, room, [session], [], false).mode).toBe('offline');
});
it('handles missing and malformed room metadata without fabricating members', () => {
  expect(readChatRoom(undefined)).toBeUndefined();
  expect(readChatRoom({ members: null })?.members).toEqual([]);
  expect(readChatRoom({ members: [null, { key: 'a', name: '#12', read: 'invalid', answering: 'yes' }] })?.members).toEqual([{ key: 'a', name: '#12', engine: '', read: undefined, answering: false, sent: false, paused: false }]);
});
