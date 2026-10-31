import { ControlRequests } from "./control-requests";
import { TerminalResize } from "./terminal-resize";
import { Uploads, quoteAttachmentPath } from "./uploads";
import { PhoneSession, type InnerMessage } from "./protocol/session";
import { InputQueue, type PendingInput } from "./input";
import { pairComputer } from "./pairing";
import type { PairedComputer, Storage } from "./storage";
import {
  paneKey,
  type PartylineActions,
  type PartylineState,
  type Session,
  type TranscriptEntry,
  type Tool,
} from "./types";

interface Connection {
  saved: PairedComputer;
  session?: PhoneSession;
  ready: boolean;
  stopped: boolean;
  failures: number;
  timer?: ReturnType<typeof setTimeout>;
}
const blank = (): PartylineState => ({
  mode: "live",
  loading: true,
  computers: [],
  sessions: [],
  panes: {},
  loops: [],
  chat: [],
  deliveries: [],
  drafts: {},
  pairing: { status: "idle" },
  tab: "sessions",
  sessionView: "messages",
});
const record = (value: unknown): Record<string, unknown> =>
  value && typeof value === "object" ? (value as Record<string, unknown>) : {};
const text = (value: unknown) => (typeof value === "string" ? value : "");
export class PartylineClient {
  private state = blank();
  private awaitingFresh = new Map<string, Set<string>>();
  private listeners = new Set<() => void>();
  private connections = new Map<string, Connection>();
  private dimensions = new Map<string, { columns: number; rows: number }>();
  private active = true;
  private disposed = false;
  private generation = 0;
  private pairing?: AbortController;
  private queue: InputQueue;
  private uploads: Uploads;
  private terminalResize: TerminalResize;
  private controls: ControlRequests;
  private writing: Promise<void> = Promise.resolve();
  constructor(
    private storage: Storage,
    private newSession: typeof PhoneSession = PhoneSession,
    private pairTransport: typeof pairComputer = pairComputer,
  ) {
    this.controls = new ControlRequests((id, message) => {
      const connection = this.connections.get(id);
      if (!this.active || !connection?.ready || !connection.session) throw new Error("Computer is not connected");
      if (!this.state.computers.find(c => c.id === id)?.capabilities.includes("loop_controls")) throw new Error("Update Partyline on this computer for loop controls");
      connection.session.send([message]);
    });
    this.terminalResize = new TerminalResize((id, message) => {
      const connection = this.connections.get(id);
      if (!this.active || !connection?.ready || !connection.session) throw new Error("Computer is not connected");
      if (!this.state.computers.find(c => c.id === id)?.capabilities.includes("terminal_resize"))
        throw new Error("Update Partyline on this computer for phone layout");
      connection.session.send([message]);
    });
    this.uploads = new Uploads((id, message) => {
      const connection = this.connections.get(id);
      if (!this.active || !connection?.ready || !connection.session)
        throw new Error("Computer is not connected");
      if (!this.state.computers.find(c => c.id === id)?.capabilities.includes("uploads"))
        throw new Error("Update Partyline on this computer to attach files");
      connection.session.send([message]);
    });
    this.queue = new InputQueue(
      (id, message) => {
        const c = this.connections.get(id);
        if (!this.active || !c?.ready || !c.session)
          throw new Error("Computer is not connected");
        const required = message.type === "chat_send" ? "chat_send" : message.type === "shell_open" ? "shell_open" : message.type === "loop_action" ? "loop_controls" : "input";
        if (
          !this.state.computers
            .find((c) => c.id === id)
            ?.capabilities.includes(required)
        )
          throw new Error("Update Partyline on this computer to enable input");
        c.session.send([message]);
      },
      (entries) => this.persist(entries),
      () => this.update({ deliveries: this.queue.deliveries() }),
    );
  }
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private update(patch: Partial<PartylineState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
  private computer(
    id: string,
    patch: Partial<PartylineState["computers"][number]>,
  ) {
    this.update({
      computers: this.state.computers.map((c) =>
        c.id === id ? { ...c, ...patch } : c,
      ),
    });
  }
  private persist(entries = this.queue.saved()): Promise<void> {
    const value = {
      tab: this.state.tab,
      selected: this.state.selected,
      sessionView: this.state.sessionView,
      drafts: this.state.drafts,
      pending: entries,
    };
    const write = this.writing
      .catch(() => {})
      .then(() => this.storage.saveState(value));
    this.writing = write;
    return write;
  }
  async start() {
    this.disposed = false;
    const generation = ++this.generation;
    try {
      const [saved, navigation] = await Promise.all([
        this.storage.loadComputers(),
        this.storage.loadState(),
      ]);
      if (this.disposed || generation !== this.generation) return;
      const ids = new Set(saved.map((c) => c.machine));
      const selected = record(navigation.selected);
      this.update({
        loading: false,
        tab: ["sessions", "loops", "chat", "computers"].includes(
          text(navigation.tab),
        )
          ? (navigation.tab as PartylineState["tab"])
          : "sessions",
        sessionView:
          navigation.sessionView === "terminal" ? "terminal" : "messages",
        selected:
          ids.has(text(selected.computerId)) &&
          /^%\d+$/.test(text(selected.pane))
            ? {
                computerId: text(selected.computerId),
                pane: text(selected.pane),
              }
            : undefined,
        drafts: Object.fromEntries(
          Object.entries(record(navigation.drafts)).filter(
            ([, v]) => typeof v === "string",
          ),
        ) as Record<string, string>,
        computers: [
          ...saved.map((c) => ({
            id: c.machine,
            name: c.name,
            relay: c.relay,
            relayHosting: c.relayHosting === "self-hosted" || c.relayHosting === "cloudflare" ? c.relayHosting : undefined,
            pairedAt: c.pairedAt,
            connection: "connecting" as const,
            capabilities: [],
          })),
          ...(this.storage.loadIssues?.() || []).map((issue) => ({
            id: issue.id,
            name: "Saved computer",
            relay: "",
            pairedAt: 0,
            connection: "offline" as const,
            capabilities: [],
            error: issue.error,
          })),
        ],
      });
      this.queue.restore(
        Array.isArray(navigation.pending)
          ? (navigation.pending as PendingInput[])
          : [],
      );
      for (const computer of saved) {
        this.connections.set(computer.machine, {
          saved: computer,
          ready: false,
          stopped: false,
          failures: 0,
        });
        this.connect(computer.machine);
      }
    } catch (error) {
      this.update({
        loading: false,
        pairing: {
          status: "error",
          message: `Could not load saved computers: ${String(error)}`,
        },
      });
    }
  }
  foreground(active: boolean) {
    this.active = active;
    for (const [id, c] of this.connections) {
      clearTimeout(c.timer);
      c.timer = undefined;
      if (!active) {
        const session = c.session;
        c.session = undefined;
        c.ready = false;
        session?.close();
        this.controls.disconnected(id);
      this.terminalResize.disconnected(id);
        this.uploads.disconnected(id);
        this.queue.disconnected(id);
        this.markStale(id);
        this.computer(id, { connection: "offline" });
      } else if (!c.stopped) this.connect(id);
    }
    if (!active) {
      this.pairing?.abort();
      this.pairing = undefined;
      if (
        this.state.pairing.status === "connecting" ||
        this.state.pairing.status === "awaiting-approval"
      ) {
        this.update({
          pairing: {
            status: "error",
            code: "pairing-failed",
            message:
              "Pairing was interrupted. Scan a new code from your computer.",
          },
        });
      }
    }
  }
  private refreshed(key: string, topic: string) {
    const pending = this.awaitingFresh.get(key);
    pending?.delete(topic);
    if (!pending?.size) {
      this.awaitingFresh.delete(key);
      return false;
    }
    return true;
  }
  private markStale(id: string) {
    for (const [key, pane] of Object.entries(this.state.panes)) {
      if (pane.computerId === id)
        this.awaitingFresh.set(
          key,
          new Set([
            ...(pane.terminal ? ["terminal"] : []),
            ...(pane.transcript ? ["transcript"] : []),
          ]),
        );
    }
    this.update({
      panes: Object.fromEntries(
        Object.entries(this.state.panes).map(([key, pane]) => [
          key,
          pane.computerId === id ? { ...pane, stale: true } : pane,
        ]),
      ),
    });
  }
  private connect(id: string) {
    const c = this.connections.get(id);
    if (!c || c.stopped || !this.active || this.disposed || c.session) return;
    clearTimeout(c.timer);
    c.timer = undefined;
    this.computer(id, {
      connection: c.failures ? "reconnecting" : "connecting",
      error: undefined,
    });
    const session = new this.newSession(
      c.saved,
      (message) => {
        if (c.session !== session) return;
        if (message.type === "hello") {
          c.ready = true;
          c.failures = 0;
          const hello = record(message.data);
          const capabilities = Array.isArray(hello.capabilities)
            ? (hello.capabilities.filter(
                (v) => typeof v === "string",
              ) as string[])
            : [];
          const name = text(hello.hostname) || c.saved.name;
          const relay = record(hello.relay);
          const hosting = relay.origin === c.saved.relay &&
            (relay.hosting === "self-hosted" || relay.hosting === "cloudflare")
            ? relay.hosting : c.saved.relayHosting;
          this.computer(id, {
            connection: "connected",
            name,
            relayHosting: hosting,
            capabilities,
            lastReceivedAt: Date.now(),
          });
          c.saved = { ...c.saved, name, relayHosting: hosting };
          void this.storage.saveComputer(c.saved).catch(() => {});
          for (const topic of ["sessions", "board", "chat"])
            session.subscribe(topic);
          this.focus();
        } else if (message.type === "input_result") {
          if (this.controls.result(id, message) || this.terminalResize.result(id, message) || this.uploads.result(id, message)) return;
          const entry = this.queue
            .saved()
            .find(
              (e) =>
                e.message.id === message.id && e.delivery.computerId === id,
            );
          this.queue.result(id, message);
          if (entry?.message.type === "shell_open" && message.status === "ok") {
            this.connections.get(id)?.session?.send([{ type: "resync", pane: "sessions" }]);
          }
          if (
            entry &&
            message.status === "ok" &&
            entry.message.kind !== "key"
          ) {
            const key = paneKey(id, entry.delivery.pane),
              sent = entry.message.text ?? entry.message.data;
            if (this.state.drafts[key] === sent) {
              const drafts = { ...this.state.drafts };
              delete drafts[key];
              this.update({ drafts });
              void this.persist().catch(() => {});
            }
          }
        } else this.receive(id, message);
      },
      (reason) => {
        if (c.session !== session) return;
        c.session = undefined;
        c.ready = false;
        c.stopped = ["revoked", "device refused", "unknown phone"].includes(
          reason,
        );
        this.controls.disconnected(id);
      this.terminalResize.disconnected(id);
        this.uploads.disconnected(id);
        this.queue.disconnected(id);
        this.markStale(id);
        this.computer(id, {
          connection: c.stopped ? "revoked" : "reconnecting",
          error: reason,
        });
        if (!c.stopped && this.active && !this.disposed)
          c.timer = setTimeout(
            () => {
              c.timer = undefined;
              this.connect(id);
            },
            Math.min(30000, 1000 * 2 ** Math.min(c.failures++, 5)) *
              (0.8 + Math.random() * 0.2),
          );
      },
    );
    c.session = session;
    try {
      session.connect(c.saved.relay);
    } catch (error) {
      session.close();
      this.computer(id, { error: String(error) });
    }
  }
  private receive(id: string, m: InnerMessage) {
    const now = Date.now();
    this.computer(id, { lastReceivedAt: now });
    if (m.type === "sessions" && Array.isArray(m.data)) {
      const sessions: Session[] = m.data
        .map((value): Session => {
          const r = record(value);
          this.dimensions.set(paneKey(id, text(r.pane)), {
            columns: Math.max(2, Math.min(1000, Number(r.width) || 80)),
            rows: Math.max(1, Math.min(500, Number(r.height) || 24)),
          });
          return {
            id: `${id}:${text(r.pane)}`,
            computerId: id,
            pane: text(r.pane),
            title: text(r.title),
            engine: text(r.engine),
            state:
              r.state === "approval" || r.state === "waiting"
                ? "waiting"
                : r.state === "working"
                  ? "working"
                  : "idle",
            lastLine: text(r.lastLine) || text(r.last),
            project: text(r.repo),
          };
        })
        .filter((s) => /^%\d+$/.test(s.pane));
      const panes = { ...this.state.panes };
      for (const session of sessions) {
        const key = paneKey(id, session.pane), previous = panes[key], dimensions = this.dimensions.get(key);
        if (previous?.terminal && dimensions && (previous.terminal.columns !== dimensions.columns || previous.terminal.rows !== dimensions.rows))
          panes[key] = { ...previous, terminal: { ...previous.terminal, ...dimensions } };
      }
      this.update({
        panes,
        sessions: [
          ...this.state.sessions.filter((s) => s.computerId !== id),
          ...sessions,
        ],
      });
    } else if (m.type === "board" && Array.isArray(m.data)) {
      this.update({
        loops: [
          ...this.state.loops.filter((l) => l.computerId !== id),
          ...m.data.map((value) => {
            const r = record(value);
            return {
              id: text(r.id),
              computerId: id,
              section:
                typeof r.section === "number"
                  ? [
                      "Needs you",
                      "Planned",
                      "In progress",
                      "In review",
                      "Done",
                    ][r.section] || `Unknown section ${r.section}`
                  : text(r.section),
              title: text(r.title),
              state: text(r.state),
              repo: text(r.repo),
              detail: text(r.detail),
              round: typeof r.round === "number" && Number.isSafeInteger(r.round) && r.round >= 0 ? r.round : undefined,
              batch: text(r.batch),
              openP1: typeof r.openP1 === "number" && Number.isSafeInteger(r.openP1) && r.openP1 >= 0 ? r.openP1 : undefined,
              dispute: text(r.dispute),
              pr: typeof r.pr === "number" && Number.isSafeInteger(r.pr) && r.pr > 0 ? r.pr : undefined,
              ci: text(r.ci),
              builder: text(r.builder),
              chips: Array.isArray(r.chips) ? r.chips.map(value => {
                const chip = record(value);
                return { name: text(chip.Name) || text(chip.name), mark: text(chip.Mark) || text(chip.mark) };
              }).filter(chip => chip.name) : [],
              receivedAt: Date.now(),
            };
          }),
        ],
      });
    } else if (m.type === "chat") {
      const messages = record(m.data).messages;
      if (Array.isArray(messages))
        this.update({
          chat: [
            ...this.state.chat.filter((c) => c.computerId !== id),
            ...messages.map((value, index) => {
              const r = record(value);
              return {
                id: text(r.id) || `${id}:${text(r.at)}:${index}`,
                computerId: id,
                name: text(r.name) || text(r.from),
                text: text(r.text),
                at: text(r.at),
                own: r.from === "you",
              };
            }),
          ],
        });
    } else if (m.type === "out" && m.pane && typeof m.data === "string") {
      const key = paneKey(id, m.pane);
      const previous = this.state.panes[key];
      this.update({
        panes: {
          ...this.state.panes,
          [key]: {
            ...previous,
            computerId: id,
            pane: m.pane,
            transcriptSupported: previous?.transcriptSupported ?? false,
            stale: this.refreshed(key, "terminal"),
            terminal: {
              ansi: m.data,
              ...(this.dimensions.get(key) || { columns: 80, rows: 24 }),
              receivedAt: now,
            },
          },
        },
      });
    } else if (m.type.startsWith("transcript:")) {
      const pane = m.type.slice("transcript:".length),
        key = paneKey(id, pane);
      const entries: TranscriptEntry[] = Array.isArray(m.data)
        ? m.data.map((value, index) => {
            const r = record(value);
            const tools: Tool[] = Array.isArray(r.tools)
              ? r.tools.map((value) => {
                  const t = record(value);
                  return {
                    name: text(t.name),
                    target: text(t.target),
                    status: ["running", "ok", "error"].includes(text(t.status))
                      ? (t.status as Tool["status"])
                      : undefined,
                  };
                })
              : [];
            return {
              id: `${id}:${pane}:${index}`,
              role: r.role === "user" ? "user" : "assistant",
              text: text(r.text),
              tools,
            };
          })
        : [];
      this.update({
        panes: {
          ...this.state.panes,
          [key]: {
            ...this.state.panes[key],
            computerId: id,
            pane,
            stale: this.refreshed(key, "transcript"),
            transcriptSupported: Array.isArray(m.data),
            transcript: Array.isArray(m.data)
              ? { entries, revision: m.revision || 0, receivedAt: now }
              : undefined,
          },
        },
      });
    }
  }
  private focus() {
    const selected = this.state.selected;
    if (!selected) return;
    const c = this.connections.get(selected.computerId);
    if (!c?.ready) return;
    c.session?.subscribe(selected.pane);
    if (
      this.state.computers
        .find((c) => c.id === selected.computerId)
        ?.capabilities.includes("transcript")
    )
      c.session?.subscribe(`transcript:${selected.pane}`);
  }
  // Native file pickers may briefly deactivate the app. Let that reconnect finish
  // before beginning a transfer; an upload already in progress is never replayed.
  private uploadReady(id: string, signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      let unsubscribe = () => {};
      const finish = (error?: Error) => {
        clearTimeout(timer);
        unsubscribe();
        signal.removeEventListener('abort', abort);
        if (error) reject(error); else resolve();
      };
      const abort = () => finish(new Error('Upload cancelled'));
      const timer = setTimeout(() => finish(new Error('Computer is not connected. Select the file again after reconnecting.')), 10000);
      const check = () => {
        if (signal.aborted) { abort(); return; }
        const connection = this.connections.get(id);
        if (!connection || connection.stopped || this.disposed) finish(new Error('Computer is unavailable'));
        else if (this.active && connection.ready) finish();
      };
      unsubscribe = this.subscribe(check);
      signal.addEventListener('abort', abort);
      check();
    });
  }
  actions: PartylineActions = {
    selectTab: (tab) => {
      const old = this.state.selected;
      if (old) {
        const c = this.connections.get(old.computerId);
        c?.session?.unsubscribe(old.pane);
        c?.session?.unsubscribe(`transcript:${old.pane}`);
      }
      this.update({ tab, selected: undefined });
      void this.persist().catch(() => {});
    },
    selectSession: (selected) => {
      const old = this.state.selected;
      if (old) {
        const c = this.connections.get(old.computerId);
        c?.session?.unsubscribe(old.pane);
        c?.session?.unsubscribe(`transcript:${old.pane}`);
      }
      this.update({ selected });
      this.focus();
      void this.persist().catch(() => {});
    },
    selectView: (sessionView) => {
      this.update({ sessionView });
      void this.persist().catch(() => {});
    },
    setDraft: (id, pane, value) => {
      this.update({
        drafts: { ...this.state.drafts, [paneKey(id, pane)]: value },
      });
      void this.persist().catch(() => {});
    },
    startScan: () => this.update({ pairing: { status: "scanning" } }),
    pair: async (code) => {
      this.pairing?.abort();
      const abort = new AbortController();
      this.pairing = abort;
      this.update({ pairing: { status: "connecting" } });
      try {
        const computer = await this.pairTransport(
          code,
          "Partyline phone",
          (pairing) => {
            if (this.pairing === abort) this.update({ pairing });
          },
          abort.signal,
        );
        if (abort.signal.aborted) return;
        await this.storage.saveComputer(computer);
        if (this.disposed || abort.signal.aborted) return;
        const old = this.connections.get(computer.machine);
        if (old) {
          clearTimeout(old.timer);
          const s = old.session;
          old.session = undefined;
          s?.close();
        }
        this.connections.set(computer.machine, {
          saved: computer,
          ready: false,
          stopped: false,
          failures: 0,
        });
        this.update({
          computers: [
            ...this.state.computers.filter((c) => c.id !== computer.machine),
            {
              id: computer.machine,
              name: computer.name,
              pairedAt: computer.pairedAt,
              relay: computer.relay,
              connection: "connecting",
              capabilities: [],
            },
          ],
          pairing: { status: "paired", computerId: computer.machine },
        });
        this.connect(computer.machine);
      } catch (error) {
        if (this.pairing === abort && !abort.signal.aborted)
          this.update({
            pairing: {
              status: "error",
              code: "pairing-failed",
              message:
                error instanceof Error
                  ? error.message
                  : "Pairing failed. Try a new code from your computer.",
            },
          });
      }
    },
    cancelPair: () => {
      this.pairing?.abort();
      this.pairing = undefined;
      this.update({ pairing: { status: "idle" } });
    },
    forgetComputer: async (id) => {
      await this.storage.forgetComputer(id);
      const c = this.connections.get(id);
      this.connections.delete(id);
      if (c) {
        clearTimeout(c.timer);
        const s = c.session;
        c.session = undefined;
        s?.close();
      }
      this.controls.disconnected(id);
      this.terminalResize.disconnected(id);
      this.uploads.disconnected(id);
      this.queue.forget(id);
      for (const key of this.awaitingFresh.keys())
        if (key.startsWith(id + ":")) this.awaitingFresh.delete(key);
      for (const key of this.dimensions.keys())
        if (key.startsWith(id + ":")) this.dimensions.delete(key);
      this.update({
        computers: this.state.computers.filter((c) => c.id !== id),
        sessions: this.state.sessions.filter((s) => s.computerId !== id),
        panes: Object.fromEntries(
          Object.entries(this.state.panes).filter(
            ([, p]) => p.computerId !== id,
          ),
        ),
        chat: this.state.chat.filter((c) => c.computerId !== id),
        loops: this.state.loops.filter((c) => c.computerId !== id),
        drafts: Object.fromEntries(
          Object.entries(this.state.drafts).filter(
            ([key]) => !key.startsWith(id + ":"),
          ),
        ),
        selected:
          this.state.selected?.computerId === id
            ? undefined
            : this.state.selected,
      });
      await this.persist();
    },
    reconnect: (id) => {
      const c = this.connections.get(id);
      if (!c || c.stopped) return;
      clearTimeout(c.timer);
      const s = c.session;
      c.session = undefined;
      c.ready = false;
      s?.close();
      this.controls.disconnected(id);
      this.terminalResize.disconnected(id);
      this.uploads.disconnected(id);
      this.queue.disconnected(id);
      this.markStale(id);
      this.connect(id);
    },
    sendText: (id, pane, data) =>
      this.queue.submit(id, pane, {
        type: "input",
        id: crypto.randomUUID(),
        pane,
        kind: "text",
        data,
      }),
    paste: (id, pane, data) =>
      this.queue.submit(id, pane, {
        type: "input",
        id: crypto.randomUUID(),
        pane,
        kind: "paste",
        data,
      }),
    sendKey: (id, pane, data) =>
      this.queue.submit(id, pane, {
        type: "input",
        id: crypto.randomUUID(),
        pane,
        kind: "key",
        data,
      }),
    sendChat: (id, text) =>
      this.queue.submit(id, "chat", {
        type: "chat_send",
        id: crypto.randomUUID(),
        text,
      }),
    openShell: (id) => this.queue.submit(id, "shell", { type: "shell_open", id: crypto.randomUUID() }),
    readLoop: async (id, loop) => await this.controls.request(id, "loop_detail", loop) as import('./types').LoopDetail,
    prepareLoopAction: async (id, loop, action) => await this.controls.request(id, "loop_prepare", loop, { action }) as import('./types').LoopConfirmation,
    confirmLoopAction: (id, loop, confirmation, code, note) => this.queue.submit(id, `loop:${loop}`, {
      type: "loop_action", id: crypto.randomUUID(), pane: loop,
      data: { action: confirmation.action, token: confirmation.token, revision: confirmation.revision, code, note },
    }),
    resizeTerminal: async (id, pane, mode, viewport) => {
      await this.terminalResize.request(id, pane, mode, viewport);
      this.connections.get(id)?.session?.send([{ type: "resync", pane }]);
    },
    uploadAttachment: async (id, pane, file, progress, signal) => {
      await this.uploadReady(id, signal);
      const path = await this.uploads.upload(id, file, progress, signal);
      // Read the current draft after transfer, preserving edits made while uploading.
      const key = paneKey(id, pane);
      const draft = this.state.drafts[key] || "";
      const next = [draft, quoteAttachmentPath(path)].filter(Boolean).join("\n");
      this.update({ drafts: { ...this.state.drafts, [key]: next } });
      await this.persist();
      return path;
    },
    retryDelivery: (id, acknowledgeRisk) =>
      this.queue.retry(id, acknowledgeRisk),
    acknowledgeDelivery: (id) => this.queue.acknowledge(id),
    answer: undefined,
  };
  dispose() {
    this.disposed = true;
    this.generation++;
    this.pairing?.abort();
    this.controls.dispose();
    this.terminalResize.dispose();
    this.uploads.dispose();
    this.queue.dispose();
    for (const c of this.connections.values()) {
      clearTimeout(c.timer);
      const session = c.session;
      c.session = undefined;
      session?.close();
    }
    this.connections.clear();
    this.awaitingFresh.clear();
    this.dimensions.clear();
    this.listeners.clear();
  }
}
