// Builds the REPLAY data in web/public/results/latest/ from curated load-test
// runs in docs/results/ (DESIGN-BRIEF §3, DEPLOY.md §A). Nothing is computed
// except unit-free reshaping: every number is copied from a summary.json or
// timeseries.csv written by `sluice rush`. Until P8 copies the CI headline run,
// the source is the P5b SMOKE run, and the files say so (source.kind).
//
//   node web/scripts/build-replay.mjs [headlineDir] [baselineDir...]
//
// Defaults: docs/results/p5b/p5b-smoke, then the P5b baseline runs.

import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const out = join(root, "web", "public", "results", "latest");
const args = process.argv.slice(2);
const headline = resolve(root, args[0] ?? "docs/results/p5b/p5b-smoke");
const baselineDirs = (
  args.length > 1
    ? args.slice(1)
    : ["p5b-b0", "p5b-b1", "p5b-b2", "p5b-smoke", "p5b-b2-20k", "p5b-b2-50k"].map((r) => `docs/results/p5b/${r}`)
).map((d) => resolve(root, d));

const readJSON = (p) => JSON.parse(readFileSync(p, "utf8"));
const rel = (p) => p.slice(root.length + 1).replaceAll("\\", "/");

mkdirSync(out, { recursive: true });

// summary.json: byte-for-byte copy of the run's summary.
const summaryPath = join(headline, "summary.json");
const summary = readJSON(summaryPath);
copyFileSync(summaryPath, join(out, "summary.json"));

// timeseries.json: the 1 s rows of timeseries.csv as columns + rows.
const csvPath = join(headline, "timeseries.csv");
if (!existsSync(csvPath)) throw new Error(`${rel(csvPath)} is missing: copy it from loadtest/results/<run_id>/`);
const [head, ...lines] = readFileSync(csvPath, "utf8").trim().split(/\r?\n/);
const columns = head.split(",");
// An empty cell (the stats sampler had no reading that second) stays null:
// never turn a missing value into a 0.
const rows = lines.map((l) =>
  l.split(",").map((v, i) => (v === "" ? null : columns[i] === "sold_out" ? (v === "true" ? 1 : 0) : Number(v))),
);
for (const r of rows) {
  if (r.some((v) => Number.isNaN(v))) throw new Error(`non-numeric row in ${rel(csvPath)}: ${r}`);
}
const partial = rows.filter((r) => r.includes(null)).length;
const ci = summary.env?.runner?.startsWith("github");
const source = {
  kind: ci ? "ci" : "local",
  run_id: summary.run_id,
  git_sha: summary.git_sha,
  scenario: summary.scenario,
  started_at: summary.started_at,
  from: rel(headline),
  note: ci ? "CI headline run" : "Local SMOKE run (P5b), a placeholder until the CI headline run replaces it in P8",
};
writeFileSync(join(out, "timeseries.json"), JSON.stringify({ source, t0: summary.timing.t0, columns, rows }) + "\n");

// baselines.json: one row per run, fields copied from each summary.json.
const pick = (s, dir) => ({
  run_id: s.run_id,
  scenario: s.scenario,
  from: rel(dir),
  users: s.config.users,
  seats: s.config.seats,
  queue: !s.config.direct,
  git_sha: s.git_sha,
  tickets: s.outcomes.TICKET,
  unresolved: s.outcomes.UNRESOLVED,
  seats_sold: s.outcomes.seats_sold,
  audit: s.audit
    ? {
        mode: s.audit.mode,
        oversold: s.audit.oversold,
        duplicate_charges: s.audit.duplicate_charges,
        orphan_charges: s.audit.orphan_charges,
        passed: s.audit.passed,
        exit_code: s.audit.exit_code,
      }
    : null,
  checkout_p95_ms: s.headline?.checkout_p95_ms ?? s.latency_ms?.checkout?.p95 ?? null,
  error_rate: s.errors.rate,
  peak_rps: s.load.peak_rps,
  peak_in_system: s.load.peak_in_system,
});
const runs = baselineDirs.map((d) => pick(readJSON(join(d, "summary.json")), d));
writeFileSync(join(out, "baselines.json"), JSON.stringify({ source, runs }, null, 2) + "\n");

console.log(`replay data -> ${rel(out)}`);
console.log(`  summary.json    ${summary.run_id} (${summary.scenario}, ${summary.git_sha}, ${source.kind})`);
console.log(
  `  timeseries.json ${rows.length} rows, t ${rows[0][0]}..${rows.at(-1)[0]} s, ${partial} with empty cells (null)`,
);
console.log(`  baselines.json  ${runs.map((r) => r.run_id).join(", ")}`);
