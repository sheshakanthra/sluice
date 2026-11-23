// Quality-bar screenshot review (DESIGN-BRIEF §10).
//
// Usage (from web/, after `pnpm build`):  pnpm screenshots [outDir]
// Serves ./out on a local port, captures every route at 375 px and 1440 px
// (full page, plus a reduced-motion pass), and prints machine checks:
// horizontal overflow, touch targets under 44 px, and interactive elements
// whose box is not aligned to the 4 px half-step of the 8 px grid.
//
// Browser: PW_CHANNEL (default "msedge" on Windows so no browser download is
// needed; bundled Chromium elsewhere).

import { createReadStream, existsSync, mkdirSync, statSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, resolve } from "node:path";
import { chromium } from "@playwright/test";

const ROUTES = ["/"];
const WIDTHS = [375, 1440];
const outDir = resolve(process.argv[2] ?? "../docs/reports/assets/p0");
const root = resolve("out");
const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript",
  ".css": "text/css",
  ".woff2": "font/woff2",
  ".svg": "image/svg+xml",
  ".ico": "image/x-icon",
  ".txt": "text/plain",
  ".json": "application/json",
};

if (!existsSync(join(root, "index.html"))) {
  console.error("out/index.html missing: run `pnpm build` first");
  process.exit(1);
}
mkdirSync(outDir, { recursive: true });

const server = createServer((req, res) => {
  const path = decodeURIComponent(new URL(req.url ?? "/", "http://x").pathname);
  let file = resolve(join(root, path));
  if (!file.startsWith(root)) {
    res.writeHead(403).end();
    return;
  }
  if (existsSync(file) && statSync(file).isDirectory()) file = join(file, "index.html");
  if (!existsSync(file)) {
    res.writeHead(404).end("not found");
    return;
  }
  res.writeHead(200, { "Content-Type": TYPES[extname(file)] ?? "application/octet-stream" });
  createReadStream(file).pipe(res);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}`;

const channel = process.env.PW_CHANNEL ?? (process.platform === "win32" ? "msedge" : undefined);
const browser = await chromium.launch(channel ? { channel } : {});
let problems = 0;

try {
  for (const route of ROUTES) {
    for (const width of WIDTHS) {
      for (const reducedMotion of ["no-preference", "reduce"]) {
        const ctx = await browser.newContext({
          viewport: { width, height: width < 768 ? 812 : 900 },
          deviceScaleFactor: 1,
          reducedMotion,
        });
        const page = await ctx.newPage();
        await page.goto(base + route, { waitUntil: "networkidle" });
        await page.evaluate(() => document.fonts.ready);

        const slug = route === "/" ? "landing" : route.replaceAll("/", "-").replace(/^-|-$/g, "");
        const suffix = reducedMotion === "reduce" ? "-reduced-motion" : "";
        const file = join(outDir, `${slug}-${width}${suffix}.png`);
        await page.screenshot({ path: file, fullPage: true });

        const report = await page.evaluate(() => {
          const doc = document.documentElement;
          const overflow = doc.scrollWidth - window.innerWidth;
          const interactive = [...document.querySelectorAll("a, button, input, select, textarea, [role=button]")];
          const small = interactive
            .map((el) => ({ el, r: el.getBoundingClientRect() }))
            .filter(({ r }) => r.width > 0 && (r.height < 44 || r.width < 44))
            .map(
              ({ el, r }) =>
                `${el.tagName.toLowerCase()} "${(el.textContent ?? "").trim().slice(0, 30)}" ${Math.round(r.width)}x${Math.round(r.height)}`,
            );
          const offGrid = interactive
            .map((el) => ({ el, r: el.getBoundingClientRect() }))
            .filter(({ r }) => r.width > 0 && Math.round(r.height) % 4 !== 0)
            .map(
              ({ el, r }) =>
                `${el.tagName.toLowerCase()} "${(el.textContent ?? "").trim().slice(0, 30)}" h=${r.height}`,
            );
          const fonts = [
            ...new Set(
              [...document.querySelectorAll("body *")].map((e) => getComputedStyle(e).fontFamily.split(",")[0]),
            ),
          ];
          return { overflow, small, offGrid, fonts, height: doc.scrollHeight };
        });

        console.log(`\n${route} @ ${width}px (${reducedMotion}) -> ${file}`);
        console.log(`  page height: ${report.height}px, horizontal overflow: ${report.overflow}px`);
        console.log(`  fonts in use: ${report.fonts.join(" | ")}`);
        console.log(`  touch targets < 44px: ${report.small.length ? report.small.join("; ") : "none"}`);
        console.log(
          `  interactive heights off the 4px half-grid: ${report.offGrid.length ? report.offGrid.join("; ") : "none"}`,
        );
        if (report.overflow > 0) problems++;
        await ctx.close();
      }
    }
  }
} finally {
  await browser.close();
  server.close();
}
console.log(problems ? `\n${problems} layout problem(s)` : "\nno horizontal overflow at any width");
process.exit(problems ? 1 : 0);
