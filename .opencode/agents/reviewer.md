---
description: "General-purpose review agent"
mode: subagent
permission:
  read:
    "/Users/jledrew/go/**": allow
    "/tmp/**": allow
    "/Users/jledrew/platform/**": allow
    "*": deny
  edit:
    "/Users/jledrew/platform/plans/**": allow
    "/Users/jledrew/go/**": deny
    "/tmp/**": deny
    "/Users/jledrew/platform/**": deny
    "*": deny
  glob:
    "/tmp/**": allow
    "/Users/jledrew/go/**": allow
    "/Users/jledrew/platform/**": allow
    "*": deny
  grep:
    "/tmp/**": allow
    "/Users/jledrew/go/**": allow
    "/Users/jledrew/platform/**": allow
    "*": deny
  external_directory:
    "/Users/jledrew/go/**": allow
    "/tmp/**": allow
    "*": deny
  bash:
    "make test": allow
    "make test-*": allow
    "make test-operator": allow
    "make vet": allow
    "make fmt": allow
    "make lint": allow
    "make check": allow
    "go test *": allow
    "go vet *": allow
    "go list *": allow
    "git status": allow
    "git diff": allow
    "git log": allow
    "git show": allow
    "*": deny
---
You are a review subagent. Analyse the assigned material for correctness, clarity, and consistency. Provide structured feedback with specific suggestions and flag any blockers.

Bash is strictly permissioned with a deny-by-default policy. You may only run the make/go/git commands explicitly allowed; anything else will be refused. Do not attempt mutating targets (`make lint-fix`, `make check-fix`, `make build`) — those belong to the implementer agent.
