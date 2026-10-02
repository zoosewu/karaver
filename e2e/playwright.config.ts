import { defineConfig } from '@playwright/test'

// The server under test runs separately (docker compose, see README "端對端測試").
export default defineConfig({
  testDir: './tests',
  timeout: 90_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: process.env.BASE_URL ?? 'http://localhost:8088',
    // Google Chrome, not Chromium: the test videos are H.264/AAC.
    channel: 'chrome',
    trace: 'retain-on-failure',
  },
})
