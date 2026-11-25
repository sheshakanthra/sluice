import { defineConfig, devices } from "@playwright/test";

// E2E (docs/phases/P6-frontend.md task 5). Two projects:
// - live:   the full stack through Caddy on :8000 (`task stack:up` serving web/out).
//           E1-E4 plus axe on every route. Serial: the tests share quiet-tuesday.
// - replay: web/out served with no API (scripts/serve-static.mjs), i.e. what the
//           static host shows. E5 plus axe on every route in REPLAY mode.
// E3 needs the short-TTL overlay (deploy/env/rush.e3.env); `task e2e` restarts
// the API and worker with it and runs `--grep @expiry` separately.
const LIVE = process.env.E2E_BASE_URL ?? "http://localhost:8000";
const REPLAY_PORT = 4173;

export default defineConfig({
  testDir: "./e2e",
  outputDir: "./test-results",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  use: {
    ...devices["Desktop Chrome"],
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "live", testMatch: /e[1-4]-.*\.spec\.ts|axe-live\.spec\.ts/, use: { baseURL: LIVE } },
    { name: "replay", testMatch: /e5-.*\.spec\.ts/, use: { baseURL: `http://localhost:${REPLAY_PORT}` } },
  ],
  webServer: {
    command: `node scripts/serve-static.mjs ${REPLAY_PORT}`,
    url: `http://localhost:${REPLAY_PORT}/`,
    reuseExistingServer: true,
  },
});
