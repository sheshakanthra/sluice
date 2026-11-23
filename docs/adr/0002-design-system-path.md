# ADR 0002: Flatten UI UX Pro Max output to design-system/MASTER.md

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P0

## Context
CLAUDE.md rule 8, DESIGN-BRIEF §2 and the P0 acceptance check all reference `design-system/MASTER.md` and `design-system/pages/<page>.md`. The installed UI UX Pro Max version (via `uipro-cli`) always persists to `design-system/<project-slug>/`, so `-p "SLUICE"` writes `design-system/sluice/MASTER.md`. Its `--output-dir` flag only changes the base directory above `design-system/`, and there is no flag to drop the slug folder.

## Decision
Keep the documented layout. After any `--persist` run, move the generated files up one level:
`design-system/sluice/MASTER.md` → `design-system/MASTER.md` and `design-system/sluice/pages/*.md` → `design-system/pages/`, then delete the empty `design-system/sluice/`. The generated MASTER.md already tells readers to check `design-system/pages/[page-name].md`, which matches the flattened layout.

## Consequences
- Every reference in CLAUDE.md, the phase files and the brief stays valid.
- P6 must repeat the move after generating each page override; DESIGN-BRIEF §2 now says so.
- Regenerating MASTER.md overwrites the appended **Direction notes** section, which then has to be re-appended (contrast numbers come from `python design-system/contrast.py`).
- Docs updated: DESIGN-BRIEF.md §2.
