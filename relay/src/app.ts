import { PhoneSession, type SavedPhone } from "./session";
import { Inputs } from "./input";
import { Screens, type Computer } from "./screens";

interface Connection extends Computer {
  saved: SavedPhone;
  session?: PhoneSession;
  ready: boolean;
  connecting?: boolean;
  stopped?: boolean;
  retry?: ReturnType<typeof setTimeout>;
  failures: number;
}
const root = document.querySelector<HTMLElement>("#app")!;
const computers: Connection[] = [];
let focused: { computer: Computer; pane: string } | undefined;
const inputs = new Inputs((id, message) => {
 const computer = computers.find(c => c.id === id);
 if (!computer?.ready || !computer.session) throw new Error("Session unavailable");
 computer.session.send([message]);
});
const screens = new Screens(
  root,
  computers,
  (computer, pane) => {
    if (focused) {
      (focused.computer as Connection).session?.unsubscribe(focused.pane);
      (focused.computer as Connection).session?.unsubscribe(`transcript:${focused.pane}`);
    }
    focused = computer && pane ? { computer, pane } : undefined;
    if (focused && (computer as Connection).ready) {
      (computer as Connection).session?.subscribe(pane!);
      (computer as Connection).session?.subscribe(`transcript:${pane}`);
    }
  },
  pair,
  showInstall,
  inputs,
);

function pair() {
  install.close();
  document.body.dataset.view = "pair";
  window.dispatchEvent(new Event("pairing-start"));
}
function loadComputers() {
  document.body.dataset.view = "machines";
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i)!;
    if (!key.startsWith("partyline.phone.")) continue;
    try {
      const saved = JSON.parse(localStorage.getItem(key)!) as SavedPhone;
      if (
        !saved.machine ||
        !saved.token ||
        !saved.private ||
        !saved.public ||
        !saved.phone
      )
        continue;
      const existing = computers.find(c => c.id === saved.machine);
      if (existing) {
        if (
          existing.saved.token === saved.token &&
          existing.saved.phone === saved.phone &&
          existing.saved.private === saved.private &&
          existing.saved.public === saved.public
        ) continue;
        clearTimeout(existing.retry);
        existing.retry = undefined;
        const previous = existing.session;
        existing.session = undefined;
        previous?.close();
        existing.saved = saved;
        existing.stopped = false;
        existing.ready = false;
        existing.connecting = false;
        existing.failures = 0;
        existing.snapshots.clear();
        existing.received?.clear();
        connect(existing);
        continue;
      }
      const computer: Connection = {
        id: saved.machine,
        saved,
        status: "Connecting…",
        snapshots: new Map(),
        ready: false,
        failures: 0,
        fresh: new Set(),
        received: new Map(),
      };
      computers.push(computer);
      connect(computer);
    } catch {
      /* A damaged record does not hide other computers. */
    }
  }
  screens.restore();
  screens.update(computers[0]);
}
function schedule(computer: Connection, immediate = false) {
  if (computer.stopped || computer.connecting || computer.ready) return;
  if (computer.retry !== undefined) {
    if (!immediate) return;
    clearTimeout(computer.retry);
  }
  const delay = immediate
    ? 0
    : Math.min(30000, 1000 * 2 ** Math.min(computer.failures++, 5)) *
      (0.8 + Math.random() * 0.2);
  computer.retry = setTimeout(() => {
    computer.retry = undefined;
    connect(computer);
  }, delay);
}
function connect(computer: Connection) {
  if (computer.stopped || computer.connecting || computer.ready || document.hidden)
    return;
  computer.connecting = true;
  computer.fresh?.clear();
  computer.status = `Reconnecting to ${computer.id}…`;
  screens.update(computer);
  const current = new PhoneSession(
    computer.saved,
    (message) => {
      if (computer.session !== current) return;
      if (message.type === "hello") {
        computer.connecting = false;
        computer.ready = true;
        computer.failures = 0;
        computer.status = "Connected";
        for (const topic of ["sessions", "board", "chat"])
          current.subscribe(topic);
        if (focused?.computer === computer) {
          current.subscribe(focused.pane);
          current.subscribe(`transcript:${focused.pane}`);
        }
      } else if (message.type === "input_result") {
        inputs.result(computer.id, message);
      } else {
        const topic = message.pane || message.type;
        computer.snapshots.set(topic, message);
        computer.fresh?.add(topic);
        computer.received?.set(topic, Date.now());
      }
      screens.update(computer, message);
    },
    (reason) => {
      if (computer.session !== current) return;
      computer.ready = false;
      inputs.disconnected(computer.id);
      computer.connecting = false;
      computer.fresh?.clear();
      computer.stopped = ["revoked", "device refused", "unknown phone"].includes(reason);
      computer.status = computer.stopped ? "Pair again" : `Reconnecting to ${computer.id}…`;
      screens.update(computer);
      if (!document.hidden) schedule(computer);
    },
  );
  computer.session = current;
  current.connect();
}

const install = document.createElement("dialog");
install.className = "install-sheet";
install.setAttribute("aria-labelledby", "install-title");
const title = document.createElement("h2");
title.id = "install-title";
title.textContent = "Add to Home Screen";
const steps = document.createElement("ol");
for (const text of [
  "Tap Share in Safari’s toolbar.",
  "Choose Add to Home Screen.",
  "Open Partyline from your Home Screen, then pair your computer.",
]) {
  const li = document.createElement("li");
  li.textContent = text;
  append(steps, li);
}
const note = document.createElement("p");
note.textContent =
  "Whether Safari pairing carries into the installed app depends on the device. Pair again if needed.";
const dismiss = document.createElement("button");
dismiss.textContent = "Not now";
dismiss.className = "primary";
dismiss.onclick = () => install.close();
append(install, title, steps, note, dismiss);
append(document.body, install);
function showInstall() {
  install.showModal();
}

interface InstallPrompt extends Event {
  prompt(): Promise<void>;
  userChoice: Promise<{ outcome: string }>;
}
let installPrompt: InstallPrompt | undefined;
const installButton = document.createElement("button");
installButton.textContent = "Install";
installButton.className = "install-button";
installButton.hidden = true;
installButton.onclick = async () => {
  const prompt = installPrompt;
  if (!prompt) return;
  await prompt.prompt();
  await prompt.userChoice;
  installPrompt = undefined;
  installButton.hidden = true;
};
append(document.body, installButton);
window.addEventListener("beforeinstallprompt", (event) => {
  event.preventDefault();
  installPrompt = event as InstallPrompt;
  installButton.hidden = false;
});
window.addEventListener("appinstalled", () => {
  installPrompt = undefined;
  installButton.hidden = true;
});
window.addEventListener("pairing-cancel", loadComputers);
window.addEventListener("hashchange", () => install.close());
document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    for (const computer of computers) computer.session?.close();
  } else resume();
});
function resume() {
  if (document.hidden) return;
  for (const computer of computers) {
    // A suspended socket can still look open. Resume with a fresh handshake
    // even when the OS never delivered the background visibility event.
    if (computer.ready) computer.session?.close();
    schedule(computer, true);
  }
}
window.addEventListener("online", resume);
window.addEventListener("pageshow", resume);
if ("serviceWorker" in navigator)
  navigator.serviceWorker.register("/sw.js").catch(() => {});
loadComputers();
const standalone =
  matchMedia("(display-mode: standalone)").matches ||
  (navigator as Navigator & { standalone?: boolean }).standalone;
const safari =
  /Safari/.test(navigator.userAgent) &&
  !/Chrome|Chromium|CriOS|FxiOS|EdgiOS|Android/.test(navigator.userAgent);
if (document.body.dataset.link || location.hash.startsWith("#p=")) pair();
else if (!computers.length) {
  if (safari && !standalone) showInstall();
  else pair();
}

function append(parent: HTMLElement, ...children: HTMLElement[]) {
  for (const child of children) parent.appendChild(child);
}

// Safari's layout viewport does not shrink with the on-screen keyboard.
function fitViewport() {
  const viewport = window.visualViewport;
  if (!viewport) return;
  document.body.dataset.keyboard = String(viewport.height < 560);
  document.documentElement.style.setProperty("--app-height", `${viewport.height}px`);
  document.documentElement.style.setProperty("--app-top", `${viewport.offsetTop}px`);
}
window.visualViewport?.addEventListener("resize", fitViewport);
window.visualViewport?.addEventListener("scroll", fitViewport);
fitViewport();
