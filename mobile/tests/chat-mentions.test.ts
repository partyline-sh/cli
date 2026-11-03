import { expect, it } from 'vitest';
import { insertMention, mentionOptions, mentionToken } from '../src/ui/chat-mentions';
import type { ChatRoomState } from '../src/runtime/types';

const member = (key: string, name: string, engine: string) => ({ key, name, engine, answering: false, sent: false, paused: false });
const room: ChatRoomState = { stopped: false, members: [member('one', '#12 codex', 'codex'), member('two', '#13 codex', 'codex'), member('three', '#14 claude', 'claude')] };
it('suggests individual IDs and explicitly labels engine-wide mentions', () => {
  expect(mentionOptions(room, 'co')).toEqual([{ token: '@12', label: '#12 codex' }, { token: '@13', label: '#13 codex' }, { token: '@codex', label: '2 codex members in this room' }]);
  expect(mentionOptions(room, '13')).toEqual([{ token: '@13', label: '#13 codex' }]);
  expect(mentionOptions(room, 'all')[0].token).toBe('@all');
});
it('uses only the supplied room and clears missing or removed membership', () => {
  expect(mentionOptions(undefined, '')).toEqual([]);
  expect(mentionOptions({ ...room, members: [] }, '')).toEqual([]);
  expect(mentionOptions({ ...room, members: [room.members[2]] }, 'co')).toEqual([]);
});
it('matches whitespace boundaries and leaves email addresses and selections alone', () => {
  expect(mentionToken('hi@co', { start: 5, end: 5 })).toBeUndefined();
  expect(mentionToken('hello @co', { start: 7, end: 9 })).toBeUndefined();
  expect(mentionToken('hello @co', { start: 9, end: 9 })).toEqual({ start: 6, end: 9, query: 'co' });
  expect(mentionToken('@', { start: 1, end: 1 })).toEqual({ start: 0, end: 1, query: '' });
});
it('replaces the whole mention at the caret while preserving Unicode, punctuation and later text', () => {
  const text = '🌊 hello\n@codex, check this';
  const range = mentionToken(text, { start: 12, end: 12 })!;
  expect(range).toEqual({ start: 9, end: 15, query: 'co' });
  expect(insertMention(text, range, { token: '@13', label: '' })).toEqual({ text: '🌊 hello\n@13, check this', caret: { start: 12, end: 12 } });
});
it('adds a separator at end of draft without duplicating existing whitespace', () => {
  expect(insertMention('@co', { start: 0, end: 3, query: 'co' }, { token: '@13', label: '' }).text).toBe('@13 ');
  expect(insertMention('@co next', { start: 0, end: 3, query: 'co' }, { token: '@13', label: '' }).text).toBe('@13 next');
});
