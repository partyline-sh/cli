import { x25519 } from "@noble/curves/ed25519";
import { sha256 } from "@noble/hashes/sha256";
import { base64 } from "@scure/base";
import { HandshakeState } from "./protocol/noise";
import type { PairedComputer } from "./storage";
import type { PairingState } from "./types";

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });
const hex = (b: Uint8Array) =>
  Array.from(b, (v) => v.toString(16).padStart(2, "0")).join("");
const unhex = (s: string, n: number) => {
  if (typeof s !== "string" || !new RegExp(`^[a-f0-9]{${n * 2}}$`).test(s))
    throw new Error("Invalid pairing code");
  return Uint8Array.from(s.match(/../g)!, (s) => parseInt(s, 16));
};
export interface Offer {
  relay: string;
  public: string;
  psk: string;
  invite: string;
  route: string;
  machine?: string;
  expires?: number;
}
export function parseOffer(text: string): Offer {
  text = text.trim();
  let offer: Offer;
  if (text.startsWith("PTLN-PAIR ")) {
    try {
      offer = JSON.parse(text.slice(10));
    } catch {
      throw new Error(
        "That pairing code is not valid. Scan a new code on your computer.",
      );
    }
    if (!offer || typeof offer !== "object")
      throw new Error(
        "That pairing code is not valid. Scan a new code on your computer.",
      );
  } else {
    let link: URL;
    try {
      link = new URL(text);
    } catch {
      throw new Error(
        "Scan or paste the pairing link shown by Partyline on your computer.",
      );
    }
    if (!link.hash.startsWith("#p="))
      throw new Error("Scan a Partyline pairing QR code");
    let encoded = link.hash.slice(3).replace(/-/g, "+").replace(/_/g, "/");
    encoded = encoded.padEnd(Math.ceil(encoded.length / 4) * 4, "=");
    const bytes = base64.decode(encoded);
    if (bytes.length !== 48) throw new Error("Invalid pairing link");
    const derive = (label: string) =>
      sha256(
        new Uint8Array([
          ...encoder.encode(`ptln pair ${label}\0`),
          ...bytes.slice(32),
        ]),
      );
    offer = {
      relay: link.origin,
      public: hex(bytes.slice(0, 32)),
      psk: hex(derive("psk")),
      invite: hex(derive("invite")),
      route: hex(derive("route").slice(0, 16)),
    };
  }
  let relay: URL;
  try {
    relay = new URL(offer.relay);
  } catch {
    throw new Error(
      "That pairing code has no valid relay address. Scan a new code on your computer.",
    );
  }
  if (
    relay.protocol !== "https:" ||
    relay.username ||
    relay.password ||
    relay.pathname !== "/" ||
    relay.search ||
    relay.hash
  )
    throw new Error("Pairing requires an HTTPS relay origin");
  unhex(offer.public, 32);
  unhex(offer.psk, 32);
  unhex(offer.invite, 32);
  unhex(offer.route, 16);
  if (offer.machine !== undefined) unhex(offer.machine, 16);
  if (
    offer.expires !== undefined &&
    (!Number.isSafeInteger(offer.expires) ||
      offer.expires <= Date.now() ||
      offer.expires > Date.now() + 600000)
  )
    throw new Error("Pairing code expired; scan the new code");
  return { ...offer, relay: relay.origin };
}
export const pairCode = (publicKey: Uint8Array) =>
  String(new DataView(sha256(publicKey).buffer).getUint32(16) % 10000).padStart(
    4,
    "0",
  );
const fingerprint = (publicKey: Uint8Array) =>
  hex(sha256(publicKey).slice(0, 16)).match(/.{4}/g)!.join(" ");
function frame(route: string, payload: Uint8Array, kind = 2) {
  if (payload.length > 65535) throw new Error("Pairing frame too large");
  const bytes = new Uint8Array(17 + payload.length);
  bytes[0] = kind;
  bytes.set(unhex(route, 16), 1);
  bytes.set(payload, 17);
  return bytes;
}
export function pairComputer(
  text: string,
  name: string,
  progress: (state: PairingState) => void,
  signal: AbortSignal,
): Promise<PairedComputer> {
  const offer = parseOffer(text);
  const privateKey = crypto.getRandomValues(new Uint8Array(32));
  const publicKey = x25519.getPublicKey(privateKey);
  const handshake = new HandshakeState({
    pattern: "IKpsk2",
    initiator: true,
    staticPrivate: privateKey,
    peerStatic: unhex(offer.public, 32),
    psk: unhex(offer.psk, 32),
  });
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new Error("Pairing cancelled"));
      return;
    }
    const ws = new WebSocket(
      offer.relay.replace(/^https:/, "wss:") + "/connect",
    );
    ws.binaryType = "arraybuffer";
    let stage = 0,
      ended = false;
    const abort = () => finish(new Error("Pairing cancelled"));
    const timeout = setTimeout(
      () => finish(new Error("Pairing timed out")),
      120000,
    );
    function finish(error?: Error, computer?: PairedComputer) {
      if (ended) return;
      ended = true;
      clearTimeout(timeout);
      signal.removeEventListener("abort", abort);
      ws.onclose = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.close();
      if (error) reject(error);
      else resolve(computer!);
    }
    signal.addEventListener("abort", abort, { once: true });
    ws.onopen = () =>
      ws.send(
        frame(
          offer.route,
          encoder.encode(
            JSON.stringify({
              role: "invite",
              token: offer.invite,
              min: 1,
              max: 1,
            }),
          ),
          1,
        ),
      );
    ws.onerror = () => finish(new Error("Relay connection failed"));
    ws.onclose = () =>
      finish(new Error("Invite refused or computer unavailable"));
    ws.onmessage = (event) => {
      try {
        if (!(event.data instanceof ArrayBuffer))
          throw new Error("Invalid relay frame");
        const b = new Uint8Array(event.data);
        if (
          b.length < 17 ||
          b.length > 65552 ||
          hex(b.slice(1, 17)) !== offer.route
        )
          throw new Error("Invalid pairing route");
        if (stage === 0) {
          const ack = JSON.parse(decoder.decode(b.slice(17)));
          if (b[0] !== 1 || !ack.ok || ack.version !== 1)
            throw new Error("Relay refused pairing");
          stage = 1;
          ws.send(frame(offer.route, handshake.writeMessage()));
        } else if (stage === 1) {
          if (b[0] !== 2) throw new Error("Invalid handshake frame");
          const challenge = JSON.parse(
            decoder.decode(handshake.readMessage(b.slice(17))),
          );
          if (base64.decode(challenge.challenge).length !== 32)
            throw new Error("Invalid computer challenge");
          handshake.split();
          stage = 2;
          progress({
            status: "awaiting-approval",
            code: pairCode(publicKey),
            fingerprint: fingerprint(publicKey),
          });
          ws.send(
            frame(
              offer.route,
              handshake
                .split()
                .send.encrypt(
                  encoder.encode(
                    JSON.stringify({ name, static: base64.encode(publicKey) }),
                  ),
                ),
            ),
          );
        } else {
          if (b[0] !== 2) throw new Error("Invalid approval frame");
          const result = JSON.parse(
            decoder.decode(handshake.split().receive.decrypt(b.slice(17))),
          );
          if (!result.approved) throw new Error("Computer refused pairing");
          if (
            result.fingerprint !== fingerprint(publicKey) ||
            (offer.machine && offer.machine !== result.machine)
          )
            throw new Error("Pairing fingerprint mismatch");
          unhex(result.machine, 16);
          unhex(result.phone, 16);
          unhex(result.token, 32);
          finish(undefined, {
            machine: result.machine,
            phone: result.phone,
            token: result.token,
            private: base64.encode(privateKey),
            public: base64.encode(unhex(offer.public, 32)),
            relay: offer.relay,
            name: "Computer",
            pairedAt: Date.now(),
          });
        }
      } catch (error) {
        finish(error instanceof Error ? error : new Error("Pairing failed"));
      }
    };
  });
}
