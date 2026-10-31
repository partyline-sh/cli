import { afterEach, describe, expect, it, vi } from "vitest";
import { InputQueue, type PendingInput } from "../src/runtime/input";
const command = (
  id: string,
  kind: "text" | "paste" | "key" = "text",
  data = "hello",
) => ({ type: "input", id, pane: "%1", kind, data });
afterEach(() => vi.useRealTimers());
describe("native delivery semantics", () => {
  it("coalesces duplicate pending text, paste and chat but permits repeated keys and edited drafts", async () => {
    const sent = vi.fn();
    const q = new InputQueue(
      sent,
      async () => {},
      () => {},
    );
    const first = await q.submit("pc", "%1", command("a"));
    expect(await q.submit("pc", "%1", command("b"))).toBe(first);
    await q.submit("pc", "%1", command("c", "text", "edited"));
    await q.submit("pc", "%1", command("d", "key", "Up"));
    await q.submit("pc", "%1", command("e", "key", "Up"));
    await q.submit("pc", "%1", command("f", "paste", "paste"));
    await q.submit("pc", "%1", command("g", "paste", "paste"));
    await q.submit("pc", "chat", { type: "chat_send", id: "h", text: "chat" });
    await q.submit("pc", "chat", { type: "chat_send", id: "i", text: "chat" });
    expect(sent).toHaveBeenCalledTimes(6);
    q.dispose();
  });
  it("never replays after reconnect/reload and requires a warning before an uncertain retry", async () => {
    vi.useFakeTimers();
    const sent = vi.fn();
    let saved: PendingInput[] = [];
    const q = new InputQueue(
      sent,
      async (e) => {
        saved = structuredClone(e);
      },
      () => {},
    );
    await q.submit("pc", "%1", command("same-id"));
    q.disconnected("pc");
    const reloadSend = vi.fn();
    const reload = new InputQueue(
      reloadSend,
      async () => {},
      () => {},
    );
    reload.restore(saved);
    expect(reloadSend).not.toHaveBeenCalled();
    await expect(reload.retry("same-id", false)).rejects.toThrow("twice");
    expect(reloadSend).not.toHaveBeenCalled();
    await reload.retry("same-id", true);
    expect(reloadSend.mock.calls[0][1].id).toBe("same-id");
    await vi.advanceTimersByTimeAsync(5000);
    expect(reload.deliveries()[0].status).toBe("unknown");
    await expect(reload.submit("pc", "%1", command("new"))).rejects.toThrow(
      "not confirmed",
    );
    reload.acknowledge("same-id");
    await reload.submit("pc", "%1", command("new"));
    expect(reloadSend).toHaveBeenCalledTimes(2);
    q.dispose();
    reload.dispose();
  });
  it("requires persisted identity before sending and bounds escaped frames before effects", async () => {
    const sent = vi.fn();
    const q = new InputQueue(
      sent,
      async () => {
        throw new Error("disk full");
      },
      () => {},
    );
    expect((await q.submit("pc", "%1", command("no"))).status).toBe("not-sent");
    expect(sent).not.toHaveBeenCalled();
    await expect(
      q.submit("pc", "%1", command("too-large", "paste", "<".repeat(24000))),
    ).rejects.toThrow("transport frame");
    expect(sent).not.toHaveBeenCalled();
    q.dispose();
  });
  it("correlates out-of-order results by computer and id, and marks partial effects unknown", async () => {
    const q = new InputQueue(
      vi.fn(),
      async () => {},
      () => {},
    );
    await q.submit("pc", "%1", command("a", "key", "Up"));
    await q.submit("pc", "%1", command("b", "key", "Up"));
    q.result("other", { type: "input_result", id: "a", status: "ok" });
    expect(q.deliveries()[0].status).toBe("waiting");
    q.result("pc", { type: "input_result", id: "b", status: "ok" });
    q.result("pc", {
      type: "input_result",
      id: "a",
      status: "refused",
      reason: "tmux failed; delivery may be partial",
    });
    expect(q.deliveries().map((d) => d.status)).toEqual([
      "unknown",
      "confirmed",
    ]);
    q.dispose();
  });
});
