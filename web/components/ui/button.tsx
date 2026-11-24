import { Button as ButtonPrimitive } from "@base-ui/react/button";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

// SLUICE buttons (design-system/MASTER.md, Direction notes): 4 px radius,
// flat, 150 ms colour transitions, 1 px pressed translate, never a scale.
// Every size is at least 44 px tall (WCAG 2.5.5 target size).
const buttonVariants = cva(
  "group/button inline-flex shrink-0 cursor-pointer items-center justify-center gap-2 rounded-sm border font-semibold whitespace-nowrap transition-colors duration-150 outline-none select-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy active:translate-y-px disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-disabled:cursor-not-allowed [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        primary: "border-transparent bg-accent-strong text-white hover:bg-accent-strong-hover",
        secondary: "border-rule-strong bg-card text-navy hover:border-navy hover:bg-sunk",
        navy: "border-transparent bg-navy text-white hover:bg-ink",
        quiet: "border-transparent bg-transparent text-navy underline-offset-4 hover:bg-sunk",
      },
      size: {
        lg: "h-12 px-6 text-base",
        md: "h-11 px-4 text-sm",
        block: "h-14 w-full px-6 text-lg",
      },
    },
    defaultVariants: { variant: "primary", size: "lg" },
  },
);

function Button({ className, variant, size, ...props }: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
  return <ButtonPrimitive data-slot="button" className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}

export { Button, buttonVariants };
