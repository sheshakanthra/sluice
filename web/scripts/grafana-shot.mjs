// Screenshot the provisioned SLUICE Grafana dashboard (P7 acceptance:
// "the dashboard loads with data during task rush:smoke").
//   node web/scripts/grafana-shot.mjs <out.png> [from=now-10m]
import { chromium } from "@playwright/test";

const out = process.argv[2] ?? "grafana.png";
const from = process.argv[3] ?? "now-10m";
const url = `http://localhost:3001/d/sluice/sluice?orgId=1&from=${from}&to=now&kiosk`;
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1600, height: 3200 } });
await page.goto(url, { waitUntil: "networkidle", timeout: 60_000 });
await page.waitForTimeout(12_000);
await page.screenshot({ path: out, fullPage: true });
await browser.close();
console.log(`saved ${out} from ${url}`);
