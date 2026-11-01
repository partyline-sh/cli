import type { ChatRoomState } from '../runtime/types';

export interface Caret { start: number; end: number }
export interface MentionToken { start: number; end: number; query: string }
export interface Mention { token: string; label: string }

/** Match the computer's whitespace-delimited routing tokens, using native UTF-16 offsets. */
export function mentionToken(text: string, caret: Caret): MentionToken | undefined {
  if (caret.start !== caret.end || caret.start < 0 || caret.end > text.length) return;
  const prefix = text.slice(0, caret.start);
  const match = /(?:^|\s)(@[\w-]*)$/.exec(prefix);
  if (!match) return;
  const start = caret.start - match[1].length;
  const suffix = /^[\w-]*/.exec(text.slice(caret.start))![0];
  return { start, end: caret.start + suffix.length, query: match[1].slice(1).toLowerCase() };
}

export function mentionOptions(room: ChatRoomState | undefined, query: string): Mention[] {
  if (!room?.members.length) return [];
  const options: Mention[] = [{ token: '@all', label: `Everyone in this room (${room.members.length})` }];
  const engines = new Map<string, number>();
  for (const member of room.members) {
    const id = member.name.trim().split(/\s/)[0].replace(/^#/, '');
    if (/^[\w-]+$/.test(id)) options.push({ token: `@${id}`, label: member.name });
    if (/^[\w-]+$/.test(member.engine)) engines.set(member.engine, (engines.get(member.engine) ?? 0) + 1);
  }
  for (const [engine, count] of engines) options.push({ token: `@${engine}`, label: `${count} ${engine} ${count === 1 ? 'member' : 'members'} in this room` });
  const seen = new Set<string>();
  return options.filter(option => {
    if (seen.has(option.token)) return false;
    seen.add(option.token);
    return option.token.slice(1).toLowerCase().startsWith(query) || option.label.toLowerCase().includes(query);
  }).slice(0, 80);
}

export function insertMention(text: string, range: MentionToken, option: Mention): { text: string; caret: Caret } {
  const suffix = text.slice(range.end);
  const space = /^[\s,.:;!?\])}"'`]/.test(suffix) ? '' : ' ';
  const value = text.slice(0, range.start) + option.token + space + suffix;
  const end = range.start + option.token.length + (space ? 1 : 0);
  return { text: value, caret: { start: end, end } };
}
