import { TerminalStreams } from "./terminal-stream";
import { promptBaseline } from './agent-session';
import { LiveTerminals } from "./live-terminal";
import { readChatRoom } from "./chat-room";
import { ControlRequests } from "./control-requests";
import { TerminalResize } from "./terminal-resize";
import { Uploads, quoteAttachmentPath } from "./uploads";
import { PhoneSession, type InnerMessage } from "./protocol/session";
import { InputQueue, type PendingInput } from "./input";
import { pairComputer } from "./pairing";
import type { PairedComputer, Storage } from "./storage";
import { boundHistory, emptyHistory, type ComputerHistory } from './history-cache';
import {
  paneKey,
  inputDraftKey,
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
  private refreshTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private draftVersions = new Map<string, number>();
  private optimisticDrafts = new Map<string, { key: string; text: string; version: number }>();
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
  private live: LiveTerminals;
  private streams: TerminalStreams;
  private writing: Promise<void> = Promise.resolve();
  private histories = new Map<string, ComputerHistory>();
  private historyTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private historyWrites = new Map<string, Promise<void>>();
  private historyDirty = new Set<string>();
  private forgotten = new Set<string>();
  constructor(
    private storage: Storage,
    private newSession: typeof PhoneSession = PhoneSession,
    private pairTransport: typeof pairComputer = pairComputer,
  ) {
    this.streams = new TerminalStreams((id,message)=>{
      const c=this.connections.get(id);
      if(!this.active||!c?.ready||!c.session)throw Error("Computer is not connected");
      if(!this.state.computers.find(k=>k.id===id)?.capabilities.includes("term_stream"))throw Error("Update Partyline for streaming terminals");
      c.session.send([message]);
    },(computer,rows)=>this.update({phoneTerminals:[...(this.state.phoneTerminals||[]).filter(t=>t.computerId!==computer),...rows]}));
    this.live = new LiveTerminals((id, message) => {
      const connection = this.connections.get(id);
      if (!this.active || !connection?.ready || !connection.session) throw new Error("Computer is not connected");
      if (!this.state.computers.find(c => c.id === id)?.capabilities.includes("live_terminal")) throw new Error("Update Partyline on this computer for direct typing");
      connection.session.send([message]);
    });
    this.controls = new ControlRequests((id, message) => {
      const connection = this.connections.get(id);
      if (!this.active || !connection?.ready || !connection.session) throw new Error("Computer is not connected");
      const feature = message.type === "terminal_history" ? "terminal_history" : message.type === "session_list" ? "session_manager" : "loop_controls";
      if (!this.state.computers.find(c => c.id === id)?.capabilities.includes(feature)) throw new Error("Update Partyline on this computer for these controls");
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
        if (message.type === 'chat_send' && this.state.chatSync?.[id]?.stale)
          throw new Error('Waiting for the current chat room. Your draft is kept.');
        if (message.pane && (this.state.sessions.find(s => s.computerId === id && s.pane === message.pane)?.cached ||
            (message.type === 'input' || message.type === 'input_submit') && this.state.panes[paneKey(id, message.pane)]?.cached))
          throw new Error('Waiting for the current session screen. Your draft is kept.');
        const required = message.type === "chat_send" ? "chat_send" : message.type === "chat_limit" ? "chat_limit" : message.type === "shell_open" ? "shell_open" : message.type === "terminal_close" ? "terminal_controls" : message.type === "session_open" || message.type === "session_close" ? "session_manager" : message.type === "loop_action" ? "loop_controls" : "input";
        if (
          !this.state.computers
            .find((c) => c.id === id)
            ?.capabilities.includes(required)
        )
          throw new Error("Update Partyline on this computer to enable input");
        c.session.send([message]);
      },
      (entries) => this.persist(entries),
      () => this.inputChanged(),
      entry => this.inputQueued(entry),
    );
  }
  snapshot = () => this.state;
  private nextDraftVersion(key: string) {
    const version = (this.draftVersions.get(key) ?? 0) + 1;
    this.draftVersions.set(key, version);
    return version;
  }
  private inputQueued(entry: PendingInput) {
    const message = entry.message;
    if (!(message.type === 'chat_send' || message.type === 'input' && message.kind === 'text')) return;
    const text = message.text ?? message.data;
    const key = inputDraftKey(entry.delivery.computerId, entry.delivery.pane, message.target);
    if (typeof text !== 'string' || this.state.drafts[key] !== text) return;
    entry.draft = text;
    const version = this.nextDraftVersion(key);
    this.optimisticDrafts.set(entry.delivery.id, { key, text, version });
    this.update({ drafts: { ...this.state.drafts, [key]: '' } });
  }
  private inputChanged() {
    const deliveries = this.queue.deliveries();
    let drafts = this.state.drafts;
    for (const [id, original] of this.optimisticDrafts) {
      const delivery = deliveries.find(d => d.id === id);
      if (delivery?.status === 'waiting' || delivery?.status === 'unknown') continue;
      if (delivery && ['refused', 'not-sent'].includes(delivery.status) && drafts[original.key] === '' && this.draftVersions.get(original.key) === original.version) {
        drafts = { ...drafts, [original.key]: original.text };
        this.nextDraftVersion(original.key);
      }
      this.optimisticDrafts.delete(id);
    }
    this.update({ deliveries, drafts });
  }
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
  private saveHistory(id: string) {
    clearTimeout(this.historyTimers.get(id)); this.historyTimers.delete(id);
    if (this.forgotten.has(id) || !this.storage.saveHistory) return;
    if (this.historyWrites.has(id)) { this.historyDirty.add(id); return; }
    const previous = this.histories.get(id) ?? emptyHistory();
    const screens = this.state.sessions.filter(s => s.computerId === id).flatMap(session => {
      const terminal = this.state.panes[paneKey(id, session.pane)]?.terminal;
      return terminal ? [{ session, terminal }] : [];
    });
    screens.sort((a, b) => b.terminal.receivedAt - a.terminal.receivedAt);
    const sync = this.state.chatSync?.[id];
    const chat = sync ? { receivedAt: sync.receivedAt, messages: this.state.chat.filter(m => m.computerId === id) } : previous.chat;
    const value = boundHistory({ version: 1, screens, chat }, id);
    this.histories.set(id, value);
    const write = this.storage.saveHistory(id, value).catch(() => {
      if (!this.forgotten.has(id)) this.update({ storageWarning: 'Recent history could not be saved. The live connection is still available.' });
    }).finally(() => {
      this.historyWrites.delete(id);
      if (this.historyDirty.delete(id) && !this.forgotten.has(id)) this.saveHistory(id);
    });
    this.historyWrites.set(id, write);
  }
  private rememberHistory(id: string, immediate = false) {
    if (this.forgotten.has(id) || !this.storage.saveHistory) return;
    if (immediate) this.saveHistory(id);
    else if (!this.historyTimers.has(id)) this.historyTimers.set(id, setTimeout(() => this.saveHistory(id), 1000));
  }
  async start() {
    this.disposed = false;
    const generation = ++this.generation;
    try {
      let storageWarning: string | undefined;
      const [saved, navigation] = await Promise.all([
        this.storage.loadComputers(),
        this.storage.loadState().catch(() => {
          storageWarning = "Saved drafts and input history could not be restored. No queued messages were sent.";
          return {} as Record<string, unknown>;
        }),
      ]);
      if (this.disposed || generation !== this.generation) return;
      const restored = await Promise.all(saved.map(async c => {
        try { return [c.machine, boundHistory(await this.storage.loadHistory?.(c.machine), c.machine)] as const; }
        catch { storageWarning = 'Saved recent history could not be restored. Connecting for fresh content.'; return [c.machine, emptyHistory()] as const; }
      }));
      if (this.disposed || generation !== this.generation) return;
      this.histories = new Map(restored);
      const cachedScreens = restored.flatMap(([, history]) => history.screens);
      const ids = new Set(saved.map((c) => c.machine));
      const selected = record(navigation.selected);
      this.update({
        loading: false,
        storageWarning,
        sessions: cachedScreens.map(s => s.session),
        panes: Object.fromEntries(cachedScreens.map(({ session, terminal }) => [paneKey(session.computerId, session.pane), {
          computerId: session.computerId, pane: session.pane, terminal, stale: true, cached: true, transcriptSupported: false,
        }])),
        chat: restored.flatMap(([, history]) => history.chat?.messages ?? []),
        chatSync: Object.fromEntries(restored.flatMap(([id, history]) => history.chat ? [[id, { receivedAt: history.chat.receivedAt, stale: true, cached: true }]] : [])),
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
        this.saveHistory(id);
        const session = c.session;
        c.session = undefined;
        c.ready = false;
        session?.close();
        this.live.disconnected(id);
        this.streams.disconnected(id);
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
    for (const [key, timer] of this.refreshTimers) if (key.startsWith(id + ':')) {
      clearTimeout(timer); this.refreshTimers.delete(key);
    }
    const chatRooms = { ...this.state.chatRooms };
    delete chatRooms[id];
    for (const [key, pane] of Object.entries(this.state.panes)) {
      if (pane.computerId === id)
        this.awaitingFresh.set(
          key,
          new Set([
            ...(pane.terminal ? ["terminal"] : []),
          ]),
        );
    }
    this.update({
      chatRooms,
      chatSync: { ...this.state.chatSync, ...(this.state.chatSync?.[id] ? { [id]: { ...this.state.chatSync[id], stale: true } } : {}) },
      panes: Object.fromEntries(
        Object.entries(this.state.panes).map(([key, pane]) => [
          key,
          pane.computerId === id ? { ...pane, stale: true, refreshError: undefined } : pane,
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
        if (this.streams.message(id,message)||this.live.message(id, message)) return;
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
          if(capabilities.includes("term_stream"))this.streams.list(id);
        } else if (message.type === "input_delivery") {
          this.queue.feedback(id, message);
        } else if (message.type === "input_result") {
          if (this.controls.result(id, message) || this.terminalResize.result(id, message) || this.uploads.result(id, message)) return;
          const entry = this.queue
            .saved()
            .find(
              (e) =>
                e.message.id === message.id && e.delivery.computerId === id,
            );
          this.queue.result(id, message);
          if (["shell_open", "session_open", "session_close", "terminal_close"].includes(entry?.message.type ?? "") && message.status === "ok") {
            this.connections.get(id)?.session?.send([{ type: "resync", pane: "sessions" }]);
          }
          if (
            entry &&
            message.status === "ok" &&
            entry.message.kind === "paste"
          ) {
            const key = inputDraftKey(id, entry.delivery.pane, entry.message.target),
              sent = entry.message.text ?? entry.message.data;
            if (this.state.drafts[key] === sent) {
              const drafts = { ...this.state.drafts };
              delete drafts[key];
              this.nextDraftVersion(key);
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
        this.live.disconnected(id);
        this.streams.disconnected(id);
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
          const prior = this.state.sessions.find(s => s.computerId === id && s.pane === text(r.pane));
          const detectionUnavailable = r.detectionUnavailable === true || r.detectionUnavailable === 'true';
          const keepDetection = detectionUnavailable && !!prior && (!prior.cached || !!prior.key && prior.key === text(r.key));
          this.dimensions.set(paneKey(id, text(r.pane)), {
            columns: Math.max(2, Math.min(1000, Number(r.width) || 80)),
            rows: Math.max(1, Math.min(500, Number(r.height) || 24)),
          });
          return {
            id: `${id}:${text(r.pane)}`,
            computerId: id,
            pane: text(r.pane),
            title: text(r.title),
            engine: keepDetection && (prior?.detected || prior?.agentTarget) ? prior!.engine : text(r.engine),
            detected: keepDetection ? prior?.detected || !!prior?.agentTarget : r.detected === true || r.detected === 'true',
            agentTarget: keepDetection ? prior?.agentTarget : text(r.agentTarget) || undefined,
            key: text(r.key) || undefined,
            terminalTarget: text(r.terminalTarget) || undefined,
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
      for (const [key, snap] of Object.entries(panes)) {
        if (snap.computerId === id && !sessions.some(s => s.pane === snap.pane)) delete panes[key];
      }
      for (const session of sessions) {
        const key = paneKey(id, session.pane), previous = panes[key], dimensions = this.dimensions.get(key);
        const prior = this.state.sessions.find(s => s.computerId === id && s.pane === session.pane);
        const changed = !!prior && (prior.key !== session.key || prior.agentTarget !== session.agentTarget || prior.engine !== session.engine);
        if (changed) {
          panes[key] = { computerId: id, pane: session.pane, stale: true, cached: true, transcriptSupported: false };
          this.awaitingFresh.set(key, new Set(['terminal']));
        }
        if (prior?.cached || changed) {
          // Inventory verifies the target; require a fresh frame before enabling input.
          this.connections.get(id)?.session?.send([{ type: 'resync', pane: session.pane }]);
        }
        if (!changed && previous?.terminal && !previous.cached && dimensions && (previous.terminal.columns !== dimensions.columns || previous.terminal.rows !== dimensions.rows))
          panes[key] = { ...panes[key], terminal: { ...previous.terminal, ...dimensions } };
      }
      this.update({
        panes,
        sessions: [
          ...this.state.sessions.filter((s) => s.computerId !== id),
          ...sessions,
        ],
      });
      const removed = this.histories.get(id)?.screens.some(({ session: old }) => !sessions.some(s =>
        s.pane === old.pane && s.key === old.key && s.agentTarget === old.agentTarget && s.engine === old.engine));
      this.rememberHistory(id, removed);
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
      const chatRooms = { ...this.state.chatRooms };
      const room = readChatRoom(record(m.data).room);
      if (room) chatRooms[id] = room; else delete chatRooms[id];
      if (Array.isArray(messages))
        this.update({
          chatRooms,
          chatSync: { ...this.state.chatSync, [id]: { receivedAt: now, stale: false } },
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
                seq: Number.isSafeInteger(r.seq) && Number(r.seq) > 0 ? Number(r.seq) : undefined,
                to: Array.isArray(r.to) ? r.to.filter((v): v is string => typeof v === "string") : undefined,
                note: r.note === true,
              };
            }),
          ],
        });
      if (Array.isArray(messages)) this.rememberHistory(id);
    } else if (m.type === "out" && m.pane && typeof m.data === "string") {
      if (this.state.sessions.find(s => s.computerId === id && s.pane === m.pane)?.cached) return;
      const key = paneKey(id, m.pane);
      const previous = this.state.panes[key];
      clearTimeout(this.refreshTimers.get(key));
      this.refreshTimers.delete(key);
      this.update({
        panes: {
          ...this.state.panes,
          [key]: {
            ...previous,
            computerId: id,
            pane: m.pane,
            transcriptSupported: previous?.transcriptSupported ?? false,
            stale: this.refreshed(key, "terminal"),
            cached: false,
            refreshError: undefined,
            terminal: {
              ansi: m.data,
              ...(this.dimensions.get(key) || { columns: 80, rows: 24 }),
              receivedAt: now,
            },
          },
        },
      });
      this.rememberHistory(id);
    } else if (m.type.startsWith("transcript:")) {
      const pane = m.type.slice("transcript:".length),
        key = paneKey(id, pane);
      const bound = record(m.data);
      const target = this.state.sessions.find(s => s.computerId === id && s.pane === pane)?.agentTarget;
      if (target || bound.target) {
        if (bound.target !== target) return;
      }
      const content = bound.target ? bound.entries : m.data;
      const entries: TranscriptEntry[] = Array.isArray(content)
        ? content.map((value, index) => {
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
            transcriptSupported: Array.isArray(content),
            transcript: Array.isArray(content)
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
    this.refreshSession(selected.computerId, selected.pane);
  }
  private refreshSession(id: string, pane: string) {
    const connection = this.connections.get(id);
    if (!this.active || !connection?.ready || !connection.session) return;
    const key = paneKey(id, pane);
    clearTimeout(this.refreshTimers.get(key));
    this.awaitingFresh.set(key, new Set(['terminal']));
    this.update({ panes: { ...this.state.panes, [key]: {
      ...this.state.panes[key], computerId: id, pane, transcriptSupported: this.state.panes[key]?.transcriptSupported ?? false,
      stale: true, refreshError: undefined,
    } } });
    const delayed = () => {
      this.refreshTimers.delete(key);
      const snapshot = this.state.panes[key];
      if (!this.disposed && this.active && snapshot?.stale)
        this.update({ panes: { ...this.state.panes, [key]: { ...snapshot, refreshError: 'Session refresh is taking longer than expected.' } } });
    };
    this.refreshTimers.set(key, setTimeout(delayed, 12000));
    try {
      connection.session.subscribe(pane);
      // A subscription can reuse a live lease. Force a complete current snapshot on entry or retry.
      connection.session.send([{ type: 'resync', pane }]);
    } catch {
      clearTimeout(this.refreshTimers.get(key)); delayed();
    }
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
    refreshSession: (id, pane) => this.refreshSession(id, pane),
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
    setDraft: (id, pane, value, target) => {
      this.nextDraftVersion(inputDraftKey(id, pane, target));
      this.update({
        drafts: { ...this.state.drafts, [inputDraftKey(id, pane, target)]: value },
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
        this.forgotten.delete(computer.machine);
        this.markStale(computer.machine);
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
      this.forgotten.add(id);
      this.historyDirty.delete(id);
      clearTimeout(this.historyTimers.get(id)); this.historyTimers.delete(id);
      this.histories.delete(id);
      await this.storage.forgetComputer(id);
      const c = this.connections.get(id);
      this.connections.delete(id);
      if (c) {
        clearTimeout(c.timer);
        const s = c.session;
        c.session = undefined;
        s?.close();
      }
      this.live.disconnected(id);
        this.streams.disconnected(id);
        this.controls.disconnected(id);
      this.terminalResize.disconnected(id);
      this.uploads.disconnected(id);
      this.queue.forget(id);
      for (const [key, timer] of this.refreshTimers) if (key.startsWith(id + ':')) { clearTimeout(timer); this.refreshTimers.delete(key); }
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
        chatRooms: Object.fromEntries(Object.entries(this.state.chatRooms ?? {}).filter(([key]) => key !== id)),
        chatSync: Object.fromEntries(Object.entries(this.state.chatSync ?? {}).filter(([key]) => key !== id)),
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
      this.live.disconnected(id);
        this.streams.disconnected(id);
        this.controls.disconnected(id);
      this.terminalResize.disconnected(id);
      this.uploads.disconnected(id);
      this.queue.disconnected(id);
      this.markStale(id);
      this.connect(id);
    },
    openTerminalStream: (id,session,columns,rows,receive,requestID)=>this.streams.open(id,session,columns,rows,receive,requestID),
    endPhoneTerminal: (id,session)=>this.streams.endSession(id,session),
    openLiveTerminal: (id, pane, receive) => this.live.open(id, pane, receive),
    sendText: (id, pane, data, target) =>
      this.queue.submit(id, pane, {
        type: "input",
        id: crypto.randomUUID(),
        pane,
        kind: "text",
        target,
        data,
      }, promptBaseline(this.state.panes[paneKey(id, pane)], data, this.queue.deliveries().filter(d => d.computerId === id && d.pane === pane && d.target === target))),
    paste: (id, pane, data, target) =>
      this.queue.submit(id, pane, {
        type: "input",
        id: crypto.randomUUID(),
        pane,
        kind: "paste",
        target,
        data,
      }),
    retryEnter: (id, pane, inputId) => {
      if (!this.state.computers.find(c => c.id === id)?.capabilities.includes("input_feedback")) throw new Error("Update Partyline on this computer for Retry Enter");
      const delivery = this.queue.deliveries().find(d => d.id === inputId && d.computerId === id && d.pane === pane);
      const data = delivery?.data as { inputDelivery?: string; retryInputId?: string } | undefined;
      if (delivery?.status !== "confirmed" || data?.inputDelivery !== "in_composer" || data.retryInputId !== inputId) throw new Error("No verified draft is available for Retry Enter");
      return this.queue.submit(id, pane, { type: "input_submit", id: crypto.randomUUID(), pane, data: inputId });
    },
    sendKey: (id, pane, data, target) =>
      this.queue.submit(id, pane, {
        type: "input",
        id: crypto.randomUUID(),
        pane,
        kind: "key",
        target,
        data,
      }),
    sendChat: (id, text) =>
      this.queue.submit(id, "chat", {
        type: "chat_send",
        id: crypto.randomUUID(),
        text,
      }),
    setChatLimit: (id, limit) => this.queue.submit(id, "chat-limit", { type: "chat_limit", id: crypto.randomUUID(), data: { limit } }),
    openShell: (id) => this.queue.submit(id, "shell", { type: "shell_open", id: crypto.randomUUID() }),
    closeSharedTerminal: (id, pane, target) => this.queue.submit(id, `close:${pane}`, { type: "terminal_close", id: crypto.randomUUID(), pane, target }),
    readTerminalHistory: async (id, pane) => await this.controls.request(id, "terminal_history", pane) as import('./types').TerminalHistory,
    listSavedSessions: async (id, query = '', offset = 0) => await this.controls.request(id, "session_list", "", { query, offset }) as import('./types').SavedSessionPage,
    openSavedSession: (id, engine, key) => this.queue.submit(id, `saved:${engine}:${key}`, { type: "session_open", id: crypto.randomUUID(), data: { engine, key } }),
    closeSavedSession: (id, pane, key) => this.queue.submit(id, `close:${pane}`, { type: "session_close", id: crypto.randomUUID(), pane, data: { key } }),
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
      const target = this.state.sessions.find(s => s.computerId === id && s.pane === pane)?.agentTarget;
      const key = inputDraftKey(id, pane, target);
      await this.uploadReady(id, signal);
      const path = await this.uploads.upload(id, file, progress, signal);
      // Read the current draft after transfer, preserving edits made while uploading.
      const draft = this.state.drafts[key] || "";
      const next = [draft, quoteAttachmentPath(path)].filter(Boolean).join("\n");
      this.nextDraftVersion(key);
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
    for (const id of this.historyTimers.keys()) this.saveHistory(id);
    this.disposed = true;
    this.generation++;
    this.pairing?.abort();
    this.live.dispose();
    this.streams.dispose();
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
    for (const timer of this.refreshTimers.values()) clearTimeout(timer);
    this.refreshTimers.clear();
    this.dimensions.clear();
    this.listeners.clear();
  }
}
