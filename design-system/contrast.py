"""WCAG 2.x contrast checker for the SLUICE palette.

Usage: python design-system/contrast.py
Prints the contrast ratio of every foreground token against each surface,
plus the text-on-fill pairs used by buttons and badges. The numbers in the
"Direction notes" section of MASTER.md come from this script.
Thresholds: 4.5 normal text, 3.0 large text (>= 24px, or >= 18.66px bold)
and UI component boundaries (WCAG 1.4.11).
"""

SURFACES = {"paper": "#EFF6FF", "card": "#FFFFFF", "sunk": "#DBEAFE"}

FOREGROUNDS = {
    "ink": "#0F1F47",
    "ink-muted": "#3B4A6B",
    "ink-subtle": "#55647F",
    "navy": "#1E3A8A",
    "accent-strong": "#C2410C",
    "accent": "#F97316",
    "rule": "#C7D5EC",
    "rule-strong": "#7D8DAA",
    "success": "#15803D",
    "warning": "#B45309",
    "danger": "#B91C1C",
    "info": "#1D4ED8",
    "rejected-#3B82F6": "#3B82F6",
}

# (label, text, fill): text sitting on a filled element.
PAIRS = [
    ("white on navy (sign band)", "#FFFFFF", "#1E3A8A"),
    ("white on accent-strong (primary button)", "#FFFFFF", "#C2410C"),
    ("white on accent-strong-hover (button hover)", "#FFFFFF", "#9A3412"),
    ("ink on accent (signature notch, badges)", "#0F1F47", "#F97316"),
    ("white on ink (inverted stub)", "#FFFFFF", "#0F1F47"),
    ("accent on ink (live marker on inverted stub)", "#F97316", "#0F1F47"),
    ("accent on navy", "#F97316", "#1E3A8A"),
    ("sky on navy (secondary text on sign band)", "#BFDBFE", "#1E3A8A"),
]


def luminance(hex_color: str) -> float:
    h = hex_color.lstrip("#")
    channels = [int(h[i : i + 2], 16) / 255 for i in (0, 2, 4)]
    lin = [c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4 for c in channels]
    return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]


def contrast(a: str, b: str) -> float:
    hi, lo = sorted([luminance(a), luminance(b)], reverse=True)
    return (hi + 0.05) / (lo + 0.05)


def main() -> None:
    print("token".ljust(17) + "hex".ljust(9) + "".join(n.rjust(8) for n in SURFACES))
    for name, fg in FOREGROUNDS.items():
        row = name.ljust(17) + fg.ljust(9)
        row += "".join(f"{contrast(fg, bg):8.2f}" for bg in SURFACES.values())
        print(row)
    print()
    for label, text, fill in PAIRS:
        print(f"{contrast(text, fill):6.2f}  {label}  ({text} on {fill})")


if __name__ == "__main__":
    main()
