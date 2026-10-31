import type { InnerMessage } from './protocol/session';
export interface TerminalDimensions { columns: number; rows: number }
interface Request {
  computer: string;
  resolve: (message: InnerMessage) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}
/** Reversible, leased layout requests never enter the persistent typing queue. */
export class TerminalResize {
  private pending = new Map<string, Request>();
  constructor(private send: (computer: string, message: InnerMessage) => void) {}
  request(computer: string, pane: string, mode: 'phone' | 'desktop', viewport?: TerminalDimensions): Promise<InnerMessage> {
    if (mode === 'phone' && (!viewport || !Number.isInteger(viewport.columns) || viewport.columns < 20 || viewport.columns > 300 || !Number.isInteger(viewport.rows) || viewport.rows < 5 || viewport.rows > 200))
      return Promise.reject(new Error('Invalid phone terminal dimensions'));
    const id = crypto.randomUUID();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error('Layout change was not confirmed')); }, 5000);
      this.pending.set(id, { computer, resolve, reject, timer });
      try { this.send(computer, { type: 'resize', id, pane, data: mode === 'phone' ? { mode, ...viewport } : { mode } }); }
      catch (error) { this.pending.delete(id); clearTimeout(timer); reject(error); }
    });
  }
  result(computer: string, message: InnerMessage): boolean {
    const entry = this.pending.get(message.id ?? '');
    if (!entry || entry.computer !== computer) return false;
    this.pending.delete(message.id!); clearTimeout(entry.timer);
    if (message.status === 'ok') entry.resolve(message);
    else entry.reject(new Error(message.reason || 'Terminal layout refused'));
    return true;
  }
  disconnected(computer: string) { this.rejectWhere(entry => entry.computer === computer); }
  dispose() { this.rejectWhere(() => true); }
  private rejectWhere(matches: (entry: Request) => boolean) {
    for (const [id, entry] of this.pending) if (matches(entry)) {
      this.pending.delete(id); clearTimeout(entry.timer); entry.reject(new Error('Computer disconnected; phone layout will end automatically'));
    }
  }
}
