import { defineWorkersConfig } from '@cloudflare/vitest-pool-workers/config';
// The pinned pool ships workerd 2024-12-30. Deployment uses the current date in wrangler.jsonc.
// Node compatibility is test-only; the relay itself uses no Node APIs.
// The pinned pool cannot restore isolated storage with Durable Object WebSockets.
// Tests use a fresh Durable Object identity for each registry case instead.
export default defineWorkersConfig({test: {include: ['src/**/*.test.ts'], testTimeout: 30_000, hookTimeout: 30_000, poolOptions: {workers: {isolatedStorage: false, singleWorker: true, wrangler: {configPath: './wrangler.jsonc'}, miniflare: {compatibilityDate: '2024-12-30', compatibilityFlags: ['nodejs_compat']}}}}});
