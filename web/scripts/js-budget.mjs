// JS budgets (DESIGN-BRIEF §7, ADR 0012) against the static export in web/out.
// Rule: framework + at most 50 KB (gzip) of app code per route.
// - First-load JS of a route = every script its exported HTML loads (<script
//   src> and <link rel=preload as=script>), minus noModule polyfills, which
//   module-capable browsers never fetch. Each file is gzipped at level 9.
// - Framework = Next's rootMainFiles in .next/build-manifest.json (React DOM,
//   the Next client runtime, the Turbopack loader). Everything else is app code.
// - Lazy chunks (next/dynamic) are not in the HTML; --lazy lists them.
//
//   node scripts/js-budget.mjs [--lazy]     exit 1 if a route is over budget

import { readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { gzipSync } from "node:zlib";

const out = resolve(import.meta.dirname, "..", "out");
const APP_BUDGET_KB = 50;
const framework = new Set(
  JSON.parse(
    readFileSync(resolve(import.meta.dirname, "..", ".next", "build-manifest.json"), "utf8"),
  ).rootMainFiles.map((f) => `_next/${f}`),
);
const routes = ["/", "/event", "/queue", "/book", "/checkout", "/order", "/control", "/results", "/how"];

const gz = new Map();
const gzSize = (p) => {
  if (!gz.has(p)) gz.set(p, gzipSync(readFileSync(join(out, p)), { level: 9 }).length);
  return gz.get(p);
};
const scriptsOf = (html) => {
  const set = new Set();
  // noModule scripts (Next's core-js polyfills) are skipped by every browser
  // that runs modules, i.e. every browser the budgets are about.
  for (const m of html.matchAll(/<script([^>]+)src="([^"]+\.js)"([^>]*)>/g)) {
    if (!/noModule/i.test(m[1] + m[3])) set.add(m[2]);
  }
  for (const m of html.matchAll(
    /<link[^>]+as="script"[^>]+href="([^"]+\.js)"|<link[^>]+href="([^"]+\.js)"[^>]+as="script"/g,
  ))
    set.add(m[1] ?? m[2]);
  return [...set].map((s) => s.replace(/^\//, ""));
};

const used = new Set();
let failed = false;
const fwKB = [...framework].reduce((a, f) => a + gzSize(f), 0) / 1024;
console.log(`framework (rootMainFiles, ${framework.size} files): ${fwKB.toFixed(1)} KB gzip`);
console.log("route        first-load   framework   app code   budget (app)");
for (const r of routes) {
  const html = readFileSync(join(out, r === "/" ? "index.html" : `${r.slice(1)}/index.html`), "utf8");
  const files = scriptsOf(html);
  files.forEach((f) => used.add(f));
  const total = files.reduce((a, f) => a + gzSize(f), 0) / 1024;
  const fw = files.filter((f) => framework.has(f)).reduce((a, f) => a + gzSize(f), 0) / 1024;
  const app = total - fw;
  const over = app > APP_BUDGET_KB;
  failed ||= over;
  console.log(
    `${r.padEnd(12)} ${total.toFixed(1).padStart(7)} KB ${fw.toFixed(1).padStart(8)} KB ${app.toFixed(1).padStart(7)} KB   <= ${APP_BUDGET_KB} KB ${over ? "OVER" : "ok"}`,
  );
}

if (process.argv.includes("--lazy")) {
  const dir = join(out, "_next", "static", "chunks");
  const lazy = readdirSync(dir, { recursive: true })
    .filter((f) => String(f).endsWith(".js"))
    .map((f) => `_next/static/chunks/${String(f).replaceAll("\\", "/")}`)
    .filter((f) => !used.has(f))
    .map((f) => [f, gzSize(f) / 1024])
    .sort((a, b) => b[1] - a[1]);
  console.log("\nlazy chunks (not in any page's HTML), gzip:");
  for (const [f, kb] of lazy.slice(0, 8)) console.log(`  ${kb.toFixed(1).padStart(7)} KB  ${f}`);
}
process.exit(failed ? 1 : 0);
