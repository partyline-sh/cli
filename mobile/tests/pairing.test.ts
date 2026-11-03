import { afterEach, describe, expect, it, vi } from "vitest";
import { base64 } from "@scure/base";
import { sha256 } from "@noble/hashes/sha256";
import { x25519 } from "@noble/curves/ed25519";
import { pairCode, pairComputer, parseOffer } from "../src/runtime/pairing";
import { HandshakeState } from "../src/runtime/protocol/noise";
const hex = (b: Uint8Array) =>
  Array.from(b, (v) => v.toString(16).padStart(2, "0")).join("");
const link =
  "https://relay.test/#p=IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIzMzMzMzMzMzMzMzMzMzMz";
afterEach(() => vi.unstubAllGlobals());
describe("native pairing", () => {
  it("turns malformed pasted input into readable guidance", () => {
    expect(() => parseOffer("not-a-code")).toThrow(
      "Scan or paste the pairing link",
    );
    expect(() => parseOffer("PTLN-PAIR {broken")).toThrow(
      "That pairing code is not valid",
    );
    expect(() => parseOffer("PTLN-PAIR null")).toThrow(
      "That pairing code is not valid",
    );
  });

  it("matches the independent Go link and human-code vectors", () => {
    expect(parseOffer(link)).toEqual({
      relay: "https://relay.test",
      public: "22".repeat(32),
      psk: "bc9ab9bf462be380ab6ba5a4ba213a3cc1267966156a419a1c707ed4320cc62d",
      invite:
        "0ce907f7ca87a8fbd2bac6f7f99fb6adef1c5be0d281367dcc4af1694530c7e7",
      route: "ac93c285392f9037ac8b833db070c830",
    });
    expect(pairCode(Uint8Array.from({ length: 32 }, (_, i) => i))).toBe("2492");
    for (const bad of [
      "hello",
      link.replace("https:", "http:"),
      link.replace("#p=", "#x="),
      "https://relay.test/#p=AAAA",
    ])
      expect(() => parseOffer(bad)).toThrow();
  });
  it("completes a real Noise pairing exchange only after encrypted machine approval", async () => {
    const key = new Uint8Array(32).fill(2),
      psk = new Uint8Array(32).fill(5);
    const responder = new HandshakeState({
      pattern: "IKpsk2",
      initiator: false,
      staticPrivate: key,
      psk,
    });
    const route = "a".repeat(32),
      encoder = new TextEncoder();
    let stage = 0;
    class Socket {
      onopen?: () => void;
      onclose?: () => void;
      onerror?: () => void;
      onmessage?: (event: { data: ArrayBuffer }) => void;
      binaryType = "";
      constructor() {
        queueMicrotask(() => this.onopen?.());
      }
      close() {}
      send(wire: Uint8Array) {
        let body: Uint8Array;
        const responseStage = stage++;
        if (responseStage === 0)
          body = encoder.encode(JSON.stringify({ ok: true, version: 1 }));
        else if (responseStage === 1) {
          responder.readMessage(wire.slice(17));
          body = responder.writeMessage(
            encoder.encode(
              JSON.stringify({
                challenge: base64.encode(new Uint8Array(32).fill(8)),
              }),
            ),
          );
        } else {
          const payload = JSON.parse(
            new TextDecoder().decode(
              responder.split().receive.decrypt(wire.slice(17)),
            ),
          );
          const fingerprint = hex(
            sha256(base64.decode(payload.static)).slice(0, 16),
          )
            .match(/.{4}/g)!
            .join(" ");
          body = responder.split().send.encrypt(
            encoder.encode(
              JSON.stringify({
                approved: true,
                fingerprint,
                machine: "1".repeat(32),
                phone: "2".repeat(32),
                token: "3".repeat(64),
              }),
            ),
          );
        }
        const bytes = new Uint8Array(17 + body.length);
        bytes[0] = responseStage === 0 ? 1 : 2;
        bytes.set(new Uint8Array(16).fill(0xaa), 1);
        bytes.set(body, 17);
        queueMicrotask(() => this.onmessage?.({ data: bytes.buffer }));
      }
    }
    vi.stubGlobal("WebSocket", Socket);
    const progress = vi.fn();
    const record = await pairComputer(
      "PTLN-PAIR " +
        JSON.stringify({
          relay: "https://relay.test",
          public: hex(x25519.getPublicKey(key)),
          psk: hex(psk),
          invite: "b".repeat(64),
          route,
        }),
      "Test phone",
      progress,
      new AbortController().signal,
    );
    expect(progress.mock.calls[0][0]).toMatchObject({
      status: "awaiting-approval",
      code: expect.stringMatching(/^\d{4}$/),
    });
    expect(record).toMatchObject({
      machine: "1".repeat(32),
      phone: "2".repeat(32),
      relay: "https://relay.test",
    });
    expect(base64.decode(record.private)).toHaveLength(32);
    expect(stage).toBe(3);
  });
});
