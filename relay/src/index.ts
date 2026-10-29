import { DurableObject } from "cloudflare:workers";

export type Env = Cloudflare.Env;
const encoder = new TextEncoder();
export async function hash(secret: string): Promise<string> {
  return Array.from(
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", encoder.encode(secret)),
    ),
    (b) => b.toString(16).padStart(2, "0"),
  ).join("");
}
function secret(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(32)), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");
}
export function frame(id: string, body: object): Uint8Array {
  const payload = encoder.encode(JSON.stringify(body));
  const out = new Uint8Array(17 + payload.length);
  out[0] = 1; // hello; the next sixteen bytes are the machine route id
  for (let i = 0; i < 16; i++)
    out[1 + i] = parseInt(id.slice(i * 2, i * 2 + 2), 16);
  out.set(payload, 17);
  return out;
}
const refuse = () => new Response("refused", { status: 403 });
export class Relay extends DurableObject<Env> {
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    ctx.storage.sql
      .exec(`CREATE TABLE IF NOT EXISTS machines (id TEXT PRIMARY KEY, hash TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS invites (hash TEXT PRIMARY KEY, expires INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS device_invites (hash TEXT PRIMARY KEY, machine TEXT NOT NULL, route TEXT NOT NULL UNIQUE, expires INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS devices (machine TEXT NOT NULL, phone TEXT NOT NULL, hash TEXT NOT NULL, PRIMARY KEY(machine, phone));
      CREATE TABLE IF NOT EXISTS incoming (day TEXT PRIMARY KEY, count INTEGER NOT NULL);`);
    // A redeploy cannot replace the original registration or turn bootstrap into an HTTP endpoint.
    if (
      /^[a-f0-9]{32}$/.test(env.INITIAL_MACHINE) &&
      /^[a-f0-9]{64}$/.test(env.INITIAL_HASH)
    ) {
      ctx.storage.sql.exec(
        "INSERT OR IGNORE INTO machines (id, hash) VALUES (?, ?)",
        env.INITIAL_MACHINE,
        env.INITIAL_HASH,
      );
    }
    ctx.setWebSocketAutoResponse(
      new WebSocketRequestResponsePair("ping", "pong"),
    );
  }
  private async authenticated(id: string, token: string): Promise<boolean> {
    if (!/^[a-f0-9]{32}$/.test(id) || token.length !== 64) return false;
    // The lookup compares digests, not the raw secret.
    return (
      this.ctx.storage.sql
        .exec(
          "SELECT id FROM machines WHERE id = ? AND hash = ?",
          id,
          await hash(token),
        )
        .toArray().length === 1
    );
  }
  async fetch(request: Request): Promise<Response> {
    const path = new URL(request.url).pathname;
    if (
      path === "/connect" &&
      request.headers.get("Upgrade")?.toLowerCase() === "websocket"
    ) {
      const pair = new WebSocketPair();
      this.ctx.acceptWebSocket(pair[1]);
      pair[1].serializeAttachment({ id: null, deadline: Date.now() + 10_000 });
      // Bound idle unauthenticated sockets without a timer that prevents hibernation.
      const alarm = await this.ctx.storage.getAlarm();
      if (alarm === null) await this.ctx.storage.setAlarm(Date.now() + 10_000);
      return new Response(null, { status: 101, webSocket: pair[0] });
    }
    if (
      request.method !== "POST" ||
      ![
        "/invite",
        "/join",
        "/status",
        "/device-invite",
        "/device-token",
        "/device-revoke",
      ].includes(path)
    )
      return new Response("not found", { status: 404 });
    if (Number(request.headers.get("Content-Length")) > 4096)
      return new Response("too large", { status: 413 });
    // Stream to a fixed bound even when Content-Length is absent or dishonest.
    const reader = request.body?.getReader();
    if (!reader) return refuse();
    let bytes = new Uint8Array(0);
    while (true) {
      const part = await reader.read();
      if (part.done) break;
      if (bytes.length + part.value.length > 4096) {
        await reader.cancel();
        return new Response("too large", { status: 413 });
      }
      const next = new Uint8Array(bytes.length + part.value.length);
      next.set(bytes);
      next.set(part.value, bytes.length);
      bytes = next;
    }
    let body: {
      id?: string;
      token?: string;
      invite?: string;
      hash?: string;
      route?: string;
      phone?: string;
    };
    try {
      body = JSON.parse(new TextDecoder().decode(bytes));
    } catch {
      return refuse();
    }
    if (!body || typeof body !== "object") return refuse();
    if (path === "/join") {
      if (
        typeof body.invite !== "string" ||
        body.invite.length !== 64 ||
        typeof body.id !== "string" ||
        !/^[a-f0-9]{32}$/.test(body.id)
      )
        return refuse();
      const digest = await hash(body.invite);
      const token = secret();
      const tokenHash = await hash(token);
      // No await between consuming the invite and registering the machine.
      const joined = this.ctx.storage.transactionSync(() => {
        if (
          this.ctx.storage.sql
            .exec("SELECT id FROM machines WHERE id = ?", body.id!)
            .toArray().length
        )
          return false;
        const invite = this.ctx.storage.sql
          .exec(
            "DELETE FROM invites WHERE hash = ? AND expires > ? RETURNING hash",
            digest,
            Date.now(),
          )
          .toArray();
        if (!invite.length) return false;
        this.ctx.storage.sql.exec(
          "INSERT INTO machines (id, hash) VALUES (?, ?)",
          body.id!,
          tokenHash,
        );
        return true;
      });
      return joined ? Response.json({ token }) : refuse();
    }
    if (
      typeof body.id !== "string" ||
      typeof body.token !== "string" ||
      !(await this.authenticated(body.id, body.token))
    )
      return refuse();
    if (path === "/device-token" || path === "/device-revoke") {
      if (typeof body.phone !== "string" || !/^[a-f0-9]{32}$/.test(body.phone))
        return refuse();
      if (path === "/device-revoke") {
        this.ctx.storage.sql.exec(
          "DELETE FROM devices WHERE machine = ? AND phone = ?",
          body.id,
          body.phone,
        );
        for (const ws of this.ctx.getWebSockets()) {
          const a = ws.deserializeAttachment();
          if (
            a?.role === "phone" &&
            a.machine === body.id &&
            a.id === body.phone
          ) {
            this.gone(ws);
            ws.close(1008, "revoked");
          }
        }
        return Response.json({ ok: true });
      }
      const token = secret();
      const digest = await hash(token);
      // A token grants access only to the authenticated machine issuing it.
      const inserted = this.ctx.storage.sql
        .exec(
          "INSERT OR IGNORE INTO devices (machine, phone, hash) VALUES (?, ?, ?) RETURNING phone",
          body.id,
          body.phone,
          digest,
        )
        .toArray();
      return inserted.length
        ? Response.json({ token, machines: [body.id] })
        : refuse();
    }
    if (path === "/device-invite") {
      if (
        !/^[a-f0-9]{64}$/.test(body.hash ?? "") ||
        !/^[a-f0-9]{32}$/.test(body.route ?? "") ||
        body.route === body.id
      )
        return refuse();
      const expires = Date.now() + 600_000;
      this.ctx.storage.sql.exec(
        "DELETE FROM device_invites WHERE expires <= ?",
        Date.now(),
      );
      this.ctx.storage.sql.exec(
        "INSERT INTO device_invites (hash, machine, route, expires) VALUES (?, ?, ?, ?)",
        body.hash!,
        body.id,
        body.route!,
        expires,
      );
      return Response.json({ expires });
    }
    if (path === "/status")
      return Response.json({
        connected: this.ctx
          .getWebSockets()
          .some((ws) => {
            const a = ws.deserializeAttachment();
            return a?.role === "machine" && a.id === body.id;
          }),
      });
    const invite = secret();
    const expires = Date.now() + 600_000;
    this.ctx.storage.sql.exec(
      "DELETE FROM invites WHERE expires <= ?",
      Date.now(),
    );
    this.ctx.storage.sql.exec(
      "INSERT INTO invites (hash, expires) VALUES (?, ?)",
      await hash(invite),
      expires,
    );
    return Response.json({ invite, expires });
  }
  async webSocketMessage(
    ws: WebSocket,
    message: string | ArrayBuffer,
  ): Promise<void> {
    if (
      typeof message === "string" ||
      message.byteLength < 17 ||
      message.byteLength > 65552
    ) {
      ws.close(1008, "invalid frame");
      return;
    }
    const attachment = ws.deserializeAttachment();
    if (!attachment?.id && attachment?.deadline <= Date.now()) {
      ws.close(1008, "hello timeout");
      return;
    }
    const bytes = new Uint8Array(message);
    const id = Array.from(bytes.slice(1, 17), (b) =>
      b.toString(16).padStart(2, "0"),
    ).join("");
    if (attachment?.id) {
      if (attachment.gone) {
        ws.close(1008, "route retired");
        return;
      }
      await this.count(ws);
      if (
        attachment.role === "phone" ||
        (attachment.role === "machine" && id === attachment.id)
      ) {
        if (bytes[0] !== 2) {
          ws.close(1008, "invalid data");
          return;
        }
        if (attachment.role === "phone") {
          if (id !== attachment.id) {
            ws.close(1008, "route refused");
            return;
          }
          const machine = this.ctx
            .getWebSockets()
            .find(
              (p) =>
                p.readyState === WebSocket.OPEN &&
                p.deserializeAttachment()?.role === "machine" &&
                p.deserializeAttachment()?.id === attachment.machine,
            );
          if (machine) machine.send(message);
          else {
            const gone = frame(id, {});
            gone[0] = 4;
            ws.send(gone);
          }
        } else {
          // Validate the full envelope before forwarding any entry. Ciphertext is opaque.
          const entries: { route: string; data: Uint8Array }[] = [];
          let at = 17;
          while (at < bytes.length) {
            if (at + 20 > bytes.length) {
              ws.close(1008, "invalid batch");
              return;
            }
            const route = Array.from(bytes.slice(at, at + 16), (b) =>
              b.toString(16).padStart(2, "0"),
            ).join("");
            const length = new DataView(
              bytes.buffer,
              bytes.byteOffset + at + 16,
              4,
            ).getUint32(0);
            at += 20;
            if (length > 65535 || at + length > bytes.length || length < 16) {
              ws.close(1008, "invalid batch");
              return;
            }
            entries.push({ route, data: bytes.slice(at, at + length) });
            at += length;
          }
          for (const entry of entries) {
            const peer = this.ctx.getWebSockets().find((p) => {
              const a = p.deserializeAttachment();
              return (
                p.readyState === WebSocket.OPEN &&
                a?.role === "phone" &&
                a.machine === attachment.id &&
                a.id === entry.route
              );
            });
            if (peer) {
              const out = new Uint8Array(17 + entry.data.length);
              out[0] = 2;
              for (let i = 0; i < 16; i++)
                out[i + 1] = parseInt(entry.route.slice(i * 2, i * 2 + 2), 16);
              out.set(entry.data, 17);
              peer.send(out);
            }
          }
        }
        return;
      }
      if (
        bytes[0] !== 2 ||
        (attachment.expires && attachment.expires <= Date.now())
      ) {
        ws.close(1008, "route refused");
        return;
      }
      const peer = this.ctx.getWebSockets().find((other) => {
        const a = other.deserializeAttachment();
        return (
          other !== ws &&
          other.readyState === WebSocket.OPEN &&
          (attachment.role === "invite"
            ? id === attachment.route &&
              a?.role === "machine" &&
              a.id === attachment.machine
            : a?.role === "invite" &&
              a.machine === attachment.id &&
              a.route === id &&
              a.expires > Date.now())
        );
      });
      if (!peer) {
        if (attachment.role === "invite") ws.close(1008, "route refused");
        return;
      }
      // The route header and opaque body are forwarded byte-for-byte.
      peer.send(message);
      return;
    }
    if (bytes[0] !== 1 || bytes.length > 4096) {
      ws.close(1008, "invalid hello");
      return;
    }
    let body;
    try {
      body = JSON.parse(
        new TextDecoder("utf-8", { fatal: true }).decode(bytes.slice(17)),
      );
    } catch {
      ws.close(1008, "invalid hello");
      return;
    }
    if (
      body?.role === "phone" &&
      body.min === 1 &&
      body.max === 1 &&
      typeof body.token === "string" &&
      /^[a-f0-9]{64}$/.test(body.token) &&
      typeof body.machine === "string"
    ) {
      const digest = await hash(body.token);
      const rows = this.ctx.storage.sql
        .exec(
          "SELECT phone FROM devices WHERE machine = ? AND phone = ? AND hash = ?",
          body.machine,
          id,
          digest,
        )
        .toArray();
      if (!rows.length || ws.deserializeAttachment()?.id) {
        ws.close(1008, "device refused");
        return;
      }
      for (const other of this.ctx.getWebSockets()) {
        const a = other.deserializeAttachment();
        if (
          other !== ws &&
          a?.role === "phone" &&
          a.id === id &&
          a.machine === body.machine
        ) {
          this.gone(other);
          other.close(1000, "replaced");
        }
      }
      ws.serializeAttachment({ id, role: "phone", machine: body.machine });
      ws.send(frame(id, { ok: true, version: 1 }));
      await this.count(ws);
      return;
    }
    if (
      body?.role === "invite" &&
      body.min === 1 &&
      body.max === 1 &&
      typeof body.token === "string" &&
      /^[a-f0-9]{64}$/.test(body.token)
    ) {
      const digest = await hash(body.token);
      const rows = this.ctx.storage.sql
        .exec<{
          machine: string;
          route: string;
          expires: number;
        }>(
          "DELETE FROM device_invites WHERE hash = ? AND route = ? AND expires > ? RETURNING machine, route, expires",
          digest,
          id,
          Date.now(),
        )
        .toArray();
      if (!rows.length || ws.deserializeAttachment()?.id) {
        ws.close(1008, "invite refused");
        return;
      }
      ws.serializeAttachment({ id, role: "invite", ...rows[0] });
      const alarm = await this.ctx.storage.getAlarm();
      if (alarm === null || alarm > rows[0].expires)
        await this.ctx.storage.setAlarm(rows[0].expires);
      ws.send(frame(id, { ok: true, version: 1 }));
      await this.count(ws);
      return;
    }
    if (
      bytes[0] !== 1 ||
      ws.deserializeAttachment()?.id ||
      !body ||
      body.role !== "machine" ||
      body.min !== 1 ||
      body.max !== 1 ||
      typeof body.token !== "string" ||
      !(await this.authenticated(id, body.token))
    ) {
      ws.close(1008, "unregistered or incompatible");
      return;
    }
    if (ws.deserializeAttachment()?.id) {
      ws.close(1008, "duplicate hello");
      return;
    }
    for (const other of this.ctx.getWebSockets()) {
      const a = other.deserializeAttachment();
      if (other !== ws && a?.role === "machine" && a.id === id) {
        this.gone(other);
        other.close(1000, "replaced");
      }
    }
    // Attachments survive hibernation; no per-message SQLite writes.
    ws.serializeAttachment({
      id,
      role: "machine",
      pending: { [new Date().toISOString().slice(0, 10)]: 1 },
      flushAt: Date.now() + 300_000,
    });
    const alarm = await this.ctx.storage.getAlarm();
    const flushAt = Date.now() + 300_000;
    if (alarm === null || alarm > flushAt)
      await this.ctx.storage.setAlarm(flushAt);
    ws.send(frame(id, { ok: true, version: 1 }));
  }
  private async count(ws: WebSocket): Promise<void> {
    const day = new Date().toISOString().slice(0, 10);
    const state = ws.deserializeAttachment();
    state.pending ??= {};
    state.pending[day] = (state.pending[day] ?? 0) + 1;
    state.flushAt ??= Date.now() + 300_000;
    ws.serializeAttachment(state);
    const persisted =
      this.ctx.storage.sql
        .exec<{
          count: number;
        }>("SELECT count FROM incoming WHERE day = ?", day)
        .toArray()[0]?.count ?? 0;
    const count =
      persisted +
      this.ctx
        .getWebSockets()
        .reduce(
          (n, p) => n + (p.deserializeAttachment()?.pending?.[day] ?? 0),
          0,
        );
    // Send only on threshold transitions (and the first authenticated message).
    const level = count >= 80000 ? 2 : count >= 50000 ? 1 : 0;
    for (const machine of this.ctx.getWebSockets()) {
      const a = machine.deserializeAttachment();
      if (a?.role === "machine" && a.budgetLevel !== level) {
        a.budgetLevel = level;
        machine.serializeAttachment(a);
        const b = frame(a.id, { count });
        b[0] = 6;
        machine.send(b);
      }
    }
    const alarm = await this.ctx.storage.getAlarm();
    if (alarm === null || alarm > state.flushAt)
      await this.ctx.storage.setAlarm(state.flushAt);
  }
  private gone(ws: WebSocket): void {
    const a = ws.deserializeAttachment();
    if (!a?.id || a.gone) return;
    a.gone = true;
    ws.serializeAttachment(a);
    for (const peer of this.ctx.getWebSockets()) {
      const p = peer.deserializeAttachment();
      if (
        peer !== ws &&
        peer.readyState === WebSocket.OPEN &&
        ((a.role === "phone" && p?.role === "machine" && p.id === a.machine) ||
          (a.role === "machine" && p?.role === "phone" && p.machine === a.id))
      ) {
        const b = frame(a.role === "phone" ? a.id : p.id, {});
        b[0] = 4;
        peer.send(b);
      }
    }
  }
  private flushCounts(sockets: WebSocket[]): void {
    const totals: Record<string, number> = {};
    for (const ws of sockets) {
      const state = ws.deserializeAttachment();
      for (const [day, count] of Object.entries(state?.pending ?? {}))
        totals[day] = (totals[day] ?? 0) + Number(count);
    }
    this.ctx.storage.transactionSync(() => {
      for (const [day, count] of Object.entries(totals))
        this.ctx.storage.sql.exec(
          "INSERT INTO incoming (day, count) VALUES (?, ?) ON CONFLICT(day) DO UPDATE SET count = count + excluded.count",
          day,
          count,
        );
      this.ctx.storage.sql.exec(
        "DELETE FROM incoming WHERE day < ?",
        new Date(Date.now() - 86_400_000).toISOString().slice(0, 10),
      );
    });
    for (const ws of sockets) {
      const state = ws.deserializeAttachment();
      if (state?.pending) {
        delete state.pending;
        delete state.flushAt;
        ws.serializeAttachment(state);
      }
    }
  }
  async alarm(): Promise<void> {
    let next: number | undefined;
    const due = this.ctx
      .getWebSockets()
      .filter((ws) => ws.deserializeAttachment()?.flushAt <= Date.now());
    if (due.length) this.flushCounts(due);
    for (const ws of this.ctx.getWebSockets()) {
      const state = ws.deserializeAttachment();
      if (state?.expires) {
        if (state.expires <= Date.now()) ws.close(1008, "invite expired");
        else next = Math.min(next ?? Infinity, state.expires);
      }
      if (state?.id) {
        if (state.flushAt) next = Math.min(next ?? Infinity, state.flushAt);
        continue;
      }
      if (state?.deadline <= Date.now()) ws.close(1008, "hello timeout");
      else if (state?.deadline)
        next = Math.min(next ?? Infinity, state.deadline);
    }
    if (next !== undefined) await this.ctx.storage.setAlarm(next);
  }
  private retire(ws: WebSocket): void {
    const state = ws.deserializeAttachment();
    if (!state?.pending) return;
    const other = this.ctx
      .getWebSockets()
      .find(
        (s) =>
          s !== ws &&
          s.readyState === WebSocket.OPEN &&
          s.deserializeAttachment()?.id,
      );
    if (other) {
      const target = other.deserializeAttachment();
      target.pending ??= {};
      for (const [day, count] of Object.entries(state.pending))
        target.pending[day] = (target.pending[day] ?? 0) + Number(count);
      target.flushAt = Math.min(target.flushAt ?? Infinity, state.flushAt);
      other.serializeAttachment(target);
      delete state.pending;
      delete state.flushAt;
      ws.serializeAttachment(state);
    } else {
      // Last socket: persist before its hibernation attachment disappears.
      this.flushCounts([ws]);
    }
  }
  webSocketClose(ws: WebSocket, code: number): void {
    this.gone(ws);
    this.retire(ws);
    ws.close([1005, 1006, 1015].includes(code) ? 1000 : code);
  }
  webSocketError(ws: WebSocket): void {
    this.gone(ws);
    this.retire(ws);
    ws.close(1011, "socket error");
  }
}
export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    if (
      [
        "/",
        "/index.html",
        "/pairing.js",
        "/session.js",
        "/app.js",
        "/app.css",
        "/layout.css",
        "/theme.css",
        "/sw.js",
        "/icon-192.png",
        "/icon-512.png",
        "/manifest.webmanifest",
      ].includes(new URL(request.url).pathname)
    ) {
      const asset = await env.ASSETS.fetch(request);
      const response = new Response(asset.body, asset);
      // Xterm's DOM renderer inserts style elements for dimensions and theme colors.
      response.headers.set(
        "Content-Security-Policy",
        "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
      );
      return response;
    }
    return env.RELAY.get(env.RELAY.idFromName("relay")).fetch(request);
  },
} satisfies ExportedHandler<Env>;
