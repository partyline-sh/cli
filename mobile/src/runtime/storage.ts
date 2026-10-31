import AsyncStorage from "@react-native-async-storage/async-storage";
import * as SecureStore from "expo-secure-store";
import { base64 } from "@scure/base";
import type { RelayHosting } from "./types";
import type { SavedPhone } from "./protocol/session";

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
    return JSON.parse(
      (await AsyncStorage.getItem("partyline.navigation.v1")) || "{}",
    );
  },
  async saveState(state) {
    await AsyncStorage.setItem(
      "partyline.navigation.v1",
      JSON.stringify(state),
    );
  },
};
