import { PhoneSession, SavedPhone, InnerMessage } from "./session";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
const root = document.querySelector<HTMLElement>("#app")!;
let session: PhoneSession | undefined, selected: SavedPhone | undefined;
let topic = "sessions",
  ready = false;
let terminal: Terminal | undefined;
let inventory: Record<string, string>[] = [];
const snapshots = new Map<string, InnerMessage>();
const content = document.createElement("section");
const status = document.createElement("p");
status.setAttribute("role", "status");
function button(label: string, action: () => void, parent: HTMLElement) {
  const b = document.createElement("button");
  b.textContent = label;
  b.onclick = action;
  parent.appendChild(b);
}
function clear() {
  terminal?.dispose();
  terminal = undefined;
  content.replaceChildren();
}
function machines() {
  session?.close();
  session = undefined;
  selected = undefined;
  ready = false;
  clear();
  root.replaceChildren();
  const h = document.createElement("h1");
  h.textContent = "Machines";
  root.appendChild(h);
  root.appendChild(content);
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i)!;
    if (!key.startsWith("partyline.phone.")) continue;
    try {
      const saved = JSON.parse(localStorage.getItem(key)!) as SavedPhone;
      if (
        saved.machine &&
        saved.token &&
        saved.private &&
        saved.public &&
        saved.phone
      )
        button(saved.machine, () => open(saved), content);
    } catch {
      /* A damaged record does not hide other machines. */
    }
  }
  // A pairing link, or no machine yet: straight to pairing.
  if (document.body.dataset.link || !content.children.length) {
    show("pair");
    return;
  }
  const add = document.createElement("button");
  add.className = "link";
  add.textContent = "Pair another machine";
  add.onclick = () => show("pair");
  root.appendChild(add);
  show("machines");
}
function show(view: "machines" | "pair") {
  document.body.dataset.view = view;
  if (view === "pair") window.dispatchEvent(new Event("pairing-start"));
}
window.addEventListener("pairing-cancel", machines);
function view(next: string) {
  if (topic !== "sessions") session?.unsubscribe(topic);
  topic = next;
  clear();
  content.textContent = "Waiting for fresh snapshot…";
  const cached = snapshots.get(topic);
  if (cached) render(cached);
  if (topic === "sessions" && inventory.length)
    render({ type: "sessions", data: inventory });
  if (ready && topic !== "sessions") session?.subscribe(topic);
}
function open(saved: SavedPhone) {
  selected = saved;
  topic = "sessions";
  root.replaceChildren();
  const nav = document.createElement("nav");
  button("Machines", machines, nav);
  for (const name of ["Sessions", "Board", "Contractors", "Chat"])
    button(name, () => view(name.toLowerCase()), nav);
  root.appendChild(nav);
  root.appendChild(status);
  root.appendChild(content);
  connect();
}
function connect() {
  session?.close();
  ready = false;
  inventory = [];
  snapshots.clear();
  clear();
  status.textContent = "Connecting…";
  if (!selected || document.hidden) return;
  const current = new PhoneSession(
    selected,
    (m) => {
      if (session !== current) return;
      if (m.type === "hello") {
        ready = true;
        status.textContent = "Read only";
        current.subscribe("sessions");
        if (topic !== "sessions") current.subscribe(topic);
      }
      if (m.type === "sessions" && Array.isArray(m.data))
        inventory = m.data as Record<string, string>[];
      if (m.type === "sessions" && topic !== "sessions") {
        const current = snapshots.get(topic);
        if (current) render(current);
      }
      snapshots.set(m.pane || m.type, m);
      render(m);
    },
    (reason) => {
      if (session === current) {
        ready = false;
        status.textContent = "Session ended: " + reason;
      }
    },
  );
  session = current;
  current.connect();
}
function render(m: InnerMessage) {
  if (m.type === "out" && m.pane === topic && typeof m.data === "string") {
    const row = inventory.find((r) => r.pane === topic);
    const cols = Math.max(2, Math.min(1000, Number(row?.width) || 80));
    const rows = Math.max(1, Math.min(500, Number(row?.height) || 24));
    if (!terminal) {
      clear();
      const host = document.createElement("div");
      host.className = "terminal";
      content.appendChild(host);
      terminal = new Terminal({
        linkHandler: {
          activate(_event, uri) {
            try {
              const protocol = new URL(uri).protocol;
              if (protocol === "http:" || protocol === "https:")
                window.open(uri, "_blank", "noopener");
            } catch {
              // Ignore malformed pane links.
            }
          },
        },
        cols,
        rows,
        disableStdin: true,
        convertEol: true,
        scrollback: 0,
        fontSize: 14,
        allowProposedApi: false,
      });
      terminal.open(host);
    }
    terminal.resize(cols, rows);
    terminal.reset();
    terminal.write(m.data);
    fitTerminal();
    return;
  }
  if (m.type !== topic) return;
  clear();
  if (topic === "sessions" && Array.isArray(m.data)) {
    for (const r of inventory)
      button(
        `${r.glyph || ""} ${r.title} · ${r.state || "idle"}`,
        () => view(r.pane),
        content,
      );
  } else if (topic === "board" && Array.isArray(m.data)) {
    for (const r of m.data)
      card(
        `${r.section} · ${r.title}`,
        `${r.state} · ${r.repo}\n${r.detail || ""}`,
      );
  } else if (topic === "contractors" && Array.isArray(m.data)) {
    for (const r of m.data) {
      const c = card(`${r.role} · ${r.title}`, `${r.state}\n${r.result || ""}`);
      const pane = inventory.find((p) => p.window === r.window);
      if (pane) button("View pane", () => view(pane.pane), c);
    }
  } else if (topic === "chat" && m.data && typeof m.data === "object") {
    const data = m.data as {
      messages?: { name: string; text: string; at: string }[];
    };
    for (const r of data.messages || []) card(`${r.name} · ${r.at}`, r.text);
  }
  if (!content.children.length) content.textContent = "Nothing here yet.";
}
function card(title: string, text: string) {
  const article = document.createElement("article"),
    h = document.createElement("h2"),
    p = document.createElement("p");
  h.textContent = title;
  p.textContent = text;
  article.appendChild(h);
  article.appendChild(p);
  content.appendChild(article);
  return article;
}
document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    session?.close();
    clear();
  } else if (selected) connect();
  else machines();
});
if ("serviceWorker" in navigator)
  navigator.serviceWorker.register("/sw.js").catch(() => {
    status.textContent = "Offline installation unavailable";
  });
machines();

function fitTerminal() {
  const host = content.firstElementChild as HTMLElement | null;
  const screen = host?.querySelector<HTMLElement>(".xterm-screen");
  if (host && screen)
    host.style.zoom = String(
      Math.min(1, Math.max(0.6, content.clientWidth / screen.clientWidth)),
    );
}
new ResizeObserver(fitTerminal).observe(content);
