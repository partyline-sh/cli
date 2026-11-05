import AsyncStorage from "@react-native-async-storage/async-storage";
import * as SecureStore from "expo-secure-store";
import { base64 } from "@scure/base";
import { chacha20poly1305 } from "@noble/ciphers/chacha";
import type { RelayHosting } from "./types";
import type { SavedPhone } from "./protocol/session";
import { boundHistory, emptyHistory, historyBytes, type ComputerHistory } from './history-cache';

export interface PairedComputer extends SavedPhone {
  relay: string;
  relayHosting?: RelayHosting;
  name: string;
  pairedAt: number;
}
const registry = "partyline.computers.v1";
let pairingIssues: { id: string; error: string }[] = [];
let registryWrites: Promise<void> = Promise.resolve();
function serialize(write: () => Promise<void>) {
  const next = registryWrites.catch(() => {}).then(write);
  registryWrites = next;
  return next;
}
const options = {
  keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
  requireAuthentication: false,
};
const legacyState = "partyline.navigation.v1";
const encryptedState = "partyline.navigation.encrypted.v2";
const stateKey = "partyline.navigation.key.v2";
const stateContext = new TextEncoder().encode("partyline.navigation.v2");
const historyKey = (id: string) => `partyline.history.encrypted.v1.${id}`;
const historyContext = (id: string) => new TextEncoder().encode(`partyline.history.v1.${id}`);

// Keychain items can be small on some native releases. Store only the random key
// there; authenticated ciphertext can safely hold larger prompts in AsyncStorage.
async function navigationKey(create: boolean): Promise<Uint8Array> {
  const saved = await SecureStore.getItemAsync(stateKey, options);
  if (saved) {
    const key = base64.decode(saved);
    if (key.length !== 32) throw new Error("Saved input encryption key is damaged");
    return key;
  }
  if (!create) throw new Error("Saved input encryption key is unavailable on this device");
  const key = crypto.getRandomValues(new Uint8Array(32));
  await SecureStore.setItemAsync(stateKey, base64.encode(key), options);
  return key;
}
function navigationObject(raw: string): Record<string, unknown> {
  const state: unknown = JSON.parse(raw);
  if (!state || typeof state !== "object" || Array.isArray(state)) throw new Error("Saved input state is invalid");
  return state as Record<string, unknown>;
}
async function writeNavigation(state: Record<string, unknown>): Promise<void> {
  const key = await navigationKey(true);
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const payload = chacha20poly1305(key, nonce, stateContext).encrypt(new TextEncoder().encode(JSON.stringify(state)));
  await AsyncStorage.setItem(encryptedState, JSON.stringify({ version: 2, nonce: base64.encode(nonce), payload: base64.encode(payload) }));
  // A failed encrypted write must leave the previous draft available for retry.
  await AsyncStorage.removeItem(legacyState);
}
const secretKey = (id: string) => {
  if (!/^[a-f0-9]{32}$/.test(id)) throw new Error("Invalid computer id");
  return `partyline.phone.${id}`;
};
export interface Storage {
  loadComputers(): Promise<PairedComputer[]>;
  loadIssues?(): readonly { id: string; error: string }[];
  saveComputer(computer: PairedComputer): Promise<void>;
  forgetComputer(id: string): Promise<void>;
  loadState(): Promise<Record<string, unknown>>;
  saveState(state: Record<string, unknown>): Promise<void>;
  loadHistory?(computer: string): Promise<ComputerHistory>;
  saveHistory?(computer: string, value: ComputerHistory): Promise<void>;
}
export const deviceStorage: Storage = {
  async loadComputers() {
    pairingIssues = [];
    const ids = JSON.parse(
      (await AsyncStorage.getItem(registry)) || "[]",
    ) as string[];
    const result: PairedComputer[] = [];
    for (const id of ids) {
      if (typeof id !== "string" || !/^[a-f0-9]{32}$/.test(id)) continue;
      try {
        const stored = await SecureStore.getItemAsync(secretKey(id), options);
        if (!stored) throw new Error("Missing stored pairing");
        const c = JSON.parse(stored) as PairedComputer;
        if (c.machine !== id || !/^[a-f0-9]{32}$/.test(c.phone) || !/^[a-f0-9]{64}$/.test(c.token) ||
            base64.decode(c.private).length !== 32 || base64.decode(c.public).length !== 32)
          throw new Error("Invalid stored pairing");
        const relay = new URL(c.relay);
        if (relay.protocol !== "https:" || relay.origin !== c.relay) throw new Error("Invalid saved relay");
        result.push(c);
      } catch {
        pairingIssues.push({ id, error: "Saved pairing is unavailable or damaged. Pair this computer again." });
      }
    }
    return result;
  },
  loadIssues: () => pairingIssues,
  async saveComputer(computer) {
    return serialize(async () => {
      await SecureStore.setItemAsync(
        secretKey(computer.machine),
        JSON.stringify(computer),
        options,
      );
      const ids = new Set<string>(
        JSON.parse((await AsyncStorage.getItem(registry)) || "[]"),
      );
      ids.add(computer.machine);
      await AsyncStorage.setItem(registry, JSON.stringify([...ids]));
    });
  },
  async forgetComputer(id) {
    return serialize(async () => {
      await serializeState(() => AsyncStorage.removeItem(historyKey(id)));
      await SecureStore.deleteItemAsync(secretKey(id), options);
      const ids = JSON.parse(
        (await AsyncStorage.getItem(registry)) || "[]",
      ) as string[];
      await AsyncStorage.setItem(
        registry,
        JSON.stringify(ids.filter((item) => item !== id)),
      );
    });
  },
  async loadState() {
    return serializeState(async () => {
      const saved = await AsyncStorage.getItem(encryptedState);
      if (saved) {
        const envelope = JSON.parse(saved) as { version?: unknown; nonce?: unknown; payload?: unknown };
        if (envelope?.version !== 2 || typeof envelope.nonce !== "string" || typeof envelope.payload !== "string")
          throw new Error("Saved input state is invalid");
        const nonce = base64.decode(envelope.nonce);
        if (nonce.length !== 12) throw new Error("Saved input nonce is invalid");
        const key = await navigationKey(false);
        const plain = chacha20poly1305(key, nonce, stateContext).decrypt(base64.decode(envelope.payload));
        const state = navigationObject(new TextDecoder("utf-8", { fatal: true }).decode(plain));
        // Also finish cleanup if an earlier migration stopped after writing ciphertext.
        await AsyncStorage.removeItem(legacyState);
        return state;
      }
      const legacy = await AsyncStorage.getItem(legacyState);
      if (!legacy) return {};
      const state = navigationObject(legacy);
      await writeNavigation(state);
      return state;
    });
  },
  async saveState(state) {
    await serializeState(() => writeNavigation(state));
  },
  async loadHistory(id) {
    secretKey(id); // Validate the storage namespace like the pairing registry.
    return serializeState(async () => {
      const saved = await AsyncStorage.getItem(historyKey(id));
      if (!saved) return emptyHistory();
      if (saved.length > historyBytes * 2) throw new Error('Saved history exceeds its limit');
      const e = JSON.parse(saved);
      if (e?.version !== 1 || typeof e.nonce !== 'string' || typeof e.payload !== 'string') throw new Error('Saved history is invalid');
      const nonce = base64.decode(e.nonce);
      if (nonce.length !== 12) throw new Error('Saved history is invalid');
      const key = await navigationKey(false);
      const plain = chacha20poly1305(key, nonce, historyContext(id)).decrypt(base64.decode(e.payload));
      if (plain.length > historyBytes) throw new Error('Saved history exceeds its limit');
      return boundHistory(JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(plain)), id);
    });
  },
  async saveHistory(id, value) {
    secretKey(id);
    return serializeState(async () => {
      const bounded = boundHistory(value, id);
      if (!bounded.screens.length && !bounded.chat) { await AsyncStorage.removeItem(historyKey(id)); return; }
      const key = await navigationKey(true), nonce = crypto.getRandomValues(new Uint8Array(12));
      const payload = chacha20poly1305(key, nonce, historyContext(id)).encrypt(new TextEncoder().encode(JSON.stringify(bounded)));
      await AsyncStorage.setItem(historyKey(id), JSON.stringify({ version: 1, nonce: base64.encode(nonce), payload: base64.encode(payload) }));
    });
  },
};

// A single first writer creates the key. Reads also wait for pending migrations
// and writes, so a concurrent startup cannot create a second, incompatible key.
let stateWrites: Promise<unknown> = Promise.resolve();
function serializeState<T>(operation: () => Promise<T>): Promise<T> {
  const next = stateWrites.catch(() => {}).then(operation);
  stateWrites = next;
  return next;
}
