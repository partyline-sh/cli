import type { InnerMessage } from './protocol/session';

export interface LiveScreen {
  ansi: string; typing: boolean; foreground: string; columns: number; rows: number; x: number; y: number;
  cursor: boolean; alternate: boolean; copy: boolean;
  acceptedSeq: number; echoSeq: number;
}
export type LiveEvent = { type: 'ready' } | { type: 'screen'; screen: LiveScreen; revision: number } | { type: 'closed'; reason: string };
export interface LiveHandle { send(sequence: number, kind: 'text' | 'key' | 'paste', data: string): void; close(): void }
interface Entry {
  computer: string; pane: string; request: string; token?: string;
  sequence: number; accepted: number; screenAccepted: number; echo: number; revision: number;
  outstanding: Map<number, number>; receive: (event: LiveEvent) => void;
  deadline: number; pingAt: number;
}
/** Ephemeral ordered input. Never persists, retries, or resumes after a disconnect. */
export class LiveTerminals {
  private entries = new Map<string, Entry>();
  private timer?: ReturnType<typeof setInterval>;
  constructor(private send: (computer: string, message: InnerMessage) => void) {}
  open(computer: string, pane: string, receive: Entry['receive']): LiveHandle {
    this.disconnected(computer, 'Terminal replaced; pending input discarded');
    const entry: Entry = { computer, pane, request: crypto.randomUUID(), sequence: 0, accepted: 0, screenAccepted: 0, echo: 0, revision: 0, outstanding: new Map(), receive, deadline: Date.now() + 8000, pingAt: Date.now() };
    this.entries.set(computer, entry);
    if (!this.timer) this.timer = setInterval(() => this.tick(), 250);
    try { this.send(computer, { type: 'live_open', id: entry.request, pane }); }
    catch (error) { this.end(entry, String(error)); }
    return {
      send: (sequence, kind, data) => {
        if (this.entries.get(computer) !== entry || !entry.token) throw new Error('Terminal is not connected');
        const size = new TextEncoder().encode(data).length;
        if (sequence !== entry.sequence + 1 || entry.outstanding.size >= 64 || size > 2048 || data.length === 0 || [...entry.outstanding.values()].reduce((a, b) => a + b, 0) + size > 16384) {
          this.end(entry, 'Typing queue exceeded its limit; pending input discarded'); return;
        }
        entry.sequence = sequence;
        if (!entry.outstanding.size) entry.deadline = Date.now() + 8000;
        entry.outstanding.set(sequence, size);
        try { this.send(computer, { type: 'live_input', id: entry.token, pane, revision: sequence, kind, data }); }
        catch (error) { this.end(entry, String(error)); }
      },
      close: () => { if (this.entries.get(computer) === entry) this.end(entry); },
    };
  }
  message(computer: string, message: InnerMessage): boolean {
    if (!message.type.startsWith('live_')) return false;
    const entry = this.entries.get(computer);
    if (!entry || message.pane !== entry.pane) return true;
    if (message.type === 'live_result') {
      if (message.id === entry.request && !entry.token) {
        const token = (message.data as { token?: unknown } | undefined)?.token;
        if (message.status !== 'ok' || typeof token !== 'string' || !/^[a-f0-9]{48}$/.test(token)) this.end(entry, message.reason || 'Terminal open refused');
        else { entry.token = token; entry.deadline = Date.now() + 8000; entry.receive({ type: 'ready' }); }
      } else if (message.id === entry.token) {
        if (message.status !== 'ok') this.end(entry, message.reason || 'Terminal input refused; pending input discarded');
        else if (message.revision !== undefined) this.accept(entry, message.revision);
      }
    } else if (message.type === 'live_screen' && message.id === entry.token && Number.isSafeInteger(message.revision) && message.revision! > entry.revision) {
      const screen = message.data as LiveScreen;
      if (!validScreen(screen) || screen.acceptedSeq > entry.sequence || screen.acceptedSeq < entry.screenAccepted || screen.echoSeq < entry.echo) { this.end(entry, 'Invalid live screen; input stopped'); return true; }
      entry.screenAccepted = screen.acceptedSeq; entry.echo = screen.echoSeq;
      this.accept(entry, screen.acceptedSeq);
      entry.revision = message.revision!;
      entry.receive({ type: 'screen', screen, revision: entry.revision });
    }
    return true;
  }
  private accept(entry: Entry, sequence: number) {
    if (!Number.isSafeInteger(sequence) || sequence < entry.accepted || sequence > entry.sequence) return;
    entry.accepted = sequence;
    for (const seq of entry.outstanding.keys()) if (seq <= sequence) entry.outstanding.delete(seq);
  }
  private tick() {
    const now = Date.now();
    for (const entry of this.entries.values()) {
      if ((!entry.token || !entry.revision || entry.outstanding.size > 0) && now > entry.deadline) { this.end(entry, 'Terminal acknowledgement timed out; delivery may be partial. Input was not replayed.'); continue; }
      if (entry.token && now - entry.pingAt > 15000) {
        entry.pingAt = now;
        try { this.send(entry.computer, { type: 'live_ping', id: entry.token, pane: entry.pane }); }
        catch (error) { this.end(entry, String(error)); }
      }
    }
  }
  private end(entry: Entry, reason?: string) {
    if (this.entries.get(entry.computer) !== entry) return;
    this.entries.delete(entry.computer); entry.outstanding.clear();
    if (entry.token) { try { this.send(entry.computer, { type: 'live_close', id: entry.token, pane: entry.pane }); } catch {} }
    entry.receive({ type: 'closed', reason: reason || 'Terminal closed' });
    if (!this.entries.size) { clearInterval(this.timer); this.timer = undefined; }
  }
  disconnected(computer: string, reason = 'Computer disconnected; pending input discarded') { const entry = this.entries.get(computer); if (entry) this.end(entry, reason); }
  dispose() { for (const entry of [...this.entries.values()]) this.end(entry); }
}
export function validScreen(s: LiveScreen): boolean {
  return !!s && typeof s.foreground === 'string' && s.foreground.length <= 128 && typeof s.ansi === 'string' && s.ansi.length <= 1024 * 1024 &&
    [s.columns, s.rows, s.x, s.y, s.acceptedSeq, s.echoSeq].every(n => Number.isSafeInteger(n) && n >= 0) &&
    s.columns >= 20 && s.columns <= 1000 && s.rows >= 8 && s.rows <= 1000 && s.x < s.columns && s.y < s.rows && s.echoSeq <= s.acceptedSeq &&
    [s.cursor, s.alternate, s.copy, s.typing].every(n => typeof n === 'boolean');
}
