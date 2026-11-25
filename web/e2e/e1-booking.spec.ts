import { copyFileSync, mkdirSync } from "node:fs";
import { resolve } from "node:path";

import { expect, test } from "@playwright/test";

import { expectAccessible, holdBestTwo, joinAndGetAdmitted, watchErrors } from "./helpers";

// E1 quiet event: join -> admitted -> best-available 2 -> order -> hosted
// MockPay checkout Pay -> ticket. Recorded on video (web/e2e/artifacts/booking.webm,
// also copied to public/results/latest/ for the REPLAY explainer). axe runs on
// every page of the flow.
const SLUG = "quiet-tuesday";

test.use({ viewport: { width: 1280, height: 720 }, video: { mode: "on", size: { width: 1280, height: 720 } } });

test("E1 quiet event: join, admitted, best 2, hosted checkout, ticket @booking", async ({ page }) => {
  const errors = watchErrors(page);

  await page.goto(`/queue/?e=${SLUG}`);
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  await expectAccessible(page, "/queue (live)");

  await joinAndGetAdmitted(page, SLUG);
  await expectAccessible(page, "/book (admitted)");

  await holdBestTwo(page);
  const pay = page.getByRole("button", { name: /on MockPay/ });
  await expect(pay).toBeEnabled();
  await expectAccessible(page, "/checkout (pending)");
  await pay.click();

  await page.waitForURL(/\/pay\//);
  await page.getByRole("button", { name: /^Pay / }).click();

  await page.waitForURL(/\/order\/?\?(?:.*&)?o=/, { timeout: 30_000 });
  const codes = page.getByText(/SLC-[A-Z0-9]{4}-[A-Z0-9]{4}/);
  await expect(codes.first()).toBeVisible({ timeout: 60_000 });
  await expect(codes).toHaveCount(2);
  await expect(page.getByText(/charged/i).first()).toBeVisible();
  await expectAccessible(page, "/order (confirmed)");
  console.log(`E1 ticket codes: ${(await codes.allTextContents()).join(", ")}`);
  await page.waitForTimeout(1500); // let the video show the ticket

  expect(errors, "console errors").toEqual([]);
  const video = page.video();
  await page.close();
  if (video) {
    const out = resolve("e2e/artifacts/booking.webm");
    mkdirSync(resolve("e2e/artifacts"), { recursive: true });
    await video.saveAs(out);
    copyFileSync(out, resolve("public/results/latest/booking.webm"));
  }
});
