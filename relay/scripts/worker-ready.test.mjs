import { createServer } from 'node:http';
import { once } from 'node:events';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { waitForWorker } from './worker-ready.mjs';

async function fixture(t, handler) {
  const server = createServer(handler).listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  return `http://127.0.0.1:${server.address().port}`;
}
test('waits for authenticated relay status even when the static page is already ready', async t => {
  let checks = 0;
  const origin = await fixture(t, (req, res) => {
    if (req.url === '/') { res.end('static page'); return; }
    assert.equal(req.url, '/status'); assert.equal(req.method, 'POST');
    let body = ''; req.on('data', chunk => body += chunk);
    req.on('end', () => {
      assert.deepEqual(JSON.parse(body), { id: 'fixture', token: 'fixture-token' });
      checks++;
      if (checks === 1) { res.writeHead(503).end('starting'); return; }
      if (checks === 2) { res.end('{}'); return; }
      res.end(JSON.stringify({ connected: false }));
    });
  });
  assert.equal((await fetch(origin)).status, 200);
  await waitForWorker(origin, 'fixture', 'fixture-token', () => undefined, { interval: 1 });
  assert.equal(checks, 3);
});
test('bounds startup, including a worker that accepts requests but never answers', async t => {
  const origin = await fixture(t, () => {});
  await assert.rejects(waitForWorker(origin, 'fixture', 'fixture-token', () => undefined, { timeout: 80, requestTimeout: 30, interval: 1 }), /authenticated startup timed out/);
});
test('fails immediately when the child process exits', async () => {
  await assert.rejects(waitForWorker('http://127.0.0.1:1', 'fixture', 'fixture-token', () => new Error('worker exited')), /worker exited/);
});
