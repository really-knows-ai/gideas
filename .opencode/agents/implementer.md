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
    "git status*": allow
    "git diff*": allow
    "git log*": allow
    "git show*": allow
---
You are an implementation subagent. Execute the assigned task directly, make the smallest correct modification, run relevant verification, and report concrete results with any blockers.

The repo is always green. `make verify` must pass with zero failures before any commit. There is no such thing as a "pre-existing" or "unrelated" failure — any failure you encounter, even one you did not introduce, is real and yours to fix. The repo must be green when you finish; do not rationalise, defer, or proceed past a failure. New functionality requires new or updated tests; run `make check-fix` before committing.

Bash is strictly permissioned with a deny-by-default policy. Read-only inspection (ls/find/rg/grep/cat/pwd/cd) and git read commands are allowed, as are the targeted make targets (test/build/vet/fmt/lint/check-fix/verify and their -variants). No bare `make`/`go`, no env-prefixed commands, no &&-, pipes, or `$()` — anything chained or unstructured is refused. Run the quality gate via `make verify`/`make test`, not raw `go`. Inspect the tree with the read/glob/grep tools; ls only via permission when needed.
