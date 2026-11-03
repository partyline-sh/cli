import { beforeEach, expect, it, vi } from 'vitest';
import * as SecureStore from 'expo-secure-store';
import { deviceStorage, type PairedComputer } from '../src/runtime/storage';
const memory = vi.hoisted(() => ({ ordinary: new Map<string, string>(), secure: new Map<string, string>() }));
vi.mock('@react-native-async-storage/async-storage', () => ({ default: {
  getItem: vi.fn(async (key: string) => memory.ordinary.get(key) ?? null),
  setItem: vi.fn(async (key: string, value: string) => { memory.ordinary.set(key, value); }),
  removeItem: vi.fn(async (key: string) => { memory.ordinary.delete(key); }),
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

it('encrypts persisted drafts and queued commands with a small device-only key', async () => {
  const state = { drafts: { pane: 'private developer prompt' }, pending: [{ data: 'sensitive command' }] };
  await deviceStorage.saveState(state);
  expect([...memory.ordinary.values()].join('')).not.toContain('private developer prompt');
  expect([...memory.ordinary.values()].join('')).not.toContain('sensitive command');
  expect([...memory.secure.values()].some(value => value.includes('private developer prompt'))).toBe(false);
  expect([...memory.secure.values()].every(value => value.length < 2048)).toBe(true);
  expect(await deviceStorage.loadState()).toEqual(state);
});

it('migrates existing input state and removes its plaintext copy only after encryption succeeds', async () => {
  const state = { drafts: { pane: 'existing unsent draft' }, pending: [] };
  memory.ordinary.set('partyline.navigation.v1', JSON.stringify(state));
  vi.mocked(SecureStore.setItemAsync).mockRejectedValueOnce(new Error('keychain unavailable'));
  await expect(deviceStorage.loadState()).rejects.toThrow('keychain unavailable');
  expect(JSON.parse(memory.ordinary.get('partyline.navigation.v1')!)).toEqual(state);
  expect(await deviceStorage.loadState()).toEqual(state);
  expect([...memory.ordinary.values()].join('')).not.toContain('existing unsent draft');
  expect(memory.ordinary.has('partyline.navigation.v1')).toBe(false);
});

it('uses fresh nonces and serializes first writes so one encryption key protects both', async () => {
  await Promise.all([deviceStorage.saveState({ drafts: { pane: 'first' } }), deviceStorage.saveState({ drafts: { pane: 'last' } })]);
  expect(await deviceStorage.loadState()).toEqual({ drafts: { pane: 'last' } });
  const first = [...memory.ordinary.values()].join('');
  await deviceStorage.saveState({ drafts: { pane: 'last' } });
  expect([...memory.ordinary.values()].join('')).not.toEqual(first);
});

it('refuses altered ciphertext and restored data without its device key', async () => {
  await deviceStorage.saveState({ pending: [{ data: 'never replay a damaged command' }] });
  const [key, value] = [...memory.ordinary.entries()][0];
  const envelope = JSON.parse(value);
  const saved = envelope.payload;
  envelope.payload = (saved[0] === 'A' ? 'B' : 'A') + saved.slice(1);
  memory.ordinary.set(key, JSON.stringify(envelope));
  await expect(deviceStorage.loadState()).rejects.toThrow();
  memory.ordinary.set(key, value);
  memory.secure.clear();
  await expect(deviceStorage.loadState()).rejects.toThrow();
});

it('round trips drafts larger than native keychain item limits', async () => {
  const state = { drafts: { pane: 'private '.repeat(5000) }, pending: [] };
  await deviceStorage.saveState(state);
  expect(await deviceStorage.loadState()).toEqual(state);
  expect([...memory.secure.values()].every(value => value.length < 2048)).toBe(true);
});
