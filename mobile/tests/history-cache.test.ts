import { afterEach, expect, it, vi } from 'vitest';
import { boundHistory, emptyHistory, historyBytes } from '../src/runtime/history-cache';
import * as cacheModule from '../src/runtime/history-cache';
import { PartylineClient } from '../src/runtime/client';
import type { Storage, PairedComputer } from '../src/runtime/storage';
import type { PhoneSession, InnerMessage } from '../src/runtime/protocol/session';
const id = '1'.repeat(32), now = Date.now();
const saved: PairedComputer = { machine: id, phone: '2'.repeat(32), token: '3'.repeat(64), private: '', public: '', relay: 'https://relay.test', name: 'Monolith', pairedAt: 1 };
const screen = (pane = '%1', ansi = 'saved output', at = now) => ({ session: { id: id + ':' + pane, computerId: id, pane, title: 'Project', engine: 'codex', state: 'idle', key: 'run-1' }, terminal: { ansi, columns: 80, rows: 24, receivedAt: at } });
const history = () => boundHistory({ version: 1, screens: [screen()], chat: { receivedAt: now, messages: [{ id: 'old', computerId: id, name: 'claude', text: 'saved message', at: new Date(now).toISOString(), own: false }] } }, id);
class Session {
    static all: Session[] = [];
    sent: InnerMessage[] = [];
    constructor(_: unknown, public receive: (m: InnerMessage) => void, public closed: (s: string) => void) { Session.all.push(this); }
    connect() { }
    subscribe() { }
    unsubscribe() { }
    send(m: InnerMessage[]) { this.sent.push(...m); }
    close() { }
}
const clients: PartylineClient[] = [];
function fixture() { let value = history(); const storage: Storage = { loadComputers: async () => [saved], saveComputer: async () => { }, forgetComputer: async () => { value = emptyHistory(); }, loadState: async () => ({}), saveState: async () => { }, loadHistory: async () => value, saveHistory: vi.fn(async (_, v) => { value = structuredClone(v); }) }; const client = new PartylineClient(storage, Session as unknown as typeof PhoneSession, async () => saved); clients.push(client); return { client, storage, cache: () => value }; }
const connect = (s: Session) => s.receive({ type: 'hello', data: { capabilities: ['sessions', 'out', 'chat', 'input', 'chat_send', 'session_manager'], hostname: 'Monolith' } });
const inventory = (s: Session, key = 'run-1') => s.receive({ type: 'sessions', data: [{ pane: '%1', key, title: 'Project', engine: 'codex', width: '80', height: '24', state: 'idle' }] });
afterEach(() => { clients.splice(0).forEach(c => c.dispose()); vi.useRealTimers(); Session.all = []; });
it('restores before connecting, then replaces the screen and chat completely', async () => {
    const { client } = fixture();
    await client.start();
    expect(client.snapshot().panes[id + ':%1']).toMatchObject({ cached: true, stale: true, terminal: { ansi: 'saved output' } });
    expect(client.snapshot().sessions[0].cached).toBe(true);
    expect(client.snapshot().chatSync?.[id]).toMatchObject({ stale: true, cached: true });
    const s = Session.all[0];
    connect(s);
    inventory(s);
    expect(client.snapshot().panes[id + ':%1'].cached).toBe(true);
    s.receive({ type: 'out', pane: '%1', data: 'new screen' });
    expect(client.snapshot().panes[id + ':%1']).toMatchObject({ cached: false, stale: false, terminal: { ansi: 'new screen' } });
    s.receive({ type: 'chat', data: { messages: [] } });
    expect(client.snapshot().chat).toEqual([]);
    expect(client.snapshot().chatSync?.[id]).toMatchObject({ stale: false });
});
it('keeps drafts and refuses cached input until inventory and a fresh frame arrive', async () => {
    const { client } = fixture();
    await client.start();
    const s = Session.all[0];
    connect(s);
    client.actions.setDraft(id, '%1', 'my draft');
    const refused = await client.actions.sendText(id, '%1', 'my draft');
    expect(refused.status).toBe('not-sent');
    expect(client.snapshot().drafts[id + ':%1']).toBe('my draft');
    expect(s.sent.some(m => m.type === 'input')).toBe(false);
    inventory(s);
    s.receive({ type: 'out', pane: '%1', data: 'fresh' });
    await client.actions.sendText(id, '%1', 'my draft');
    expect(s.sent.some(m => m.type === 'input')).toBe(true);
});
it('drops a replaced target and removes closed sessions from disk', async () => {
    const { client, cache } = fixture();
    await client.start();
    const s = Session.all[0];
    connect(s);
    inventory(s, 'replacement');
    expect(client.snapshot().panes[id + ':%1']?.terminal).toBeUndefined();
    expect(cache().screens).toEqual([]);
    s.receive({ type: 'out', pane: '%1', data: 'replacement output' });
    s.receive({ type: 'sessions', data: [] });
    expect(cache().screens).toEqual([]);
    expect(client.snapshot().panes[id + ':%1']).toBeUndefined();
});
it('coalesces writes and never resurrects a forgotten cache from a timer', async () => {
    vi.useFakeTimers();
    const { client, storage, cache } = fixture();
    await client.start();
    const s = Session.all[0];
    connect(s);
    inventory(s);
    for (let i = 0; i < 10; i++)
        s.receive({ type: 'out', pane: '%1', data: 'frame ' + i });
    expect(storage.saveHistory).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1000);
    expect(storage.saveHistory).toHaveBeenCalledTimes(1);
    expect(cache().screens[0].terminal.ansi).toBe('frame 9');
    s.receive({ type: 'out', pane: '%1', data: 'unsaved' });
    await client.actions.forgetComputer(id);
    await vi.advanceTimersByTimeAsync(2000);
    expect(cache().screens).toEqual([]);
    expect(storage.saveHistory).toHaveBeenCalledTimes(1);
});
it('continues connecting when saved cache is corrupt', async () => { const { client, storage } = fixture(); storage.loadHistory = async () => { throw Error('corrupt ciphertext'); }; await client.start(); expect(Session.all).toHaveLength(1); expect(client.snapshot().loading).toBe(false); expect(client.snapshot().storageWarning).toContain('recent history'); });
it('caps storage, rejects foreign entries and never truncates ANSI', () => {
    const many = Array.from({ length: 30 }, (_, i) => screen('%' + i, 'x'.repeat(100000), now - i));
    const bounded = boundHistory({ version: 1, screens: [screen('%90', 'x'.repeat(200000)), ...many, { ...screen('%99'), session: { ...screen().session, computerId: 'another' } }] }, id);
    expect(new TextEncoder().encode(JSON.stringify(bounded)).length).toBeLessThanOrEqual(historyBytes);
    expect(bounded.screens.length).toBeLessThanOrEqual(12);
    expect(bounded.screens.some(s => ['%90', '%99'].includes(s.session.pane))).toBe(false);
    expect(bounded.screens[0].session.pane).toBe('%0');
    expect(bounded.screens.every(s => s.terminal.ansi.length === 100000)).toBe(true);
});
it('keeps only one cache write in flight and writes the latest state after a slow write', async () => {
    vi.useFakeTimers();
    const { client, storage } = fixture();
    let release!: () => void;
    vi.mocked(storage.saveHistory!).mockImplementationOnce(() => new Promise<void>(resolve => { release = resolve; }));
    await client.start();
    const s = Session.all[0];
    connect(s);
    inventory(s);
    s.receive({ type: 'out', pane: '%1', data: 'first' });
    await vi.advanceTimersByTimeAsync(1000);
    for (let i = 0; i < 3; i++) {
        s.receive({ type: 'out', pane: '%1', data: 'latest ' + i });
        await vi.advanceTimersByTimeAsync(1000);
    }
    expect(storage.saveHistory).toHaveBeenCalledTimes(1);
    release();
    await vi.advanceTimersByTimeAsync(0);
    expect(storage.saveHistory).toHaveBeenCalledTimes(2);
    expect(vi.mocked(storage.saveHistory!).mock.calls[1][1].screens[0].terminal.ansi).toBe('latest 2');
});
it('keeps in-memory terminal ownership across a transient reconnect', async () => {
    const { client } = fixture(); await client.start();
    const first = Session.all[0]; connect(first); inventory(first);
    first.receive({ type: 'out', pane: '%1', data: 'before disconnect' });
    client.foreground(false);
    expect(client.snapshot().sessions[0].cached).toBeFalsy();
    expect(client.snapshot().panes[id + ':%1']).toMatchObject({ cached: false, stale: true });
    client.foreground(true);
    const second = Session.all[1]; connect(second); inventory(second);
    second.receive({ type: 'out', pane: '%1', data: 'current' });
    expect(client.snapshot().panes[id + ':%1']).toMatchObject({ cached: false, terminal: { ansi: 'current' } });
});
it('waits for current room content before sending from a saved chat', async () => {
    const { client } = fixture();
    await client.start();
    const s = Session.all[0];
    connect(s);
    expect((await client.actions.sendChat(id, 'hello')).status).toBe('not-sent');
    expect(s.sent.some(m => m.type === 'chat_send')).toBe(false);
    s.receive({ type: 'chat', data: { messages: [] } });
    await client.actions.sendChat(id, 'hello');
    expect(s.sent.some(m => m.type === 'chat_send')).toBe(true);
});
it('starts caching again when the same computer is paired after being forgotten', async () => {
    vi.useFakeTimers();
    const { client, storage } = fixture();
    await client.start();
    await client.actions.forgetComputer(id);
    await client.actions.pair('test pairing transport');
    const current = Session.all.at(-1)!;
    connect(current);
    inventory(current);
    current.receive({ type: 'out', pane: '%1', data: 'new pairing content' });
    await vi.advanceTimersByTimeAsync(1000);
    expect(storage.saveHistory).toHaveBeenCalled();
    expect(vi.mocked(storage.saveHistory!).mock.calls.at(-1)![1].screens[0].terminal.ansi).toBe('new pairing content');
});

it('releases a restored shell after fresh inventory and output even if detection stays unavailable', async () => {
    const { client, storage } = fixture();
    const savedHistory = history(); savedHistory.screens[0].session.engine = '';
    storage.loadHistory = async () => savedHistory;
    await client.start(); const s = Session.all[0]; connect(s);
    const update = () => s.receive({ type: 'sessions', data: [{ pane: '%1', engine: '', key: 'run-1', title: 'Shell', detectionUnavailable: true }] });
    update();
    expect(client.snapshot().panes[id + ':%1'].cached).toBe(true);
    const resyncs = s.sent.filter(m => m.type === 'resync').length;
    update(); expect(s.sent.filter(m => m.type === 'resync')).toHaveLength(resyncs);
    s.receive({ type: 'out', pane: '%1', data: 'fresh shell prompt' });
    expect(client.snapshot().panes[id + ':%1']).toMatchObject({ cached: false, terminal: { ansi: 'fresh shell prompt' } });
    expect((await client.actions.sendKey(id, '%1', 'Tab')).status).toBe('waiting');
});

it('does no history bounding while frames arrive and bounds once in the throttled writer', async () => {
    vi.useFakeTimers(); const spy = vi.spyOn(cacheModule, 'boundHistory');
    try {
        const { client } = fixture(); await client.start(); const s = Session.all[0]; connect(s); inventory(s);
        spy.mockClear();
        for (let i = 0; i < 100; i++) s.receive({ type: 'out', pane: '%1', data: 'frame ' + i });
        expect(spy).not.toHaveBeenCalled();
        await vi.advanceTimersByTimeAsync(1000); expect(spy).toHaveBeenCalledTimes(1);
    } finally { spy.mockRestore(); }
});

it('prunes a previously live pane when the fresh inventory says it is closed', async () => {
    const { client, cache } = fixture(); await client.start(); const s = Session.all[0]; connect(s); inventory(s);
    s.receive({ type: 'out', pane: '%1', data: 'live screen' });
    s.receive({ type: 'sessions', data: [] });
    expect(client.snapshot().panes[id + ':%1']).toBeUndefined(); expect(cache().screens).toEqual([]);
});

it('permits confirmed closing after fresh inventory without waiting for the cached screen', async () => {
  const { client } = fixture(); await client.start(); const s = Session.all[0]; connect(s);
  expect((await client.actions.closeSavedSession!(id, '%1', 'run-1')).status).toBe('not-sent');
  inventory(s);
  expect(client.snapshot().panes[id + ':%1'].cached).toBe(true);
  expect((await client.actions.closeSavedSession!(id, '%1', 'run-1')).status).toBe('waiting');
  expect(s.sent.some(m => m.type === 'session_close')).toBe(true);
  expect((await client.actions.sendKey(id, '%1', 'Tab')).status).toBe('not-sent');
});
