/** Static assets can respond before the relay's Durable Object and auth are ready. */
export async function waitForWorker(origin, machine, token, failed, { timeout = 90000, requestTimeout = 5000, interval = 150 } = {}) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const failure = failed();
    if (failure) throw failure;
    try {
      const response = await fetch(origin + '/status', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: machine, token }),
        signal: AbortSignal.timeout(Math.max(1, Math.min(requestTimeout, deadline - Date.now()))),
      });
      if (response.ok && typeof (await response.json()).connected === 'boolean') return;
    } catch { /* Retry only readiness requests; protocol assertions are never retried. */ }
    if (Date.now() >= deadline) throw new Error('Local Worker authenticated startup timed out');
    await new Promise(resolve => setTimeout(resolve, Math.min(interval, deadline - Date.now())));
  }
}
