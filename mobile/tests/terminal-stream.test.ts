import { afterEach, expect, it, vi } from "vitest";
import {
  TerminalStreams,
  type StreamEvent,
} from "../src/runtime/terminal-stream";
import type { InnerMessage } from "../src/runtime/protocol/session";
import { base64 } from "@scure/base";
const wire: InnerMessage[] = [],
  events: StreamEvent[] = [];
let streams: TerminalStreams;
const epoch = "ab".repeat(24),
  session = "cd".repeat(16),
  encode = (s: string) => base64.encode(new TextEncoder().encode(s));
function setup() {
  wire.length = events.length = 0;
  streams = new TerminalStreams(
    (_, m) => wire.push(m),
    () => {},
  );
  const h = streams.open(
    "mac",
    undefined,
    80,
    24,
    (e) => events.push(e),
    "stable-create",
  );
  streams.message("mac", {
    type: "term_result",
    id: "stable-create",
    pane: session,
    status: "ok",
    data: { session, attachment: epoch },
  });
  return h;
}
afterEach(() => {
  streams?.dispose();
  vi.useRealTimers();
});
it("waits for parser ACK, preserves byte output, and sends input immediately", () => {
  const h = setup();
  h.input(encode("ls\r"));
  expect(wire.find((m) => m.type === "term_input")).toMatchObject({
    id: epoch,
    pane: session,
    revision: 1,
    data: encode("ls\r"),
  });
  streams.message("mac", {
    type: "term_output",
    id: epoch,
    pane: session,
    revision: 1,
    data: base64.encode(new Uint8Array([0xff, 0, 27])),
  });
  expect(events.at(-1)).toEqual({ type: "output", sequence: 1, data: "/wAb" });
  expect(wire.some((m) => m.type === "term_ack")).toBe(false);
  h.ack(1);
  expect(wire.at(-1)).toMatchObject({ type: "term_ack", revision: 1 });
});
it("rejects output gaps and discards input on disconnect without replay", () => {
  const h = setup();
  h.input(encode("pending"));
  streams.disconnected("mac");
  expect(() => h.input(encode("lost"))).not.toThrow();
  const count = wire.filter((m) => m.type === "term_input").length;
  expect(count).toBe(1);
  streams.open("mac", session, 80, 24, (e) => events.push(e));
  expect(wire.filter((m) => m.type === "term_input")).toHaveLength(1);
  const open = wire.at(-1)!;
  streams.message("mac", {
    type: "term_result",
    id: open.id,
    status: "ok",
    data: { session, attachment: epoch },
  });
  streams.message("mac", {
    type: "term_output",
    id: epoch,
    pane: session,
    revision: 2,
    data: encode("gap"),
  });
  expect(events.at(-1)?.type).toBe("closed");
});
it("ignores stale attachment output and rejects future input acknowledgements", () => {
  setup();
  streams.message("mac", {
    type: "term_output",
    id: "old",
    pane: session,
    revision: 1,
    data: encode("stale"),
  });
  expect(events.filter((e) => e.type === "output")).toHaveLength(0);
  streams.message("mac", {
    type: "term_input_ack",
    id: epoch,
    pane: session,
    revision: 1,
  });
  expect(events.at(-1)?.type).toBe("closed");
});
it("times out unconfirmed input and reuses a stable creation request", () => {
  vi.useFakeTimers();
  const h = setup();
  h.input(encode("x"));
  vi.advanceTimersByTime(10250);
  expect(events.at(-1)?.type).toBe("closed");
  streams.open("mac", undefined, 80, 24, () => {}, "stable-create");
  expect(wire.at(-1)).toMatchObject({ type: "term_open", id: "stable-create" });
});

it("detaches an old visible terminal before opening another on the same computer", () => {
  setup();
  streams.open("mac", undefined, 80, 24, () => {});
  expect(wire.slice(-2).map((m) => m.type)).toEqual([
    "term_detach",
    "term_open",
  ]);
});

it("holds early viewport changes until the attachment exists", () => {
  wire.length = events.length = 0;
  streams = new TerminalStreams(
    (_, m) => wire.push(m),
    () => {},
  );
  const h = streams.open(
    "mac",
    undefined,
    80,
    24,
    (e) => events.push(e),
    "early",
  );
  h.resize(45, 12);
  expect(events).toHaveLength(0);
  expect(wire).toHaveLength(1);
  streams.message("mac", {
    type: "term_result",
    id: "early",
    status: "ok",
    data: { session, attachment: epoch },
  });
  expect(wire.find((m) => m.type === "term_resize")).toMatchObject({
    id: epoch,
    pane: session,
    data: { columns: 45, rows: 12 },
  });
  expect(events.at(-1)).toEqual({ type: "ready", session });
});
