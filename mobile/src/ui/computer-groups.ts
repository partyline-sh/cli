import type { Computer } from '../runtime/types';

export interface ComputerGroup {
  id: string;
  origin?: string;
  title: string;
  address?: string;
  computers: Computer[];
  connected: number;
}

/** Group only saved pairings by their exact HTTPS origin; never infer hosting
 * from a hostname, or invent unpaired computers from gateway membership. */
export function computerGroups(computers: readonly Computer[]): ComputerGroup[] {
  const groups = new Map<string, ComputerGroup>();
  for (const computer of computers) {
    let origin: string | undefined;
    let address: string | undefined;
    try {
      const url = new URL(computer.relay);
      if (url.protocol === 'https:' && url.origin === computer.relay && !url.username && !url.password) {
        origin = url.origin;
        address = url.host;
      }
    } catch { /* An unreadable saved pairing has no known route. */ }
    const id = origin ?? `unavailable:${computer.id}`;
    let group = groups.get(id);
    if (!group) {
      group = { id, origin, address, title: origin ? 'Relay' : 'Saved pairing', computers: [], connected: 0 };
      groups.set(id, group);
    }
    group.computers.push(computer);
    if (computer.connection === 'connected') group.connected++;
    if (origin && computer.relayHosting === 'self-hosted') group.title = 'Private gateway';
    else if (origin && computer.relayHosting === 'cloudflare' && group.title === 'Relay') group.title = 'Cloudflare relay';
  }
  return [...groups.values()];
}

export function computerStatus(computer: Computer): string {
  switch (computer.connection) {
    case 'connected': return 'Connected';
    case 'connecting': return 'Connecting';
    case 'reconnecting': return 'Reconnecting';
    case 'revoked': return 'Pair again';
    default: return 'Offline';
  }
}
