import { expect, test } from "@playwright/test";

import { expectAccessible, watchErrors } from "./helpers";

// axe on the LIVE routes that E1-E4 don't walk through (DESIGN-BRIEF §6).
const ROUTES = ["/", "/event/?e=quiet-tuesday", "/event/?e=tatkal-chn-sbc", "/control/", "/results/", "/how/"];

for (const path of ROUTES) {
  test(`axe live ${path}`, async ({ page }) => {
    const errors = watchErrors(page);
    await page.goto(path);
    await expect(page.getByText("Live", { exact: true }).first()).toBeVisible();
    if (path === "/control/") await expect(page.getByText(/updated .* · every 1 s/)).toBeVisible();
    await page.waitForTimeout(1000);
    await expectAccessible(page, `${path} (live)`);
    expect(errors, "console errors").toEqual([]);
  });
}
