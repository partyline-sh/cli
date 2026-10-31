import { encodeMessages } from "./input-protocol";
export { encodeMessages } from "./input-protocol";
import { HandshakeState } from "./noise";
import { Decompress } from "fzstd";

export interface SavedPhone {
  machine: string;
  private: string;
  public: string;
  phone: string;
  token: string;
}
export interface InnerMessage {
  type: string;
  id?: string;
  kind?: "text" | "key" | "paste";
  text?: string;
  status?: "ok" | "refused";
  reason?: string;
  pane?: string;
  mode?: "focus" | "summary";
  revision?: number;
  encoding?: string;
  data?: unknown;
}
const encoder = new TextEncoder(),
  decoder = new TextDecoder("utf-8", { fatal: true });
const from64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
function frame(route: string, body: Uint8Array, kind = 2): Uint8Array {
  if (!/^[a-f0-9]{32}$/.test(route) || body.length > 65535)
    throw new Error("Invalid frame");
  const out = new Uint8Array(body.length + 17);
  out[0] = kind;
  out.set(
    Uint8Array.from(route.match(/../g)!, (b) => parseInt(b, 16)),
    1,
  );
  out.set(body, 17);
  return out;
}
// One instance owns one fresh KK handshake and its ordered transport ciphers.
// The caller renews subscriptions while visible; backgrounding closes the socket.
export class PhoneSession {
  private transcriptRevisions = new Map<string, number>();
  private resyncing = new Set<string>();
  private ws?: WebSocket;
  private ready = false;
  private ended = false;
  private ping?: ReturnType<typeof setInterval>;
  private renew?: ReturnType<typeof setInterval>;
  private pong?: ReturnType<typeof setTimeout>;
  private subscriptions = new Map<string, "focus" | "summary">();
  private handshake: HandshakeState;
  private hello = false;
  private fragments?: {
    id: number;
    parts: number;
    chunks: Uint8Array[];
    size: number;
  };
  private timer?: ReturnType<typeof setTimeout>;
  private readonly hidden = () => {
    if (document.hidden) this.close();
  };
  constructor(
    private saved: SavedPhone,
    private receive: (m: InnerMessage) => void,
    private closed: (reason: string) => void = () => {},
  ) {
    this.handshake = new HandshakeState({
      pattern: "KK",
      initiator: true,
      staticPrivate: from64(saved.private),
      peerStatic: from64(saved.public),
    });
  }
  connect(origin = location.origin): void {
    if (this.ws) throw new Error("Session already used");
    const url = new URL(origin);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.pathname = "/connect";
    url.search = "";
    url.hash = "";
    const ws = (this.ws = new WebSocket(url));
    ws.binaryType = "arraybuffer";
    this.timer = setTimeout(() => this.fail("Session timed out"), 10000);
    document.addEventListener("visibilitychange", this.hidden);
    ws.onopen = () => {
      this.ping = setInterval(() => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.send("ping");
          this.pong = setTimeout(() => this.fail("Relay heartbeat timed out"), 10000);
        }
      }, 30000);
      ws.send(
        frame(
          this.saved.phone,
          encoder.encode(
            JSON.stringify({
              role: "phone",
              machine: this.saved.machine,
              token: this.saved.token,
              min: 1,
              max: 1,
            }),
          ),
          1,
        ),
      );
    };
    ws.onerror = () => this.fail("Relay connection failed");
    ws.onclose = (event) =>
      this.fail(
        event.code === 1008 && ["revoked", "device refused"].includes(event.reason)
          ? event.reason
          : "Session closed",
      );
    ws.onmessage = (e) => {
      try {
        if (e.data === "pong") {
          clearTimeout(this.pong);
          return;
        }
        if (!(e.data instanceof ArrayBuffer)) throw new Error("Invalid frame");
        const b = new Uint8Array(e.data);
        if (
          b.length < 17 ||
          b.length > 65552 ||
          Array.from(b.slice(1, 17), (v) =>
            v.toString(16).padStart(2, "0"),
          ).join("") !== this.saved.phone
        )
          throw new Error("Invalid route");
        if (b[0] === 4) throw new Error("Machine disconnected");
        if (!this.hello) {
          const ack = JSON.parse(decoder.decode(b.slice(17)));
          if (b[0] !== 1 || !ack.ok || ack.version !== 1)
            throw new Error("Invalid hello");
          this.hello = true;
          ws.send(frame(this.saved.phone, this.handshake.writeMessage()));
          return;
        }
        if (b[0] !== 2) throw new Error("Invalid data frame");
        if (!this.ready) {
          this.handshake.readMessage(b.slice(17));
          this.ready = true;
          this.renew = setInterval(() => {
            try {
              if (this.subscriptions.size)
                this.send(
                  Array.from(this.subscriptions, ([pane, mode]) => ({
                    type: "sub",
                    pane,
                    mode,
                  })),
                );
            } catch {
              this.fail("Lease renewal failed");
            }
          }, 30000);
          this.send([
            { type: "hello", data: { version: 1, capabilities: ["zstd"] } },
          ]);
          return;
        }
        const messages = JSON.parse(
          decoder.decode(this.handshake.split().receive.decrypt(b.slice(17))),
        ) as InnerMessage[];
        if (!Array.isArray(messages)) throw new Error("Invalid messages");
        this.deliver(messages);
      } catch (e) {
        this.fail(e instanceof Error ? e.message : "Session failed");
      }
    };
  }
  private deliver(messages: InnerMessage[]): void {
    for (const m of messages) {
      if (this.ended) return;
      if (m.type === "bye") {
        this.fail(typeof m.reason === "string" ? m.reason : "Machine ended the session");
        return;
      }
      if (m.type === "reset") {
        this.fragments = undefined;
        continue;
      }
      if (m.type === "fragment") {
        const f = m as InnerMessage & {
          id: number;
          index: number;
          parts: number;
          data: string;
        };
        if (
          !Number.isSafeInteger(f.id) ||
          !Number.isSafeInteger(f.parts) ||
          f.parts < 1 ||
          f.parts > 1024
        )
          throw new Error("Invalid fragment");
        if (f.index === 0) {
          if (this.fragments) throw new Error("Interrupted snapshot");
          this.fragments = { id: f.id, parts: f.parts, chunks: [], size: 0 };
        }
        const state = this.fragments;
        if (
          !state ||
          state.id !== f.id ||
          state.parts !== f.parts ||
          f.index !== state.chunks.length
        )
          throw new Error("Fragment order");
        const chunk = from64(f.data);
        state.size += chunk.length;
        if (state.size > 8 * 1024 * 1024) throw new Error("Oversize snapshot");
        state.chunks.push(chunk);
        if (state.chunks.length === state.parts) {
          const all = new Uint8Array(state.size);
          let at = 0;
          for (const c of state.chunks) {
            all.set(c, at);
            at += c.length;
          }
          this.fragments = undefined;
          const messages = JSON.parse(decoder.decode(all));
          if (
            !Array.isArray(messages) ||
            messages.some((m) => m.type === "fragment")
          )
            throw new Error("Invalid snapshot");
          this.deliver(messages);
        }
        continue;
      }
      if (m.type === "out" && m.encoding === "zstd") {
        const chunks: Uint8Array[] = [];
        let size = 0;
        const zstd = new Decompress((chunk) => {
          size += chunk.length;
          if (size > 8 * 1024 * 1024) throw new Error("Oversize screen");
          chunks.push(chunk);
        });
        zstd.push(from64(m.data as string), true);
        const all = new Uint8Array(size);
        let at = 0;
        for (const c of chunks) {
          all.set(c, at);
          at += c.length;
        }
        m.data = decoder.decode(all);
      }
      if (m.type.startsWith("transcript:")) {
        const previous = this.transcriptRevisions.get(m.type);
        if (previous !== undefined && m.revision !== previous + 1 && !this.resyncing.has(m.type)) {
          this.resyncing.add(m.type);
          this.send([{ type: "resync", pane: m.type }]);
          continue;
        }
        this.resyncing.delete(m.type);
        if (m.revision !== undefined) this.transcriptRevisions.set(m.type, m.revision);
      }
      if (m.type === "hello") clearTimeout(this.timer);
      this.receive(m);
    }
  }
  send(messages: InnerMessage[]): void {
    if (!this.ready || this.ws?.readyState !== WebSocket.OPEN)
      throw new Error("Session unavailable");
    const plain = encodeMessages(messages);
    this.ws.send(frame(this.saved.phone, this.handshake.split().send.encrypt(plain)));
  }
  subscribe(pane: string, mode: "focus" | "summary" = "focus"): void {
    this.send([{ type: "sub", pane, mode }]);
    this.subscriptions.set(pane, mode);
  }
  // Stop renewal; the machine expires the last lease within 60 seconds.
  unsubscribe(pane: string): void {
    this.subscriptions.delete(pane);
  }
  close(): void {
    this.fail("Session closed");
  }
  private fail(reason: string): void {
    if (this.ended) return;
    this.ended = true;
    clearInterval(this.ping);
    clearInterval(this.renew);
    clearTimeout(this.pong);
    clearTimeout(this.timer);
    document.removeEventListener("visibilitychange", this.hidden);
    this.ready = false;
    if (this.ws) {
      this.ws.onclose = null;
      this.ws.onmessage = null;
      this.ws.onerror = null;
      this.ws.close();
    }
    this.closed(reason);
  }
}
