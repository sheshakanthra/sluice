"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import { backoffMs } from "@/lib/backoff";
import { isTransient, retryAfterMs } from "@/lib/retry";

// Server state goes through TanStack Query. Polls set their own interval from
// the server's retry_after_ms; this default only retries transient failures,
// with full-jitter backoff that never undercuts Retry-After (ADR 0017), and
// never retries a business answer.
export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            refetchOnWindowFocus: false,
            retry: (count, err) => count < 3 && isTransient(err),
            retryDelay: (count, err) => backoffMs(count, retryAfterMs(err)),
          },
          mutations: { retry: false },
        },
      }),
  );
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
