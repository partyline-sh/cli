import { readInputFeedback } from "./input-feedback";
import type { InnerMessage } from "./protocol/session";
import { encodeMessages } from "./protocol/input-protocol";
import type { Delivery } from "./types";

export interface PendingInput {
  message: InnerMessage;
  delivery: Delivery;
  draft?: string;
}
export class InputQueue {
  private entries = new Map<string, PendingInput>();
  private timers = new Map<string, ReturnType<typeof setTimeout>>();
  constructor(
    private send: (computer: string, message: InnerMessage) => void,
    private persist: (entries: PendingInput[]) => Promise<void>,
    private changed: () => void,
    private queued?: (entry: PendingInput) => void,
  ) {}
  restore(entries: PendingInput[]) {
    for (const entry of entries) {
      if (!entry.message.id || !entry.delivery?.computerId) continue;
      if (entry.delivery.status === "waiting")
        entry.delivery.status = "unknown";
      this.entries.set(entry.message.id, entry);
    }
    this.changed();
  }
  saved() {
    return [...this.entries.values()];
  }
  deliveries() {
    return this.saved().map((entry) => entry.message.type === 'input' && entry.message.kind === 'text' && typeof entry.message.data === 'string'
      ? { ...entry.delivery, prompt: entry.delivery.prompt ?? { text: entry.message.data } }
      : entry.message.type === 'chat_send' && typeof entry.message.text === 'string'
      ? { ...entry.delivery, prompt: { text: entry.message.text } }
      : entry.delivery);
  }
  async submit(
    computerId: string,
    pane: string,
    message: InnerMessage,
    previousMatches?: number,
  ): Promise<Delivery> {
    if (
      this.saved().some(
        (e) =>
          e.delivery.computerId === computerId &&
          e.delivery.pane === pane &&
          e.delivery.status === "unknown",
      )
    )
      throw new Error(
        "A previous command is not confirmed. Resolve it before sending another.",
      );
    if (message.kind !== "key") {
      const same = this.saved().find(
        (e) =>
          e.delivery.computerId === computerId &&
          e.delivery.pane === pane &&
          e.delivery.status === "waiting" &&
          e.message.type === message.type &&
          e.message.kind === message.kind &&
          e.message.target === message.target &&
          e.message.data === message.data &&
          e.message.text === message.text,
      );
      if (same) {
        if (this.queued) {
          this.queued(same);
          void this.persist(this.saved()).catch(() => {});
        }
        return same.delivery;
      }
    }
    if (
      (message.type === "chat_send" || message.kind === "text") &&
      !String(message.text ?? message.data ?? "").trim()
    )
      throw new Error("Enter a message first");
    encodeMessages([message]);
    const entry: PendingInput = {
      message,
      delivery: { id: message.id!, computerId, pane, status: "waiting",
        ...(message.target ? { target: message.target } : {}),
        ...(message.type === 'input' && message.kind === 'text' && typeof message.data === 'string'
          ? { prompt: { text: message.data, previousMatches } } : {}),
      },
    };
    this.entries.set(message.id!, entry);
    return this.transmit(entry);
  }
  private async transmit(entry: PendingInput): Promise<Delivery> {
    const previous = entry.delivery.status;
    entry.delivery = {
      ...entry.delivery,
      status: "waiting",
      reason: undefined,
    };
    try {
      this.queued?.(entry);
      this.changed();
      // Persist identity before an effect can leave the phone.
      await this.persist(this.saved());
      if (!this.entries.has(entry.message.id!))
        throw new Error("Computer was forgotten");
      this.send(entry.delivery.computerId, entry.message);
      clearTimeout(this.timers.get(entry.message.id!));
      this.timers.set(
        entry.message.id!,
        setTimeout(() => {
          if (entry.delivery.status === "waiting") {
            entry.delivery = { ...entry.delivery, status: "unknown" };
            void this.persist(this.saved()).catch(() => {});
            this.changed();
          }
        }, 5000),
      );
    } catch (error) {
      entry.delivery = {
        ...entry.delivery,
        status: previous === "unknown" ? "unknown" : "not-sent",
        reason: String(error),
      };
      this.changed();
      await this.persist(this.saved()).catch(() => {});
    }
    return entry.delivery;
  }
  async retry(id: string, acknowledgeRisk: boolean) {
    const entry = this.entries.get(id);
    if (!entry) throw new Error("Command no longer available");
    if (
      entry.delivery.status !== "unknown" &&
      entry.delivery.status !== "not-sent"
    )
      throw new Error("This command cannot be retried");
    if (entry.delivery.status === "unknown" && !acknowledgeRisk)
      throw new Error(
        "Retry may execute this command twice; confirm the warning first",
      );
    return this.transmit(entry);
  }
  acknowledge(id: string) {
    const entry = this.entries.get(id);
    if (entry?.delivery.status !== "unknown") return;
    this.entries.delete(id);
    clearTimeout(this.timers.get(id));
    this.timers.delete(id);
    this.changed();
    void this.persist(this.saved()).catch(() => {});
  }
  result(computerId: string, message: InnerMessage) {
    const entry = this.entries.get(message.id!);
    if (
      !entry ||
      entry.delivery.computerId !== computerId ||
      !["ok", "refused"].includes(message.status!)
    )
      return;
    clearTimeout(this.timers.get(message.id!));
    this.timers.delete(message.id!);
    const partial = message.reason?.includes("delivery may be partial");
    entry.delivery = {
      ...entry.delivery,
      status: partial
        ? "unknown"
        : message.status === "ok"
          ? "confirmed"
          : "refused",
      reason: message.reason,
      data: message.status === "ok" ? message.data : undefined,
    };
    // Keep unresolved commands and a bounded tail of final results.
    const final = this.saved().filter(
      (e) => !["waiting", "unknown"].includes(e.delivery.status),
    );
    for (const old of final.slice(0, -50)) this.entries.delete(old.message.id!);
    this.changed();
    void this.persist(this.saved()).catch(() => {});
  }
  feedback(computerId: string, message: InnerMessage) {
    const entry = this.entries.get(message.id!);
    const data = readInputFeedback(message.data);
    if (!entry || entry.delivery.computerId !== computerId || entry.delivery.status !== "confirmed" || !data ||
      !(entry.message.type === "input_submit" || entry.message.type === "input" && entry.message.kind === "text")) return;
    entry.delivery = { ...entry.delivery, data };
    void this.persist(this.saved()).catch(() => {});
    this.changed();
  }
  disconnected(computerId: string) {
    for (const entry of this.entries.values())
      if (
        entry.delivery.computerId === computerId &&
        entry.delivery.status === "waiting"
      ) {
        entry.delivery = { ...entry.delivery, status: "unknown" };
        clearTimeout(this.timers.get(entry.message.id!));
        this.timers.delete(entry.message.id!);
      }
    void this.persist(this.saved()).catch(() => {});
    this.changed();
  }
  forget(computerId: string) {
    for (const entry of this.saved())
      if (entry.delivery.computerId === computerId) {
        clearTimeout(this.timers.get(entry.message.id!));
        this.timers.delete(entry.message.id!);
        this.entries.delete(entry.message.id!);
      }
    this.changed();
  }
  dispose() {
    for (const timer of this.timers.values()) clearTimeout(timer);
    this.timers.clear();
  }
}
