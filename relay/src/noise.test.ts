import { describe, expect, it } from 'vitest';
import { x25519 } from '@noble/curves/ed25519';
import vectors from '../testdata/noise-vectors.json';
import { CipherState, HandshakeState, type NoiseConfig } from './noise';

const unhex = (s: string): Uint8Array => Uint8Array.from(s.match(/../g) ?? [], byte => parseInt(byte, 16));
const hex = (b: Uint8Array): string => Array.from(b, byte => byte.toString(16).padStart(2, '0')).join('');
function configs(v: typeof vectors[number]): [NoiseConfig, NoiseConfig] {
  const shared = { pattern: v.pattern as NoiseConfig['pattern'], prologue: unhex(v.prologue), psk: v.psk ? unhex(v.psk) : undefined };
  return [
    { ...shared, initiator: true, staticPrivate: unhex(v.initiatorStatic), peerStatic: unhex(v.responderPublic), random: () => unhex(v.initiatorEphemeral) },
    { ...shared, initiator: false, staticPrivate: unhex(v.responderStatic), peerStatic: v.pattern === 'KK' ? unhex(v.initiatorPublic) : undefined, random: () => unhex(v.responderEphemeral) },
  ];
}
function complete(i: HandshakeState, r: HandshakeState): void {
  r.readMessage(i.writeMessage()); i.readMessage(r.writeMessage());
}

describe('Go Noise vectors', () => {
  for (const v of vectors) {
    it(`${v.pattern}, prologue=${v.prologue}`, () => {
      expect(hex(x25519.getPublicKey(unhex(v.initiatorStatic)))).toBe(v.initiatorPublic);
      expect(hex(x25519.getPublicKey(unhex(v.responderStatic)))).toBe(v.responderPublic);
      // Each side reads Go bytes, independently of the bytes produced by its TS peer.
      const [ic, rc] = configs(v);
      const i = new HandshakeState(ic), r = new HandshakeState(rc);
      for (const message of v.handshake) {
        const [sender, receiver] = message.sender === 'initiator' ? [i, r] : [r, i];
        expect(hex(sender.writeMessage(unhex(message.payload)))).toBe(message.ciphertext);
        expect(hex(receiver.readMessage(unhex(message.ciphertext)))).toBe(message.payload);
      }
      expect(hex(i.handshakeHash)).toBe(v.hash);
      expect(hex(r.handshakeHash)).toBe(v.hash);
      expect(hex(r.peerStatic!)).toBe(v.initiatorPublic);
      expect(hex(i.peerStatic!)).toBe(v.responderPublic);
      for (const message of v.transport) {
        const [sender, receiver] = message.sender === 'initiator' ? [i.split().send, r.split().receive] : [r.split().send, i.split().receive];
        expect(hex(sender.encrypt(unhex(message.payload)))).toBe(message.ciphertext);
        const ciphertext = unhex(message.ciphertext);
        const tampered = ciphertext.slice(); tampered[tampered.length - 1] ^= 1;
        expect(() => receiver.decrypt(tampered)).toThrow();
        expect(hex(receiver.decrypt(ciphertext))).toBe(message.payload);
        expect(() => receiver.decrypt(ciphertext)).toThrow();
      }
      expect(() => i.writeMessage()).toThrow();
      expect(() => r.readMessage(new Uint8Array())).toThrow();
    });
  }
});

for (const pattern of ['IKpsk2', 'KK']) {
  const vector = vectors.find(v => v.pattern === pattern)!;
  describe(`${pattern} failure handling`, () => {
    it('enforces handshake turns and completion', () => {
      const [ic, rc] = configs(vector);
      const i = new HandshakeState(ic), r = new HandshakeState(rc);
      expect(() => i.split()).toThrow();
      expect(() => i.handshakeHash).toThrow();
      expect(() => r.writeMessage()).toThrow();
      expect(() => i.readMessage(new Uint8Array())).toThrow();
      complete(i, r);
      expect(i.split().send).toBe(i.split().send);
    });
    it('rejects a truncated or tampered handshake and disables the state', () => {
      for (const truncated of [true, false]) {
        const [ic, rc] = configs(vector);
        const i = new HandshakeState(ic), r = new HandshakeState(rc);
        const msg = i.writeMessage();
        const bad = truncated ? msg.slice(0, 31) : msg.slice();
        if (!truncated) bad[bad.length - 1] ^= 1;
        expect(() => r.readMessage(bad)).toThrow();
        expect(() => r.readMessage(msg)).toThrow();
        expect(() => r.split()).toThrow();
      }
    });
    it('rejects a truncated or tampered responder message and disables the state', () => {
      for (const truncated of [true, false]) {
        const [ic, rc] = configs(vector);
        const i = new HandshakeState(ic), r = new HandshakeState(rc);
        r.readMessage(i.writeMessage());
        const msg = r.writeMessage();
        const bad = truncated ? msg.slice(0, 31) : msg.slice();
        if (!truncated) bad[bad.length - 1] ^= 1;
        expect(() => i.readMessage(bad)).toThrow();
        expect(() => i.readMessage(msg)).toThrow();
        expect(() => i.split()).toThrow();
      }
    });
    it('rejects a mismatched prologue or peer static', () => {
      for (const wrongStatic of [false, true]) {
        const [ic, rc] = configs(vector);
        if (wrongStatic) ic.peerStatic = x25519.getPublicKey(new Uint8Array(32).fill(99));
        else rc.prologue = new Uint8Array([99]);
        expect(() => complete(new HandshakeState(ic), new HandshakeState(rc))).toThrow();
      }
    });
    it('rejects low-order remote ephemeral keys', () => {
      const [, rc] = configs(vector);
      const message = unhex(vector.handshake[0].ciphertext); message.fill(0, 0, 32);
      expect(() => new HandshakeState(rc).readMessage(message)).toThrow();
    });
    it('uses fresh production ephemeral keys', () => {
      const [ic] = configs(vector); delete ic.random;
      const first = new HandshakeState(ic).writeMessage().slice(0, 32);
      const second = new HandshakeState(ic).writeMessage().slice(0, 32);
      expect(hex(first)).not.toBe(hex(second));
    });
  });
}

it('rejects a wrong PSK at the second message', () => {
  const [ic, rc] = configs(vectors.find(v => v.pattern === 'IKpsk2')!);
  rc.psk = new Uint8Array(32).fill(99);
  const i = new HandshakeState(ic), r = new HandshakeState(rc);
  r.readMessage(i.writeMessage());
  expect(() => i.readMessage(r.writeMessage())).toThrow();
  expect(() => i.split()).toThrow();
});

it('validates configuration and key lengths', () => {
  const [ic] = configs(vectors[0]);
  expect(() => new HandshakeState({ ...ic, staticPrivate: new Uint8Array(31) })).toThrow();
  expect(() => new HandshakeState({ ...ic, peerStatic: undefined })).toThrow();
  expect(() => new HandshakeState({ ...ic, psk: new Uint8Array(31) })).toThrow();
  expect(() => new HandshakeState({ ...ic, pattern: 'KK' })).toThrow();
  expect(() => new CipherState(new Uint8Array(31))).toThrow();
});

it('enforces the Noise transport size limit and authenticates associated data', () => {
  const key = new Uint8Array(32).fill(7);
  const sender = new CipherState(key), receiver = new CipherState(key);
  expect(() => sender.encrypt(new Uint8Array(65520))).toThrow();
  const ad = new Uint8Array([1, 2]);
  const ciphertext = sender.encrypt(new Uint8Array(65519), ad);
  expect(ciphertext.length).toBe(65535);
  expect(() => receiver.decrypt(new Uint8Array(65536))).toThrow();
  expect(() => receiver.decrypt(ciphertext, new Uint8Array([2, 1]))).toThrow();
  expect(receiver.decrypt(ciphertext, ad).length).toBe(65519);
});

it('uses the last permitted nonce once and rejects further operations', () => {
  const sender = new CipherState(new Uint8Array(32)), receiver = new CipherState(new Uint8Array(32));
  // Reach the otherwise impractical 2^64 boundary without exposing a production nonce setter.
  Reflect.set(sender, 'nonce', (1n << 64n) - 2n);
  Reflect.set(receiver, 'nonce', (1n << 64n) - 2n);
  const ciphertext = sender.encrypt(new Uint8Array([7]));
  expect(receiver.decrypt(ciphertext)).toEqual(new Uint8Array([7]));
  expect(() => sender.encrypt(new Uint8Array())).toThrow('nonce exhausted');
  expect(() => receiver.decrypt(ciphertext)).toThrow('nonce exhausted');
});

it('enforces the complete handshake message size including pattern overhead', () => {
  for (const v of vectors) {
    const [ic, rc] = configs(v);
    const overhead = v.pattern === 'IKpsk2' ? 96 : 48;
    const i = new HandshakeState(ic), r = new HandshakeState(rc);
    const msg = i.writeMessage(new Uint8Array(65535 - overhead));
    expect(msg.length).toBe(65535);
    expect(r.readMessage(msg).length).toBe(65535 - overhead);
    expect(() => new HandshakeState(ic).writeMessage(new Uint8Array(65536 - overhead))).toThrow();
    expect(() => new HandshakeState(rc).readMessage(new Uint8Array(65536))).toThrow();
  }
});
