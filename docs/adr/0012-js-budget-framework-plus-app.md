# ADR 0012: JS budget is framework + at most 50 KB of app code per route

- **Status:** accepted (owner decision, session P6b)
- **Date:** 2026-11-25
- **Phase:** P6

## Context
DESIGN-BRIEF §7 set absolute first-load budgets: `/queue` ≤ 120 KB, `/book` ≤ 200 KB, `/control` and `/results` ≤ 300 KB (gzip). Next.js 16 no longer prints per-route sizes, so P6b added `web/scripts/js-budget.mjs`, which gzips (level 9) every script a route's exported HTML loads.

The framework alone is over the `/queue` budget. Next's `rootMainFiles` (`.next/build-manifest.json`), which every route loads, come to **128.7 KB gzip** on Next 16.4.6 + React 19.3.0:

| File | gzip |
|---|---|
| React DOM client | 69.9 KB |
| Next client runtime (2 chunks) | 44.6 + 8.5 KB |
| Turbopack loader + bootstrap | 3.8 + 1.9 KB |

No app code can bring `/queue` under 120 KB, and Next/React is the stack the owner chose (ADR 0001). The `noModule` core-js polyfill chunk (38.6 KB) is in the HTML too, but browsers that run modules never fetch it, so it isn't counted.

## Decision
The JS rule for every route is now **framework + at most 50 KB (gzip) of app code**. Framework means the `rootMainFiles` in the build manifest, and app code is every other script the route's HTML loads. Lazy chunks don't count toward first load: Recharts on `/results` (129.3 KB) loads through `next/dynamic`. `/queue` must still import nothing heavy, meaning no chart library and no motion library, since its animations are CSS. `node web/scripts/js-budget.mjs` checks the rule and exits 1 when a route is over.

## Consequences
- The budget now limits what we control. A framework upgrade that grows `rootMainFiles` shows up as a framework number in the report, not as an app regression.
- P6b measurement: app code is 28.0–48.2 KB per route, all within 50 KB. `/book` (48.2 KB) has the least headroom.
- To stay under the line, P6b swapped the Base UI button primitive for a native `<button>`, which saved about 14 KB of gzip on every page.
- Docs updated: `docs/DESIGN-BRIEF.md` §7.
