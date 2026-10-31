import type { InnerMessage } from './protocol/session';

interface Request {
  computer: string;
  resolve: (data: unknown) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/** Read/prepare only. Effects use the persistent InputQueue; nothing is replayed. */
export class ControlRequests {
  private pending = new Map<string, Request>();
  constructor(private send: (computer: string, message: InnerMessage) => void) {}
  request(computer: string, type: 'loop_detail' | 'loop_prepare', pane: string, data?: unknown): Promise<unknown> {
    if (this.pending.size >= 8) return Promise.reject(new Error('Wait for the current requests to finish'));
    const id = crypto.randomUUID();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error('Computer did not respond. Try again after it reconnects.')); }, 60000);
      this.pending.set(id, { computer, resolve, reject, timer });
      try { this.send(computer, { type, id, pane, data }); }
      catch (error) { clearTimeout(timer); this.pending.delete(id); reject(error); }
    });
  }
  result(computer: string, message: InnerMessage): boolean {
    const entry = this.pending.get(message.id ?? '');
    if (!entry || entry.computer !== computer) return false;
    this.pending.delete(message.id!); clearTimeout(entry.timer);
    if (message.status === 'ok') entry.resolve(message.data);
    else entry.reject(new Error(message.reason || 'Request refused'));
    return true;
  }
  disconnected(computer: string) { this.clear(entry => entry.computer === computer); }
  dispose() { this.clear(() => true); }
  private clear(matches: (entry: Request) => boolean) {
    for (const [id, entry] of this.pending) if (matches(entry)) {
      clearTimeout(entry.timer); this.pending.delete(id); entry.reject(new Error('Computer disconnected. Read the loop again after reconnecting.'));
    }
  }
}
