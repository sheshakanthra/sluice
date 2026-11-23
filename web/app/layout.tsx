import type { Metadata, Viewport } from "next";
import { Barlow, Barlow_Condensed } from "next/font/google";
import "./globals.css";
import { cn } from "@/lib/utils";

// Body: Barlow. Display + every numeral: Barlow Condensed.
// Chosen in design-system/MASTER.md (Direction notes): drawn from transit signage, tabular figures.
const barlow = Barlow({
  variable: "--font-barlow",
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
  display: "swap",
});

const barlowCondensed = Barlow_Condensed({
  variable: "--font-barlow-condensed",
  subsets: ["latin"],
  weight: ["500", "600", "700"],
  display: "swap",
});

export const metadata: Metadata = {
  title: "SLUICE · Rush-proof ticket booking",
  description:
    "A virtual waiting room, fenced seat holds and idempotent payments, built to be load-tested with 50,000 people on 500 seats.",
};

export const viewport: Viewport = {
  themeColor: "#1e3a8a",
  colorScheme: "light",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en-IN" className={cn("h-full antialiased", barlow.variable, barlowCondensed.variable)}>
      <body className="flex min-h-full flex-col">{children}</body>
    </html>
  );
}
