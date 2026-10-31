import { defineConfig } from "vitest/config";
export default defineConfig({
  test: {
    include: ["tests/relay.integration.ts"],
    environment: "node",
    testTimeout: 120000,
  },
});
