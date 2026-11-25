import { expect, test } from "@playwright/test";

import { expectAccessible, holdBestTwo, joinAndGetAdmitted, watchErrors } from "./helpers";

// E2 decline: Decline on MockPay -> "you were not charged" -> new attempt ->
// saved test card -> ticket.
const SLUG = "quiet-tuesday";

test("E2 decline: not charged, new attempt, ticket", async ({ page }) => {
  const errors = watchErrors(page);
  await joinAndGetAdmitted(page, SLUG);
  await holdBestTwo(page);

  await page.getByRole("button", { name: /on MockPay/ }).click();
  await page.waitForURL(/\/pay\//);
  await page.getByRole("button", { name: /^Decline/ }).click();

  await expect(page.getByText(/you were not charged/i).first()).toBeVisible({ timeout: 30_000 });
  await expectAccessible(page, "/order (declined, back from MockPay)");

  // MockPay returns to /order; it offers the way back to the checkout.
  await page.getByRole("link", { name: /Try paying again/ }).click();
  await page.waitForURL(/\/checkout\/?\?o=/);
  await expectAccessible(page, "/checkout (declined)");
  await page.getByRole("button", { name: /Try again \(attempt 2 of 3\)/ }).click();
  await page.getByRole("button", { name: /with saved card/ }).click();

  await page.waitForURL(/\/order\/?\?(?:.*&)?o=/, { timeout: 30_000 });
  await expect(page.getByText(/SLC-[A-Z0-9]{4}-[A-Z0-9]{4}/).first()).toBeVisible({ timeout: 60_000 });
  expect(errors, "console errors").toEqual([]);
});
