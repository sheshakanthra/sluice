import { expect, test } from "@playwright/test";

import { counts, expectAccessible, holdBestTwo, joinAndGetAdmitted } from "./helpers";

// E3 expiry (stack with HOLD_TTL=10s and PAYMENT_HOLD_TTL=10s, deploy/env/rush.e3.env):
// hold -> wait -> "expired" + "not charged", and the seats are released.
// `task e2e` restarts the API and worker with the overlay and runs --grep @expiry.
const SLUG = "quiet-tuesday";

test("E3 hold expiry: expired, not charged, seats released @expiry", async ({ page, request }) => {
  await joinAndGetAdmitted(page, SLUG);
  const before = await counts(request, SLUG);
  const orderId = await holdBestTwo(page);
  expect(orderId).not.toBe("");

  // The overlay must be active: the timer starts at <= 10 s, not the 90 s rush default.
  const timer = page.getByText(/^0[01]:\d\d$/).first();
  await expect(timer).toBeVisible();
  const [mm, ss] = (await timer.textContent())!.split(":").map(Number);
  expect(mm * 60 + ss, "hold timer (is rush.e3.env applied?)").toBeLessThanOrEqual(10);

  await expect(page.getByText("Your hold ran out")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText(/you were not charged/i).first()).toBeVisible();
  await expectAccessible(page, "checkout (expired)");

  // The sweeper reclaims within 2 intervals (I5): both seats come back.
  await expect.poll(async () => (await counts(request, SLUG)).available, { timeout: 15_000 }).toBe(before.available);
  expect((await counts(request, SLUG)).held).toBe(0);
});
