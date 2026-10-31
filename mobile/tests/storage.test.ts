import { beforeEach, expect, it, vi } from 'vitest';
import * as SecureStore from 'expo-secure-store';
import { deviceStorage, type PairedComputer } from '../src/runtime/storage';
const memory = vi.hoisted(() => ({ ordinary: new Map<string, string>(), secure: new Map<string, string>() }));
vi.mock('@react-native-async-storage/async-storage', () => ({ default: {
  getItem: vi.fn(async (key: string) => memory.ordinary.get(key) ?? null),
  setItem: vi.fn(async (key: string, value: string) => { memory.ordinary.set(key, value); }),
} }));
vi.mock('expo-secure-store', () => ({
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 'device-only',
  getItemAsync: vi.fn(async (key: string) => memory.secure.get(key) ?? null),
  setItemAsync: vi.fn(async (key: string, value: string) => { memory.secure.set(key, value); }),
  deleteItemAsync: vi.fn(async (key: string) => { memory.secure.delete(key); }),
}));
const computer = (id: string): PairedComputer => ({ machine: id.repeat(32), phone: '2'.repeat(32), token: 'c'.repeat(64), private: 'AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=', public: 'AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=', relay: 'https://relay.test', name: 'Monolith', pairedAt: 1 });
beforeEach(() => { memory.ordinary.clear(); memory.secure.clear(); vi.clearAllMocks(); });
it('stores credentials only in SecureStore and serializes concurrent registry writes', async () => {
  await Promise.all([deviceStorage.saveComputer(computer('a')), deviceStorage.saveComputer(computer('b'))]);
  expect((await deviceStorage.loadComputers()).map(c => c.machine)).toEqual(['a'.repeat(32), 'b'.repeat(32)]);
  expect([...memory.ordinary.values()].join('')).not.toContain('secret');
  expect(SecureStore.setItemAsync).toHaveBeenCalledWith(expect.any(String), expect.any(String), { keychainAccessible: 'device-only', requireAuthentication: false });
  await deviceStorage.forgetComputer('a'.repeat(32));
  expect((await deviceStorage.loadComputers()).map(c => c.machine)).toEqual(['b'.repeat(32)]);
  expect(memory.secure.has('partyline.phone.' + 'a'.repeat(32))).toBe(false);
});

it('keeps valid computers visible when another pairing is damaged', async () => {
  await deviceStorage.saveComputer(computer('a'));
  await deviceStorage.saveComputer(computer('b'));
  memory.secure.set('partyline.phone.' + 'a'.repeat(32), '{broken');
  expect((await deviceStorage.loadComputers()).map(c => c.machine)).toEqual(['b'.repeat(32)]);
  expect(deviceStorage.loadIssues?.()).toEqual([{id: 'a'.repeat(32), error: expect.stringContaining('damaged')}]);
});
