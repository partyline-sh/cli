import { useCallback, useState } from 'react';
import { fixtureState } from './fixtures';
import { storePreview } from '../preview/config';
import { storeState } from '../preview/store-state';
import { paneKey, type Delivery, type PartylineController } from './types';

/** Explicit demo controller. It never creates sockets, stores keys, or authorizes actions. */
export function useFixturePartyline(): PartylineController {
  const [state, setState] = useState(storePreview ? storeState : fixtureState);
  const deliver = useCallback(async (computerId: string, pane: string, text?: string): Promise<Delivery> => {
    const id = `fixture-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const result: Delivery = { id, computerId, pane, status: 'confirmed', reason: 'Fixture only; nothing sent' };
    setState(previous => ({ ...previous, drafts: Object.fromEntries(Object.entries(previous.drafts).filter(([key, value]) => key !== paneKey(computerId, pane) || value !== text)), deliveries: [...previous.deliveries.slice(-49), result],
      chat: pane === 'chat' && text ? [...previous.chat, { id, computerId, name: 'you', own: true, text, at: new Date().toISOString() }] : previous.chat,
    }));
    return result;
  }, []);
  return {
    ...state,
    selectTab: tab => setState(s => ({ ...s, tab, selected: undefined })),
    selectSession: selected => setState(s => ({ ...s, selected })),
    selectView: sessionView => setState(s => ({ ...s, sessionView })),
    setDraft: (computerId, pane, text) => setState(s => ({ ...s, drafts: { ...s.drafts, [paneKey(computerId, pane)]: text } })),
    startScan: () => setState(s => ({ ...s, pairing: { status: 'scanning' } })),
    pair: async () => setState(s => ({ ...s, pairing: { status: 'error', message: 'Fixture mode cannot pair. Use the live runtime to pair a computer.' } })),
    cancelPair: () => setState(s => ({ ...s, pairing: { status: 'idle' } })),
    forgetComputer: async computerId => setState(s => ({ ...s, computers: s.computers.filter(c => c.id !== computerId), sessions: s.sessions.filter(c => c.computerId !== computerId), selected: s.selected?.computerId === computerId ? undefined : s.selected })),
    reconnect: computerId => setState(s => ({ ...s, computers: s.computers.map(c => c.id === computerId ? { ...c, connection: 'connected' } : c) })),
    sendText: (computerId, pane, text) => deliver(computerId, pane, text),
    paste: (computerId, pane, text) => deliver(computerId, pane, text),
    sendKey: (computerId, pane) => deliver(computerId, pane),
    sendChat: (computerId, text) => deliver(computerId, 'chat', text),
    resizeTerminal: async () => { throw new Error('Demo mode does not resize a real terminal.'); },
    uploadAttachment: async () => { throw new Error('Connect a computer to upload files. Demo mode never transfers files.'); },
    retryDelivery: async () => { throw new Error('Fixture mode has no uncertain deliveries'); },
    acknowledgeDelivery: id => setState(s => ({ ...s, deliveries: s.deliveries.filter(d => d.id !== id) })),
    answer: undefined,
  };
}
