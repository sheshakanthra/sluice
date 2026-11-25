import { expect, test } from "@playwright/test";

import { expectAccessible, watchErrors } from "./helpers";

// E5 REPLAY: web/out served with no API (scripts/serve-static.mjs). Landing,
// Rush Control replay and Results render with no errors; every route shows its
// REPLAY state and passes axe.

const ROUTES = [
  "/",
  "/event/?e=tatkal-chn-sbc",
  "/queue/?e=tatkal-chn-sbc",
  "/book/?e=tatkal-chn-sbc",
  "/checkout/?o=00000000-0000-0000-0000-000000000000",
  "/order/?o=00000000-0000-0000-0000-000000000000",
  "/control/",
  "/results/",
  "/how/",
];

// The /api/v1/healthz probe 404s on a static host by design; that is how REPLAY is detected.
const expected = (e: string) => /Failed to load resource.*404/.test(e);

for (const path of ROUTES) {
  test(`E5 replay ${path}`, async ({ page }) => {
    const errors = watchErrors(page);
    await page.goto(path);
    await expect(page.getByText("Replay", { exact: true }).first()).toBeVisible();
    if (path === "/") {
      await expect(page.getByText(/Replay of local run p5b-smoke/)).toBeVisible();
      await expect(page.getByRole("link", { name: /Open Rush Control/ })).toBeVisible();
    } else if (path === "/control/") {
      await expect(page.getByText(/Replay of local run p5b-smoke · .* · commit 570aefc/)).toBeVisible();
      await expect(page.getByRole("slider", { name: "Replay position" })).toBeVisible();
      await page.getByRole("button", { name: "10×" }).click();
      await expect(page.getByRole("button", { name: "10×" })).toHaveAttribute("aria-pressed", "true");
      await page.getByRole("button", { name: /Jump to Sold out/ }).click();
      await expect(page.getByText(/Sold out at T\+3:51/)).toBeVisible();
    } else if (path === "/results/") {
      await expect(page.getByRole("heading", { level: 1 })).toContainText("5,000 people, 500 seats, 0 sold twice");
      await expect(page.getByRole("table")).toContainText("p5b-b0");
      await expect(page.locator(".recharts-surface").first()).toBeVisible();
    } else if (path === "/how/") {
      await expect(page.getByRole("img", { name: /SLUICE architecture/ })).toBeVisible();
    } else {
      await expect(page.getByText(/This is the replay site/)).toBeVisible();
    }
    await expectAccessible(page, `${path} (replay)`);
    expect(
      errors.filter((e) => !expected(e)),
      "console errors",
    ).toEqual([]);
  });
}
