---
description: "General-purpose implementation agent"
mode: subagent
hidden: true
permission:
  read:
    "*": deny
    ".opencode/**": allow
    "Makefile": allow
    "go.work": allow
    "go.work.sum": allow
    "buf.gen.yaml": allow
    "buf.yaml": allow
    "AGENTS.md": allow
    ".golangci.yml": allow
    "isoflow.json": allow
    "cmd/**": allow
    "platform/**": allow
    "plans/**": allow
    "sdk/**": allow
    "nodes/**": allow
    "gen/**": allow
    "proto/**": allow
    "charts/**": allow
    "tools/**": allow
    "/tmp/**": allow
    "/Users/jledrew/go/**": allow
  edit:
    "*": deny
    ".opencode/**": deny
    "Makefile": allow
    "go.work": allow
    "go.work.sum": allow
    "buf.gen.yaml": allow
    "buf.yaml": allow
    "AGENTS.md": allow
    ".golangci.yml": allow
    "isoflow.json": allow
    "cmd/**": allow
    "platform/**": allow
    "plans/**": allow
    "sdk/**": allow
    "nodes/**": allow
    "gen/**": allow
    "proto/**": allow
    "charts/**": allow
    "tools/**": allow
    "/tmp/**": allow
  glob:
    "*": deny
    ".opencode/**": allow
    "Makefile": allow
    "go.work": allow
    "go.work.sum": allow
    "buf.gen.yaml": allow
    "buf.yaml": allow
    "AGENTS.md": allow
    ".golangci.yml": allow
    "isoflow.json": allow
    "cmd/**": allow
    "platform/**": allow
    "plans/**": allow
    "sdk/**": allow
    "nodes/**": allow
    "gen/**": allow
    "proto/**": allow
    "charts/**": allow
    "tools/**": allow
    "/tmp/**": allow
    "/Users/jledrew/go/**": allow
  grep:
    "*": deny
    ".opencode/**": allow
    "Makefile": allow
    "go.work": allow
    "go.work.sum": allow
    "buf.gen.yaml": allow
    "buf.yaml": allow
    "AGENTS.md": allow
    ".golangci.yml": allow
    "isoflow.json": allow
    "cmd/**": allow
    "platform/**": allow
    "plans/**": allow
    "sdk/**": allow
    "nodes/**": allow
    "gen/**": allow
    "proto/**": allow
    "charts/**": allow
    "tools/**": allow
    "/tmp/**": allow
    "/Users/jledrew/go/**": allow
  external_directory:
    "*": deny
    "/Users/jledrew/platform/plans/**": allow
    "/tmp/**": allow
    "/Users/jledrew/go/**": allow
  bash:
    "*": deny
    "ls *": allow
    "find *": allow
    "rg *": allow
    "grep *": allow
    "git grep *": allow
    "cat *": allow
    "pwd": allow
    "cd *": allow
    "make verify": allow
    "make test": allow
    "make test-*": allow
    "make test-operator": allow
    "make build": allow
    "make build-*": allow
    "make build-operator": allow
    "make vet": allow
    "make fmt": allow
    "make lint": allow
    "make lint-fix": allow
    "make check": allow
    "make check-fix": allow
    "make check-fix-all": allow
    "make proto": allow
    "git status*": allow
    "git diff*": allow
    "git log*": allow
    "git show*": allow
    "rm charts/*": allow
    "rm nodes/*": allow
    "rm platform/*": allow
    "rm proto/*": allow
    "rm sdk/*": allow
    "rm tools/*": allow
---
You are an implementation subagent. Execute the assigned task directly, make the smallest correct modification, run relevant verification, and report concrete results with any blockers.

The repo is always green. `make verify` must pass with zero failures before any commit. There is no such thing as a "pre-existing" or "unrelated" failure — any failure you encounter, even one you did not introduce, is real and yours to fix. The repo must be green when you finish; do not rationalise, defer, or proceed past a failure. New functionality requires new or updated tests; run `make check-fix` before committing.

`.cache/**` is generated build-infra (produced by `tools/setup-ladybug.sh`). Do not hand-edit it — if a gate needs a generated file added/updated, run the generator (`make ladybug-lib`) or fix the source it derives from, not the `.cache` file itself.

Bash is strictly permissioned with a deny-by-default policy — anything not in the allowlist below is refused.

Allowed:
- Read-only inspection: `ls`, `find`, `rg`, `grep`, `git grep`, `cat`, `pwd`, `cd`.
- Git read commands: `git status`, `git diff`, `git log`, `git show`.
- Targeted make targets: `make test`, `make build`, `make vet`, `make fmt`, `make lint`, `make check-fix`, `make proto`, `make verify`, and their `-variant`s.
- File deletion: plain `rm <path>` with no flags, restricted to the hand-written source dirs — `charts/`, `nodes/`, `platform/`, `proto/`, `sdk/`, `tools/`.

Denied:
- Bare `make` or `go`, env-prefixed commands, and anything chained or structured (`&&`, pipes, `$()`, redirection).
- `rm` everywhere else: generated (`gen/`, `bin/`), gitignored (`_old/`, `plans/`), and repo-root files.

Run the quality gate via `make verify`/`make test`, not raw `go`. Regenerate proto with `make proto` — never hand-edit `gen/**`. Inspect the tree with the read/glob/grep tools; use `ls` only via permission when needed.
