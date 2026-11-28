import { x25519 } from "@noble/curves/ed25519";
import jsQR from "jsqr";
import { HandshakeState } from "./noise";
const unhex = (s: string, n: number) => {
  if (typeof s !== "string" || !new RegExp(`^[a-f0-9]{${n * 2}}$`).test(s))
    throw new Error("Invalid pairing field");
  return Uint8Array.from(s.match(/../g)!, (x) => parseInt(x, 16));
};
export function dataFrame(
  route: string,
  payload: Uint8Array,
  kind = 2,
): Uint8Array {
  const out = new Uint8Array(17 + payload.length);
  out[0] = kind;
  out.set(unhex(route, 16), 1);
  out.set(payload, 17);
  return out;
}
const base64 = (b: ArrayBuffer | Uint8Array) =>
  btoa(String.fromCharCode(...new Uint8Array(b instanceof Uint8Array ? b : b)));
const from64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const credentialID = (b: ArrayBuffer) =>
  base64(b).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
export async function fingerprint(key: Uint8Array): Promise<string> {
  const hash = new Uint8Array(
    await crypto.subtle.digest("SHA-256", new Uint8Array(key)),
  );
  return Array.from(hash.slice(0, 16), (b) => b.toString(16).padStart(2, "0"))
    .join("")
    .match(/.{4}/g)!
    .join(" ");
}
// pairCode is the four digits the phone and partyline's pairing popup both show for the phone's
// key (phonePairCode in Go): the person compares them, then presses ⏎ in partyline.
export async function pairCode(key: Uint8Array): Promise<string> {
  const hash = new Uint8Array(
    await crypto.subtle.digest("SHA-256", new Uint8Array(key)),
  );
  const n = new DataView(hash.buffer).getUint32(16) % 10000;
  return String(n).padStart(4, "0");
}
const hexOf = (b: Uint8Array) =>
  Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
// fromLink turns what was scanned, pasted or opened into the pairing string: a pairing link
// (<relay>/#p=<machine key, secret>, pairLink in Go) or the PTLN-PAIR string itself; "" for anything
// else. The link carries a 16-byte secret; the handshake key, invite and route derive from it as
// pairDerive does in Go.
export async function fromLink(text: string): Promise<string> {
  text = text.trim();
  if (text.startsWith("PTLN-PAIR ")) return text;
  const at = text.indexOf("#p=");
  if (at < 0) return "";
  try {
    const relay = new URL(text.slice(0, at));
    const b = Uint8Array.from(
      atob(
        text
          .slice(at + 3)
          .replace(/-/g, "+")
          .replace(/_/g, "/"),
      ),
      (c) => c.charCodeAt(0),
    );
    if (b.length !== 48) return "";
    const secret = b.slice(32);
    const derive = async (label: string) =>
      new Uint8Array(
        await crypto.subtle.digest(
          "SHA-256",
          new Uint8Array([
            ...new TextEncoder().encode(`ptln pair ${label}\0`),
            ...secret,
          ]),
        ),
      );
    return (
      "PTLN-PAIR " +
      JSON.stringify({
        relay: relay.origin,
        public: hexOf(b.slice(0, 32)),
        psk: hexOf(await derive("psk")),
        invite: hexOf(await derive("invite")),
        route: hexOf((await derive("route")).slice(0, 16)),
      })
    );
  } catch {
    return "";
  }
}
// phoneName is what the computer is told this phone is called.
export function phoneName(): string {
  const ua = typeof navigator === "undefined" ? "" : navigator.userAgent;
  if (/iPad/.test(ua)) return "iPad";
  if (/iPhone|iPod/.test(ua)) return "iPhone";
  if (/Android/.test(ua)) return "Android phone";
  return "Phone";
}
export async function pair(
  typed: string,
  name = "Phone",
  progress: (message: string) => void = () => {},
): Promise<void> {
  if (!typed.startsWith("PTLN-PAIR "))
    throw new Error("Scan the code on your computer");
  const o = JSON.parse(typed.slice(10));
  const origin = new URL(o.relay);
  if (
    origin.origin !== location.origin ||
    origin.pathname !== "/" ||
    origin.search ||
    origin.hash
  )
    throw new Error("Open the app at the relay shown in the popup");
  // The short link carries no expiry or machine id: the computer enforces the expiry, and the
  // machine id arrives with the approval.
  if (
    o.expires !== undefined &&
    (!Number.isSafeInteger(o.expires) ||
      o.expires <= Date.now() ||
      o.expires > Date.now() + 600_000)
  )
    throw new Error(
      "That code has expired. Scan the new one on your computer.",
    );
  if (o.machine !== undefined) unhex(o.machine, 16);
  unhex(o.invite, 32);
  unhex(o.route, 16);
  if (!name.trim() || new TextEncoder().encode(name).length > 80)
    throw new Error("Use a phone name of 1–80 bytes");
  const staticPrivate = crypto.getRandomValues(new Uint8Array(32));
  const phonePublic = x25519.getPublicKey(staticPrivate);
  const phoneFingerprint = await fingerprint(phonePublic);
  const machineFingerprint = await fingerprint(unhex(o.public, 32));
  const code = await pairCode(phonePublic);
  const handshake = new HandshakeState({
    pattern: "IKpsk2",
    initiator: true,
    staticPrivate,
    peerStatic: unhex(o.public, 32),
    psk: unhex(o.psk, 32),
  });
  await new Promise<void>((resolve, reject) => {
    const ws = new WebSocket(
      `${origin.protocol === "https:" ? "wss:" : "ws:"}//${origin.host}/connect`,
    );
    ws.binaryType = "arraybuffer";
    let hello = false;
    let stage = 0;
    let settled = false;
    const abort = new AbortController();
    const timer = setTimeout(
      () => finish(new Error("Pairing timed out")),
      120_000,
    );
    function finish(error?: Error) {
      if (settled) return;
      settled = true;
      abort.abort();
      clearTimeout(timer);
      ws.onclose = null;
      ws.close();
      if (error) reject(error);
      else resolve();
    }
    ws.onopen = () =>
      ws.send(
        dataFrame(
          o.route,
          new TextEncoder().encode(
            JSON.stringify({ role: "invite", token: o.invite, min: 1, max: 1 }),
          ),
          1,
        ),
      );
    ws.onerror = () => finish(new Error("Relay connection failed"));
    ws.onclose = () =>
      finish(new Error("Invite refused or machine unavailable"));
    ws.onmessage = async (event) => {
      try {
        if (!(event.data instanceof ArrayBuffer))
          throw new Error("Invalid relay frame");
        const b = new Uint8Array(event.data);
        if (
          b.length < 17 ||
          b.length > 65552 ||
          !unhex(o.route, 16).every((v, i) => v === b[i + 1])
        )
          throw new Error("Invalid route");
        if (!hello) {
          const ack = JSON.parse(new TextDecoder().decode(b.slice(17)));
          if (b[0] !== 1 || !ack.ok || ack.version !== 1)
            throw new Error("Invalid relay hello");
          hello = true;
          ws.send(dataFrame(o.route, handshake.writeMessage()));
        } else {
          if (b[0] !== 2) throw new Error("Invalid handshake frame");
          if (stage === 0) {
            stage = 1;
            const challengeMessage = JSON.parse(
              new TextDecoder().decode(handshake.readMessage(b.slice(17))),
            );
            handshake.split();
            const challenge = from64(challengeMessage.challenge);
            if (challenge.length !== 32)
              throw new Error("Invalid machine challenge");
            if (settled) return;
            const payload = { name, static: base64(phonePublic) };
            progress(
              `Check that partyline on your computer shows ${code}, then press ⏎ there to pair.`,
            );
            window.dispatchEvent(
              new CustomEvent("pair-code", { detail: code }),
            );
            stage = 2;
            ws.send(
              dataFrame(
                o.route,
                handshake
                  .split()
                  .send.encrypt(
                    new TextEncoder().encode(JSON.stringify(payload)),
                  ),
              ),
            );
          } else if (stage === 2) {
            stage = 3;
            const result = JSON.parse(
              new TextDecoder().decode(
                handshake.split().receive.decrypt(b.slice(17)),
              ),
            );
            if (!result.approved)
              throw new Error(
                "Pairing was refused or timed out. Nothing was saved.",
              );
            if (
              result.fingerprint !== phoneFingerprint ||
              (o.machine !== undefined && result.machine !== o.machine) ||
              !/^[a-f0-9]{32}$/.test(result.machine) ||
              !/^[a-f0-9]{32}$/.test(result.phone) ||
              !/^[a-f0-9]{64}$/.test(result.token)
            )
              throw new Error("Invalid pairing approval");
            const storageKey = `partyline.phone.${result.machine}`;
            localStorage.setItem(
              storageKey,
              JSON.stringify({
                machine: result.machine,
                public: base64(unhex(o.public, 32)),
                phone: result.phone,
                private: base64(staticPrivate),
                credential: "",
                token: result.token,
                fingerprint: phoneFingerprint,
                machineFingerprint,
              }),
            );
            progress("Paired with your computer.");
            window.dispatchEvent(new Event("paired"));
            finish();
          } else throw new Error("Unexpected pairing message");
        }
      } catch (error) {
        finish(error instanceof Error ? error : new Error("Pairing failed"));
      }
    };
  });
}
// Pairing on the phone: a code from the Camera's link (on load, or when an open page's link
// changes), the in-app scanner, a photo or a paste starts it at once. The phone connects, shows the
// four-digit code, and waits while the person checks partyline shows the same and presses ⏎ there.
const pairing =
  typeof document === "undefined"
    ? null
    : document.querySelector<HTMLElement>("#pairing");
function step(name: "scan" | "wait" | "done") {
  pairing!.dataset.step = name;
}
let pairingNow = false;
async function took(typed: string) {
  if (pairingNow) return;
  pairingNow = true;
  const status = document.querySelector("#status")!;
  document.querySelector("#paircode")!.textContent = "";
  status.textContent = "Connecting to your computer…";
  document.body.dataset.view = "pair";
  step("wait");
  try {
    await pair(typed, phoneName(), (message) => {
      status.textContent = message;
    });
    step("done");
  } catch (error) {
    status.textContent =
      (error instanceof Error ? error.message : "Pairing failed") +
      " Scan the code on your computer again.";
    step("scan"); // a code works once: a retry needs a fresh one
  } finally {
    pairingNow = false;
  }
}
// A link in the address: read it, then take it out of the address and history entry at once, so
// the page never shows it and a reload cannot reuse it.
async function linkInAddress() {
  if (!location.hash.startsWith("#p=")) return;
  const href = location.href;
  history.replaceState(null, "", location.pathname + location.search);
  const typed = await fromLink(href);
  if (typed) took(typed);
}
if (pairing) {
  if (location.hash.startsWith("#p=")) document.body.dataset.link = "1";
  // Scanning again while the page is open changes only the part after #, which reloads nothing.
  window.addEventListener("hashchange", linkInAddress);
  document.querySelector("#paste-show")!.addEventListener("click", () => {
    document.querySelector<HTMLElement>("#paste")!.hidden = false;
    document.querySelector<HTMLTextAreaElement>("#offer")!.focus();
  });
  document.querySelector("#paste-use")!.addEventListener("click", async () => {
    const typed = await fromLink(
      document.querySelector<HTMLTextAreaElement>("#offer")!.value,
    );
    if (!typed) {
      document.querySelector("#status")!.textContent =
        "That is not a pairing code. Copy it from partyline's pairing popup.";
      return;
    }
    took(typed);
  });
  document.querySelector("#pair-done")!.addEventListener("click", () => {
    window.dispatchEvent(new Event("pairing-cancel"));
  });
  document.querySelector("#pair-cancel")!.addEventListener("click", () => {
    window.dispatchEvent(new Event("pairing-cancel"));
  });
  window.addEventListener("pair-code", (event) => {
    document.querySelector("#paircode")!.textContent = (
      event as CustomEvent<string>
    ).detail;
  });
  window.addEventListener("pairing-start", () => {
    if (document.body.dataset.link) {
      delete document.body.dataset.link;
      linkInAddress();
      return;
    }
    if (pairingNow) return;
    document.querySelector("#paircode")!.textContent = "";
    document.querySelector<HTMLTextAreaElement>("#offer")!.value = "";
    document.querySelector<HTMLElement>("#paste")!.hidden = true;
    document.querySelector("#status")!.textContent = "";
    document.querySelector("#photo-show")!.classList.remove("lead");
    step("scan");
  });
}
// Camera frames stay on the phone. Two ways in: a live view (getUserMedia), and a photo taken with
// the phone's own camera, which works where the live view does not start (some Home Screen apps).
// Pasting is the last resort.
function decodeFrame(source: CanvasImageSource, w: number, h: number) {
  const scale = Math.min(1, 1600 / Math.max(w, h)); // a phone photo is far larger than a QR needs
  const canvas = document.createElement("canvas");
  canvas.width = Math.round(w * scale);
  canvas.height = Math.round(h * scale);
  const ctx = canvas.getContext("2d", { willReadFrequently: true })!;
  ctx.drawImage(source, 0, 0, canvas.width, canvas.height);
  const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height);
  const code = jsQR(pixels.data, pixels.width, pixels.height);
  return code?.data ?? ""; // the QR's text; fromLink says whether it is a pairing code
}
function cameraTrouble(why: string) {
  document.querySelector("#status")!.textContent =
    why + " Take a photo of the code instead, or paste it.";
  document.querySelector<HTMLElement>("#photo-show")!.classList.add("lead");
}
if (typeof document !== "undefined") {
  const photo = document.querySelector<HTMLInputElement>("#photo");
  document
    .querySelector("#photo-show")
    ?.addEventListener("click", () => photo?.click());
  photo?.addEventListener("change", async () => {
    const file = photo.files?.[0];
    photo.value = "";
    if (!file) return;
    const status = document.querySelector("#status")!;
    status.textContent = "Reading the photo…";
    try {
      const image = await createImageBitmap(file);
      const raw = decodeFrame(image, image.width, image.height);
      image.close();
      const typed = raw ? await fromLink(raw) : "";
      if (typed) took(typed);
      else
        status.textContent =
          "No pairing code in that photo. Fill the frame with the code and try again.";
    } catch {
      status.textContent =
        "That photo could not be read. Try again, or paste the code.";
    }
  });
}
if (typeof document !== "undefined")
  document.querySelector("#scan")?.addEventListener("click", async () => {
    const status = document.querySelector("#status")!;
    const video = document.querySelector("video")!;
    const scanButton = document.querySelector<HTMLButtonElement>("#scan")!;
    scanButton.disabled = true;
    status.textContent = "Starting the camera…";
    let stream: MediaStream | undefined;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onVisibility = () => {
      if (document.hidden) stop();
    };
    const stop = () => {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
      scanButton.disabled = false;
      stream?.getTracks().forEach((track) => track.stop());
      video.hidden = true;
    };
    // A camera that neither starts nor fails would otherwise leave the button doing nothing.
    const startedIn = <T>(p: Promise<T>) =>
      Promise.race([
        p,
        new Promise<never>((_, reject) =>
          setTimeout(() => reject(new Error("timeout")), 8000),
        ),
      ]);
    try {
      if (!navigator.mediaDevices?.getUserMedia)
        throw new Error("This browser has no live camera here.");
      stream = await startedIn(
        navigator.mediaDevices.getUserMedia({
          video: { facingMode: "environment" },
          audio: false,
        }),
      );
      video.srcObject = stream;
      video.hidden = false;
      await startedIn(video.play());
      status.textContent = "Point the camera at the code on your computer.";
      document.addEventListener("visibilitychange", onVisibility);
      timer = setTimeout(stop, 60_000);
      const deadline = Date.now() + 60_000;
      const scan = () => {
        if (stopped) return;
        if (document.hidden || Date.now() > deadline) {
          stop();
          return;
        }
        if (video.readyState >= 2) {
          const raw = decodeFrame(video, video.videoWidth, video.videoHeight);
          if (raw.includes("#p=") || raw.startsWith("PTLN-PAIR ")) {
            stop();
            fromLink(raw).then((typed) =>
              typed
                ? took(typed)
                : cameraTrouble("That is not a partyline pairing code."),
            );
            return;
          }
        }
        setTimeout(scan, 150);
      };
      scan();
    } catch (error) {
      stop();
      const name = error instanceof Error ? error.name : "";
      const message = error instanceof Error ? error.message : "";
      cameraTrouble(
        name === "NotAllowedError"
          ? "Camera access is off for this app (Settings → Safari → Camera on an iPhone)."
          : message === "timeout"
            ? "The camera did not start."
            : message.startsWith("This browser")
              ? message
              : `The camera could not start (${name || "error"}).`,
      );
    }
  });
