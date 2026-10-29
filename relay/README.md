# Mobile relay and pairing shell

Run `make relay-deps`, `make check-relay`, then `make check-pwa` from the repository root.
The last command builds the PWA and starts local Wrangler with a machine double and test-only
credentials. Nothing is deployed. Chromium headless shell (the browser used by this suite) is installed by `relay-deps`;
the unused full Chromium browser is not downloaded. Both Make targets use
`PLAYWRIGHT_BROWSERS_PATH=0`, keeping the pinned browser under ignored `node_modules`
instead of relying on a writable user cache.

Production uses the compatibility date in `wrangler.jsonc`. The pinned local Wrangler runtime
supports 2026-06-02, so the Playwright harness overrides the date for local testing only. The
Worker Vitest pool retains its separate 2024-12-30 runtime. Playwright also tests the production
TypeScript transport directly through Wrangler without a browser; this does not replace the
browser page test or the real-iPhone camera check.

`ptln remote pair` opens a tmux popup. Its QR and typed string contain the same non-URL
`PTLN-PAIR ` JSON object: relay origin, machine id, machine Noise static public key, 32-byte PSK,
independently generated 32-byte invite, 16-byte route id and expiry in Unix milliseconds.
Scan inside the relay app, or paste the string. Enlarge the terminal if the QR does not fit.
The phone keeps camera frames locally. Do not put the string in a URL.

The machine registers **only the SHA-256 digest of the invite**, machine id and pairing route at
`POST /device-invite`, authenticated with its machine token. The relay sets a ten-minute expiry;
the machine independently caps the PSK lifetime at ten minutes. Opening the popup serializes
local pairing attempts. Closing it removes the pending offer. The watcher consumes that offer
before answering the first handshake; a failed attempt needs a new popup.

Outer binary frames are `kind (1 byte) | route (16 bytes) | opaque body`:

- Kind 1 is JSON hello: `{role, token, min:1, max:1}`. Machine hello uses the machine id as route.
  Invite hello uses the pairing route; admission atomically consumes the stored invite digest.
- Kind 2 carries at most 65535 opaque bytes, forwarded unchanged in both directions. An admitted
  invite socket can address only its pairing route on its owning machine. The machine addresses
  that same route for replies. Authenticated identities and expiry survive hibernation in socket
  attachments. Invites cannot register machines, create invites or access other routes.

After popup approval, the phone stores the issued device token, phone route id and both Noise
static keys (the machine key is public). Each subsequent connection authenticates with
`{role:"phone", machine, token, min:1, max:1}` on its phone route, then starts a fresh Noise KK
handshake. The machine resolves that route to its locally approved static key. A socket close
or replacement sends kind 4 (`gone`) to its peer. Revocation closes the device socket and drops
the machine's corresponding session; the machine also rereads its local phone projection.

For KK traffic, a phone's kind 2 body is one opaque handshake or transport message. A machine's
kind 2 route is its own machine id and its body is a concatenation of entries:
`phone route (16 bytes) | ciphertext length (uint32, big endian) | ciphertext`.
The relay validates the complete envelope before forwarding any entry; it does not decode
Noise or application JSON. Pairing replies retain the direct pairing-route format above.
The total WebSocket frame is limited to 65,552 bytes. Full batches defer excess entries fairly
without consuming transport nonces.

Transport plaintext is a JSON array of messages with `type`, optional `pane`, `mode`, `revision`,
`encoding` and `data`. Pane `out` messages contain zstd-compressed UTF-8 snapshots as base64
`data` with `encoding:"zstd"`. Subscriptions use `{type:"sub",pane,mode:"focus"|"summary"}`;
`pane` can also name `board`, `contractors`, `chat`, `sessions` or `gates`. Each renewal grants
60 seconds. The phone session renews every 30 seconds while connected and closes when hidden.
`unsubscribe` stops renewal; the last lease then expires. Focus output and changed snapshots
share one machine frame every 500 ms. Summaries contain state, glyph and last line, at most
every ten seconds. Board data comes from the same rows as the local board; contractor snapshots
omit process arguments and environment variables. Session metadata uses the watcher activity
cache, so it does not capture unsubscribed panes; `lastLine` there is the cached activity summary.

Large plaintext batches use authenticated `fragment` messages (`id`, zero-based `index`,
`parts`, base64 `data`), assembled in order with an 8 MiB limit. Expired leases discard queued
snapshots and partial transfers; an encrypted `reset` clears the phone's partial assembly.
Reconnect creates a fresh session and redraws from complete snapshots.

The relay counts admitted incoming frames by UTC day, retaining unflushed counts in hibernating
socket attachments and flushing to SQLite every five minutes or when the last socket closes.
Kind 6 (`budget`) reports threshold transitions to machines. At 50,000 frames the machine sends
at most once per second; at 80,000, once per five seconds and summaries stop. The machine forwards
that state inside encrypted `budget` messages for the phone. Application ping/pong uses the
existing hibernation auto-response and is outside this incoming-frame estimate.

`src/session.ts` exports `PhoneSession` for the next step's screens. `make check-pwa` exercises
it in Chromium against a Go responder using the production Noise, lease, compression and batch
code. On hosts that cannot launch Chromium, `cd relay && npm run test:channel` runs the same
phone module with Node's WebSocket against local Wrangler and the Go responder; that check
does not establish browser or real-iPhone behavior.
