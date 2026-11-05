import type { ChatMessage, Session } from './types';
export interface CachedScreen {
    session: Session;
    terminal: {
        ansi: string;
        columns: number;
        rows: number;
        receivedAt: number;
    };
}
export interface ComputerHistory {
    version: 1;
    screens: CachedScreen[];
    chat?: {
        messages: ChatMessage[];
        receivedAt: number;
    };
}
export const emptyHistory = (): ComputerHistory => ({ version: 1, screens: [] });
export const historyBytes = 1024 * 1024; // Per paired computer, before encryption.
const itemBytes = 128 * 1024;
const bytes = (value: unknown) => new TextEncoder().encode(JSON.stringify(value)).length;
const obj = (v: unknown): Record<string, unknown> => v && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : {};
const time = (v: unknown): v is number => typeof v === 'number' && Number.isSafeInteger(v) && v > 0 && v <= Date.now() + 60000;
/** Only display data crosses this boundary. Never restore capabilities or input authority. */
export function boundHistory(raw: unknown, computer: string): ComputerHistory {
    const value = obj(raw), result = emptyHistory();
    if (value.version !== 1)
        return result;
    const seen = new Set<string>();
    for (const entry of (Array.isArray(value.screens) ? value.screens : []).slice(0, 100)) {
        const r = obj(entry), s = obj(r.session), t = obj(r.terminal);
        if (s.computerId !== computer || typeof s.pane !== 'string' || !/^%\d+$/.test(s.pane) || seen.has(s.pane) ||
            typeof s.title !== 'string' || s.title.length > 1024 || typeof s.engine !== 'string' || s.engine.length > 128 ||
            typeof t.ansi !== 'string' || t.ansi.length > itemBytes || !time(t.receivedAt) ||
            !Number.isInteger(t.columns) || Number(t.columns) < 2 || Number(t.columns) > 1000 ||
            !Number.isInteger(t.rows) || Number(t.rows) < 1 || Number(t.rows) > 500)
            continue;
        const session: Session = { id: `${computer}:${s.pane}`, computerId: computer, pane: s.pane, title: s.title, engine: s.engine,
            state: 'idle', cached: true, updatedAt: t.receivedAt,
            ...Object.fromEntries(['key', 'agentTarget', 'project'].flatMap(k => typeof s[k] === 'string' && s[k].length <= 4096 ? [[k, s[k]]] : [])) };
        const screen: CachedScreen = { session, terminal: { ansi: t.ansi, columns: Number(t.columns), rows: Number(t.rows), receivedAt: t.receivedAt } };
        if (bytes(screen) > itemBytes)
            continue; // Do not cut ANSI sequences in half.
        seen.add(s.pane);
        result.screens.push(screen);
    }
    result.screens.sort((a, b) => b.terminal.receivedAt - a.terminal.receivedAt);
    result.screens = result.screens.slice(0, 12);
    const chat = obj(value.chat);
    if (Array.isArray(chat.messages) && time(chat.receivedAt)) {
        const messages: ChatMessage[] = [];
        let size = 0;
        for (const entry of chat.messages.slice(-100).reverse()) {
            const m = obj(entry);
            if (m.computerId !== computer || typeof m.id !== 'string' || m.id.length > 1024 || typeof m.name !== 'string' || m.name.length > 1024 ||
                typeof m.text !== 'string' || m.text.length > itemBytes || typeof m.at !== 'string' || !Number.isFinite(Date.parse(m.at)))
                continue;
            const message: ChatMessage = { id: m.id, computerId: computer, name: m.name, text: m.text, at: m.at, own: m.own === true };
            if (size + bytes(message) > itemBytes)
                break;
            size += bytes(message);
            messages.unshift(message);
        }
        result.chat = { messages, receivedAt: chat.receivedAt };
    }
    while (bytes(result) > historyBytes && result.screens.length)
        result.screens.pop();
    return result;
}
