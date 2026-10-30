import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  // WebAuthn needs a domain RP ID; use localhost rather than a loopback IP.
  use: { baseURL: "http://localhost:8787" },
  webServer: {
    command:
      "CHOKIDAR_USEPOLLING=1 XDG_CONFIG_HOME=.wrangler/config npx wrangler dev --compatibility-date 2026-06-02 --port 8787 --persist-to .wrangler/pwa --var INITIAL_MACHINE:11111111111111111111111111111111 --var INITIAL_HASH:4f2e8d65483c641648cdb374ae9d8abd368d269e4ddffe092a8237b8162cddd6",
    url: "http://localhost:8787",
    reuseExistingServer: false,
  },
});
