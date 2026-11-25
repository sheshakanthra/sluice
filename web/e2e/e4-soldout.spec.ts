import { execFileSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { resolve } from "node:path";

import { expect, test } from "@playwright/test";

import { expectAccessible } from "./helpers";

// E4 sold out: seed a 1-seat event (the seed command; no admin endpoint creates
// events), a bot takes the seat through the API (test admission -> hold ->
// order -> pay -> confirm), then a person joins and gets the SOLD_OUT screen.
const repo = resolve(__dirname, "..", "..");

function seedOneSeat(slug: string) {
  execFileSync(
    "docker",
    [
      "compose",
      "-f",
      "deploy/compose.yml",
      "-f",
      "deploy/compose.app.yml",
      "-f",
      "deploy/compose.rush.yml",
      "run",
      "--rm",
      "--no-deps",
      "seed",
      "seed",
      "--event",
      slug,
      "--capacity",
      "1",
      "--opens-in",
      "1s",
    ],
    { cwd: repo, stdio: "inherit", env: { ...process.env, SLUICE_ENV: "rush" } },
  );
}

test("E4 sold out: a bot takes the only seat, the person sees SOLD_OUT", async ({ page, request }) => {
  const slug = `e2e-soldout-${Date.now().toString(36)}`;
  seedOneSeat(slug);
  await page.waitForTimeout(2000); // past T0

  const sess = await (await request.post("/api/v1/sessions")).json();
  const auth = { Authorization: `Bearer ${sess.session_token}` };
  const adm = await request.post("/api/v1/test/admit", { data: { event_id: slug, user_id: sess.user_id } });
  expect(adm.status(), await adm.text()).toBe(200);
  const admTok = { Authorization: `Bearer ${(await adm.json()).admission_token}` };

  const hold = await request.post(`/api/v1/events/${slug}/holds`, {
    headers: { ...admTok, "Idempotency-Key": randomUUID() },
    data: { quantity: 1 },
  });
  expect(hold.status(), await hold.text()).toBe(201);
  const order = await request.post("/api/v1/orders", {
    headers: { ...admTok, "Idempotency-Key": randomUUID() },
    data: { hold_id: (await hold.json()).hold_id },
  });
  expect(order.status(), await order.text()).toBe(201);
  const oid = (await order.json()).order_id;
  const pay = await request.post(`/api/v1/orders/${oid}/pay`, {
    headers: { ...auth, "Idempotency-Key": randomUUID() },
    data: { payment_method: "pm_ok" },
  });
  expect(pay.ok(), await pay.text()).toBeTruthy();
  await expect
    .poll(
      async () => {
        const c = await request.post(`/api/v1/orders/${oid}/confirm`, {
          headers: { ...auth, "Idempotency-Key": randomUUID() },
        });
        return (await c.json()).status;
      },
      { timeout: 30_000 },
    )
    .toBe("CONFIRMED");

  await page.goto(`/queue/?e=${slug}`);
  await expect(page.getByText("Every seat found an owner, and nobody got the same seat twice")).toBeVisible({
    timeout: 30_000,
  });
  await expectAccessible(page, "/queue (sold out)");
});
