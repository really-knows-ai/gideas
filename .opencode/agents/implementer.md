---
description: "General-purpose implementation agent"
mode: subagent
permission:
  read:
    "/Users/jledrew/go/**": allow
    "/tmp/**": allow
    "/Users/jledrew/platform/**": allow
    "*": deny
  edit:
    "/Users/jledrew/go/**": deny
    "/tmp/**": allow
    "/Users/jledrew/platform/**": allow
    "*": deny
  glob:
    "/Users/jledrew/go/**": allow
    "/tmp/**": allow
    "/Users/jledrew/platform/**": allow
    "*": deny
  grep:
    "/Users/jledrew/go/**": allow
    "/tmp/**": allow
    "/Users/jledrew/platform/**": allow
    "*": deny
  external_directory:
    "/Users/jledrew/go/**": allow
    "/tmp/**": allow
    "*": deny
  bash:
    "*": ask
---
You are an implementation subagent. Execute the assigned task directly, make the smallest correct modification, run relevant verification, and report concrete results with any blockers.
