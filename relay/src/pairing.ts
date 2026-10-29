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
export async function pair(
  typed: string,
  name = "Phone",
  progress: (message: string) => void = () => {},
): Promise<void> {
  if (!typed.startsWith("PTLN-PAIR "))
    throw new Error("Paste the pairing string from the popup");
  const o = JSON.parse(typed.slice(10));
  const origin = new URL(o.relay);
  if (
    origin.origin !== location.origin ||
    origin.pathname !== "/" ||
    origin.search ||
    origin.hash
  )
    throw new Error("Open the app at the relay shown in the popup");
  if (
    !Number.isSafeInteger(o.expires) ||
    o.expires <= Date.now() ||
    o.expires > Date.now() + 600_000
  )
    throw new Error("Pairing string expired");
  unhex(o.machine, 16);
  unhex(o.invite, 32);
  unhex(o.route, 16);
  if (!name.trim() || new TextEncoder().encode(name).length > 80)
    throw new Error("Use a phone name of 1–80 bytes");
  const staticPrivate = crypto.getRandomValues(new Uint8Array(32));
  const phonePublic = x25519.getPublicKey(staticPrivate);
  const phoneFingerprint = await fingerprint(phonePublic);
  const machineFingerprint = await fingerprint(unhex(o.public, 32));
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
    let registration: PublicKeyCredential | undefined;
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
            registration =
              ((await navigator.credentials.create({
                signal: abort.signal,
                publicKey: {
                  challenge: new Uint8Array(challenge),
                  rp: { id: origin.hostname, name: "Partyline" },
                  user: {
                    id: crypto.getRandomValues(new Uint8Array(32)),
                    name,
                    displayName: name,
                  },
                  pubKeyCredParams: [{ type: "public-key", alg: -7 }],
                  authenticatorSelection: {
                    userVerification: "required",
                    residentKey: "required",
                  },
                  attestation: "none",
                  timeout: 60_000,
                },
              })) as PublicKeyCredential | null) ?? undefined;
            if (!registration) throw new Error("Passkey creation cancelled");
            const assertion = (await navigator.credentials.get({
              signal: abort.signal,
              publicKey: {
                challenge: new Uint8Array(challenge),
                rpId: origin.hostname,
                allowCredentials: [
                  { type: "public-key", id: registration.rawId },
                ],
                userVerification: "required",
                timeout: 60_000,
              },
            })) as PublicKeyCredential | null;
            if (!assertion) throw new Error("Passkey verification cancelled");
            if (settled) return;
            const att =
              registration.response as AuthenticatorAttestationResponse;
            const response =
              assertion.response as AuthenticatorAssertionResponse;
            const payload = {
              name,
              static: base64(phonePublic),
              registration: {
                id: credentialID(registration.rawId),
                clientData: base64(att.clientDataJSON),
                attestation: base64(att.attestationObject),
              },
              assertion: {
                id: credentialID(assertion.rawId),
                clientData: base64(response.clientDataJSON),
                authenticatorData: base64(response.authenticatorData),
                signature: base64(response.signature),
              },
            };
            progress(
              `Compare phone fingerprint ${phoneFingerprint} in the machine popup, then approve. Machine fingerprint ${machineFingerprint}.`,
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
              throw new Error("Pairing refused or timed out; nothing saved");
            if (
              result.fingerprint !== phoneFingerprint ||
              result.machine !== o.machine ||
              !/^[a-f0-9]{32}$/.test(result.phone) ||
              !/^[a-f0-9]{64}$/.test(result.token)
            )
              throw new Error("Invalid pairing approval");
            const storageKey = `partyline.phone.${o.machine}`;
            localStorage.setItem(
              storageKey,
              JSON.stringify({
                machine: o.machine,
                public: base64(unhex(o.public, 32)),
                phone: result.phone,
                private: base64(staticPrivate),
                credential: credentialID(registration!.rawId),
                token: result.token,
                fingerprint: phoneFingerprint,
                machineFingerprint,
              }),
            );
            progress(
              `Paired. Phone fingerprint ${phoneFingerprint}. Machine fingerprint ${machineFingerprint}.`,
            );
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
const form =
  typeof document === "undefined" ? null : document.querySelector("form");
form?.addEventListener("submit", async (event) => {
  event.preventDefault();
  const status = document.querySelector("#status")!;
  const button = form.querySelector("button")!;
  button.disabled = true;
  try {
    await pair(
      (document.querySelector("textarea") as HTMLTextAreaElement).value.trim(),
      (document.querySelector("#name") as HTMLInputElement).value.trim(),
      (message) => {
        status.textContent = message;
      },
    );
  } catch (error) {
    status.textContent =
      error instanceof Error ? error.message : "Pairing failed";
  } finally {
    button.disabled = false;
  }
});

// Camera frames stay on the phone. Pasting works when camera access is unavailable.
if (typeof document !== "undefined")
  document.querySelector("#scan")?.addEventListener("click", async () => {
    const status = document.querySelector("#status")!;
    const video = document.querySelector("video")!;
    const scanButton = document.querySelector<HTMLButtonElement>("#scan")!;
    scanButton.disabled = true;
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
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: "environment" },
        audio: false,
      });
      video.srcObject = stream;
      video.hidden = false;
      await video.play();
      document.addEventListener("visibilitychange", onVisibility);
      timer = setTimeout(stop, 60_000);
      const canvas = document.createElement("canvas");
      const ctx = canvas.getContext("2d", { willReadFrequently: true })!;
      const deadline = Date.now() + 60_000;
      const scan = () => {
        if (stopped) return;
        if (document.hidden || Date.now() > deadline) {
          stop();
          return;
        }
        if (video.readyState >= 2) {
          canvas.width = video.videoWidth;
          canvas.height = video.videoHeight;
          ctx.drawImage(video, 0, 0);
          const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height);
          const code = jsQR(pixels.data, pixels.width, pixels.height);
          if (code?.data.startsWith("PTLN-PAIR ")) {
            (document.querySelector("textarea") as HTMLTextAreaElement).value =
              code.data;
            stop();
            status.textContent = "QR scanned. Tap Pair to connect.";
            return;
          }
        }
        setTimeout(scan, 150);
      };
      scan();
    } catch {
      stop();
      status.textContent =
        "Camera unavailable. Paste the pairing string instead.";
    }
  });
