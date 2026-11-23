# KICKOFF: how to run this build with Claude Code

## 0. One-time prerequisites (Windows, Git Bash)
| Tool | Install | Check |
|---|---|---|
| Docker Desktop (WSL2 backend), **running** | docker.com | `docker version` |
| Go (latest stable) | go.dev/dl | `go version` |
| Node 24 LTS + pnpm | nodejs.org, then `npm i -g pnpm` | `node -v && pnpm -v` |
| Python 3 (for UI UX Pro Max) | `winget install Python.Python.3.12` | `python --version` or `python3 --version` |
| Task | `go install github.com/go-task/task/v3/cmd/task@latest` | `task --version` |
| gcc (for `go test -race` on Windows) | `winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT` | `gcc --version` |
| cloudflared (P8 only) | `winget install --id Cloudflare.cloudflared` | `cloudflared --version` |

Make sure `%USERPROFILE%\go\bin` is on PATH (for `task`).

## 1. Set up the folder
1. Unzip `sluice-kit.zip` onto the Desktop. It creates `Desktop\sluice\` containing `CLAUDE.md`, `README.md`, `.gitattributes`, `.claude/settings.json` and `docs/`. Hidden dotfiles are included, so check that `.claude/` is there.
2. Commit the kit yourself (see the commands in chat).
3. Open Claude Code in `Desktop\sluice`.

## 2. Session rules that keep it fast
- **One phase per session.** Run `/clear` (or start a new session) between phases. The CLAUDE.md file plus the phase file plus the previous reports carry all the state.
- After each phase: read the report, run the git block yourself, and skim the "Understand this before moving on" Q&A.
- If a phase overruns, Claude Code stops at 1.5× the timebox with a PARTIAL report. Decide the cut, then use the **Resume** prompt.

## 3. Prompt for session 1 (P0 + skill install). Paste as-is.
```
You are building SLUICE. Read CLAUDE.md fully first, then docs/SPEC.md, docs/ARCHITECTURE.md, docs/STACK.md and docs/DESIGN-BRIEF.md sections 1-2.

Step 1 - Install the UI UX Pro Max skill (project scope):
  npm install -g uipro-cli
  uipro init --ai claude
  (if the global install fails: npx -y uipro-cli init --ai claude)
Verify .claude/skills/ui-ux-pro-max/scripts/search.py exists and that python3 or python runs it:
  python3 .claude/skills/ui-ux-pro-max/scripts/search.py "ticket booking" --design-system -p "SLUICE"
Use whichever python command works on this machine from now on.

Step 2 - Execute docs/phases/P0-bootstrap.md exactly. Generate design-system/MASTER.md with the P0 command in docs/DESIGN-BRIEF.md and add the Direction notes section.

Rules: git is mine - never run mutating git commands (they are also blocked in .claude/settings.json). Zero paid services, zero sign-ups. When every acceptance check passes, follow the Phase-End Protocol in CLAUDE.md: write docs/reports/P0-bootstrap.md, print the summary, the checklist and the git command block, then STOP.
```

## 4. Prompt for every later phase (change N and the slug)
```
Execute docs/phases/P<N>-<slug>.md.
Re-read CLAUDE.md first, then only the docs listed under "Read first" in that phase file, plus docs/reports/P<N-1>-*.md for context.
Work until every acceptance check passes, with tests for every invariant you touch. Then follow the Phase-End Protocol and STOP. Do not start the next phase.
```
Phase slugs: `P1-schema`, `P2-holds`, `P3-payments`, `P4-queue`, `P5-rush`, `P6-frontend`, `P7-hardening`, `P8-ship`.

**P6 tip:** start that session with "Use the ui-ux-pro-max skill for every page. Generate the page overrides first."
**P8 tip:** paste the CI run URLs, and confirm the artifacts are in `loadtest/results/`.

## 5. Resume prompt (after a PARTIAL stop or a context reset)
```
Resume P<N>. Read CLAUDE.md, docs/phases/P<N>-<slug>.md and docs/reports/P<N>-<slug>.md (WIP notes).
Check the current state with read-only commands (git status, git diff --stat, task test).
Continue from the first unchecked acceptance item. Cut scope: <your decision here, or "none">.
Finish with the Phase-End Protocol and STOP.
```

## 6. Fix prompt (when you reviewed a report and want changes)
```
In P<N>, fix the following before we close the phase:
1. <issue>
2. <issue>
Keep all invariant tests green. Update docs/reports/P<N>-<slug>.md (append a "Fixes" section) and print a fresh git command block, using a fix(...) subject if the phase commit is already made, then STOP.
```

## 7. Order and timeboxes (≈ 41 h of Claude Code time)
| Phase | Timebox | You do after |
|---|---|---|
| P0 Bootstrap | 1.5 h | Commit |
| P1 Schema | 2.5 h | Commit |
| P2 Holds | 4 h | Commit |
| P3 Payments | 6 h | Commit |
| P4 Queue | 4 h | Commit |
| P5 Rush harness | 6 h | Commit |
| P6 Frontend | 7 h | Commit |
| P7 Hardening | 6 h | Commit, push, make the repo public, run the CI `rush` workflow (H 50k, plus B0–B2) |
| P8 Ship | 4 h | Commit, push, Vercel import, tag v1.0.0 |
