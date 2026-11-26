import { describe, expect, it } from 'vitest';
import type { Computer } from '../src/runtime/types';
import { computerGroups, computerStatus } from '../src/ui/computer-groups';
const computer = (id: string, patch: Partial<Computer> = {}): Computer => ({
  id, name: id, relay: 'https://private.example.test', connection: 'offline', capabilities: [], pairedAt: 1, ...patch,
});

describe('paired computers grouped by route', () => {
  it('groups shared private routes without merging other gateways or inventing unpaired members', () => {
    const groups = computerGroups([
      computer('laptop', { relayHosting: 'self-hosted', connection: 'connected' }),
      computer('desktop'),
      computer('server', { relay: 'https://server.example.test', relayHosting: 'cloudflare' }),
    ]);
    expect(groups).toHaveLength(2);
    expect(groups[0]).toMatchObject({ title: 'Private gateway', connected: 1, address: 'private.example.test' });
    expect(groups[0].computers.map(item => item.id)).toEqual(['laptop', 'desktop']);
    expect(groups[1]).toMatchObject({ title: 'Cloudflare relay', connected: 0 });
    expect(groups[1].computers.map(item => item.id)).toEqual(['server']);
  });
  it('does not infer hosting or a sleeping gateway from domains and connection failures', () => {
    const groups = computerGroups([computer('old', { relay: 'https://example.workers.dev', error: 'connection failed' })]);
    expect(groups[0].title).toBe('Relay');
    expect(groups[0].connected).toBe(0);
    expect(computerStatus(groups[0].computers[0])).toBe('Offline');
  });
  it('keeps damaged pairings separate and never renders URL credentials', () => {
    const groups = computerGroups([computer('a', { relay: '' }), computer('b', { relay: 'https://secret@example.test' }), computer('c', { relay: 'http://example.test' })]);
    expect(groups).toHaveLength(3);
    for (const group of groups) expect(group).toMatchObject({ title: 'Saved pairing', origin: undefined, address: undefined });
    expect(computerGroups([])).toEqual([]);
  });
});
