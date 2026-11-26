import { base64 } from "@scure/base";
import type { StreamEvent, StreamHandle } from "../runtime/terminal-stream";
/** Debug-only native keyboard fixture. It never contacts a shell or computer. */
export function openPreviewStream(
  receive: (event: StreamEvent) => void,
): StreamHandle {
  let active = true,
    sequence = 0;
  const timers = new Set<ReturnType<typeof setTimeout>>();
  const later = (fn: () => void) => {
    const timer = setTimeout(() => {
      timers.delete(timer);
      if (active) fn();
    }, 20);
    timers.add(timer);
  };
  const output = (bytes: Uint8Array) =>
    receive({
      type: "output",
      sequence: ++sequence,
      data: base64.encode(bytes),
    });
  later(() => {
    receive({ type: "ready", session: "aa".repeat(16) });
    output(
      new TextEncoder().encode(
        "PARTYLINE · streaming keyboard fixture\r\n\r\n$ ",
      ),
    );
  });
  return {
    input(data) {
      if (!active) return;
      const raw = base64.decode(data);
      later(() => output(raw));
    },
    ack() {},
    resize() {},
    close() {
      active = false;
      for (const timer of timers) clearTimeout(timer);
      timers.clear();
    },
  };
}
