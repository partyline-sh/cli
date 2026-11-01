import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import type { Inputs } from "./input";
import type { InnerMessage } from "./session";

export interface Computer {
  id: string;
  status: string;
  snapshots: Map<string, InnerMessage>;
  fresh?: Set<string>;
  received?: Map<string, number>;
}
type Row = Record<string, string>;
type Tab = "Sessions" | "Loops" | "Chat" | "Computers";
const tabs: Tab[] = ["Sessions", "Loops", "Chat", "Computers"];
const icons = [
  "M5 4h14a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2 M7 9l3 3-3 3 M12 15h5",
  "M4 12a8 8 0 0 1 14-5.3 M20 12a8 8 0 0 1-14 5.3 M18 3v4h-4 M6 21v-4h4",
  "M4 5h16v11H9l-5 4z",
  "M5 4h14a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2 M8 20h8 M12 16v4",
];
const waiting = (row: Row) =>
  row.state === "approval" || row.state === "waiting";
function element<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text = "",
  className = "",
) {
  const el = document.createElement(tag);
  el.textContent = text;
  el.className = className;
  return el;
}
function button(text: string, action: () => void, className = "") {
  const el = element("button", text, className);
  el.type = "button";
  el.onclick = action;
  return el;
}
function rows(computer: Computer, topic: string): Row[] {
  const data = computer.snapshots.get(topic)?.data;
  return Array.isArray(data) ? data : [];
}
function pill(row: Row) {
  const label = waiting(row)
    ? "Waiting"
    : row.state === "working"
      ? "Working"
      : "Idle";
  return element("span", label, `pill state-${label.toLowerCase()}`);
}

// Screens consume snapshots and report selection; transport and pairing stay outside.
export class Screens {
  private tab: Tab = "Sessions";
  private lastComputer?: string;
  private selected?: { computer: Computer; pane: string };
  private terminal?: Terminal;
  private terminalHost?: HTMLElement;
  private fontStep = 2;
  private sessionView: "messages" | "terminal" | undefined;
  private conversation?: HTMLElement;
  private terminalScroll?: HTMLElement;
  private messageToggle?: HTMLButtonElement;
  private terminalToggle?: HTMLButtonElement;
  private header = element("header", "", "page-header");
  private content = element("section", "", "content");
  private nav = element("nav", "", "tabbar");
  private observer: ResizeObserver;
  constructor(
    private root: HTMLElement,
    private computers: Computer[],
    private select: (computer?: Computer, pane?: string) => void,
    private pair: () => void,
    private install: () => void,
    private inputs?: Inputs,
  ) {
    this.nav.setAttribute("aria-label", "Tabs");
    root.replaceChildren(this.header, this.content, this.nav);
    this.observer = new ResizeObserver(() => this.fitTerminal());
    this.observer.observe(this.content);
    this.draw();
  }
  restore() {
    try {
      const saved = JSON.parse(localStorage.getItem("partyline.navigation") || "null");
      if (!saved) return;
      if (tabs.includes(saved.tab)) this.tab = saved.tab;
      const computer = this.computers.find(c => c.id === saved.computer);
      this.lastComputer = computer?.id;
      if (computer && typeof saved.pane === "string") {
        this.selected = { computer, pane: saved.pane };
        this.select(computer, saved.pane);
      }
      this.draw();
    } catch { /* Storage can be unavailable or damaged. */ }
  }
  private remember() {
    try {
      localStorage.setItem("partyline.navigation", JSON.stringify({
        computer: this.selected?.computer.id || this.lastComputer,
        tab: this.tab, pane: this.selected?.pane,
      }));
    } catch { /* Navigation still works without storage. */ }
  }
  private connection(computer: Computer, topic: string) {
    const box = element("div", "", "connection-status");
    box.setAttribute("role", "status");
    append(box, element("span", computer.status));
    if (computer.status === "Pair again")
      append(box, button("Pair again", this.pair, "primary"));
    const at = computer.received?.get(topic);
    if (at && !computer.fresh?.has(topic)) {
      const time = new Date(at).toLocaleTimeString([], {
        hour: "numeric", minute: "2-digit",
      });
      append(box, element("span", `Stale · as of ${time}`, "stale"));
    }
    return box;
  }
  update(computer?: Computer, message?: InnerMessage) {
    if (!message || message.type === "sessions") this.drawTabs();
    if (!this.selected && this.tab === "Chat" && computer) {
      const group = Array.from(this.content.querySelectorAll<HTMLElement>(".computer-group")).find(g => g.dataset.computer === computer.id);
      if (group) {
        if (message?.type === "chat") this.drawChat(group.querySelector<HTMLElement>(".chat-history")!, computer);
        const connection = group.querySelector(".connection-status");
        connection?.replaceWith(this.connection(computer, "chat"));
        return;
      }
    }
    if (this.selected) {
      if (this.selected.computer !== computer) return;
      if (message?.type === `transcript:${this.selected.pane}`) {
        this.drawConversation();
        this.drawHeader();
      } else if (message?.type === "out" && message.pane === this.selected.pane) {
        this.drawTerminal(message);
        this.drawHeader();
      } else if (
        !message ||
        message.type === "sessions" ||
        message.type === "hello"
      )
        this.drawHeader();
    } else {
      const topic = {
        Sessions: "sessions",
        Loops: "board",
        Chat: "chat",
        Computers: "",
      }[this.tab];
      if (!message || message.type === "hello" || message.type === topic) {
        const active = document.activeElement as HTMLElement | null;
        const focusKey = active?.dataset.pane;
        const machine = active
          ?.closest(".computer-group")
          ?.getAttribute("aria-label");
        this.drawContent();
        if (focusKey && machine) {
          const replacement = Array.from(
            this.content.querySelectorAll<HTMLElement>("[data-pane]"),
          ).find(
            (el) =>
              el.dataset.pane === focusKey &&
              el.closest(".computer-group")?.getAttribute("aria-label") ===
                machine,
          );
          replacement?.focus({ preventScroll: true });
        }
      }
    }
  }
  clearTerminal() {
    this.terminal?.dispose();
    this.terminal = undefined;
  }
  private draw() {
    this.clearTerminal();
    this.drawTabs();
    this.drawHeader();
    this.drawContent();
  }
  private drawTabs() {
    if (!this.nav.children.length)
      tabs.forEach((tab, index) => {
        const b = button(
          "",
          () => {
            this.selected = undefined;
            this.tab = tab;
            this.select();
            this.remember();
            this.draw();
          },
          "tab",
        );
        b.setAttribute("aria-label", tab);
        const icon = document.createElementNS(
          "http://www.w3.org/2000/svg",
          "svg",
        );
        icon.setAttribute("viewBox", "0 0 24 24");
        icon.setAttribute("class", "tab-icon");
        icon.setAttribute("aria-hidden", "true");
        icon.setAttribute("fill", "none");
        icon.setAttribute("stroke", "currentColor");
        icon.setAttribute("stroke-width", "2");
        const path = document.createElementNS(
          "http://www.w3.org/2000/svg",
          "path",
        );
        path.setAttribute("d", icons[index]);
        icon.appendChild(path);
        b.appendChild(icon);
        append(b, element("span", tab));
        append(this.nav, b);
      });
    tabs.forEach((tab, index) => {
      const b = this.nav.children[index];
      if (this.tab === tab) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    });
    const count = this.computers.reduce(
      (n, c) => n + rows(c, "sessions").filter(waiting).length,
      0,
    );
    let badge = this.nav.querySelector<HTMLElement>(".badge");
    if (!count) badge?.remove();
    else {
      if (!badge) {
        badge = element("span", "", "badge");
        this.nav.children[0].appendChild(badge);
      }
      badge.textContent = String(count);
      badge.setAttribute("aria-label", `${count} waiting on you`);
    }
  }
  private drawHeader() {
    this.header.replaceChildren();
    if (!this.selected) {
      append(this.header, element("h1", this.tab));
      return;
    }
    const { computer, pane } = this.selected;
    const row = rows(computer, "sessions").find((r) => r.pane === pane);
    const back = button(
      "‹",
      () => {
        this.selected = undefined;
        this.select();
        this.remember();
        this.draw();
      },
      "back",
    );
    back.setAttribute("aria-label", "Back to Sessions");
    const title = element("div", "", "session-title");
    append(
      title,
      element("h1", row?.title || "Session"),
      element("p", `${row?.engine || "terminal"} · ${computer.id}`, "metadata"),
      pill(row || {}),
    );
    append(this.header, back, title);
    append(this.header, this.connection(computer, this.usingMessages() ? `transcript:${pane}` : pane));
  }
  private group(computer: Computer) {
    const group = element("section", "", "computer-group");
    group.setAttribute("aria-label", computer.id);
    group.dataset.computer = computer.id;
    append(
      group,
      element("h2", computer.id, "group-title"),
      this.connection(computer, { Sessions: "sessions", Loops: "board", Chat: "chat", Computers: "" }[this.tab]),
    );
    append(this.content, group);
    return group;
  }
  private drawContent() {
    if (this.selected) {
      this.content.replaceChildren();
      const controls = element("div", "", "terminal-controls");
      this.messageToggle = button("Messages", () => {
        this.sessionView = "messages"; this.drawConversation(); this.drawHeader();
      });
      this.terminalToggle = button("Terminal", () => {
        this.sessionView = "terminal"; this.drawConversation(); this.fitTerminal(); this.drawHeader();
      });
      append(controls, this.messageToggle, this.terminalToggle);
      ["Small", "Medium", "Large"].forEach((label, step) => {
        const b = button(label, () => {
          this.fontStep = step;
          for (const child of controls.querySelectorAll("button[aria-label]"))
            child.setAttribute("aria-pressed", String(child === b));
          this.fitTerminal();
        });
        b.setAttribute("aria-label", `${label} terminal font`);
        b.setAttribute("aria-pressed", String(step === this.fontStep));
        append(controls, b);
      });
      const scroll = element("div", "", "terminal-scroll");
      this.terminalScroll = scroll;
      scroll.setAttribute("aria-label", "Session terminal");
      scroll.tabIndex = 0;
      this.terminalHost = element("div", "", "terminal");
      append(scroll, this.terminalHost);
      this.conversation = element("div", "", "conversation");
      this.conversation.setAttribute("aria-label", "Session messages");
      this.conversation.tabIndex = 0;
      append(this.content, controls, this.conversation, scroll);
      this.drawConversation();
      if (this.inputs) append(this.content, this.inputs.composer(this.selected.computer.id, this.selected.pane));
      const cached = this.selected.computer.snapshots.get(this.selected.pane);
      if (cached) this.drawTerminal(cached);
      return;
    }
    this.content.replaceChildren();
    if (!this.computers.length)
      append(
        this.content,
        element(
          "p",
          "Pair a computer to see its sessions, loops and chat.",
          "note",
        ),
        button("Pair a computer", this.pair, "primary"),
      );
    for (const computer of this.computers) {
      const group = this.group(computer);
      if (this.tab === "Computers") continue;
      if (this.tab === "Sessions") {
        const list = element("div", "", "session-list");
        const inventory = rows(computer, "sessions");
        for (const row of [...inventory].sort(
          (a, b) => Number(waiting(b)) - Number(waiting(a)),
        )) {
          const b = button(
            "",
            () => {
              this.lastComputer = computer.id;
              this.selected = { computer, pane: row.pane };
              this.sessionView = undefined;
              this.select(computer, row.pane);
              this.remember();
              this.draw();
            },
            "session-row",
          );
          b.dataset.pane = row.pane;
          const avatar = element(
            "span",
            (row.engine || ">_").slice(0, 2),
            `avatar ${row.engine === "codex" ? "codex" : ""}`,
          );
          const text = element("span", "", "row-main");
          append(
            text,
            element("span", row.title || "Session", "row-title"),
            element(
              "span",
              `${row.engine || "terminal"}${row.lastLine ? " · " + row.lastLine : ""}`,
              "row-sub",
            ),
          );
          append(b, avatar, text, pill(row));
          append(list, b);
        }
        append(group, list);
        if (!inventory.length)
          append(
            group,
            element(
              "p",
              computer.snapshots.has("sessions")
                ? "No sessions."
                : "Waiting for fresh snapshot…",
              "note",
            ),
          );
      } else if (this.tab === "Loops") {
        for (const row of rows(computer, "board")) {
          const card = element("article", "", "card");
          append(
            card,
            element("p", `${row.section} · ${row.state}`, "metadata"),
            element("h3", row.title),
            element("p", row.repo || "", "metadata"),
            element("p", row.detail || ""),
          );
          append(group, card);
        }
        if (!rows(computer, "board").length)
          append(
            group,
            element(
              "p",
              computer.snapshots.has("board")
                ? "No loops."
                : "Waiting for fresh snapshot…",
              "note",
            ),
          );
      } else {
        const history = element("div", "", "chat-history");
        append(group, history);
        this.drawChat(history, computer);
        if (this.inputs) append(group, this.inputs.composer(computer.id));
      }
    }
    if (this.tab === "Computers")
      append(
        this.content,
        button("Pair another computer", this.pair, "primary"),
        button("Add to Home Screen", this.install, "link"),
      );
  }
  private usingMessages() {
    return this.selected && this.sessionView !== "terminal" &&
      Array.isArray(this.selected.computer.snapshots.get(`transcript:${this.selected.pane}`)?.data);
  }
  private drawConversation() {
    if (!this.selected || !this.conversation) return;
    const data = this.selected.computer.snapshots.get(`transcript:${this.selected.pane}`)?.data;
    const available = Array.isArray(data);
    const messages = available && this.sessionView !== "terminal";
    this.messageToggle!.disabled = !available;
    this.messageToggle!.setAttribute("aria-pressed", String(messages));
    this.terminalToggle!.setAttribute("aria-pressed", String(!messages));
    this.conversation.hidden = !messages;
    if (this.terminalScroll) this.terminalScroll.hidden = messages;
    const history = this.conversation;
    const top = history.scrollTop;
    const bottom = history.scrollHeight - top - history.clientHeight < 40;
    history.replaceChildren();
    if (available) for (const entry of data as { role: string; text: string; tools: { name: string; target: string; status: string }[] }[]) {
      const card = element("article", "", `conversation-message ${entry.role}`);
      append(card, element("h3", entry.role === "user" ? "You" : "Assistant"), element("p", entry.text));
      for (const tool of entry.tools || []) {
        const status = tool.status === "ok" ? "passed" : tool.status === "error" ? "failed" : "running";
        const line = element("div", `${tool.name} · ${tool.target} · ${status}`, `tool-line ${tool.status}`);
        line.title = line.textContent || "";
        append(card, line);
      }
      append(history, card);
    }
    history.scrollTop = bottom ? history.scrollHeight : top;
  }
  private drawChat(history: HTMLElement, computer: Computer) {
    history.replaceChildren();
    const data = computer.snapshots.get("chat")?.data as
      { messages?: { name: string; text: string; at: string }[] } | undefined;
    for (const message of data?.messages || []) {
      const card = element("article", "", "message");
      append(card, element("h3", message.name), element("time", message.at), element("p", message.text));
      append(history, card);
    }
    if (!data?.messages?.length) append(history, element("p", data ? "No messages." : "Waiting for fresh snapshot…", "note"));
  }
  private drawTerminal(message: InnerMessage) {
    if (
      !this.selected ||
      !this.terminalHost ||
      typeof message.data !== "string"
    )
      return;
    const row = rows(this.selected.computer, "sessions").find(
      (r) => r.pane === this.selected!.pane,
    );
    const cols = Math.max(2, Math.min(1000, Number(row?.width) || 80));
    const height = Math.max(1, Math.min(500, Number(row?.height) || 24));
    if (!this.terminal) {
      this.terminal = new Terminal({
        cols,
        rows: height,
        disableStdin: true,
        convertEol: true,
        scrollback: 0,
        fontSize: 14,
        theme: { background: "#1b1a19", foreground: "#f5f3f0" },
        linkHandler: {
          activate(_event, uri) {
            try {
              if (["http:", "https:"].includes(new URL(uri).protocol))
                window.open(uri, "_blank", "noopener");
            } catch {
              /* Ignore malformed pane links. */
            }
          },
        },
      });
      this.terminal.open(this.terminalHost);
    }
    this.terminal.resize(cols, height);
    this.terminal.reset();
    this.terminal.write(message.data);
    this.fitTerminal();
  }
  private fitTerminal() {
    if (!this.terminalHost || !this.terminal) return;
    // Measure at a fixed reference size so repeated snapshots cannot compound the font step.
    const available = this.terminalHost.parentElement!.clientWidth - 18;
    if (available <= 0) return;
    const context = document.createElement("canvas").getContext("2d");
    if (!context) return;
    context.font = `14px ${this.terminal.options.fontFamily}`;
    const cell = context.measureText("W").width;
    if (!cell) return;
    const fit = Math.min(16, (14 * available) / (cell * this.terminal.cols));
    this.terminal.options.fontSize = Math.max(
      0.1,
      Math.floor(fit * [0.65, 0.82, 1][this.fontStep] * 10) / 10,
    );
  }
}

function append(parent: HTMLElement, ...children: HTMLElement[]) {
  for (const child of children) parent.appendChild(child);
}
