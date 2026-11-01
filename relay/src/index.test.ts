import { env, SELF, runInDurableObject } from "cloudflare:test";
import { afterEach, beforeEach, describe, it, expect } from "vitest";
import { Relay, frame, hash, type Env } from "./index";
import { HandshakeState } from "./noise";
import { x25519 } from "@noble/curves/ed25519";
declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}
const id = "11".repeat(16),
  token = "22".repeat(32);
let relay: DurableObjectStub;
const stub = () => relay;
const sockets = new Set<WebSocket>();
beforeEach(() => {
  // Hibernation sockets require shared storage mode. Give each test its own
  // object instead of resetting the singleton's live storage and alarm state.
  relay = env.RELAY.get(env.RELAY.newUniqueId());
});
afterEach(async () => {
  await Promise.all(
    [...sockets].map(async (ws) => {
      if (ws.readyState === WebSocket.CLOSED) return;
      const closed = new Promise<void>((resolve) =>
        ws.addEventListener("close", () => resolve(), { once: true }),
      );
      if (ws.readyState === WebSocket.OPEN) ws.close();
      await closed;
    }),
  );
  sockets.clear();
});
async function registered() {
  const digest = await hash(token);
  await runInDurableObject(stub(), (_obj, state) => {
    state.storage.sql.exec(
      "INSERT OR REPLACE INTO machines (id, hash) VALUES (?, ?)",
      id,
      digest,
    );
  });
}
async function post(path: string, data: object) {
  return stub().fetch("https://relay.test" + path, {
    method: "POST",
    body: JSON.stringify(data),
  });
}
async function connect() {
  const response = await stub().fetch("https://relay.test/connect", {
    headers: { Upgrade: "websocket" },
  });
  expect(response.status).toBe(101);
  const ws = response.webSocket!;
  // Workers sockets require an explicit reply to a server-initiated close.
  // Remove it when the event arrives so teardown never waits for that event twice.
  ws.addEventListener(
    "close",
    () => {
      if (ws.readyState !== WebSocket.CLOSED) ws.close();
      sockets.delete(ws);
    },
    { once: true },
  );
  ws.accept();
  sockets.add(ws);
  return ws;
}
function message(ws: WebSocket): Promise<MessageEvent> {
  return new Promise((resolve) =>
    ws.addEventListener("message", resolve, { once: true }),
  );
}
describe("relay registry", () => {
  it("bootstraps the deploying machine and preserves its hash on redeploy", async () => {
    const digest = await hash(token);
    await runInDurableObject(stub(), (_obj, state) => {
      new Relay(state, { ...env, INITIAL_MACHINE: id, INITIAL_HASH: digest });
      new Relay(state, {
        ...env,
        INITIAL_MACHINE: id,
        INITIAL_HASH: "ff".repeat(32),
      });
      expect(
        state.storage.sql
          .exec<{ hash: string }>("SELECT hash FROM machines WHERE id = ?", id)
          .one().hash,
      ).toBe(digest);
    });
    const ws = await connect();
    const reply = message(ws);
    ws.send(frame(id, { role: "machine", token, min: 1, max: 1 }));
    expect(
      JSON.parse(
        new TextDecoder().decode(
          new Uint8Array((await reply).data as ArrayBuffer).slice(17),
        ),
      ).ok,
    ).toBe(true);
  });
  it("does not write incoming counts for rejected hellos", async () => {
    const ws = await connect();
    const closed = new Promise((resolve) =>
      ws.addEventListener("close", resolve, { once: true }),
    );
    ws.send(frame(id, { role: "machine", token, min: 1, max: 1 }));
    await closed;
    await runInDurableObject(stub(), (_obj, state) => {
      expect(
        state.storage.sql.exec("SELECT * FROM incoming").toArray(),
      ).toEqual([]);
    });
  });
  it("keeps accepted counts in hibernation attachments until the five-minute alarm", async () => {
    await registered();
    const ws = await connect();
    const reply = message(ws);
    ws.send(frame(id, { role: "machine", token, min: 1, max: 1 }));
    await reply;
    await runInDurableObject(stub(), async (obj, state) => {
      expect(
        state.storage.sql.exec("SELECT * FROM incoming").toArray(),
      ).toEqual([]);
      const socket = state
        .getWebSockets()
        .find((s) => s.deserializeAttachment()?.id === id)!;
      const attachment = socket.deserializeAttachment();
      expect(attachment.flushAt - Date.now()).toBeGreaterThan(290_000);
      attachment.flushAt = Date.now();
      socket.serializeAttachment(attachment);
      // Reconstruct the object to discard all instance fields, as hibernation does.
      const awakened = new Relay(state, env);
      await awakened.alarm();
      expect(
        state.storage.sql
          .exec<{ count: number }>("SELECT count FROM incoming")
          .one().count,
      ).toBe(1);
      expect(socket.deserializeAttachment().pending).toBeUndefined();
    });
  });
  it("routes Worker requests to the relay singleton", async () => {
    relay = env.RELAY.get(env.RELAY.idFromName("relay"));
    await registered();
    const response = await SELF.fetch("https://relay.test/status", {
      method: "POST",
      body: JSON.stringify({ id, token }),
    });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ connected: false });
  });
  it("refuses an unregistered hello", async () => {
    const ws = await connect();
    const closed = new Promise<CloseEvent>((resolve) =>
      ws.addEventListener("close", resolve, { once: true }),
    );
    ws.send(frame(id, { role: "machine", token, min: 1, max: 1 }));
    expect((await closed).code).toBe(1008);
  });
  it("attaches a registered hello with hibernation and auto responds to keepalive", async () => {
    await registered();
    const ws = await connect();
    const reply = message(ws);
    ws.send(frame(id, { role: "machine", token, min: 1, max: 1 }));
    const bytes = new Uint8Array((await reply).data as ArrayBuffer);
    expect(bytes[0]).toBe(1);
    expect(JSON.parse(new TextDecoder().decode(bytes.slice(17)))).toEqual({
      ok: true,
      version: 1,
    });
    await runInDurableObject(stub(), (_obj, state) => {
      // Only acceptWebSocket populates this hibernation registry.
      expect(
        state.getWebSockets().some((s) => s.deserializeAttachment()?.id === id),
      ).toBe(true);
      expect(state.getWebSocketAutoResponse()?.request).toBe("ping");
    });
    expect(await (await post("/status", { id, token })).json()).toEqual({
      connected: true,
    });
    const pong = message(ws);
    ws.send("ping");
    expect((await pong).data).toBe("pong");
    const closed = new Promise((resolve) =>
      ws.addEventListener("close", resolve, { once: true }),
    );
    ws.close();
    await closed;
    await runInDurableObject(stub(), (_obj, state) => {
      expect(
        state.storage.sql
          .exec<{ count: number }>("SELECT count FROM incoming")
          .one().count,
      ).toBe(1);
    });
  });
  it("requires credentials for invites and status", async () => {
    expect((await post("/invite", { id, token })).status).toBe(403);
    expect((await post("/status", { id, token })).status).toBe(403);
  });
  it("redeems invites once, stores hashes and expires invites", async () => {
    await registered();
    const { invite, expires } = (await (
      await post("/invite", { id, token })
    ).json()) as { invite: string; expires: number };
    expect(expires - Date.now()).toBeGreaterThan(590_000);
    expect(expires - Date.now()).toBeLessThanOrEqual(600_000);
    const joined = await post("/join", { id: "33".repeat(16), invite });
    expect(joined.status).toBe(200);
    const credentials = (await joined.json()) as { token: string };
    expect(credentials.token).toHaveLength(64);
    expect((await post("/join", { id: "44".repeat(16), invite })).status).toBe(
      403,
    );
    const expectedHash = await hash(credentials.token);
    await runInDurableObject(stub(), (_obj, state) => {
      const row = state.storage.sql
        .exec<{
          hash: string;
        }>("SELECT hash FROM machines WHERE id = ?", "33".repeat(16))
        .one();
      expect(row.hash).toBe(expectedHash);
    });
    const expired = (await (await post("/invite", { id, token })).json()) as {
      invite: string;
    };
    await runInDurableObject(stub(), (_obj, state) => {
      state.storage.sql.exec("UPDATE invites SET expires = 0");
    });
    expect(
      (await post("/join", { id: "55".repeat(16), invite: expired.invite }))
        .status,
    ).toBe(403);
  });
});

describe("pairing transport", () => {
  async function offer() {
    await registered();
    const invite = "ab".repeat(32),
      route = "cd".repeat(16);
    const response = await post("/device-invite", {
      id,
      token,
      hash: await hash(invite),
      route,
    });
    expect(response.status).toBe(200);
    return { invite, route };
  }
  async function hello(
    ws: WebSocket,
    route: string,
    role: string,
    credential: string,
  ) {
    const reply = message(ws);
    ws.send(frame(route, { role, token: credential, min: 1, max: 1 }));
    await reply;
  }
  function data(route: string, body: Uint8Array) {
    const b = frame(route, {});
    const out = new Uint8Array(17 + body.length);
    out.set(b.slice(0, 17));
    out[0] = 2;
    out.set(body, 17);
    return out;
  }
  function closed(ws: WebSocket) {
    return new Promise<CloseEvent>((resolve) =>
      ws.addEventListener("close", resolve, { once: true }),
    );
  }
  it("registers only a hash, consumes once, restricts routes and forwards opaque bytes across hibernation", async () => {
    const { invite, route } = await offer();
    const machine = await connect();
    await hello(machine, id, "machine", token);
    const phone = await connect();
    await hello(phone, route, "invite", invite);
    // The independent PSK is never provided to any relay API or frame.
    const psk = "ef".repeat(32);
    const machineKey = crypto.getRandomValues(new Uint8Array(32));
    const pskBytes = new Uint8Array(32).fill(0xef);
    const initiator = new HandshakeState({
      pattern: "IKpsk2",
      initiator: true,
      staticPrivate: crypto.getRandomValues(new Uint8Array(32)),
      peerStatic: x25519.getPublicKey(machineKey),
      psk: pskBytes,
    });
    const responder = new HandshakeState({
      pattern: "IKpsk2",
      initiator: false,
      staticPrivate: machineKey,
      psk: pskBytes,
    });
    await runInDurableObject(stub(), (_obj, state) => {
      expect(
        state.storage.sql.exec("SELECT * FROM device_invites").toArray(),
      ).toEqual([]);
      expect(
        JSON.stringify(
          state.getWebSockets().map((s) => s.deserializeAttachment()),
        ),
      ).not.toContain(psk);
      new Relay(state, env);
    });
    const opaque = new Uint8Array([0xff, 0, 0x80, 0x7b, 0x22, 0, 2]);
    let reply = message(machine);
    phone.send(data(route, opaque));
    expect(new Uint8Array((await reply).data as ArrayBuffer)).toEqual(
      data(route, opaque),
    );
    reply = message(phone);
    machine.send(data(route, opaque));
    expect(new Uint8Array((await reply).data as ArrayBuffer)).toEqual(
      data(route, opaque),
    );
    reply = message(machine);
    phone.send(data(route, initiator.writeMessage()));
    const first = new Uint8Array((await reply).data as ArrayBuffer).slice(17);
    expect(
      Array.from(first, (b) => b.toString(16).padStart(2, "0")).join(""),
    ).not.toContain(psk);
    responder.readMessage(first);
    reply = message(phone);
    machine.send(data(route, responder.writeMessage()));
    initiator.readMessage(
      new Uint8Array((await reply).data as ArrayBuffer).slice(17),
    );
    initiator.split();
    await runInDurableObject(stub(), (_obj, state) => {
      for (const table of ["machines", "invites", "device_invites", "incoming"])
        expect(
          JSON.stringify(
            state.storage.sql.exec(`SELECT * FROM ${table}`).toArray(),
          ),
        ).not.toContain(psk);
      expect(
        JSON.stringify(
          state.getWebSockets().map((s) => s.deserializeAttachment()),
        ),
      ).not.toContain(psk);
    });
    const reused = await connect();
    let end = closed(reused);
    reused.send(
      frame(route, { role: "invite", token: invite, min: 1, max: 1 }),
    );
    expect((await end).code).toBe(1008);
    end = closed(phone);
    phone.send(data(id, opaque));
    expect((await end).code).toBe(1008);
  });
  it("machine socket stays connected when a route has no peer", async () => {
    const { invite, route } = await offer();
    const machine = await connect();
    await hello(machine, id, "machine", token);
    machine.send(data(route, new Uint8Array([1])));
    const pong = message(machine);
    machine.send("ping");
    expect((await pong).data).toBe("pong");
    const phone = await connect();
    await hello(phone, route, "invite", invite);
    const machineEnd = closed(machine);
    machine.close();
    await machineEnd;
    const end = closed(phone);
    phone.send(data(route, new Uint8Array([1])));
    expect((await end).code).toBe(1008);
  });
  it("status counts only machines and replacement ignores invite sockets", async () => {
    await registered();
    const invite = "ab".repeat(32);
    const issuer = "33".repeat(16);
    const digest = await hash(token);
    await runInDurableObject(stub(), (_obj, state) => {
      state.storage.sql.exec("INSERT INTO machines (id, hash) VALUES (?, ?)", issuer, digest);
    });
    expect((await post("/device-invite", { id: issuer, token, hash: await hash(invite), route: id })).status).toBe(200);
    const phone = await connect();
    await hello(phone, id, "invite", invite);
    expect(await (await post("/status", { id, token })).json()).toEqual({ connected: false });
    const machine = await connect();
    await hello(machine, id, "machine", token);
    expect(await (await post("/status", { id, token })).json()).toEqual({ connected: true });
    const pong = message(phone);
    phone.send("ping");
    expect((await pong).data).toBe("pong");
    await runInDurableObject(stub(), (_obj, state) => {
      expect(state.getWebSockets().filter(ws => ws.deserializeAttachment()?.role === "invite" && ws.readyState === WebSocket.OPEN)).toHaveLength(1);
    });
  });
  it("refuses expired invites", async () => {
    const { invite, route } = await offer();
    await runInDurableObject(stub(), (_obj, state) => {
      state.storage.sql.exec("UPDATE device_invites SET expires = 0");
    });
    const phone = await connect();
    const end = closed(phone);
    phone.send(frame(route, { role: "invite", token: invite, min: 1, max: 1 }));
    expect((await end).code).toBe(1008);
  });
  it("closes admitted sockets at their deadline", async () => {
    const { invite, route } = await offer();
    const phone = await connect();
    await hello(phone, route, "invite", invite);
    const end = closed(phone);
    await runInDurableObject(stub(), async (obj, state) => {
      const ws = state
        .getWebSockets()
        .find((s) => s.deserializeAttachment()?.role === "invite")!;
      const a = ws.deserializeAttachment();
      a.expires = Date.now() - 1;
      ws.serializeAttachment(a);
      await new Relay(state, env).alarm();
    });
    expect((await end).code).toBe(1008);
  });
  it("does not grant invite registration to unauthenticated callers", async () => {
    expect(
      (
        await post("/device-invite", {
          id,
          token,
          hash: "ab".repeat(32),
          route: "cd".repeat(16),
        })
      ).status,
    ).toBe(403);
  });
});

describe("device token lifecycle", () => {
  it("authenticates issuance, stores only a hash scoped to one machine, and revokes", async () => {
    const phone = "ab".repeat(16);
    expect((await post("/device-token", { id, token, phone })).status).toBe(
      403,
    );
    await registered();
    expect(
      (await post("/device-token", { id, token, phone: "bad" })).status,
    ).toBe(403);
    const issued = await post("/device-token", { id, token, phone });
    expect(issued.status).toBe(200);
    const device = (await issued.json()) as {
      token: string;
      machines: string[];
    };
    expect(device.token).toMatch(/^[a-f0-9]{64}$/);
    expect(device.machines).toEqual([id]);
    const digest = await hash(device.token);
    await runInDurableObject(stub(), (_obj, state) => {
      expect(state.storage.sql.exec("SELECT * FROM devices").toArray()).toEqual(
        [{ machine: id, phone, hash: digest }],
      );
    });
    expect((await post("/device-token", { id, token, phone })).status).toBe(
      403,
    );
    expect(
      (await post("/device-revoke", { id, token: "ff".repeat(32), phone }))
        .status,
    ).toBe(403);
    expect((await post("/device-revoke", { id, token, phone })).status).toBe(
      200,
    );
    expect((await post("/device-revoke", { id, token, phone })).status).toBe(
      200,
    );
    await runInDurableObject(stub(), (_obj, state) => {
      expect(state.storage.sql.exec("SELECT * FROM devices").toArray()).toEqual(
        [],
      );
    });
  });
  it("cannot revoke a different machine's token", async () => {
    await registered();
    const phone = "ab".repeat(16),
      other = "cd".repeat(16),
      otherToken = "ef".repeat(32),
      digest = await hash(otherToken);
    await runInDurableObject(stub(), (_obj, state) => {
      state.storage.sql.exec(
        "INSERT INTO machines (id,hash) VALUES (?,?)",
        other,
        digest,
      );
    });
    expect((await post("/device-token", { id, token, phone })).status).toBe(
      200,
    );
    expect(
      (await post("/device-revoke", { id: other, token: otherToken, phone }))
        .status,
    ).toBe(200);
    await runInDurableObject(stub(), (_obj, state) => {
      expect(
        state.storage.sql.exec("SELECT machine FROM devices").toArray(),
      ).toEqual([{ machine: id }]);
    });
  });
});

describe("encrypted channel", () => {
  it("fans out opaque KK ciphertext, reports budget, and closes revoked routes", async () => {
    await registered();
    const phone = "33".repeat(16);
    const issued = await post("/device-token", { id, token, phone });
    const device = ((await issued.json()) as { token: string }).token;
    const machine = await connect();
    let received = message(machine);
    machine.send(frame(id, { role: "machine", token, min: 1, max: 1 }));
    await received;
    const mobile = await connect();
    received = message(mobile);
    const budget = message(machine);
    mobile.send(
      frame(phone, {
        role: "phone",
        machine: id,
        token: device,
        min: 1,
        max: 1,
      }),
    );
    await received;
    expect(new Uint8Array((await budget).data as ArrayBuffer)[0]).toBe(6);
    const priv = new Uint8Array(32).fill(4),
      machinePriv = new Uint8Array(32).fill(5);
    const a = new HandshakeState({
      pattern: "KK",
      initiator: true,
      staticPrivate: priv,
      peerStatic: x25519.getPublicKey(machinePriv),
    });
    const b = new HandshakeState({
      pattern: "KK",
      initiator: false,
      staticPrivate: machinePriv,
      peerStatic: x25519.getPublicKey(priv),
    });
    const routed = (route: string, data: Uint8Array) => {
      const out = frame(route, {}).slice(0, 17);
      out[0] = 2;
      const all = new Uint8Array(17 + data.length);
      all.set(out);
      all.set(data, 17);
      return all;
    };
    const batch = (cipher: Uint8Array) => {
      const out = new Uint8Array(20 + cipher.length);
      out.set(Uint8Array.from(phone.match(/../g)!, (b) => parseInt(b, 16)));
      new DataView(out.buffer).setUint32(16, cipher.length);
      out.set(cipher, 20);
      return routed(id, out);
    };
    const frames: Uint8Array[] = [];
    let wire = a.writeMessage();
    received = message(machine);
    mobile.send(routed(phone, wire));
    let raw = new Uint8Array((await received).data as ArrayBuffer);
    frames.push(raw);
    b.readMessage(raw.slice(17));
    wire = b.writeMessage();
    received = message(mobile);
    const batched = batch(wire);
    frames.push(batched);
    machine.send(batched);
    raw = new Uint8Array((await received).data as ArrayBuffer);
    frames.push(raw);
    a.readMessage(raw.slice(17));
    const known = "private screen text which must never reach relay cleartext";
    wire = a.split().send.encrypt(new TextEncoder().encode(known));
    received = message(machine);
    mobile.send(routed(phone, wire));
    raw = new Uint8Array((await received).data as ArrayBuffer);
    frames.push(raw);
    expect(
      new TextDecoder().decode(b.split().receive.decrypt(raw.slice(17))),
    ).toBe(known);
    wire = b.split().send.encrypt(new TextEncoder().encode(known));
    received = message(mobile);
    const output = batch(wire);
    frames.push(output);
    machine.send(output);
    raw = new Uint8Array((await received).data as ArrayBuffer);
    frames.push(raw);
    expect(
      new TextDecoder().decode(a.split().receive.decrypt(raw.slice(17))),
    ).toBe(known);
    for (const f of frames)
      expect(new TextDecoder().decode(f)).not.toContain(known);
    received = message(machine);
    const closed = new Promise<void>((resolve) =>
      mobile.addEventListener("close", () => resolve(), { once: true }),
    );
    expect((await post("/device-revoke", { id, token, phone })).ok).toBe(true);
    await closed;
    expect(new Uint8Array((await received).data as ArrayBuffer)[0]).toBe(4);
  });
  it("crosses budget thresholds and resets the UTC day", async () => {
    await registered();
    const machine = await connect();
    let received = message(machine);
    machine.send(frame(id, { role: "machine", token, min: 1, max: 1 }));
    await received;
    const phone = "44".repeat(16);
    const issued = await post("/device-token", { id, token, phone });
    const device = ((await issued.json()) as { token: string }).token;
    const day = new Date().toISOString().slice(0, 10);
    await runInDurableObject(stub(), (_obj, state) => {
      state.storage.sql.exec(
        "INSERT INTO incoming(day,count) VALUES (?,?)",
        day,
        49998,
      );
    });
    received = message(machine);
    const mobile = await connect();
    const ack = message(mobile);
    mobile.send(
      frame(phone, {
        role: "phone",
        machine: id,
        token: device,
        min: 1,
        max: 1,
      }),
    );
    await ack;
    const first = new Uint8Array((await received).data as ArrayBuffer);
    expect(JSON.parse(new TextDecoder().decode(first.slice(17))).count).toBe(
      50000,
    );
    await runInDurableObject(stub(), (_obj, state) => {
      state.storage.sql.exec(
        "UPDATE incoming SET count = 79998 WHERE day = ?",
        day,
      );
    });
    received = message(machine);
    const data = frame(phone, {});
    data[0] = 2;
    mobile.send(data);
    const second = new Uint8Array((await received).data as ArrayBuffer);
    expect(second[0]).toBe(6);
    expect(
      JSON.parse(new TextDecoder().decode(second.slice(17))).count,
    ).toBeGreaterThanOrEqual(80000);
    // Model sockets surviving midnight: yesterday's pending attachment counts
    // must not keep a machine in yesterday's throttled budget tier.
    await runInDurableObject(stub(), (_obj, state) => {
      const yesterday = new Date(Date.now() - 86400000)
        .toISOString()
        .slice(0, 10);
      state.storage.sql.exec("DELETE FROM incoming WHERE day = ?", day);
      for (const ws of state.getWebSockets()) {
        const a = ws.deserializeAttachment();
        if (a?.id) {
          a.pending = { [yesterday]: 80000 };
          a.budgetLevel = 2;
          ws.serializeAttachment(a);
        }
      }
    });
    const reset = new Promise<MessageEvent>((resolve) => {
      const listener = (e: MessageEvent) => {
        if (new Uint8Array(e.data as ArrayBuffer)[0] === 6) {
          machine.removeEventListener("message", listener);
          resolve(e);
        }
      };
      machine.addEventListener("message", listener);
    });
    mobile.send(data);
    const resetFrame = new Uint8Array((await reset).data as ArrayBuffer);
    expect(
      JSON.parse(new TextDecoder().decode(resetFrame.slice(17))).count,
    ).toBe(1);
  });
});
