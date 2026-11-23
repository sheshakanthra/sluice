# P<N>: <Phase name>: Report

| Field | Value |
|---|---|
| Status | DONE / PARTIAL |
| Date (IST) | |
| Time spent vs timebox | e.g. 3h40m / 4h |
| Commit base | output of `git rev-parse --short HEAD` (read-only) |

## 1. What was built
- Bullet per deliverable. Name the files and packages.

## 2. Acceptance checklist
Copy every acceptance item from the phase file. Each one needs evidence: the command you ran and the relevant output lines.
- [x] Item: `command` → key output
- [ ] Item: why not, and what's needed

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit | | | |
| integration (testcontainers) | | | |
| race (`-race`) | | | local / CI only |
| property (rapid) | | | |
| e2e (Playwright) | | | |

Coverage for touched core packages (`go test -cover`): inventory __%, orders __%, queue __%.

## 4. Numbers (only if this phase produced any)
Paste them from `summary.json` / `audit.json` / benchmark output, and include the command. Never type a number by hand.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|

## 6. Known issues and debt
- 

## 7. Understand this before moving on (Sheshakanth's interview prep)
Three questions an interviewer could ask about this phase, each with a short answer that references the real code (file:function).
1. **Q:** ...
   **A:** ...
2. **Q:** ...
   **A:** ...
3. **Q:** ...
   **A:** ...

## 8. Files changed
Paste the output of `git status --short`, grouped by area.

## 9. Next phase readiness
Prerequisites for P<N+1>, and anything Sheshakanth must do first (for example push, run CI, install a tool).

## 10. Git commands (Sheshakanth runs these)
```bash
git add -A
git status --short
git commit -m "<type>(<scope>): <summary of 72 chars or fewer>" -m "- <change 1>" -m "- <change 2>" -m "- <change 3>" -m "Phase P<N> - report: docs/reports/P<N>-<slug>.md"
```
Rules:
- Plain ASCII only. No backticks, `$`, `!` or double quotes inside the messages.
- No AI co-author or attribution lines.
- If the build or tests are red, don't give a commit block. List what's broken instead.
