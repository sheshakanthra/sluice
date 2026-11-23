import type { NextConfig } from "next";

// Static export: Caddy (live stack) and Vercel (replay site) serve plain files.
// Dynamic IDs travel in query params (DESIGN-BRIEF §3), so no server is needed.
const nextConfig: NextConfig = {
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
  reactStrictMode: true,
  poweredByHeader: false,
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;
