import { encodeMessages } from "./input-protocol";
import type { InnerMessage } from "./session";

const uncertain = (command?: Command) => command?.state === "unknown" || command?.reason?.includes("delivery may be partial");

type Command = { message: InnerMessage; state: "waiting" | "unknown" | "ok" | "refused" | "not_sent"; reason?: string; draft?: string };
export class Inputs {
  private commands = new Map<string, Map<string, Command>>();
  private views = new Map<string, (confirmedDraft?: string) => void>();
  constructor(private send: (computer: string, message: InnerMessage) => void) {}
  private key(computer: string, pane: string) { return `partyline.input.${computer}.${pane}`; }
  private get(key: string): Map<string, Command> {
    if (!this.commands.has(key)) {
      const commands = new Map<string, Command>();
      try {
        const saved = JSON.parse(localStorage.getItem(key) || "null");
        // Read the single-command format saved by earlier versions too.
        for (const command of (Array.isArray(saved) ? saved : saved ? [saved] : []) as Command[]) {
          if (!command?.message?.id) continue;
          if (command.state === "waiting") command.state = "unknown";
          commands.set(command.message.id, command);
        }
      } catch { /* Damaged storage cannot send a command. */ }
      this.commands.set(key, commands);
    }
    return this.commands.get(key)!;
  }
  private save(key: string, command: Command) {
    const commands = this.get(key);
    commands.set(command.message.id!, command);
    // Keep unresolved commands and the latest result, without growing storage forever.
    for (const [id, old] of commands) {
      if (id !== command.message.id && !uncertain(old) && old.state !== "waiting") commands.delete(id);
    }
    let persisted = true;
    try { localStorage.setItem(key, JSON.stringify([...commands.values()])); } catch { persisted = false; }
    this.views.get(key)?.();
    return persisted;
  }
  result(computer: string, message: InnerMessage) {
    for (const [key, commands] of this.commands) {
      const command = commands.get(message.id!);
      if (key.startsWith(`partyline.input.${computer}.`) && command &&
          (message.status === "ok" || message.status === "refused")) {
        this.save(key, { ...command, state: message.status, reason: message.reason });
        if (message.status === "ok" && command.draft !== undefined) {
          const draftKey = key.replace("partyline.input.", "partyline.draft.");
          try {
            if (localStorage.getItem(draftKey) === command.draft) localStorage.removeItem(draftKey);
          } catch {}
          this.views.get(key)?.(command.draft);
        }
      }
    }
  }
  disconnected(computer: string) {
    for (const [key, commands] of this.commands) {
      if (!key.startsWith(`partyline.input.${computer}.`)) continue;
      for (const command of commands.values()) {
        if (command.state === "waiting") this.save(key, { ...command, state: "unknown" });
      }
    }
  }
  composer(computer: string, pane = "chat"): HTMLElement {
    const key = this.key(computer, pane), draftKey = `partyline.draft.${computer}.${pane}`;
    this.get(key);
    const box = document.createElement("section");
    box.className = "composer";
    box.setAttribute("aria-label", pane === "chat" ? "Chat composer" : "Session composer");
    const textarea = document.createElement("textarea");
    textarea.rows = 3;
    textarea.setAttribute("aria-label", "Message");
    try { textarea.value = localStorage.getItem(draftKey) || ""; } catch {}
    textarea.onfocus = () => { document.body.dataset.composing = "true"; };
    textarea.onblur = () => { delete document.body.dataset.composing; };
    textarea.oninput = () => { try { localStorage.setItem(draftKey, textarea.value); } catch {} };
    const status = document.createElement("div");
    status.setAttribute("role", "status");
    const actions = document.createElement("div");
    const addButton = (parent: HTMLElement, label: string, action: () => void) => {
      const b = document.createElement("button"); b.type = "button"; b.textContent = label;
      b.onclick = action; parent.appendChild(b); return b;
    };
    const transmit = (command: Command) => {
      try {
        encodeMessages([command.message]);
        if (!this.save(key, { ...command, state: "waiting", reason: undefined }))
          throw new Error("Cannot save command ID; storage is unavailable");
        this.send(computer, command.message);
        setTimeout(() => {
          const current = this.get(key).get(command.message.id!);
          if (current?.state === "waiting")
            this.save(key, { ...current, state: "unknown" });
        }, 5000);
        return true;
      } catch (e) {
        this.save(key, { ...command, state: uncertain(command) ? "unknown" : "not_sent", reason: String(e) });
        return false;
      }
    };
    const submit = (kind: "text" | "key" | "paste", data: string) => {
      const pending = [...this.get(key).values()];
      if (pending.some(uncertain)) {
        status.textContent = "Previous command not confirmed. Retry it or acknowledge the warning before sending a new command.";
        return;
      }
      // A second tap on Send before the first is confirmed would send the same text again under a
      // new ID, which the computer cannot tell apart. Keys repeat on purpose and are not held back.
      if (kind !== "key" && pending.some(c => c.state === "waiting" && c.draft === textarea.value)) {
        status.textContent = "Already sent; waiting for confirmation.";
        return;
      }
      const message: InnerMessage = pane === "chat"
        ? { type: "chat_send", id: crypto.randomUUID(), text: data }
        : { type: "input", id: crypto.randomUUID(), pane, kind, data };
      try { encodeMessages([message]); } catch (e) { status.textContent = String(e); return; }
      transmit({ message, state: "waiting", draft: kind === "key" ? undefined : textarea.value });
    };
    if (pane !== "chat") {
      const keys = document.createElement("div"); keys.className = "key-row";
      for (const [label, keyName] of [["esc", "Escape"], ["ctrl-c", "C-c"], ["tab", "Tab"], ["↑", "Up"], ["↓", "Down"], ["←", "Left"], ["→", "Right"], ["⏎", "Enter"], ["ctrl-r", "C-r"]]) {
        const b = addButton(keys, label, () => submit("key", keyName));
        b.onpointerdown = event => event.preventDefault();
      }
      box.appendChild(keys);
    }
    const drawStatus = (confirmedDraft?: string) => {
      if (confirmedDraft !== undefined && textarea.value === confirmedDraft) textarea.value = "";
      const commands = [...this.get(key).values()];
      const command = commands.find(uncertain) || commands.find(c => c.state === "waiting") || commands.at(-1);
      status.textContent = command ? ({ waiting: "Waiting for confirmation…", unknown: "Not confirmed — delivery unknown", ok: "Confirmed", refused: "Refused", not_sent: "Not sent" }[command.state]) + (command.reason ? `: ${command.reason}` : "") : "";
      actions.replaceChildren();
      if (command && (uncertain(command) || command.state === "not_sent")) {
        // Reusing the ID only helps while the computer remembers it: after a restart, or once the ID
        // has aged out of its record, a retry of an unconfirmed command runs it again.
        addButton(actions, "Retry", () => {
          if (uncertain(command) && !confirm("Delivery of this command was not confirmed. If it did arrive and the computer has since restarted, retrying runs it a second time. Retry?")) return;
          transmit(command);
        });
      }
      if (command && uncertain(command)) {
        addButton(actions, "Send a new command…", () => {
          if (!confirm("Delivery cannot be established. The computer may have restarted or forgotten this ID. A new command could run the same action twice. Continue?")) return;
          const commands = this.get(key);
          // Acknowledge only this uncertainty; other pending IDs stay tracked.
          commands.delete(command.message.id!);
          localStorage.setItem(key, JSON.stringify([...commands.values()])); drawStatus();
        });
      }
    };
    this.views.set(key, drawStatus);
    box.appendChild(textarea);
    addButton(box, "Send", () => submit("text", textarea.value));
    if (pane !== "chat") {
      addButton(box, "Paste", () => submit("paste", textarea.value));
      const warning = document.createElement("small");
      warning.textContent = "Paste can run lines in a shell unless the program enabled bracketed paste.";
      box.appendChild(warning);
    }
    box.appendChild(status); box.appendChild(actions); drawStatus();
    return box;
  }
}
