import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, type APIRequestContext } from "@playwright/test";

/** DESIGN-BRIEF §6: 0 serious or critical axe violations on every route. */
export async function expectAccessible(page: Page, label: string): Promise<void> {
  const res = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
    .analyze();
  const bad = res.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  const summary = bad.map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`);
  console.log(`axe ${label}: ${res.violations.length} violations, ${bad.length} serious/critical`);
  expect(summary, `axe on ${label}`).toEqual([]);
}

/** Fail the test on any console error or uncaught page error. */
export function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}

/** Join the line and wait until the browser is on /book, admitted. */
export async function joinAndGetAdmitted(page: Page, slug: string): Promise<void> {
  await page.goto(`/queue/?e=${slug}`);
  // The admitted stub counts 3-2-1 and moves to /book on its own; the button is a shortcut.
  const go = page.getByRole("button", { name: /Pick seats now/ });
  const deadline = Date.now() + 60_000;
  while (!/\/book\/\?e=/.test(page.url())) {
    if (Date.now() > deadline) throw new Error(`not admitted within 60 s (still on ${page.url()})`);
    // Short click timeout: the stub may navigate away mid-click, and an unbounded
    // click would then retry against a detached button until the test times out.
    if (await go.isVisible().catch(() => false)) await go.click({ timeout: 2_000 }).catch(() => undefined);
    await page.waitForTimeout(250);
  }
  await expect(page.getByRole("button", { name: /Hold best 2 seats/ })).toBeVisible();
}

/** Hold the best 2 seats; the app creates the order and lands on /checkout. */
export async function holdBestTwo(page: Page): Promise<string> {
  await page.getByRole("button", { name: /Hold best 2 seats/ }).click();
  await page.waitForURL(/\/checkout\/?\?o=/, { timeout: 30_000 });
  return new URL(page.url()).searchParams.get("o") ?? "";
}

export interface Counts {
  available: number;
  held: number;
  sold: number;
  capacity: number;
}

export async function counts(request: APIRequestContext, slug: string): Promise<Counts> {
  const r = await request.get(`/api/v1/events/${slug}/availability`);
  expect(r.ok()).toBeTruthy();
  return ((await r.json()) as { counts: Counts }).counts;
}
