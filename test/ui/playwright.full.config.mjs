// Full GUI end-to-end tests against the real service and every source
// running in Docker (deploy/e2e). Start the environment first:
//   docker compose -f deploy/e2e/docker-compose.yml up -d --wait
//   go run ./test/ui/lab serve -seed          # :8799
//   (set -a; . deploy/e2e/cdc.env; ./bin/meilisearch-cdc)   # :8780
//   npx playwright test -c playwright.full.config.mjs
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './full',
  fullyParallel: false,
  workers: 1,
  timeout: 180_000,
  expect: { timeout: 45_000 },
  reporter: process.env.CI ? 'line' : [['list']],
  use: {
    baseURL: process.env.CDC_URL || 'http://127.0.0.1:8780',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    ...devices['Desktop Chrome'],
    viewport: { width: 1440, height: 960 },
    // SLOWMO=250 npx playwright test -c playwright.full.config.mjs --headed  to watch it run.
    launchOptions: { slowMo: Number(process.env.SLOWMO || 0) },
  },
});
