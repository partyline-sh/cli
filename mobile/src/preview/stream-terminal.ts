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
        "PARTYLINE · remote terminal\r\n\r\n\u001b[38;2;255;94;138m~/projects/storefront\u001b[0m\r\n\r\n$ git status\r\nOn branch feature/checkout\r\nYour branch is up to date.\r\n\r\nChanges ready to commit:\r\n  checkout/payment.ts\r\n  checkout/payment.test.ts\r\n\r\n$ npm test -- checkout\r\n\r\n\u001b[32m✓\u001b[0m failed requests can be retried\r\n\u001b[32m✓\u001b[0m duplicate payments are refused\r\n\u001b[32m✓\u001b[0m successful payments are confirmed\r\n\r\n\u001b[32m3 checks passed\u001b[0m\r\n\r\n$ ",
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
