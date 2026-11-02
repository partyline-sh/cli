import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { base64 } from '@scure/base';
import { MAX_ATTACHMENT_BYTES, Uploads, quoteAttachmentPath } from '../src/runtime/uploads';
import type { InnerMessage } from '../src/runtime/protocol/session';

const tick = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
const data = (m: InnerMessage) => m.data as Record<string, unknown>;
const file = (size = 100000) => ({ name: 'reference.png', mime: 'image/png', bytes: Uint8Array.from({ length: size }, (_, i) => i % 251) });

describe('encrypted attachment transfers', () => {
  it('reconstructs exact bytes, limits the pending window, and commits only after every chunk is confirmed', async () => {
    const sent: InnerMessage[] = [];
    const uploads = new Uploads((_, m) => { sent.push(m); });
    const attachment = file(180000), progress: number[] = [];
    const promise = uploads.upload('mac', attachment, n => progress.push(n), new AbortController().signal);
    const begin = sent[0];
    expect(data(begin).sha256).toBe(createHash('sha256').update(attachment.bytes).digest('hex'));
    uploads.result('mac', { type: 'input_result', id: begin.id, status: 'ok' });
    await tick();
    expect(sent.filter(m => m.type === 'upload_chunk')).toHaveLength(4);
    const first = sent.slice(1);
    // Acknowledgments may arrive out of order. No fifth chunk or finish leaves yet.
    for (const m of first.slice(1).reverse()) uploads.result('mac', { type: 'input_result', id: m.id, status: 'ok' });
    await tick();
    expect(sent).toHaveLength(5);
    uploads.result('mac', { type: 'input_result', id: first[0].id, status: 'ok' });
    await tick();
    const remaining = sent.filter(m => m.type === 'upload_chunk').slice(4);
    expect(remaining).toHaveLength(2);
    for (const m of remaining) uploads.result('mac', { type: 'input_result', id: m.id, status: 'ok' });
    await tick();
    const finish = sent.at(-1)!;
    expect(finish.type).toBe('upload_finish');
    const reconstructed = Buffer.concat(sent.filter(m => m.type === 'upload_chunk').map(m => Buffer.from(base64.decode(String(data(m).bytes)))));
    expect(reconstructed).toEqual(Buffer.from(attachment.bytes));
    uploads.result('mac', { type: 'input_result', id: finish.id, status: 'ok', data: { uploadId: data(begin).uploadId, path: '/private/cache/reference.png' } });
    expect(await promise).toBe('/private/cache/reference.png');
    expect(progress.at(-1)).toBe(1);
    expect(sent.some(m => ['input', 'chat_send'].includes(m.type))).toBe(false);
  });

  it('does not transfer bytes when the receiver refuses begin', async () => {
    const sent: InnerMessage[] = [];
    const uploads = new Uploads((_, m) => { sent.push(m); });
    const promise = uploads.upload('mac', file(), () => {}, new AbortController().signal);
    uploads.result('mac', { type: 'input_result', id: sent[0].id, status: 'refused', reason: 'Storage is full' });
    await expect(promise).rejects.toThrow('Storage is full');
    expect(sent.map(m => m.type)).toEqual(['upload_begin', 'upload_cancel']);
  });

  it('rejects a disconnect and never retries or finishes the transfer', async () => {
    const sent: InnerMessage[] = [];
    const uploads = new Uploads((_, m) => { sent.push(m); });
    const promise = uploads.upload('mac', file(), () => {}, new AbortController().signal);
    expect(uploads.result('other-mac', { type: 'input_result', id: sent[0].id, status: 'ok' })).toBe(false);
    uploads.disconnected('mac');
    await expect(promise).rejects.toThrow('Upload interrupted');
    await tick();
    expect(sent.map(m => m.type)).toEqual(['upload_begin', 'upload_cancel']);
  });

  it('cancels pending chunks when the person leaves or presses Cancel', async () => {
    const sent: InnerMessage[] = [];
    const uploads = new Uploads((_, m) => { sent.push(m); });
    const abort = new AbortController();
    const promise = uploads.upload('mac', file(), () => {}, abort.signal);
    uploads.result('mac', { type: 'input_result', id: sent[0].id, status: 'ok' });
    await tick();
    abort.abort();
    await expect(promise).rejects.toThrow('Upload cancelled');
    expect(sent.some(m => m.type === 'upload_finish')).toBe(false);
  });

  it('refuses oversized files before emitting a message', async () => {
    const sent: InnerMessage[] = [];
    const uploads = new Uploads((_, m) => { sent.push(m); });
    await expect(uploads.upload('mac', file(MAX_ATTACHMENT_BYTES + 1), () => {}, new AbortController().signal)).rejects.toThrow('20 MiB');
    expect(sent).toHaveLength(0);
  });

  it('quotes spaces and shell characters in a returned path', () => {
    expect(quoteAttachmentPath("/Users/Jo's Mac/$cache/a.png")).toBe("'/Users/Jo'\\''s Mac/$cache/a.png'");
  });
});
