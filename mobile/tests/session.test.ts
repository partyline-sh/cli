import { afterEach, expect, it, vi } from 'vitest';
import { base64 } from '@scure/base';
import { x25519 } from '@noble/curves/ed25519';
import { HandshakeState } from '../src/runtime/protocol/noise';
import { PhoneSession, type InnerMessage } from '../src/runtime/protocol/session';

afterEach(() => vi.unstubAllGlobals());

it('opens the native string-URL WebSocket and completes the saved-phone handshake', async () => {
  const phoneKey = new Uint8Array(32).fill(1);
  const machineKey = new Uint8Array(32).fill(2);
  const route = 'ab'.repeat(16);
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  const responder = new HandshakeState({ pattern: 'KK', initiator: false, staticPrivate: machineKey, peerStatic: x25519.getPublicKey(phoneKey) });
  const addresses: string[] = [];
  const clientHello: unknown[] = [];

  class NativeSocket {
    static OPEN = 1;
    readyState = 1;
    binaryType = '';
    onopen?: () => void;
    onmessage?: (event: { data: ArrayBuffer }) => void;
    onerror?: () => void;
    onclose?: () => void;
    stage = 0;
    constructor(url: string) {
      // React Native forwards this value to its string-only native connect API;
      // unlike a browser or Node WebSocket it does not stringify a URL object.
      if (typeof url !== 'string') throw new Error('Native WebSocket URL must be a string');
      addresses.push(url);
      queueMicrotask(() => this.onopen?.());
    }
    reply(kind: number, body: Uint8Array) {
      const frame = new Uint8Array(17 + body.length);
      frame[0] = kind;
      frame.set(Uint8Array.from(route.match(/../g)!, byte => parseInt(byte, 16)), 1);
      frame.set(body, 17);
      queueMicrotask(() => this.onmessage?.({ data: frame.buffer }));
    }
    send(wire: Uint8Array) {
      if (this.stage++ === 0) {
        expect(JSON.parse(decoder.decode(wire.slice(17)))).toMatchObject({ role: 'phone', min: 1, max: 1 });
        this.reply(1, encoder.encode(JSON.stringify({ ok: true, version: 1 })));
      } else if (this.stage === 2) {
        responder.readMessage(wire.slice(17));
        this.reply(2, responder.writeMessage());
      } else {
        clientHello.push(...JSON.parse(decoder.decode(responder.split().receive.decrypt(wire.slice(17)))));
        this.reply(2, responder.split().send.encrypt(encoder.encode(JSON.stringify([
          { type: 'hello', data: { hostname: 'native-fixture', capabilities: ['input'] } },
        ]))));
      }
    }
    close() { this.readyState = 3; }
  }
  vi.stubGlobal('WebSocket', NativeSocket);
  const received: InnerMessage[] = [];
  const session = new PhoneSession({
    machine: 'cd'.repeat(16), phone: route, token: 'ef'.repeat(32),
    private: base64.encode(phoneKey), public: base64.encode(x25519.getPublicKey(machineKey)),
  }, message => received.push(message));
  try {
    session.connect('https://relay.example/old-path?unused=1#unused');
    await vi.waitFor(() => expect(received).toEqual([
      { type: 'hello', data: { hostname: 'native-fixture', capabilities: ['input'] } },
    ]));
    expect(addresses).toEqual(['wss://relay.example/connect']);
    expect(clientHello).toEqual([{ type: 'hello', data: { version: 1, capabilities: ['zstd'] } }]);
  } finally { session.close(); }
});
