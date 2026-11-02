import { base64 } from "@scure/base";
import type { InnerMessage } from "./protocol/session";
export interface PhoneTerminal {
  computerId: string;
  id: string;
  title: string;
  lastAttached: string;
}
export type StreamEvent =
  | { type: "ready"; session: string }
  | { type: "output"; data: string; sequence: number }
  | { type: "closed"; reason: string };
export interface StreamHandle {
  input(data: string): void;
  ack(sequence: number): void;
  resize(columns: number, rows: number): void;
  close(): void;
}
interface Entry {
  computer: string;
  request: string;
  session?: string;
  epoch?: string;
  input: number;
  accepted: number;
  output: number;
  acked: number;
  pending: Map<number, number>;
  receive: (event: StreamEvent) => void;
  deadline: number;
  viewport?: { columns: number; rows: number };
}
export class TerminalStreams {
  private entries = new Map<string, Entry>();
  private lists = new Map<string, string>();
  private endings = new Map<
    string,
    {
      computer: string;
      resolve: () => void;
      reject: (e: Error) => void;
      timer: ReturnType<typeof setTimeout>;
    }
  >();
  private timer?: ReturnType<typeof setInterval>;
  constructor(
    private send: (computer: string, message: InnerMessage) => void,
    private listed: (computer: string, rows: PhoneTerminal[]) => void,
  ) {}
  endSession(computer: string, session: string): Promise<void> {
    return new Promise((resolve, reject) => {
      const id = crypto.randomUUID();
      const timer = setTimeout(() => {
        this.endings.delete(id);
        reject(
          Error(
            "Terminal end outcome unknown; refresh the list before retrying",
          ),
        );
      }, 10000);
      this.endings.set(id, { computer, resolve, reject, timer });
      try {
        this.send(computer, { type: "term_close", id, pane: session });
      } catch (error) {
        clearTimeout(timer);
        this.endings.delete(id);
        reject(error);
      }
    });
  }
  list(computer: string) {
    const id = crypto.randomUUID();
    this.lists.set(computer, id);
    try {
      this.send(computer, { type: "term_list", id });
    } catch {
      this.lists.delete(computer);
    }
  }
  open(
    computer: string,
    session: string | undefined,
    columns: number,
    rows: number,
    receive: Entry["receive"],
    requestID?: string,
  ): StreamHandle {
    const previous = this.entries.get(computer);
    if (previous)
      this.end(previous, "Terminal replaced; unsent input discarded");
    const e: Entry = {
      computer,
      request: requestID || crypto.randomUUID(),
      session,
      input: 0,
      accepted: 0,
      output: 0,
      acked: 0,
      pending: new Map(),
      receive,
      deadline: Date.now() + 10000,
    };
    this.entries.set(computer, e);
    if (!this.timer)
      this.timer = setInterval(() => {
        for (const item of this.entries.values())
          if (Date.now() > item.deadline && (!item.epoch || item.pending.size))
            this.end(
              item,
              "Terminal response timed out; unconfirmed input was not replayed",
            );
      }, 250);
    try {
      this.send(computer, {
        type: "term_open",
        id: e.request,
        data: { session, columns, rows },
      });
    } catch (error) {
      this.end(e, String(error));
    }
    const attached = () => {
      if (this.entries.get(computer) !== e || !e.epoch || !e.session)
        throw Error("Terminal is disconnected");
      return { id: e.epoch, pane: e.session };
    };
    return {
      input: (data) => {
        try {
          const route = attached();
          const raw = base64.decode(data);
          if (
            !raw.length ||
            raw.length > 8192 ||
            e.pending.size >= 32 ||
            [...e.pending.values()].reduce((a, b) => a + b, 0) + raw.length >
              65536
          )
            throw Error("Terminal input queue exceeded its limit");
          const seq = ++e.input;
          if (!e.pending.size) e.deadline = Date.now() + 10000;
          e.pending.set(seq, raw.length);
          this.send(computer, {
            type: "term_input",
            ...route,
            revision: seq,
            data,
          });
        } catch (error) {
          this.end(e, String(error));
        }
      },
      ack: (sequence) => {
        try {
          if (
            !Number.isSafeInteger(sequence) ||
            sequence !== e.acked + 1 ||
            sequence > e.output
          )
            throw Error("Terminal parser acknowledgement is out of order");
          this.send(computer, {
            type: "term_ack",
            ...attached(),
            revision: sequence,
          });
          e.acked = sequence;
        } catch (error) {
          this.end(e, String(error));
        }
      },
      resize: (columns, rows) => {
        if (!e.epoch) {
          e.viewport = { columns, rows };
          return;
        }
        try {
          this.send(computer, {
            type: "term_resize",
            ...attached(),
            data: { columns, rows },
          });
        } catch (error) {
          this.end(e, String(error));
        }
      },
      close: () => {
        if (this.entries.get(computer) === e) this.end(e, "Terminal detached");
      },
    };
  }
  message(computer: string, m: InnerMessage): boolean {
    if (!m.type.startsWith("term_")) return false;
    const end = m.id ? this.endings.get(m.id) : undefined;
    if (end && end.computer === computer && m.type === "term_result") {
      clearTimeout(end.timer);
      this.endings.delete(m.id!);
      if (m.status === "ok") {
        end.resolve();
        this.list(computer);
      } else end.reject(Error(m.reason || "Terminal could not end"));
      return true;
    }
    if (m.type === "term_result" && m.id === this.lists.get(computer)) {
      this.lists.delete(computer);
      if (m.status === "ok" && Array.isArray(m.data))
        this.listed(
          computer,
          m.data
            .filter(
              (r): r is { id: string; title: string; lastAttached: string } =>
                !!r &&
                typeof r === "object" &&
                typeof r.id === "string" &&
                /^[a-f0-9]{32}$/.test(r.id),
            )
            .map((r) => ({
              computerId: computer,
              id: r.id,
              title: r.title || "Phone terminal",
              lastAttached: r.lastAttached,
            })),
        );
      return true;
    }
    const e = this.entries.get(computer);
    if (!e) return true;
    if (m.type === "term_result" && m.id === e.request && !e.epoch) {
      const data = m.data as
        | { session?: unknown; attachment?: unknown }
        | undefined;
      if (
        m.status !== "ok" ||
        typeof data?.session !== "string" ||
        !/^[a-f0-9]{32}$/.test(data.session) ||
        typeof data?.attachment !== "string" ||
        !/^[a-f0-9]{48}$/.test(data.attachment)
      ) {
        this.end(e, m.reason || "Terminal open refused");
        return true;
      }
      e.session = data.session;
      e.epoch = data.attachment;
      if (e.viewport) {
        try {
          this.send(computer, {
            type: "term_resize",
            id: e.epoch,
            pane: e.session,
            data: e.viewport,
          });
        } catch (error) {
          this.end(e, String(error));
          return true;
        }
        e.viewport = undefined;
      }
      e.receive({ type: "ready", session: data.session });
      this.list(computer);
      return true;
    }
    if (m.id !== e.epoch || m.pane !== e.session) return true;
    if (m.type === "term_output") {
      try {
        if (
          !Number.isSafeInteger(m.revision) ||
          m.revision !== e.output + 1 ||
          typeof m.data !== "string" ||
          base64.decode(m.data).length > 16384 ||
          e.output - e.acked >= 64
        )
          throw Error(
            "Terminal output is out of sequence or exceeds its window",
          );
        e.output = m.revision!;
        e.receive({ type: "output", data: m.data, sequence: e.output });
      } catch (error) {
        this.end(e, String(error));
      }
    } else if (m.type === "term_input_ack") {
      if (!Number.isSafeInteger(m.revision) || m.revision! > e.input) {
        this.end(e, "Invalid terminal input acknowledgement");
        return true;
      }
      for (const seq of e.pending.keys())
        if (seq <= m.revision!) e.pending.delete(seq);
      e.accepted = Math.max(e.accepted, m.revision!);
    } else if (m.type === "term_exit" || m.status === "refused") {
      this.end(e, m.reason || "Terminal ended");
      this.list(computer);
    }
    return true;
  }
  disconnected(
    computer: string,
    reason = "Connection lost; unconfirmed input discarded",
  ) {
    const e = this.entries.get(computer);
    if (e) this.end(e, reason, false);
  }
  private end(e: Entry, reason: string, notify = true) {
    if (this.entries.get(e.computer) !== e) return;
    this.entries.delete(e.computer);
    e.pending.clear();
    if (notify && e.epoch && e.session)
      try {
        this.send(e.computer, {
          type: "term_detach",
          id: e.epoch,
          pane: e.session,
        });
      } catch {}
    e.receive({ type: "closed", reason });
    if (!this.entries.size && this.timer) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
  }
  dispose() {
    for (const key of this.entries.keys()) this.disconnected(key);
    this.lists.clear();
    for (const end of this.endings.values()) {
      clearTimeout(end.timer);
      end.reject(Error("Connection closed"));
    }
    this.endings.clear();
  }
}
