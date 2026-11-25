// Serves web/out the way a static host (Vercel) does, with no API behind it:
// /api/* is a 404, so the app detects REPLAY (DESIGN-BRIEF §3). No deps.
//
//   node scripts/serve-static.mjs [port]      default 4173

import { createReadStream, existsSync, statSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, resolve, sep } from "node:path";
import { createGzip } from "node:zlib";

const root = resolve(import.meta.dirname, "..", "out");
const port = Number(process.argv[2] ?? process.env.PORT ?? 4173);
const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".woff2": "font/woff2",
  ".svg": "image/svg+xml",
  ".ico": "image/x-icon",
  ".txt": "text/plain; charset=utf-8",
  ".webm": "video/webm",
  ".png": "image/png",
};

if (!existsSync(join(root, "index.html"))) {
  console.error("web/out/index.html is missing: run `pnpm build` first");
  process.exit(1);
}

function resolveFile(pathname) {
  const p = resolve(join(root, decodeURIComponent(pathname)));
  if (p !== root && !p.startsWith(root + sep)) return null; // no path traversal
  for (const c of [p, join(p, "index.html"), `${p}.html`]) {
    if (existsSync(c) && statSync(c).isFile()) return c;
  }
  return null;
}

createServer((req, res) => {
  const { pathname } = new URL(req.url ?? "/", "http://localhost");
  const file = pathname.startsWith("/api/") ? null : resolveFile(pathname);
  if (!file) {
    res.writeHead(404, { "Content-Type": "text/html; charset=utf-8" });
    createReadStream(join(root, "404.html"))
      .on("error", () => res.end("Not found"))
      .pipe(res);
    return;
  }
  const type = TYPES[extname(file)] ?? "application/octet-stream";
  const immutable = pathname.startsWith("/_next/static/");
  // Static hosts compress text responses; without this, Lighthouse runs against
  // web/out measure a transfer cost the real deploy doesn't have.
  const gzip =
    /\bgzip\b/.test(String(req.headers["accept-encoding"] ?? "")) &&
    /^(text\/|application\/json|image\/svg)/.test(type);
  res.writeHead(200, {
    "Content-Type": type,
    "Cache-Control": immutable ? "public, max-age=31536000, immutable" : "no-cache",
    Vary: "Accept-Encoding",
    ...(gzip ? { "Content-Encoding": "gzip" } : {}),
  });
  const body = createReadStream(file);
  if (gzip) body.pipe(createGzip()).pipe(res);
  else body.pipe(res);
}).listen(port, () => console.log(`serving web/out on http://localhost:${port} (no API: REPLAY mode)`));
