import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Inputs } from "./input";
import { encodeMessages, type InnerMessage } from "./session";

describe("input frame validation before encryption", () => {
  const command = (data: string): InnerMessage[] => [{ type: "input", id: "random-id", pane: "%1", kind: "paste", data }];
  it("accepts the byte boundary, Unicode, tab and multiline input", () => {
    expect(encodeMessages(command("a".repeat(24 * 1024))).length).toBeLessThan(65519);
    expect(() => encodeMessages(command("a\tb\n😀"))).not.toThrow();
  });
  it("bounds UTF-8 bytes rather than string length, including chat", () => {
    expect(() => encodeMessages(command("😀".repeat(6145)))).toThrow("24 KiB");
    expect(() => encodeMessages([{ type: "chat_send", id: "random-id", text: "x".repeat(24577) }])).toThrow("24 KiB");
  });
  it("checks escaped final frames, including batches, before consuming a nonce", () => {
    expect(() => encodeMessages(command("<".repeat(24000)))).toThrow("transport frame");
    expect(() => encodeMessages([...command("a".repeat(24000)), ...command("a".repeat(24000)), ...command("a".repeat(24000))])).toThrow("transport frame");
  });
  it("refuses control characters while preserving literal tmux-looking payloads", () => {
    for (const data of ["\0", "\r", "\x1b", "\x7f", "\x85"]) expect(() => encodeMessages(command(data))).toThrow("Control character");
    expect(JSON.parse(new TextDecoder().decode(encodeMessages(command("-t %other ; Enter"))))[0].data).toBe("-t %other ; Enter");
  });
});

// A small DOM adapter exercises composer state in the Worker test environment.
// Actual DOM/layout and encrypted transport remain covered by Playwright.
class InputElement {
  children: InputElement[] = [];
  textContent = "";
  value = "";
  onclick?: () => void;
  oninput?: () => void;
  setAttribute() {}
  appendChild(child: InputElement) { this.children.push(child); }
  replaceChildren() { this.children = []; }
  button(label: string): InputElement {
    if (this.textContent === label) return this;
    for (const child of this.children) {
      try { return child.button(label); } catch {}
    }
    throw new Error(`No button ${label}`);
  }
  contains(text: string): boolean { return this.textContent.includes(text) || this.children.some(child => child.contains(text)); }
}


describe("composer delivery and drafts", () => {
  let storage: Map<string, string>;
  beforeEach(() => {
    vi.useFakeTimers();
    storage = new Map();
    vi.stubGlobal("localStorage", {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    });
    vi.stubGlobal("document", { createElement: () => new InputElement() });
    vi.stubGlobal("confirm", vi.fn(() => true));
  });
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });
  const draftKey = "partyline.draft.pc.%1";
  const inputKey = "partyline.input.pc.%1";
  const setup = (send = vi.fn()) => {
    const inputs = new Inputs(send);
    const box = inputs.composer("pc", "%1") as unknown as InputElement;
    const textarea = box.children[1];
    const fill = (text: string) => { textarea.value = text; textarea.oninput!(); };
    return { inputs, box, textarea, fill, send };
  };
  const reply = (inputs: Inputs, message: InnerMessage, status: "ok" | "refused", reason?: string) =>
    inputs.result("pc", { type: "input_result", id: message.id, status, reason });

  it("allows rapid keys before confirmation and tracks out-of-order replies by ID", () => {
    const { inputs, box, send } = setup();
    for (const label of ["↑", "↑", "esc", "ctrl-c"]) box.button(label).onclick!();
    expect(send).toHaveBeenCalledTimes(4);
    const messages = send.mock.calls.map(call => call[1] as InnerMessage);
    expect(new Set(messages.map(message => message.id)).size).toBe(4);
    for (const message of messages.slice(1).reverse()) reply(inputs, message, "ok");
    expect(box.contains("Waiting for confirmation")).toBe(true);
    reply(inputs, messages[0], "ok");
    vi.advanceTimersByTime(5000);
    expect(box.contains("Confirmed")).toBe(true);
    expect(box.contains("Not confirmed")).toBe(false);
  });

  it("blocks on unknown delivery, preserves every pending ID across reload, and never resends automatically", () => {
    const { inputs, box, send } = setup();
    box.button("↑").onclick!(); box.button("esc").onclick!();
    inputs.disconnected("pc");
    box.button("ctrl-c").onclick!();
    expect(send).toHaveBeenCalledTimes(2);
    const reload = setup();
    expect(reload.send).not.toHaveBeenCalled();
    expect(reload.box.contains("Not confirmed")).toBe(true);
    reload.box.button("Retry").onclick!();
    expect(reload.send.mock.calls[0][1].id).toBe(send.mock.calls[0][1].id);
    reply(reload.inputs, send.mock.calls[0][1], "ok");
    expect(reload.box.contains("Not confirmed")).toBe(true);
    reply(reload.inputs, send.mock.calls[1][1], "ok");
    expect(reload.box.contains("Confirmed")).toBe(true);
    expect(JSON.parse(storage.get(inputKey)!)).toHaveLength(1);
  });

  it("shows a local send failure as not sent and lets a new command proceed", () => {
    const send = vi.fn().mockImplementationOnce(() => { throw new Error("Session unavailable"); });
    const { box, fill, textarea } = setup(send);
    fill("keep this draft"); box.button("Send").onclick!();
    expect(box.contains("Not sent: Error: Session unavailable")).toBe(true);
    expect(box.contains("delivery unknown")).toBe(false);
    expect(textarea.value).toBe("keep this draft");
    box.button("↑").onclick!();
    expect(send).toHaveBeenCalledTimes(2);
  });

  it("keeps uncertainty when retrying a previously sent command fails locally", () => {
    const { box, send } = setup();
    box.button("↑").onclick!();
    vi.advanceTimersByTime(5000);
    send.mockImplementationOnce(() => { throw new Error("Session unavailable"); });
    box.button("Retry").onclick!();
    expect(box.contains("delivery unknown")).toBe(true);
    box.button("esc").onclick!();
    expect(send).toHaveBeenCalledTimes(2);
  });

  it("retains refused drafts through reload and clears them only after ok", () => {
    const { inputs, box, send, fill, textarea } = setup();
    fill("rejected text"); box.button("Send").onclick!();
    expect(textarea.value).toBe("rejected text");
    expect(storage.get(draftKey)).toBe("rejected text");
    reply(inputs, send.mock.calls[0][1], "refused", "rate limit");
    const reload = setup();
    expect(reload.textarea.value).toBe("rejected text");
    reload.box.button("Send").onclick!();
    reply(reload.inputs, reload.send.mock.calls[0][1], "ok");
    expect(reload.textarea.value).toBe("");
    expect(storage.has(draftKey)).toBe(false);
  });

  it("asks before retrying an unconfirmed command, and sends nothing when told no", () => {
    const { box, send } = setup();
    box.button("↑").onclick!();
    vi.advanceTimersByTime(5000);
    vi.mocked(confirm).mockReturnValueOnce(false);
    box.button("Retry").onclick!();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledTimes(1);
    box.button("Retry").onclick!();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[1][1].id).toBe(send.mock.calls[0][1].id);
  });

  it("retries a command that was never sent without asking", () => {
    const send = vi.fn().mockImplementationOnce(() => { throw new Error("Session unavailable"); });
    const { box, fill } = setup(send);
    fill("hello"); box.button("Send").onclick!();
    box.button("Retry").onclick!();
    expect(confirm).not.toHaveBeenCalled();
    expect(send).toHaveBeenCalledTimes(2);
  });

  it("sends a draft once however often Send is tapped before it is confirmed; edited text and keys still go", () => {
    const { inputs, box, fill, send } = setup();
    fill("run the tests"); box.button("Send").onclick!(); box.button("Send").onclick!();
    expect(send).toHaveBeenCalledTimes(1);
    expect(box.contains("Already sent")).toBe(true);
    box.button("esc").onclick!(); box.button("esc").onclick!();
    expect(send).toHaveBeenCalledTimes(3);
    fill("run the tests, then lint"); box.button("Send").onclick!();
    expect(send).toHaveBeenCalledTimes(4);
    reply(inputs, send.mock.calls[0][1], "ok");
    fill("run the tests"); box.button("Send").onclick!();
    expect(send).toHaveBeenCalledTimes(5);
  });

  it("does not erase a draft edited while the previous text waits for confirmation", () => {
    const { inputs, box, fill, send, textarea } = setup();
    fill("first"); box.button("Send").onclick!(); fill("next draft");
    reply(inputs, send.mock.calls[0][1], "ok");
    expect(textarea.value).toBe("next draft");
    expect(storage.get(draftKey)).toBe("next draft");
  });
});
