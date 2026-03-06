// Playwright config for the dashboard UI tests.
// UI_BASE_URL points the suite at a running server (e.g. the Go binary);
// otherwise the mock server is started on PORT (default 8790).
import { defineConfig, devices } from '@playwright/test';

const port = process.env.PORT || '8790';
const external = process.env.UI_BASE_URL;
const baseURL = external || `http://localhost:${port}`;

export default defineConfig({
  testDir: './tests',
  // the mock keeps shared in-memory state, so tests run one at a time
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  timeout: 30_000,
  expect: { timeout: 7_000 },
  reporter: process.env.CI ? 'line' : [['list']],
  use: {
    baseURL,
    trace: 'retain-on-failure',
    ...devices['Desktop Chrome'],
    viewport: { width: 1360, height: 900 },
  },
  // Desktop runs every spec; the device projects run the responsive spec on every page.
  projects: [
    { name: 'desktop', testIgnore: /responsive\.spec/ },
    { name: 'iphone-13', testMatch: /responsive\.spec/, use: { ...devices['iPhone 13'] } },
    { name: 'pixel-7', testMatch: /responsive\.spec/, use: { ...devices['Pixel 7'] } },
    { name: 'pixel-7-landscape', testMatch: /responsive\.spec/, use: { ...devices['Pixel 7 landscape'] } },
    { name: 'ipad-gen-7', testMatch: /responsive\.spec/, use: { ...devices['iPad (gen 7)'] } },
  ],
  webServer: external ? undefined : {
    command: 'node mock-server.mjs',
    url: `${baseURL}/api/v1/health`,
    env: { PORT: port },
    reuseExistingServer: !process.env.CI,
    timeout: 15_000,
  },
});
