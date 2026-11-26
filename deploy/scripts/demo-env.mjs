// Creates deploy/env/demo.env from demo.env.example with fresh random
// secrets (ADR 0016). Idempotent: an existing demo.env is kept, so tokens
// already issued to visitors stay valid across `task demo` runs. Delete the
// file to rotate every secret. Prints no secret. No deps.
//
//   node deploy/scripts/demo-env.mjs

import { randomBytes } from "node:crypto";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const dir = resolve(import.meta.dirname, "..", "env");
const example = join(dir, "demo.env.example");
const target = join(dir, "demo.env");
const PLACEHOLDER = "generate-with-task-demo";

// KEY=value lines only: the header comment names the placeholder too.
const hasPlaceholder = (text) =>
  text.split(/\r?\n/).some((l) => /^[A-Z0-9_]+=/.test(l) && l.includes(PLACEHOLDER));

if (existsSync(target)) {
  if (hasPlaceholder(readFileSync(target, "utf8"))) {
    console.error(`${target} still has ${PLACEHOLDER} values: delete it and run again`);
    process.exit(1);
  }
  console.log("deploy/env/demo.env exists: keeping its secrets (delete it to rotate)");
  process.exit(0);
}

// 32 random bytes each (DEPLOY.md security checklist).
const b64 = () => randomBytes(32).toString("base64");
const b64url = () => randomBytes(32).toString("base64url");
const values = {
  TOKEN_KEYS: () => `demo1:${b64()}`,
  ADMIN_TOKEN: b64url,
  MOCKPAY_API_KEY: b64url,
  MOCKPAY_WEBHOOK_SECRET: b64url,
};

const out = readFileSync(example, "utf8")
  .split(/\r?\n/)
  .map((line) => {
    const m = /^([A-Z0-9_]+)=(.*)$/.exec(line);
    if (!m || !m[2].includes(PLACEHOLDER)) return line;
    const gen = values[m[1]];
    if (!gen) {
      console.error(`no generator for ${m[1]}`);
      process.exit(1);
    }
    return `${m[1]}=${gen()}`;
  })
  .join("\n");

writeFileSync(target, out, { mode: 0o600 });
console.log(`wrote deploy/env/demo.env with fresh secrets for ${Object.keys(values).join(", ")}`);
