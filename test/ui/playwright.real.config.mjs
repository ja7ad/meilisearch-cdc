// Dashboard tests against the real Go service, a real Meilisearch and a
// SQLite source (see realserver/). Run: npx playwright test -c playwright.real.config.mjs
import { defineConfig, devices } from '@playwright/test';

const port = process.env.REAL_PORT || '8791';
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: './real',
  fullyParallel: false,
  workers: 1,
  timeout: 120_000,
  expect: { timeout: 30_000 },
  reporter: process.env.CI ? 'line' : [['list']],
  use: { baseURL, trace: 'retain-on-failure', ...devices['Desktop Chrome'], viewport: { width: 1360, height: 900 } },
  webServer: {
    command: 'go run ./test/ui/realserver',
    cwd: '../..',
    url: `${baseURL}/healthz`,
    env: { PORT: port },
    reuseExistingServer: false,
    timeout: 240_000,
    stdout: 'pipe',
  },
});
