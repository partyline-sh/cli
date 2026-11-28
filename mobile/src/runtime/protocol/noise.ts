// Noise revision 34, sections 5, 7, 9 and 12: https://noiseprotocol.org/noise.html
// Only the two mobile protocol patterns are supported; no negotiation or fallback.
import { x25519 } from '@noble/curves/ed25519';
import { chacha20poly1305 } from '@noble/ciphers/chacha';
import { blake2s } from '@noble/hashes/blake2s';
import { hmac } from '@noble/hashes/hmac';

const empty = new Uint8Array();
const join = (...parts: Uint8Array[]): Uint8Array => {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let offset = 0;
  for (const p of parts) { out.set(p, offset); offset += p.length; }
  return out;
};
function key32(key: Uint8Array): Uint8Array {
  if (key.length !== 32) throw new Error('Noise key must be 32 bytes');
  return key.slice();
}
function hkdf(ck: Uint8Array, input: Uint8Array, count: number): Uint8Array[] {
  const temp = hmac(blake2s, ck, input);
  const out: Uint8Array[] = [];
  for (let i = 1; i <= count; i++) out.push(hmac(blake2s, temp, join(out[i - 2] ?? empty, new Uint8Array([i]))));
  return out;
}

// Ordered transport: successful operations advance the nonce; failed authentication
// does not. A cipher belongs to exactly one direction of one session.
export class CipherState {
  private nonce = 0n;
  private readonly key: Uint8Array;
  constructor(key: Uint8Array) { this.key = key32(key); }
  private crypt(data: Uint8Array, ad: Uint8Array, decrypt: boolean): Uint8Array {
    if (this.nonce >= (1n << 64n) - 1n) throw new Error('Noise nonce exhausted');
    if (data.length > (decrypt ? 65535 : 65519)) throw new Error('Noise message too long');
    const nonce = new Uint8Array(12);
    new DataView(nonce.buffer).setBigUint64(4, this.nonce, true);
    const cipher = chacha20poly1305(this.key, nonce, ad);
    const result = decrypt ? cipher.decrypt(data) : cipher.encrypt(data);
    this.nonce++;
    return result;
  }
  encrypt(data: Uint8Array, ad: Uint8Array = empty): Uint8Array { return this.crypt(data, ad, false); }
  decrypt(data: Uint8Array, ad: Uint8Array = empty): Uint8Array { return this.crypt(data, ad, true); }
}

export interface NoiseConfig {
  pattern: 'IKpsk2' | 'KK';
  initiator: boolean;
  staticPrivate: Uint8Array;
  peerStatic?: Uint8Array;
  psk?: Uint8Array;
  prologue?: Uint8Array;
  // Test-only entropy injection. Production uses fresh crypto.getRandomValues.
  random?: () => Uint8Array;
}

// Single-owner, ordered state. Handshake payloads and peerStatic are provisional
// until split() succeeds; a failed handshake requires a new state and ephemerals.
export class HandshakeState {
  private readonly config: NoiseConfig;
  private readonly s: Uint8Array;
  private readonly sp: Uint8Array;
  private rs?: Uint8Array;
  private e?: Uint8Array;
  private re?: Uint8Array;
  private ck: Uint8Array;
  private h: Uint8Array;
  private cipher?: CipherState;
  private index = 0;
  private failed = false;
  private transport?: { send: CipherState; receive: CipherState };
  constructor(config: NoiseConfig) {
    if (config.pattern !== 'IKpsk2' && config.pattern !== 'KK') throw new Error('Unsupported Noise pattern');
    this.config = { ...config, psk: config.psk?.slice() };
    this.s = key32(config.staticPrivate);
    this.sp = x25519.getPublicKey(this.s);
    this.rs = config.peerStatic && key32(config.peerStatic);
    if ((config.initiator || config.pattern === 'KK') && !this.rs) throw new Error('Peer static required');
    if (config.pattern === 'IKpsk2') {
      key32(config.psk ?? empty);
      if (!config.initiator && config.peerStatic !== undefined) throw new Error('IK responder learns peer static during handshake');
    } else if (config.psk !== undefined) throw new Error('KK does not use a PSK');
    const name = new TextEncoder().encode(`Noise_${config.pattern}_25519_ChaChaPoly_BLAKE2s`);
    this.h = name.length <= 32 ? join(name, new Uint8Array(32 - name.length)) : blake2s(name);
    this.ck = this.h.slice();
    this.mixHash(config.prologue ?? empty);
    if (config.pattern === 'KK') this.mixHash(config.initiator ? this.sp : this.rs!);
    this.mixHash(config.initiator ? this.rs! : this.sp);
  }
  private mixHash(data: Uint8Array): void { this.h = blake2s(join(this.h, data)); }
  private mixKey(input: Uint8Array): void {
    const [ck, key] = hkdf(this.ck, input, 2); this.ck = ck; this.cipher = new CipherState(key);
  }
  private mixPSK(): void {
    const [ck, hash, key] = hkdf(this.ck, this.config.psk!, 3);
    this.ck = ck; this.mixHash(hash); this.cipher = new CipherState(key);
  }
  private hashCrypt(data: Uint8Array, writing: boolean): Uint8Array {
    const result = this.cipher ? (writing ? this.cipher.encrypt(data, this.h) : this.cipher.decrypt(data, this.h)) : data.slice();
    this.mixHash(writing ? result : data); return result;
  }
  private dh(token: string): void {
    const initiator = this.config.initiator;
    let local: Uint8Array; let remote: Uint8Array;
    switch (token) {
      case 'ee': local = this.e!; remote = this.re!; break;
      case 'ss': local = this.s; remote = this.rs!; break;
      case 'es': local = initiator ? this.e! : this.s; remote = initiator ? this.rs! : this.re!; break;
      case 'se': local = initiator ? this.s : this.e!; remote = initiator ? this.re! : this.rs!; break;
      default: throw new Error('Invalid DH token');
    }
    this.mixKey(x25519.getSharedSecret(local, remote));
  }
  private message(data: Uint8Array, writing: boolean): Uint8Array {
    if (this.failed || this.index >= 2) throw new Error('Noise handshake unavailable');
    if (writing !== (this.config.initiator === (this.index === 0))) throw new Error('Wrong Noise handshake turn');
    if (data.length > 65535) throw new Error('Noise message too long');
    try {
      const ik = this.config.pattern === 'IKpsk2';
      const tokens = this.index === 0 ? (ik ? ['e', 'es', 's', 'ss'] : ['e', 'es', 'ss']) : (ik ? ['e', 'ee', 'se', 'psk'] : ['e', 'ee', 'se']);
      const output: Uint8Array[] = [];
      let offset = 0;
      const take = (length: number): Uint8Array => {
        if (offset + length > data.length) throw new Error('Truncated Noise message');
        const part = data.slice(offset, offset + length); offset += length; return part;
      };
      for (const token of tokens) {
        if (token === 'e') {
          let pub: Uint8Array;
          if (writing) {
            this.e = key32(this.config.random ? this.config.random() : crypto.getRandomValues(new Uint8Array(32)));
            pub = x25519.getPublicKey(this.e); output.push(pub);
          } else { pub = take(32); this.re = pub; }
          this.mixHash(pub); if (ik) this.mixKey(pub);
        } else if (token === 's') {
          if (writing) output.push(this.hashCrypt(this.sp, true));
          else this.rs = this.hashCrypt(take(this.cipher ? 48 : 32), false);
        } else if (token === 'psk') this.mixPSK();
        else this.dh(token);
      }
      const payload = this.hashCrypt(writing ? data : data.slice(offset), writing);
      const result = writing ? join(...output, payload) : payload;
      if (writing && result.length > 65535) throw new Error('Noise message too long');
      this.index++;
      if (this.index === 2) {
        const keys = hkdf(this.ck, empty, 2).map(key => new CipherState(key));
        this.transport = this.config.initiator ? { send: keys[0], receive: keys[1] } : { send: keys[1], receive: keys[0] };
      }
      return result;
    } catch (error) { this.failed = true; throw error; }
  }
  writeMessage(payload: Uint8Array = empty): Uint8Array { return this.message(payload, true); }
  readMessage(message: Uint8Array): Uint8Array { return this.message(message, false); }
  split(): { send: CipherState; receive: CipherState } {
    if (!this.transport) throw new Error('Noise handshake incomplete');
    return this.transport;
  }
  get handshakeHash(): Uint8Array {
    if (!this.transport) throw new Error('Noise handshake incomplete');
    return this.h.slice();
  }
  get peerStatic(): Uint8Array | undefined { return this.rs?.slice(); }
}
