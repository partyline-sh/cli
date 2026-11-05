import { afterEach, describe, expect, it, vi } from "vitest";
import { PartylineClient } from "../src/runtime/client";
import type {
  InnerMessage,
  PhoneSession,
} from "../src/runtime/protocol/session";
import type { Storage, PairedComputer } from "../src/runtime/storage";
const saved: PairedComputer = {
  machine: "1".repeat(32),
  phone: "2".repeat(32),
  token: "3".repeat(64),
  private: "",
  public: "",
  relay: "https://relay.example",
  name: "Computer",
  pairedAt: 1,
};
class Session {
  static all: Session[] = [];
  sent: InnerMessage[] = [];
  subs: string[] = [];
  constructor(
    _: unknown,
    public receive: (m: InnerMessage) => void,
    public closed: (reason: string) => void,
  ) {
    Session.all.push(this);
  }
  connect() {}
  subscribe(topic: string) {
    this.subs.push(topic);
  }
  unsubscribe() {}
  send(messages: InnerMessage[]) {
    this.sent.push(...messages);
  }
  close() {
    this.closed("Session closed");
  }
}
const fixture = () => {
  let navigation: Record<string, unknown> = {};
  const storage: Storage = {
    loadComputers: async () => [saved],
    saveComputer: async () => {},
    forgetComputer: async () => {},
    loadState: async () => navigation,
    saveState: async (s) => {
      navigation = structuredClone(s);
    },
  };
  const client = new PartylineClient(
    storage,
    Session as unknown as typeof PhoneSession,
  );
  return { client, storage };
};
afterEach(() => {
  vi.useRealTimers();
  Session.all = [];
});
it('keeps paired computers available when saved input state cannot be restored', async () => {
  const { client, storage } = fixture();
  vi.spyOn(storage, 'loadState').mockRejectedValue(new Error('saved input authentication failed'));
  await client.start();
  expect(client.snapshot().computers).toHaveLength(1);
  expect(Session.all).toHaveLength(1);
  expect(client.snapshot().drafts).toEqual({});
  expect(client.snapshot().deliveries).toEqual([]);
  expect(client.snapshot().storageWarning).toContain('No queued messages were sent');
  client.dispose();
});
it('sends agent messages directly to the pane and keeps room chat on its own channel', async () => {
  const { client } = fixture();
  await client.start();
  const wire = Session.all[0];
  wire.receive({ type: 'hello', data: { capabilities: ['input', 'chat_send', 'transcript'] } });
  client.actions.selectSession({ computerId: saved.machine, pane: '%21' });
  expect(wire.subs).toContain('%21');
  expect(wire.subs.some(topic => topic.startsWith('transcript:'))).toBe(false);
  await client.actions.sendText(saved.machine, '%21', 'Direct instruction');
  expect(wire.sent.at(-1)).toMatchObject({ type: 'input', pane: '%21', kind: 'text', data: 'Direct instruction' });
  wire.receive({ type: 'out', pane: '%21', data: 'Actual agent reply' });
  expect(client.snapshot().panes[`${saved.machine}:%21`].terminal?.ansi).toBe('Actual agent reply');
  await client.actions.sendChat(saved.machine, 'Message for the room');
  expect(wire.sent.at(-1)).toMatchObject({ type: 'chat_send', text: 'Message for the room' });
  client.dispose();
});
it('adopts shell agents, preserves drafts across exit, and rejects stale transcript frames', async () => {
  const { client } = fixture();
  await client.start();
  const wire = Session.all[0];
  wire.receive({ type: 'hello', data: { capabilities: ['input', 'transcript', 'detected_agents'] } });
  const inventory = (data: Record<string, string>) => wire.receive({ type: 'sessions', data: [{ pane: '%1', ...data }] });
  inventory({ engine: 'claude', detected: 'true', agentTarget: 'agent:claude:10:10:100' });
  const target = client.snapshot().sessions[0].agentTarget!;
  client.actions.setDraft(saved.machine, '%1', 'agent draft', target);
  wire.receive({ type: 'transcript:%1', data: { target, entries: [{ role: 'assistant', text: 'Current agent reply' }] } });
  const key = `${saved.machine}:%1`;
  expect(client.snapshot().panes[key].transcript?.entries[0].text).toBe('Current agent reply');
  inventory({ detectionUnavailable: 'true' });
  expect(client.snapshot().sessions[0]).toMatchObject({ engine: 'claude', agentTarget: target });
  inventory({ engine: '' });
  expect(client.snapshot().sessions[0].agentTarget).toBeUndefined();
  expect(client.snapshot().panes[key].transcript).toBeUndefined();
  expect(client.snapshot().drafts[`${key}@${target}`]).toBe('agent draft');
  expect(client.snapshot().drafts[key]).toBeUndefined();
  wire.receive({ type: 'transcript:%1', data: { target, entries: [{ role: 'assistant', text: 'Late old reply' }] } });
  expect(client.snapshot().panes[key].transcript).toBeUndefined();
  inventory({ engine: 'codex', detected: 'true', agentTarget: 'agent:codex:20:20:101' });
  wire.receive({ type: 'transcript:%1', data: [{ role: 'assistant', text: 'Unbound history' }] });
  expect(client.snapshot().panes[key].transcript).toBeUndefined();
  client.dispose();
});

it('keeps a captured input target and clears only its draft after the pane changes', async () => {
  const { client } = fixture();
  await client.start();
  const wire = Session.all[0];
  wire.receive({ type: 'hello', data: { capabilities: ['input', 'detected_agents'] } });
  const target = 'agent:claude:10:10:100';
  wire.receive({ type: 'sessions', data: [{ pane: '%1', engine: 'claude', detected: 'true', agentTarget: target }] });
  client.actions.setDraft(saved.machine, '%1', 'original', target);
  const sent = await client.actions.sendText(saved.machine, '%1', 'original', target);
  wire.receive({ type: 'sessions', data: [{ pane: '%1', engine: '' }] });
  client.actions.setDraft(saved.machine, '%1', 'new shell command');
  wire.receive({ type: 'input_result', id: sent.id, status: 'ok' });
  expect(wire.sent.find(m => m.id === sent.id)?.target).toBe(target);
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe('new shell command');
  expect(client.snapshot().drafts[`${saved.machine}:%1@${target}`] ?? '').toBe('');
  client.dispose();
});
it('updates an existing terminal grid when inventory dimensions change without new output', async () => {
  const { client } = fixture();
  await client.start();
  const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['input'] } });
  session.receive({ type: 'out', pane: '%21', data: 'unchanged terminal contents' });
  const inventory = (width: string, height: string) => session.receive({ type: 'sessions', data: [{ pane: '%21', title: 'Agent', state: 'working', width, height }] });
  inventory('52', '26');
  expect(client.snapshot().panes[`${saved.machine}:%21`].terminal).toMatchObject({ columns: 52, rows: 26, ansi: 'unchanged terminal contents' });
  inventory('120', '40');
  expect(client.snapshot().panes[`${saved.machine}:%21`].terminal).toMatchObject({ columns: 120, rows: 40, ansi: 'unchanged terminal contents' });
  client.dispose();
});
it('adds a confirmed attachment to the current draft without sending text or persisting bytes', async () => {
  const { client, storage } = fixture();
  await client.start();
  const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['input', 'uploads'] } });
  client.actions.setDraft(saved.machine, '%21', 'Original draft');
  const promise = client.actions.uploadAttachment(saved.machine, '%21', { name: 'ref.png', mime: 'image/png', bytes: new Uint8Array([1, 2, 3]) }, () => {}, new AbortController().signal);
  await vi.waitFor(() => expect(session.sent).toHaveLength(1));
  const begin = session.sent[0];
  session.receive({ type: 'input_result', id: begin.id, status: 'ok' });
  await vi.waitFor(() => expect(session.sent).toHaveLength(2));
  client.actions.setDraft(saved.machine, '%21', 'Edited during upload');
  session.receive({ type: 'input_result', id: session.sent[1].id, status: 'ok' });
  await vi.waitFor(() => expect(session.sent).toHaveLength(3));
  const uploadId = (begin.data as { uploadId: string }).uploadId;
  session.receive({ type: 'input_result', id: session.sent[2].id, status: 'ok', data: { uploadId, path: '/private/cache/ref.png' } });
  await promise;
  expect(client.snapshot().drafts[`${saved.machine}:%21`]).toBe("Edited during upload\n'/private/cache/ref.png'");
  expect(session.sent.map(m => m.type)).toEqual(['upload_begin', 'upload_chunk', 'upload_finish']);
  expect(client.snapshot().deliveries).toEqual([]);
  expect((await storage.loadState()).pending).toEqual([]);
  client.dispose();
});

it('waits for the native picker reconnect before beginning an upload, and can cancel that wait', async () => {
  const { client } = fixture();
  await client.start();
  client.foreground(false);
  client.foreground(true);
  const session = Session.all[1];
  const abort = new AbortController();
  const promise = client.actions.uploadAttachment(saved.machine, 'chat', { name: 'ref.png', mime: 'image/png', bytes: new Uint8Array([1]) }, () => {}, abort.signal);
  expect(session.sent).toEqual([]);
  abort.abort();
  await expect(promise).rejects.toThrow('Upload cancelled');
  session.receive({ type: 'hello', data: { capabilities: ['uploads'] } });
  expect(session.sent).toEqual([]);
  client.dispose();
});

describe("native lifecycle", () => {
  it("resyncs on resume without resending input and preserves stale output while disconnected", async () => {
    const { client } = fixture();
    await client.start();
    const session = Session.all[0];
    session.receive({
      type: "hello",
      data: { hostname: "Monolith", capabilities: ["input", "transcript"] },
    });
    session.receive({
      type: "board",
      data: [
        { id: "a", section: 0, title: "Older computer", state: "waiting" },
        {
          id: "b",
          section: "In review",
          title: "Newer computer",
          state: "reviewing",
        },
      ],
    });
    expect(client.snapshot().loops.map((card) => card.section)).toEqual([
      "Needs you",
      "In review",
    ]);
    session.receive({
      type: "sessions",
      data: [
        {
          pane: "%1",
          title: "Builder",
          engine: "claude",
          state: "working",
          width: "120",
          height: "40",
        },
      ],
    });
    client.actions.selectSession({ computerId: saved.machine, pane: "%1" });
    session.receive({ type: "out", pane: "%1", data: "screen" });
    expect(Object.values(client.snapshot().panes)[0].terminal?.columns).toBe(
      120,
    );
    await client.actions.sendText(saved.machine, "%1", "one");
    client.foreground(false);
    expect(client.snapshot().deliveries[0].status).toBe("unknown");
    expect(Object.values(client.snapshot().panes)[0].stale).toBe(true);
    client.foreground(true);
    const resumed = Session.all[1];
    resumed.receive({
      type: "hello",
      data: { capabilities: ["input", "transcript"] },
    });
    expect(resumed.sent).toEqual([{ type: "resync", pane: "%1" }]);
    expect(resumed.subs).toContain("%1");
    expect(resumed.subs).not.toContain("transcript:%1");
    client.dispose();
  });
  it("stops retrying when revoked and has one reconnect after a transport failure", async () => {
    vi.useFakeTimers();
    const { client } = fixture();
    await client.start();
    Session.all[0].closed("Session closed");
    await vi.advanceTimersByTimeAsync(1000);
    expect(Session.all).toHaveLength(2);
    Session.all[1].closed("revoked");
    await vi.advanceTimersByTimeAsync(60000);
    expect(Session.all).toHaveLength(2);
    expect(client.snapshot().computers[0].connection).toBe("revoked");
    client.actions.reconnect(saved.machine);
    expect(Session.all).toHaveLength(2);
    client.dispose();
  });
  it("can be started again after effect cleanup without retaining dead transports", async () => {
    const { client } = fixture();
    await client.start();
    client.dispose();
    await client.start();
    expect(Session.all).toHaveLength(2);
    expect(client.snapshot().loading).toBe(false);
    client.dispose();
  });
});

it("preserves scanning state and offers a new code after pairing is interrupted by backgrounding", async () => {
  const { storage } = fixture();
  const pairTransport: typeof import("../src/runtime/pairing").pairComputer = (
    _code,
    _name,
    progress,
    signal,
  ) =>
    new Promise((_resolve, reject) => {
      progress({
        status: "awaiting-approval",
        code: "1234",
        fingerprint: "fixture-phone-key",
      });
      signal.addEventListener("abort", () => reject(new Error("cancelled")), {
        once: true,
      });
    });
  const client = new PartylineClient(
    storage,
    Session as unknown as typeof PhoneSession,
    pairTransport,
  );
  client.actions.startScan();
  client.foreground(false);
  expect(client.snapshot().pairing.status).toBe("scanning");
  client.foreground(true);
  const operation = client.actions.pair("fixture-code");
  expect(client.snapshot().pairing.status).toBe("awaiting-approval");
  client.foreground(false);
  await operation;
  expect(client.snapshot().pairing).toMatchObject({
    status: "error",
    message: expect.stringContaining("Scan a new code"),
  });
  client.foreground(true);
  expect(client.snapshot().pairing.status).toBe("error");
  client.dispose();
});

it("refreshes the session from terminal output without waiting for an unused transcript", async () => {
  const { client } = fixture();
  await client.start();
  const first = Session.all[0];
  first.receive({
    type: "hello",
    data: { capabilities: ["input", "transcript"] },
  });
  first.receive({ type: "out", pane: "%1", data: "old terminal" });
  first.receive({
    type: "transcript:%1",
    revision: 1,
    data: [{ role: "assistant", text: "old reply", tools: [] }],
  });
  client.foreground(false);
  client.foreground(true);
  const next = Session.all[1];
  next.receive({
    type: "hello",
    data: { capabilities: ["input", "transcript"] },
  });
  next.receive({ type: "out", pane: "%1", data: "fresh terminal" });
  expect(Object.values(client.snapshot().panes)[0].stale).toBe(false);
  next.receive({
    type: "transcript:%1",
    revision: 1,
    data: [{ role: "assistant", text: "fresh reply", tools: [] }],
  });
  expect(Object.values(client.snapshot().panes)[0].stale).toBe(false);
  client.dispose();
});

it('learns and persists hosting only from a paired computer reporting its actual route', async () => {
  const { client, storage } = fixture();
  const save = vi.spyOn(storage, 'saveComputer');
  await client.start();
  const session = Session.all[0];
  session.receive({ type: 'hello', data: { relay: { origin: saved.relay, hosting: 'self-hosted' } } });
  expect(client.snapshot().computers[0]).toMatchObject({ relay: saved.relay, relayHosting: 'self-hosted' });
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ relay: saved.relay, relayHosting: 'self-hosted' }));
  session.receive({ type: 'hello', data: { relay: { origin: 'https://different.example', hosting: 'cloudflare' } } });
  expect(client.snapshot().computers[0]).toMatchObject({ relay: saved.relay, relayHosting: 'self-hosted' });
  client.dispose();
});

it('keeps legacy paired computers usable when hosting metadata is absent or invalid', async () => {
  const { client } = fixture();
  await client.start();
  const session = Session.all[0];
  session.receive({ type: 'hello', data: { hostname: 'Old computer' } });
  expect(client.snapshot().computers[0]).toMatchObject({ name: 'Old computer', connection: 'connected', relay: saved.relay });
  expect(client.snapshot().computers[0].relayHosting).toBeUndefined();
  session.receive({ type: 'hello', data: { relay: { origin: saved.relay, hosting: 'invented-provider' } } });
  expect(client.snapshot().computers[0].relayHosting).toBeUndefined();
  client.dispose();
});

it('retains reported loop details and treats malformed or absent optional data as unknown', async () => {
  const { client } = fixture(); await client.start();
  const session = Session.all[0];
  session.receive({ type: 'board', data: [
    { id: 'a', section: 'In review', title: 'Review', state: 'reviewing', batch: 'step 2 of 3', round: 2, openP1: 0, dispute: 'Check retry handling', pr: 42, ci: 'pending', builder: 'codex', chips: [{ Name: 'checks', Mark: 'done' }, { name: 'claude', mark: 'reading' }] },
    { id: 'b', section: 'Done', title: 'Legacy', state: 'done', round: -1, openP1: '0', pr: 1.5, ci: null, builder: {}, chips: [null, { Name: 12 }, { Name: 'reviewer', Mark: {} }] },
  ] });
  expect(client.snapshot().loops[0]).toMatchObject({ batch: 'step 2 of 3', round: 2, openP1: 0, dispute: 'Check retry handling', pr: 42, ci: 'pending', builder: 'codex', chips: [{ name: 'checks', mark: 'done' }, { name: 'claude', mark: 'reading' }], receivedAt: expect.any(Number) });
  expect(client.snapshot().loops[1]).toMatchObject({ round: undefined, openP1: undefined, pr: undefined, ci: '', builder: '', chips: [{ name: 'reviewer', mark: '' }] });
  client.dispose();
});

it('uses advertised capabilities and the persisted queue for shells and loop decisions', async () => {
  const { client, storage } = fixture();
  await client.start();
  const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['input'] } });
  expect((await client.actions.openShell(saved.machine)).status).toBe('not-sent');
  expect(session.sent.some(m => m.type === 'shell_open')).toBe(false);
  await expect(client.actions.readLoop(saved.machine, 'abcdef')).rejects.toThrow('Update');
  session.receive({ type: 'hello', data: { capabilities: ['input', 'shell_open', 'loop_controls'] } });
  const shell = await client.actions.openShell(saved.machine);
  const shellMessage = session.sent.find(m => m.id === shell.id)!;
  expect(shellMessage).toEqual({ type: 'shell_open', id: shell.id });
  session.receive({ type: 'input_result', id: shell.id, status: 'ok', data: { pane: '%77' } });
  expect(client.snapshot().deliveries.find(d => d.id === shell.id)).toMatchObject({ status: 'confirmed', data: { pane: '%77' } });
  expect(session.sent).toContainEqual({ type: 'resync', pane: 'sessions' });
  const read = client.actions.readLoop(saved.machine, 'abcdef');
  const readMessage = session.sent.at(-1)!;
  expect(readMessage.type).toBe('loop_detail');
  session.receive({ type: 'input_result', id: readMessage.id, status: 'ok', data: { id: 'abcdef', brief: 'all of it' } });
  await expect(read).resolves.toMatchObject({ brief: 'all of it' });
  const preparation = client.actions.prepareLoopAction(saved.machine, 'abcdef', 'reject');
  const prepareMessage = session.sent.at(-1)!;
  expect(prepareMessage).toMatchObject({ type: 'loop_prepare', pane: 'abcdef', data: { action: 'reject' } });
  const confirmation = { token: 'token', code: '1234', action: 'reject', revision: 3, detail: { id: 'abcdef' } };
  session.receive({ type: 'input_result', id: prepareMessage.id, status: 'ok', data: confirmation });
  const value = await preparation;
  const decision = await client.actions.confirmLoopAction(saved.machine, 'abcdef', value, '1234', 'smaller scope');
  expect(session.sent.at(-1)).toMatchObject({ type: 'loop_action', id: decision.id, pane: 'abcdef', data: { token: 'token', code: '1234', revision: 3, action: 'reject', note: 'smaller scope' } });
  expect(JSON.stringify(await storage.loadState())).toContain(decision.id);
  session.closed('offline');
  expect(client.snapshot().deliveries.find(d => d.id === decision.id)?.status).toBe('unknown');
  await expect(client.actions.confirmLoopAction(saved.machine, 'abcdef', value, '1234', 'smaller scope')).rejects.toThrow('not confirmed');
  await expect(client.actions.retryDelivery(decision.id, false)).rejects.toThrow('twice');
  client.dispose();
});

it('keeps room-member metadata scoped and clears it on disconnect', async () => {
  const { client } = fixture(); await client.start(); const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['chat_send', 'input_feedback'] } });
  session.receive({ type: 'chat', data: { room: { stopped: false, members: [{ key: 'agent', name: '#12 claude', read: 3, answering: true }] }, messages: [{ seq: 4, name: 'you', from: 'you', text: 'hello', at: '2026-10-31T20:00:00Z', to: ['agent'] }] } });
  expect(client.snapshot().chatRooms?.[saved.machine].members[0]).toMatchObject({ key: 'agent', answering: true, read: 3 });
  expect(client.snapshot().chat[0]).toMatchObject({ seq: 4, to: ['agent'] });
  session.closed('network down'); expect(client.snapshot().chatRooms?.[saved.machine]).toBeUndefined();
  client.dispose();
});
it('post-Enter feedback cannot clear a newly typed draft or resend its text', async () => {
  const { client } = fixture(); await client.start(); const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['input', 'input_feedback'] } });
  client.actions.setDraft(saved.machine, '%1', 'hello');
  const delivery = await client.actions.sendText(saved.machine, '%1', 'hello');
  session.receive({ type: 'input_result', id: delivery.id, status: 'pending' });
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe('');
  client.actions.setDraft(saved.machine, '%1', 'hello');
  session.receive({ type: 'input_result', id: delivery.id, status: 'ok' });
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe('hello');
  client.actions.setDraft(saved.machine, '%1', 'hello');
  session.receive({ type: 'input_delivery', id: delivery.id, status: 'ok', data: { inputDelivery: 'in_composer', retryInputId: delivery.id } });
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe('hello');
  await client.actions.retryEnter(saved.machine, '%1', delivery.id);
  expect(session.sent.at(-1)).toMatchObject({ type: 'input_submit', pane: '%1', data: delivery.id });
  expect(session.sent.filter(m => m.type === 'input')).toHaveLength(1);
  client.dispose();
});

it('clears a queued message before storage or network finishes and durably retains its payload', async () => {
  const { client, storage } = fixture(); await client.start(); const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['input'] } });
  client.actions.setDraft(saved.machine, '%1', 'with attachment\n\'/cache/photo.png\'');
  const original = client.snapshot().drafts[`${saved.machine}:%1`];
  let release!: () => void;
  const blocked = new Promise<void>(resolve => { release = resolve; });
  const save = vi.spyOn(storage, 'saveState').mockImplementation(async () => { await blocked; });
  const sending = client.actions.sendText(saved.machine, '%1', original);
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe('');
  expect(client.snapshot().deliveries.at(-1)).toMatchObject({ status: 'waiting', prompt: { text: original } });
  expect(session.sent.some(m => m.type === 'input')).toBe(false);
  release(); const delivery = await sending;
  expect(save.mock.calls.at(-1)?.[0]).toMatchObject({ drafts: { [`${saved.machine}:%1`]: '' }, pending: [ { draft: original, message: { id: delivery.id, data: original } } ] });
  expect(session.sent.at(-1)).toMatchObject({ id: delivery.id, data: original });
  session.receive({ type: 'input_result', id: delivery.id, status: 'refused', reason: 'Agent exited' });
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe(original);
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(save.mock.calls.at(-1)?.[0]).toMatchObject({ drafts: { [`${saved.machine}:%1`]: original } });
  client.dispose();
});
it.each(['new text', ''])('preserves edits after optimistic send when refused (draft %j)', async edited => {
  const { client } = fixture(); await client.start(); const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['input'] } });
  client.actions.setDraft(saved.machine, '%1', 'original');
  const delivery = await client.actions.sendText(saved.machine, '%1', 'original');
  client.actions.setDraft(saved.machine, '%1', edited);
  session.receive({ type: 'input_result', id: delivery.id, status: 'refused', reason: 'Agent exited' });
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe(edited);
  expect(client.snapshot().deliveries.at(-1)?.prompt?.text).toBe('original');
  client.dispose();
});
it('restores an optimistic draft when local persistence fails without sending anything', async () => {
  const { client, storage } = fixture(); await client.start(); const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['input'] } });
  client.actions.setDraft(saved.machine, '%1', 'keep me');
  vi.spyOn(storage, 'saveState').mockRejectedValue(new Error('Storage unavailable'));
  const sending = client.actions.sendText(saved.machine, '%1', 'keep me');
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe('');
  expect(await sending).toMatchObject({ status: 'not-sent' });
  expect(client.snapshot().drafts[`${saved.machine}:%1`]).toBe('keep me');
  expect(session.sent.some(m => m.type === 'input')).toBe(false);
  client.dispose();
});
it('optimistically clears room chat and preserves unknown delivery without automatic replay', async () => {
  const { client } = fixture(); await client.start(); const session = Session.all[0];
  session.receive({ type: 'hello', data: { capabilities: ['chat_send'] } });
  client.actions.setDraft(saved.machine, 'chat', 'Room message');
  const sending = client.actions.sendChat(saved.machine, 'Room message');
  expect(client.snapshot().drafts[`${saved.machine}:chat`]).toBe('');
  const delivery = await sending;
  session.closed('Network down');
  expect(client.snapshot().deliveries.find(d => d.id === delivery.id)).toMatchObject({ status: 'unknown', prompt: { text: 'Room message' } });
  expect(client.snapshot().drafts[`${saved.machine}:chat`]).toBe('');
  expect(session.sent.filter(m => m.type === 'chat_send')).toHaveLength(1);
  client.dispose();
});

it('requests a complete latest screen on entry and retry; only terminal data completes refresh', async () => {
  vi.useFakeTimers();
  const { client } = fixture(); await client.start(); const wire = Session.all[0];
  wire.receive({ type: 'hello', data: { capabilities: ['input'] } });
  wire.receive({ type: 'out', pane: '%1', data: 'earlier screen' });
  client.actions.selectSession({ computerId: saved.machine, pane: '%1' });
  const key = `${saved.machine}:%1`;
  expect(wire.sent.at(-1)).toEqual({ type: 'resync', pane: '%1' });
  expect(client.snapshot().panes[key]).toMatchObject({ stale: true, terminal: { ansi: 'earlier screen' } });
  wire.receive({ type: 'sessions', data: [{ pane: '%1', engine: 'codex', state: 'idle' }] });
  await vi.advanceTimersByTimeAsync(12001);
  expect(client.snapshot().panes[key].stale).toBe(true);
  expect(client.snapshot().panes[key].refreshError).toContain('taking longer');
  client.actions.refreshSession!(saved.machine, '%1');
  expect(client.snapshot().panes[key].refreshError).toBeUndefined();
  wire.receive({ type: 'out', pane: '%1', data: 'latest complete screen' });
  await vi.advanceTimersByTimeAsync(12001);
  expect(client.snapshot().panes[key]).toMatchObject({ stale: false, refreshError: undefined, terminal: { ansi: 'latest complete screen' } });
  expect(wire.sent.filter(m => m.type === 'input')).toHaveLength(0);
  client.dispose();
});
