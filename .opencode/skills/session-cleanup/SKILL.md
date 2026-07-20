---
name: session-cleanup
description: Delete old opencode sessions that exceed a given age threshold. Cleans
  up DB records (with CASCADE), orphaned snapshots, and orphaned storage directories,
  then vacuums the database.
---

## Overview

Deletes sessions from the local opencode database where `time_created` is older
than the specified cutoff. Works by:

1. Querying the SQLite database for matching session IDs.
2. Deleting via SQL — FK constraints cascade to messages, parts, todos, etc.
3. Removing orphaned snapshot and storage directories for deleted sessions.
4. Running `VACUUM` to reclaim filesystem space.

---

## Usage

Call the skill with a cutoff description, e.g.:

> `session-cleanup > 1 week`
> `session-cleanup > 30 days`
> `session-cleanup > 2 months`

The argument must match the pattern `> <N> (day|days|week|weeks|month|months)`.

---

## Entry Point

1.  Read `AGENTS.md` at the repository root.
2.  Locate the opencode data directory:
    - Check `~/.local/share/opencode/`.
    - Verify `opencode.db` exists inside it.
3.  Extract the cutoff description from the user's message (e.g. `> 1 week`).

---

## Parse the Cutoff

Parse the user-provided cutoff string:

- `> N days` → multiply `N * 86400 * 1000`
- `> N weeks` → multiply `N * 604800 * 1000`
- `> N months` → multiply `N * 2592000 * 1000`

Calculate `cutoff_ms = now_ms - (N * unit_ms)` where `now_ms` comes from the
most recent session's `time_created` (or `Date.now()` if the DB is empty).

---

## Preview Phase

Before deleting, show the user a count of affected sessions grouped by age:

```sql
SELECT
  CASE
    WHEN (? - time_created) < 86400000 THEN 'today'
    WHEN (? - time_created) < 172800000 THEN '1 day'
    WHEN (? - time_created) < 259200000 THEN '2 days'
    WHEN (? - time_created) < 432000000 THEN '3-5 days'
    WHEN (? - time_created) < 604800000 THEN '6-7 days'
    WHEN (? - time_created) < 1209600000 THEN '1-2 weeks'
    WHEN (? - time_created) < 2592000000 THEN '2-4 weeks'
    ELSE '> 1 month'
  END as age,
  COUNT(*) as count
FROM session
WHERE time_created < ?
GROUP BY age
ORDER BY MIN(time_created) DESC;
```

Ask the user to confirm before proceeding. Start with `?` placeholders using
`now_ms` and `cutoff_ms`.

---

## Deletion

1.  Delete matching sessions from the DB:

    ```bash
    sqlite3 ~/.local/share/opencode/opencode.db "DELETE FROM session WHERE time_created < $cutoff_ms;"
    ```

2.  Remove orphaned snapshot directories:

    ```bash
    REMAINING=$(sqlite3 ~/.local/share/opencode/opencode.db "SELECT id FROM session;")
    for dir in ~/.local/share/opencode/snapshot/*/; do
      id=$(basename "$dir")
      if ! echo "$REMAINING" | grep -q "$id"; then
        rm -rf "$dir"
      fi
    done
    ```

3.  Remove orphaned storage session directories:

    ```bash
    for dir in ~/.local/share/opencode/storage/session/*/; do
      [ -d "$dir" ] || continue
      id=$(basename "$dir")
      if ! echo "$REMAINING" | grep -q "$id"; then
        rm -rf "$dir"
      fi
    done
    ```

4.  Vacuum the database:

    ```bash
    sqlite3 ~/.local/share/opencode/opencode.db "VACUUM;"
    ```

---

## Report

Report to the user:

- How many sessions were deleted.
- How many orphaned snapshot / storage dirs were removed.
- The resulting DB size and total opencode data size.
