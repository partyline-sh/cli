import { defineConfig } from "@playwright/test";
// The port is chosen per run (make check-pwa picks a free one): CI runners share a host, and two
// Relay jobs on one fixed port fail with "already used".
const origin = process.env.PWA_ORIGIN ?? "http://localhost:8787";
const port = new URL(origin).port;
export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  // WebAuthn needs a domain RP ID; use localhost rather than a loopback IP.
  use: { baseURL: origin },
  webServer: {
    command:
      `CHOKIDAR_USEPOLLING=1 XDG_CONFIG_HOME=.wrangler/config npx wrangler dev --compatibility-date 2026-06-02 --port ${port} --persist-to .wrangler/pwa --var INITIAL_MACHINE:11111111111111111111111111111111 --var INITIAL_HASH:4f2e8d65483c641648cdb374ae9d8abd368d269e4ddffe092a8237b8162cddd6`,
    url: origin,
    reuseExistingServer: false,
  },
});
