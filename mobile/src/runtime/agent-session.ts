import { readInputFeedback } from './input-feedback';
import type { Delivery, PaneSnapshot, TranscriptEntry } from './types';

/** Engine-specific capabilities belong here, never in the shared screen. */
export function agentSessionAdapter(engine: string) {
  return { transcriptReader: engine === 'claude' || engine === 'codex' };
}
export type AgentSessionAdapter = ReturnType<typeof agentSessionAdapter>;
const promptText = (text: string) => text.replace(/\r\n/g, '\n').trim();
const matches = (entry: TranscriptEntry, text: string) => entry.role === 'user' && promptText(entry.text) === promptText(text);

export function promptBaseline(snapshot: PaneSnapshot | undefined, text: string, deliveries: readonly Delivery[] = []): number | undefined {
  if (!snapshot?.transcriptSupported || !snapshot.transcript || snapshot.stale) return;
  let count = snapshot.transcript.entries.filter(entry => matches(entry, text)).length;
  // A second identical prompt may be sent before its first transcript echo arrives.
  for (const delivery of deliveries) {
    if (!['waiting', 'confirmed', 'unknown'].includes(delivery.status) || !delivery.prompt || delivery.prompt.previousMatches === undefined) continue;
    if (promptText(delivery.prompt.text) === promptText(text)) count = Math.max(count, delivery.prompt.previousMatches + 1);
  }
  return count;
}

function promptIndex(entries: readonly TranscriptEntry[], prompt: NonNullable<Delivery['prompt']>): number {
  if (prompt.previousMatches === undefined) return -1;
  let occurrence = 0;
  return entries.findIndex(entry => matches(entry, prompt.text) && occurrence++ === prompt.previousMatches);
}

/** Only native conversation data is attributed to an assistant; never parse its terminal screen. */
export function simpleConversation(adapter: AgentSessionAdapter, snapshot: PaneSnapshot | undefined, deliveries: readonly Delivery[]) {
  const native = snapshot?.transcriptSupported ? snapshot.transcript?.entries : undefined;
  const entries: TranscriptEntry[] = [...(native ?? [])];
  for (const delivery of deliveries) {
    if (delivery.status !== 'confirmed' || !delivery.prompt) continue;
    if (native && (delivery.prompt.previousMatches === undefined || promptIndex(native, delivery.prompt) >= 0)) continue;
    entries.push({ id: `sent:${delivery.id}`, role: 'user', text: delivery.prompt.text, tools: [] });
  }
  const notice = native ? undefined : adapter.transcriptReader
    ? 'Conversation is not available yet. Replies appear in Session.'
    : 'Replies for this engine appear in Session. Simple shows messages sent from this phone.';
  return { entries, notice };
}

/** A fast response can be observed without ever seeing the agent's working state. */
export function agentDeliveryLabel(adapter: AgentSessionAdapter, delivery: Delivery, snapshot: PaneSnapshot | undefined, working: boolean): string | undefined {
  if (delivery.status !== 'confirmed' || !delivery.prompt) return;
  const entries = snapshot?.transcriptSupported ? snapshot.transcript?.entries : undefined;
  if (entries && !snapshot?.stale) {
    const at = promptIndex(entries, delivery.prompt);
    if (at >= 0) {
      for (const entry of entries.slice(at + 1)) {
        if (entry.role === 'user') break;
        if (entry.text.trim()) return working ? 'Reply arriving…' : 'Answered';
      }
    }
  }
  const feedback = readInputFeedback(delivery.data);
  if (feedback?.inputDelivery === 'in_composer' || feedback?.inputDelivery === 'agent_working') return;
  if (!adapter.transcriptReader || snapshot?.transcriptSupported === false) return 'Sent · replies in Session';
  return 'Sent · waiting for reply';
}
