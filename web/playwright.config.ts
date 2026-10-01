import { defineConfig, devices } from '@playwright/test'

// The dashboard's end-to-end tests, against a Park serving a database seeded by cmd/seed: scripts/e2e.sh runs them.
// They run one at a time, in order, since the last one changes what the others read.
export default defineConfig({
  testDir: 'e2e',
  fullyParallel: false,
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? 'list' : 'line',
  use: {
    baseURL: process.env.PARK_URL,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
