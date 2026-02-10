---
name: spec-review
description: Continue the spec review process — reads AGENTS.md for context and REVIEW.md for current status, then walks through outstanding issues with the user
---

## Workflow

1. **Read `AGENTS.md`** at the repository root. This contains the project context, key decisions, writing principles, and spec structure. Internalise it fully before proceeding.

2. **Read `REVIEW.md`** at the repository root. This tracks the review of completed spec documents against AGENTS.md key decisions, writing principles, and cross-document consistency.

3. **Identify outstanding issues.** Any issue in REVIEW.md that is not marked as ~~struck through~~ or tagged RESOLVED is still open.

4. **Walk through each outstanding issue with the user**, one at a time:
   - Present the issue clearly: what the problem is, which file(s) and line(s) are affected, and what the suggested fix is.
   - Read the affected file(s) to understand the current state of the text.
   - Discuss the fix with the user. They may agree, disagree, or want to modify the approach.
   - Once agreed, make the edit.
   - Update REVIEW.md to mark the issue as resolved (use strikethrough on the heading and add a RESOLVED tag, following the pattern of already-resolved issues).

5. **After all issues are addressed**, summarise what was changed and update REVIEW.md's summary table.

## Rules

- Never skip an issue without explicit user agreement.
- Follow the writing principles in AGENTS.md when making edits.
- Preserve cross-document consistency — if fixing one document requires a corresponding change in another, flag it and make both changes.
- Do not modify AGENTS.md key decisions unless the user explicitly asks.
