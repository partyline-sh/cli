import { getRandomValues, randomUUID } from "expo-crypto";
// Noble/Noise uses the Web Crypto entropy interface; Expo provides native entropy.
if (!globalThis.crypto?.getRandomValues) {
  Object.defineProperty(globalThis, "crypto", {
    configurable: true,
    value: { ...globalThis.crypto, getRandomValues, randomUUID },
  });
} else if (!globalThis.crypto.randomUUID) {
  Object.defineProperty(globalThis.crypto, "randomUUID", {
    configurable: true,
    value: randomUUID,
  });
}
