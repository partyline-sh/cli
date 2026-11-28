import { Terminal } from "@xterm/xterm";
declare global {
  interface Window {
    terminalReceive: (message: unknown) => void;
    ReactNativeWebView?: { postMessage(value: string): void };
  }
}
const send = (message: unknown) =>
  window.ReactNativeWebView?.postMessage(JSON.stringify(message));
const terminal = new Terminal({
  fontSize: 13,
  fontFamily: "Menlo, monospace",
  scrollback: 2000,
  cursorBlink: true,
  theme: { background: "#0c1016", foreground: "#e5e7eb", cursor: "#ff9b55" },
});
terminal.open(document.getElementById("terminal")!);
terminal.options.disableStdin = true;
terminal.textarea?.setAttribute("autocorrect", "off");
terminal.textarea?.setAttribute("autocapitalize", "off");
terminal.textarea?.setAttribute("spellcheck", "false");
let generation = 0,
  ready = false,
  batch: number[] = [],
  timer: ReturnType<typeof setTimeout> | undefined,
  lastViewport = "";
function flush() {
  clearTimeout(timer);
  timer = undefined;
  if (!batch.length) return;
  const data = btoa(String.fromCharCode(...batch));
  batch = [];
  if (ready) send({ type: "input", data });
}
function input(bytes: Uint8Array) {
  if (!ready) return;
  for (const byte of bytes) {
    batch.push(byte);
    if (batch.length >= 8192) flush();
  }
  if (batch.length && !timer) timer = setTimeout(flush, 8);
}
terminal.onData((data) => input(new TextEncoder().encode(data)));
terminal.onBinary((data) =>
  input(Uint8Array.from(data, (c) => c.charCodeAt(0))),
);
function fit() {
  const cell = terminal.element!.querySelector(
    ".xterm-screen",
  ) as HTMLElement | null;
  if (!cell) return;
  const width = cell.offsetWidth / terminal.cols,
    height = cell.offsetHeight / terminal.rows;
  if (!width || !height) return;
  const columns = Math.max(
      20,
      Math.min(
        300,
        Math.floor((document.documentElement.clientWidth - 4) / width),
      ),
    ),
    rows = Math.max(
      8,
      Math.min(200, Math.floor(document.documentElement.clientHeight / height)),
    );
  const key = `${columns}:${rows}`;
  if (key === lastViewport) return;
  lastViewport = key;
  terminal.resize(columns, rows);
  send({ type: "viewport", columns, rows });
}
window.terminalReceive = (raw: unknown) => {
  if (!raw || typeof raw !== "object") return;
  const m = raw as Record<string, unknown>;
  if (
    m.type === "paste" &&
    typeof m.text === "string" &&
    new TextEncoder().encode(m.text).length > 16384
  ) {
    send({ type: "notice", text: "Paste up to 16 KiB at a time." });
    return;
  }
  if (m.type === "ready") {
    generation++;
    ready = true;
    batch = [];
    clearTimeout(timer);
    terminal.reset();
    terminal.options.disableStdin = false;
    fit();
  } else if (m.type === "closed") {
    generation++;
    ready = false;
    batch = [];
    clearTimeout(timer);
    terminal.options.disableStdin = true;
  } else if (
    m.type === "output" &&
    ready &&
    typeof m.data === "string" &&
    Number.isSafeInteger(m.sequence)
  ) {
    const epoch = generation;
    const bytes = Uint8Array.from(atob(m.data), (c) => c.charCodeAt(0));
    terminal.write(bytes, () => {
      if (epoch === generation) send({ type: "ack", sequence: m.sequence });
    });
  } else if (m.type === "focus") terminal.focus();
  else if (m.type === "blur") { flush(); terminal.blur(); }
  else if (m.type === "history") terminal.scrollPages(-1);
  else if (m.type === "latest") terminal.scrollToBottom();
  else if (m.type === "paste" && ready && typeof m.text === "string") {
    flush();
    terminal.paste(m.text);
  } else if (m.type === "key" && ready && typeof m.key === "string") {
    const app = terminal.modes.applicationCursorKeysMode;
    const keys: Record<string, string> = {
      Escape: "\x1b",
      "C-c": "\x03",
      "C-z": "\x1a",
      Tab: "\t",
      Up: app ? "\x1bOA" : "\x1b[A",
      Down: app ? "\x1bOB" : "\x1b[B",
      Left: app ? "\x1bOD" : "\x1b[D",
      Right: app ? "\x1bOC" : "\x1b[C",
    };
    if (keys[m.key]) {
      flush();
      input(new TextEncoder().encode(keys[m.key]));
      flush();
    }
  }
};
new ResizeObserver(fit).observe(document.documentElement);
requestAnimationFrame(() => {
  fit();
  send({ type: "loaded" });
});

document.addEventListener(
  "paste",
  (event) => {
    event.preventDefault();
    event.stopImmediatePropagation();
    const text = event.clipboardData?.getData("text/plain") || "";
    if (!ready || !text) return;
    if (new TextEncoder().encode(text).length > 16384) {
      send({ type: "notice", text: "Paste up to 16 KiB at a time." });
      return;
    }
    if (/[\r\n]/.test(text)) send({ type: "paste", text });
    else terminal.paste(text);
  },
  true,
);
