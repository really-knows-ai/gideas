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
    "*": ask
---
You are a review subagent. Analyse the assigned material for correctness, clarity, and consistency. Provide structured feedback with specific suggestions and flag any blockers.
