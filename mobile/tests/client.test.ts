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
    expect(resumed.sent).toEqual([]);
    expect(resumed.subs).toContain("%1");
    expect(resumed.subs).toContain("transcript:%1");
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

it("keeps an old conversation stale when only its terminal has refreshed after reconnect", async () => {
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
  expect(Object.values(client.snapshot().panes)[0].stale).toBe(true);
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
