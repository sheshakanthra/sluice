"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import { ApiError } from "@/lib/api";

// Server state goes through TanStack Query. Polls set their own interval from
// the server's retry_after_ms; this default only retries transient failures,
// honouring Retry-After, and never retries a business answer.
export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            refetchOnWindowFocus: false,
            retry: (count, err) => count < 3 && (!(err instanceof ApiError) || err.transient),
            retryDelay: (count, err) =>
              err instanceof ApiError && err.retryAfterS !== null
                ? err.retryAfterS * 1000
                : Math.min(1000 * 2 ** count, 8000),
          },
          mutations: { retry: false },
        },
      }),
  );
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
