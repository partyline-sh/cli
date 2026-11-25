import { base64 } from '@scure/base';
import { sha256 } from '@noble/hashes/sha256';
import { bytesToHex } from '@noble/hashes/utils';
import type { InnerMessage } from './protocol/session';

export const MAX_ATTACHMENT_BYTES = 20 * 1024 * 1024;
const CHUNK_BYTES = 32 * 1024;
export interface Attachment {
  name: string;
  mime: string;
  bytes: Uint8Array;
}
interface Pending {
  computer: string;
  uploadId: string;
  resolve: (m: InnerMessage) => void;
  reject: (e: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/** Uploads never enter the persistent input queue and never execute a command.
 * Losing the connection cancels the transfer; selecting it again starts a new one. */
export class Uploads {
  private pending = new Map<string, Pending>();
  constructor(private send: (computer: string, message: InnerMessage) => void) {}

  result(computer: string, message: InnerMessage): boolean {
    const p = this.pending.get(message.id ?? '');
    if (!p || p.computer !== computer) return false;
    this.pending.delete(message.id!);
    clearTimeout(p.timer);
    if (message.status === 'ok') p.resolve(message);
    else p.reject(new Error(message.reason || 'Upload refused by computer'));
    return true;
  }
  private request(computer: string, type: string, data: Record<string, unknown>): Promise<InnerMessage> {
    const id = crypto.randomUUID();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error('Upload confirmation timed out. Select the file again to retry.'));
      }, 30000);
      this.pending.set(id, { computer, uploadId: String(data.uploadId), resolve, reject, timer });
      try { this.send(computer, { type, id, data }); }
      catch (e) {
        this.pending.delete(id);
        clearTimeout(timer);
        reject(e);
      }
    });
  }
  private rejectMatching(matches: (p: Pending) => boolean, error: Error) {
    for (const [id, p] of this.pending) if (matches(p)) {
      clearTimeout(p.timer);
      this.pending.delete(id);
      p.reject(error);
    }
  }
  disconnected(computer: string) {
    this.rejectMatching(p => p.computer === computer, new Error('Upload interrupted. Reconnect and select the file again.'));
  }
  dispose() { this.rejectMatching(() => true, new Error('Upload cancelled')); }

  async upload(computer: string, file: Attachment, progress: (fraction: number) => void, signal: AbortSignal): Promise<string> {
    if (file.bytes.length === 0) throw new Error('Choose a non-empty file.');
    if (file.bytes.length > MAX_ATTACHMENT_BYTES) throw new Error('Choose a file of 20 MiB or less.');
    if (signal.aborted) throw new Error('Upload cancelled');
    const uploadId = crypto.randomUUID();
    let complete = false;
    const cancel = () => this.rejectMatching(p => p.computer === computer && p.uploadId === uploadId, new Error('Upload cancelled'));
    signal.addEventListener('abort', cancel);
    const check = () => { if (signal.aborted) throw new Error('Upload cancelled'); };
    try {
      progress(0);
      check();
      await this.request(computer, 'upload_begin', {
        uploadId, name: file.name, mime: file.mime || 'application/octet-stream', size: file.bytes.length,
        sha256: bytesToHex(sha256(file.bytes)),
      });
      check();
      const parts = Math.ceil(file.bytes.length / CHUNK_BYTES);
      let acknowledged = 0;
      for (let start = 0; start < parts; start += 4) {
        check();
        await Promise.all(Array.from({ length: Math.min(4, parts - start) }, (_, offset) => {
          const index = start + offset;
          return this.request(computer, 'upload_chunk', {
            uploadId, index, bytes: base64.encode(file.bytes.subarray(index * CHUNK_BYTES, (index + 1) * CHUNK_BYTES)),
          }).then(() => { acknowledged++; progress(acknowledged / Math.max(parts, 1) * 0.95); });
        }));
      }
      check();
      const result = await this.request(computer, 'upload_finish', { uploadId });
      check();
      const data = result.data as { uploadId?: string; path?: string } | undefined;
      if (data?.uploadId !== uploadId || typeof data.path !== 'string' || !data.path.startsWith('/') || /[\x00-\x1f\x7f]/.test(data.path))
        throw new Error('Computer returned an invalid attachment path');
      complete = true;
      progress(1);
      return data.path;
    } finally {
      signal.removeEventListener('abort', cancel);
      cancel();
      if (!complete) {
        try { this.send(computer, { type: 'upload_cancel', id: crypto.randomUUID(), data: { uploadId } }); }
        catch { /* Disconnected computers discard their incomplete uploads. */ }
      }
    }
  }
}

/** A path can be pasted into a shell without interpreting spaces, quotes or dollars. */
export const quoteAttachmentPath = (path: string) => "'" + path.replace(/'/g, "'\\''") + "'";
