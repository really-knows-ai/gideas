---
name: special-review
description: Review requested files against provided criteria and produce a flat checklist of issues in REVIEW_ITEMS.md. REVIEW.md stores only the review criteria and a log line per pass. If REVIEW.md exists, the criteria is read from it; if not, this is a fresh review and the user confirms the criteria. Verifies and prunes prior resolved/wont-fix items upfront, captures learnings, then runs a full fresh review with parallel subagents — one per review facet when REVIEW_CRITERIA.md is present. Deduplicates against learnings and prior open items.
---

# Special Review

Review requested files against provided criteria. Produce a flat checklist of
issues at the given output path. Make no severity judgements, no ranking, no
blocker-versus-non-blocker classification — if something diverges from the
provided criteria, it goes in the list.

Every invocation is a **full-rigour fresh review**. Prior resolved (`[x]`) and
wont-fix (`[~]`) items are verified and pruned *before* the fresh review runs,
so the review always starts from a clean base.  Reviewers are provided with
`LEARNINGS.md` so they do not re-flag already-documented patterns.

## ⚠️ Pre-flight: READ THIS FIRST

**Before any tool call**, answer these three questions in your initial response:

1. Does `plans/<project>/REVIEW.md` exist?
   - **If yes** — read the `**Criteria:**` field from it. That is the
     criteria for this review. Proceed to Step 1.
   - **If no** — this is a **fresh review**. There is no criteria on file;
     ask the user to confirm the criteria before proceeding. Do NOT read
     any plan files or code — only read REVIEW_ITEMS.md and LEARNINGS.md
     if they exist.
2. Does the user's request include files, criteria (if fresh), and an output
   path? →
   - Files to review: required. If missing, ask.
   - Criteria: if `REVIEW.md` exists, it comes from there. If not, confirm
     with the user.
   - Output path: implicit. `REVIEW_ITEMS.md` in the project directory is
     the checklist output; `REVIEW.md` holds criteria + pass log. Do not ask
     unless the user references a directory without an existing
     `plans/<project>/` layout.
3. Have you already read any target file (plan files, source code, etc.)? →
   If yes, stop. You have violated the skill. Report the error to the user.

### Golden Rule

**Never read target files yourself.** The subagents read them. Your job is
dispatch, not investigation. If you find yourself reading anything other than
REVIEW.md, REVIEW_ITEMS.md, LEARNINGS.md, REVIEW_CRITERIA.md, or the criteria
document, stop — you are violating the skill.

## Workflow

### 1. Read the review artifacts and determine what needs reviewing

Three files may live in the project directory:

- **`REVIEW.md`** — the review criteria plus a pass log. One line is
  appended per review pass.
- **`REVIEW_ITEMS.md`** — the checklist of findings. This is the output
  of the review and the input to `special-fixer`.
- **`REVIEW_CRITERIA.md`** (optional) — decomposes the criteria into
  numbered **facets**, one `## Facet N — <title>` section each.  If it
  exists, Step 4 dispatches one reviewer subagent per facet instead of
  partitioning by file.

**If `REVIEW.md` exists:** read it. The `**Criteria:**` field gives the
criteria for this review — a statement of what is being reviewed and what
it is reviewed against (it may reference a file path). The `## Pass
Log` section records what each prior pass produced.

**If `REVIEW.md` does not exist:** this is a fresh review. Confirm the
criteria with the user in Step 3.

Read `REVIEW_ITEMS.md` if it exists:

- **If `REVIEW_ITEMS.md` contains `[x]` or `[~]` items** — those claims
  need verifying first. Proceed to Step 2 (verify and prune). Do NOT gather
  criteria or target file lists yet; the verification subagents only need
  the file references in each item, not the full target set.
- **If `REVIEW_ITEMS.md` has only `[ ]` and `[!]` items, or `REVIEW_ITEMS.md`
  does not exist** — there is nothing to verify. Skip to Step 3 (gather
  inputs for fresh review).

Read the companion `LEARNINGS.md` if it exists (same directory as
REVIEW.md, named `LEARNINGS.md`).  LEARNINGS.md contains permanent
pattern learnings and candidate patterns. It does not document SPEC
deviations — those are `[FIX]` items. Candidate patterns are unproven —
they emerged from fresh findings in a prior cycle but have not yet had
items fixed and verified. They do not cause pruning, but fresh reviewers
should be aware of them to avoid re-discovering the same root causes.

Parse the actual checklist entries in REVIEW_ITEMS.md (not the summary
header counts, which may be stale):

- `- [ ]` — open, not yet addressed
- `- [x]` — previously fixed and approved
- `- [~]` — previously marked wont-fix with a justification
- `- [!]` — previously re-opened (a prior review found a resolved or
  wont-fix item was no longer valid)

### 2. Verify and prune existing review (separate phase)

This is a **separate phase** from the fresh review below.  It runs only when
REVIEW_ITEMS.md contains `[x]` or `[~]` items.

**Only the items listed in the file matter** — ignore the summary header
counts.  If the file lists 10 items but the header says 233, you verify
the 10 items that are actually present.

**How to dispatch verification subagents:**
Use `task(subagent_type: "reviewer")` for each item (or small batch of
related items).  The verification subagent does NOT run a full review of
the target files — it reads only the specific file(s) and line(s) referenced
by each item to check whether the claim still holds.  Do NOT read the target
files yourself in this step.

Each verification subagent receives this prompt:

```
Verify whether the following prior review item(s) still hold against the
current code.  This is not a fresh review of the whole file — you are only
checking whether the specific claim(s) below remain valid.

**Target files to read:**
[list of files referenced by the item(s)]

**Prior item(s) to verify:**

1. `[x] <file>:<line> — <description>`
   - Previously marked fixed.  Check whether the divergence described
     is still absent from the current code.
2. `[~] <file>:<line> — <description>`
   - Previously marked wont-fix with justification: <justification>.
     Check whether that justification is still valid given the current
     code state.

**Rules:**
- Read the relevant lines in the target files to determine current state.
- For each item, report one of:
  - **Verified** — the claim still holds (fix is still in place /
    justification is still valid).
  - **Re-opened** — the claim no longer holds, with a specific
    explanation of what changed.
- Be precise: reference the file and line numbers you checked.

**Output format:**
For each verified item:
`VERIFIED <state> <file>:<line> — <description>`

For each re-opened item:
`REOPENED <file>:<line> — <description>
  - Re-opened: <specific reason the claim no longer holds.>`
```

Wait for all verification subagents to complete.

**You may NOT proceed to Step 3 until the following four substeps (2a, 2b-i,
2b-ii, 2c) are all complete.  A reading of the subagent reports is not enough
— you must materially edit REVIEW_ITEMS.md and LEARNINGS.md.**

### 2a. Process results

- Items reported as `VERIFIED` → mark for pruning.
- Items reported as `REOPENED` → change to `- [!]` with the re-opened
  explanation from the subagent.

If any verification subagent reports an unexpected state or ambiguity,
re-read the relevant file manually to resolve.

### 2b-i. Pattern learnings (from verified `[x]` items)

Scan the verified `[x]` items for **recurring patterns** — clusters of 3+
items that share the same root-cause category.  For each cluster, append
a prescriptive learning to `LEARNINGS.md` (create it if it does not exist).

**A pattern learning requires proven fixes.** The `[x]` items are proven:
the finding was raised, a fix was applied, and verification confirmed the
fix is still in place.  This is the reliable signal that the pattern is
real and the fix approach works.  Fresh-review findings (step 4) are
unproven — they may be valid, invalid, or marked wont-fix later.

**How to identify patterns:**
Look for verified items where the *type of divergence* and the *type of fix*
are the same across different files or lines:
- Same class of bug fixed the same way in multiple locations → a rule
  the plan should follow from the start next time
- Same missing documentation/justification added as a `ponytail:` across
  multiple items → a pattern the plan authors aren't following
- Same cross-phase mismatch caught repeatedly → a structural gap in how
  the plan defines handoffs

**For each pattern, append to LEARNINGS.md:**

```markdown
## <Section Name>

- **<Rule title>**: <Concrete, actionable rule that would prevent this class
  of finding.  Use "must", "must not", or standard imperative form.>
```

**Do not** add a learning for a pattern already documented in `LEARNINGS.md`
(check the permanent learnings, Known Deviations, and Candidate Patterns
sections — if a candidate pattern matches, promote it: move the entry from
`## Candidate Patterns` to its appropriate permanent section, dropping the
*(unproven)* tag, and delete the `## Candidate Patterns` section if empty).
**Do not** add a learning for a cluster of fewer than 3 items — it may be
an isolated issue, not a pattern.

**Note:** The verified items are deleted from REVIEW_ITEMS.md in step 2c
below, so perform this scan *before* deleting them.  If you later prune
fresh findings in step 5c as "covered by a learning," consider whether
that learning needs tightening — a learning that matches many findings
per pass is too broad and may need splitting into narrower sub-rules.

### 2b-ii. No learnings from wont-fix items

`[~]` wont-fix items do not produce learnings. SPEC divergences are
never `[~]` — they are `[FIX]` items (fix the code or update the SPEC).
`[~]` items that survive verification are pruned in step 2c; their
justification lives in git history. If a `[~]` item's justification is
found invalid during verification, the item is re-opened to `[!]`.

### 2c. Delete verified items from the review checklist

Remove all verified `[x]` and `[~]` items from REVIEW_ITEMS.md.  Re-opened
`[!]` items and pre-existing `[ ]` items remain.  Their resolution history
is preserved in git history.

Only after all four substeps (2a, 2b-i, 2b-ii, 2c) are done should you proceed to Step 3.

After this step, REVIEW_ITEMS.md contains only `[ ]` and `[!]` items.

### 3. Gather inputs for the full-rigour review

Before the fresh review can run, three things are needed:

1. **Files to review** — a list of file paths (glob patterns accepted).
   The user typically provides these in their message.  If missing, ask.
2. **Criteria** — read from the `**Criteria:**` field of `REVIEW.md` if it
   exists.  If `REVIEW.md` does not exist (fresh review), ask the user to
   confirm the criteria.  It can be a file path (e.g. a spec document),
   inline text, or a description of the review standard.
3. **Output paths** — implicit from the project directory:
   - `plans/<project>/REVIEW_ITEMS.md` — the checklist (written in Step 6).
   - `plans/<project>/REVIEW.md` — criteria + pass log (created in Step 6).
   Do not ask unless the user references a directory with no existing
   `plans/<project>/` layout.

Read the criteria.  If it is a file path, read the file.  If it is inline
text, use it directly.  If it is a description, treat it as the review
standard.

Read `REVIEW_CRITERIA.md` if it exists in the project directory.  It lists
the facets for this review.  For each facet, note its title and its
**Check** / **Closure** fields (and its advisory **Focus** note) — these
become the per-facet reviewer briefs in Step 4.  If it does not exist, Step 4
falls back to partitioning by file.

**Do NOT read the target files here.**  The full-rigour subagents
(Step 4) will read the files they need.  Your job is to partition the
work and dispatch.

If any of the three is missing, ask the user before proceeding.

### 4. Dispatch reviewer subagents — fresh full-rigour review

**If `REVIEW_CRITERIA.md` exists** (preferred), break the review into one
independent unit **per facet**.  Dispatch one `task(subagent_type:
"reviewer")` per facet from Step 3, all in parallel.  Each facet reviews the
**full target file set** (the head of main) against its own Check and Closure
— a facet is never given a pre-limited subset, so a gap in one file cannot
hide because the facet reviewer wasn't told to read it.

**If `REVIEW_CRITERIA.md` does not exist**, fall back to partitioning by
target files or criteria sections: dispatch one subagent per unit, all in
parallel.

**Do NOT read the target files yourself** — the subagents read them.  Your
job is to partition the work and dispatch.  If you already read parts of a
target file (e.g., during verification), the subagents still read the full
file for their complete assessment.

**When building subagent prompts, reference file paths rather than
inlining their contents** — the subagent reviewers have access to every
file in the repository and will read the files themselves.  Do not embed
Criteria document text, LEARNINGS.md entries, or long excerpts inline;
instead, provide the file path and let the subagent read it.  This keeps
prompts concise and avoids stale copies.

The fresh review is **full rigour** — it applies the provided criteria
comprehensively, as if for the first time.  The existence of prior review
passes does not reduce the depth or scope of this review.  Each subagent
receives this prompt:

```
Full-rigour review.  Assess the target files against the review unit below,
as if for the first time.  Produce a flat checklist of issues.  Make no
severity judgements — if something diverges from the unit, it goes in the
list.

**Review unit — read exactly one:**
- Facet review: read `## Facet <N> — <title>` from
  [path to REVIEW_CRITERIA.md] and apply its **Check** and **Closure** fields.
  Follow Closure exactly: it names the finite artifact to enumerate; enumerate
  it completely so nothing is missed.  (This is the norm when
  REVIEW_CRITERIA.md exists.)
- Whole-criteria review: read [path to the criteria document] and apply it as
  a whole.  (Used only when REVIEW_CRITERIA.md does not exist.)

**Target files to review (the full head of main — every file in scope, never
a pre-limited subset):**
[list of file paths]

**Criteria document (read, do not inline):**
[path, e.g. "plans/cartographer/SPEC.md"]

**Prior learnings (read this file, do not re-flag documented patterns):**
[path to LEARNINGS.md]

Candidate patterns in LEARNINGS.md are unproven — do not prune findings
that match them, but note them as context.  The same root cause observed
across multiple fresh findings signals the pattern may be real.

**Rules:**
- No severity labels.  No ranking.  No "blocker" vs "minor."
- If the target does something that contradicts or deviates from the review
  unit, list it as a finding.  Be specific: file paths and line numbers.
- If a requirement of the review unit is not addressed at all by the target
  files, list that as a finding.
- Review only against this unit.  If it is a facet, do not flag divergences
  that belong to a different facet — that facet's reviewer will catch them.
- Do not flag stylistic preferences.  Do not flag patterns that work but
  could be written differently.
- Do not review the criteria itself.  Only review the target files against
  the unit.
- Do not re-flag patterns documented in Prior learnings.
- Classify each finding with one of three tags:
  - `[FIX]` — the text/code is wrong and must be corrected (errors,
    contradictions, broken commands, SPEC violations).
  - `[PONYTAIL]` — a deliberate simplification with a known ceiling that
    needs a `ponytail:` annotation documenting the trade-off and upgrade
    path.  Never for SPEC divergences — any divergence from the criteria is
    `[FIX]`.
  - `[IMPL-NOTE]` — correct but incomplete; needs implementer context at
    coding time.  Only for plans/specs — for source code a gap is `[FIX]` or
    not a finding.
  (A facet's section may further constrain which tags apply.)
- This is a full-rigour pass.  Prior passes do not reduce the depth or scope
  of this review.

**Output format per finding:**
`- [ ] [TAG] <file>:<line> — <description of the divergence.>`

Where `TAG` is one of `FIX`, `PONYTAIL`, or `IMPL-NOTE`.

If you find no divergences, respond with "No findings."
```

Wait for all subagents to complete before proceeding.

### 5. Dispatch consolidation sub-agents (sequential pipeline)

All consolidation work runs in dedicated `reviewer` sub-agents, strictly
sequential — each stage's output feeds the next.  The main agent only
dispatches, forwards inputs/outputs, and writes artifacts.  Do NOT process
findings yourself between sub-agent stages.

**File-based staging.**  Instead of inlining large lists in prompts, each
stage reads from and writes to a working file:
`plans/<project>/REVIEW_WORKING.md`.  The main agent writes this file before
dispatching each subagent and overwrites it with the subagent's full output
after the subagent completes.  The next stage reads the file to get its
input.  Save PRUNE and PRUNE-LEARNING lines from subagent outputs as you
receive them — they feed into the `## Pruned by Impact` section of
REVIEW_ITEMS.md in step 6.  At the end of step 6, remove
`REVIEW_WORKING.md`.

#### 5a. Dedupe + Merge

First, write `REVIEW_WORKING.md` in the project directory containing the
raw findings from all fresh-review subagents (Step 4) plus the pre-existing
`[ ]` and `[!]` items carried forward from Step 2c.  Format it as two
sections:

```markdown
## Carried Items (from REVIEW_ITEMS.md)
[all pre-existing [ ] and [!] items, verbatim]

## Raw Fresh-Review Findings (from Step 4 subagents)
[all findings from every fresh-review subagent, verbatim]
```

Then dispatch ONE reviewer subagent.  The subagent receives:

```
You are the consolidation dedupe-and-merge stage.  Read the input file at
<path/to/REVIEW_WORKING.md>.  It has two sections: "Carried Items" and
"Raw Fresh-Review Findings".  Produce a single numbered master list.

**Task:**

1. **Deduplicate fresh findings across reviewers.**  Compare by file, line,
   and description.  If the same finding appears in multiple reviewer
   outputs, keep one copy — prefer the most specific.  Do not drop a
   genuine finding that only one reviewer reported.

2. **Check fresh findings against carried items.**  The carried items are
   pre-existing `[ ]` and `[!]` items from REVIEW_ITEMS.md.  For each
   fresh finding, check whether it matches a carried item by file, line,
   and description:
   - Matches a carried `[ ]` or `[!]` → keep the existing item and fold
     any additional detail from the fresh finding into it.  Do not create a
     new duplicate.
   - No match → append the fresh finding as a new `- [ ] [TAG] ...` item,
     preserving the classification tag (`[FIX]`, `[PONYTAIL]`, `[IMPL-NOTE]`)
     from the reviewer's output.

3. **Carried-only items remain.**  Pre-existing `[ ]` and `[!]` items that
   are NOT re-discovered by any fresh finding remain as-is — they were
   not addressed and are still open.

4. **Number the master list.**  Assign each item a stable index (1, 2, 3,
   …).  The numbers are a communication convenience for downstream stages;
   they do not appear in the final REVIEW_ITEMS.md.

**Output:**

For each item that absorbed a fresh duplicate:
`FOLDED <#> <carried state + description> — absorbed fresh finding <description of the fresh duplicate>`

For each genuinely new item:
`NEW <#> - [ ] [TAG] <file>:<line> — <description>`

For each carried item not re-discovered:
`CARRIED <#> <carried state + description>`

Then the complete numbered master list:
```
1. [ ]/[!] [TAG] <file>:<line> — <description>
2. [ ] [TAG] <file>:<line> — <description>
...
```
```

Wait for the subagent to complete.  Overwrite `REVIEW_WORKING.md` with the
subagent's full output — it is the input to the next stage.

#### 5b. Impact-tier

Dispatch ONE reviewer subagent.  This is the only stage that makes value
judgements about fix-worthiness; fresh-review reviewers never do this.
The subagent receives:

```
You are the consolidation impact-tiering stage.  Read the numbered master
list from <path/to/REVIEW_WORKING.md> — find the code block at the bottom
containing the numbered items.  Assign a tier to every item and prune
Tier-3 items.

**Tier every item.**  The question is: for each valid finding, is the cost of
fixing it worth the benefit?  This is a judgement about value, not validity.

- **Tier 1 — must fix (always keep).** The fix is load-bearing or a concrete
  defect:
  - A real bug: behaviour is wrong, a CLI/API is broken, a spec-mandated
    error code or check-order is surfaced incorrectly, data can be lost or
    corrupted.
  - A security / trust-boundary / data-loss / accessibility implication.
  - A missing test for a **behaviourally significant, likely-to-regress**
    SPEC branch (e.g. a divergence guard, a conflict resolution path, an
    error-table row that a refactor could silently flip).
  - A finding whose resolution changes observable behaviour.
  Tier 1 items are never pruned.

- **Tier 2 — worth fixing (keep).** A genuine divergence whose fix is
  bounded and low-risk. This covers most ordinary coverage gaps, small
  deletions, and correctness-blocking annotations: the fix pays for its
  churn. Tier 2 items are kept.

- **Tier 3 — should be pruned (prune).** The finding is technically valid
  but the cost of fixing it outweighs the benefit. A "nice-to-have" that
  the repo does not need. Typical Tier 3 forms:
  - A doc/SPEC wording mismatch or layering note with no behaviour change.
  - Re-flagging a non-SPEC trade-off that already carries a `ponytail:` (the finding
    just restates the ponytail). SPEC divergences with a `ponytail:` are still
    divergences — those are Tier 1 or Tier 2, never Tier 3.
  - A speculative concern about a codepath that is provably unreachable.
  - A niche/edge-case test that nobody will run or that protects a branch
    with no plausible regression.
  - A cosmetic/style nit in an untouched file.

**Pruning rules:**
- Tier 3 items MUST be pruned.  For each, give a one-line reason.
- Between Tier 2 and Tier 3, default to prune.  If you cannot defend an
  item's value, it is Tier 3.
- NEVER prune a real bug, ANY security / trust-boundary / data-loss /
  accessibility item, a spec-mandated error code or check-order issued by
  wrong code, or a missing test for a behaviour the criteria explicitly
  names.  Those are Tier 1 regardless of size.

**Output:**

For each kept item:
`KEEP <#> TIER:<1|2> <description>`

For each pruned item:
`PRUNE <#> TIER-3 <description>
  - Reason: <one-line justification>`

Then the pruned numbered list (kept items only, with original numbers):
```
1. TIER:1 <description>
2. TIER:2 <description>
...
```
```

Wait for the subagent to complete.  Save any `PRUNE` lines verbatim (they
feed the `## Pruned by Impact` section in step 6).  Overwrite
`REVIEW_WORKING.md` with the subagent's full output — it is the input to
the next stage.

#### 5c. Learning-prune

Dispatch ONE reviewer subagent.  The subagent reads the impact-pruned list
from `REVIEW_WORKING.md` and prunes items covered by Known Deviations or
observation-type learnings, but never prunes defect-pattern instances.
The subagent receives:

```
You are the consolidation learning-pruning stage.  Read the impact-pruned
list from <path/to/REVIEW_WORKING.md> — find the code block at the bottom
containing the numbered items.  Remove items from the list that are covered
by observation-type learnings in LEARNINGS.md.  Defect-pattern learnings
NEVER prune.

**Pruning rules:**

- **Observation-type learnings** (e.g. "no existing codebase uses mTLS")
  describe current state, not a rule.  A finding that contradicts an
  observation-type learning is pruned — the learning documents that the
  observed behavior is the expected norm.

- **Defect-pattern learnings** (permanent learnings prescribing a fix,
  e.g. "I/O errors must not be silently discarded", "no production code
  duplication") — document a *class of bug that must be fixed*.  A finding
  that matches a defect-pattern learning is a NEW INSTANCE of a known bug
  at a specific location — it is a live bug and MUST BE KEPT.  Pruning a
  defect-pattern instance drops a live bug.

- **Implementation Notes** — document engine constraints, library
  limitations, or design decisions consistent with the criteria. They
  explain *what to skip* when the behavior is correct per the criteria
  despite appearing to diverge. A finding that matches an Implementation
  Note IS pruned.

- **Candidate patterns** do NOT trigger pruning.  `## Candidate Patterns`
  contains unproven patterns from fresh findings; findings matching a
  candidate are kept.

Examples:

| Learning | Finding | Covered? |
|----------|---------|----------|
| Observation: "no existing codebase uses mTLS" | "Plan introduces mTLS" | No — observation, not a rule |
| Defect pattern: "I/O errors must not be silently discarded" | "`_ = CleanUntracked(ctx)` discards error in WipeGraph" | **No — live bug, KEEP** |
| Defect pattern: "no production code duplication" | "`createNodeTable`/`createNodeTableOnConn` near-identical" | **No — live bug, KEEP** |
| Implementation Note: "Export edge enumeration is un-paginated" | "Edge export is un-paginated, entity export is paginated" | Yes — prune |

**When in doubt whether a finding is covered by an Implementation Note, KEEP it**
— removing a live finding is worse than the downstream audit catching a
false positive.

After pruning, note if any learning was matched multiple times.  A learning
that catches many instances per pass is too broad — flag it for tightening.

Also scan for **clusters of 3+ related findings** that share a root-cause
category not yet covered by LEARNINGS.md.  These are *candidate* patterns — they
may become learnings once fixed and verified in a future cycle, but fresh
findings are unproven.  Report them as suggestions.

**LEARNINGS.md:**
[path to LEARNINGS.md]

**Output:**

For each kept item:
`KEEP <#> <description>`

For each pruned-by-learning:
`PRUNE-LEARNING <#> <description>
  - Covered by: <learning title from LEARNINGS.md>`

For each learning that matched 3+ items:
`TIGHTEN <learning title> — matched <N> instances, consider splitting`

For each candidate pattern cluster (3+ related findings):
`LEARNING-SUGGESTION <proposed title> — <pattern observed>
  Rule: <actionable rule using "must" or "must not">`

Then the final keep list (numbered, kept items only):
```
1. <description>
2. <description>
...
```
```

Wait for the subagent to complete.  Save any `PRUNE-LEARNING` and
`LEARNING-SUGGESTION` lines verbatim.  Overwrite `REVIEW_WORKING.md` with
the subagent's full output — it is the input to step 5d and step 6.

#### 5d. Write candidate patterns to LEARNINGS.md

After the learning-prune stage completes, take any `LEARNING-SUGGESTION`
lines from the 5c subagent's output and write them to `LEARNINGS.md`
under a `## Candidate Patterns` section.

**Format each candidate pattern as:**

```markdown
## Candidate Patterns

These patterns emerged from fresh findings but are unproven — the
underlying items haven't been fixed and verified yet.  They do not cause
pruning (findings matching them are kept).  Once 3+ items in a cluster are
fixed `[x]` in a future cycle, the candidate pattern is promoted to a
permanent learning and moved to its appropriate section (see step 2b-i).

- **<Rule title>** *(unproven — <count> findings)*: <Concrete, actionable
  rule that would prevent this class of finding.  Use "must" or "must not".>
```

**Merge with existing candidates:** If `LEARNINGS.md` already has a
`## Candidate Patterns` section, compare existing entries against the new
suggestions:

- If a new suggestion matches an existing candidate → keep the existing
  entry and update its count.
- If a new suggestion is not in the existing list → append it.
- If an existing candidate was NOT re-suggested by the audit → it may have
  been resolved or dissipated.  Remove it from the section.
- If the section becomes empty after cleanup, delete it.

**Do not** write a candidate pattern for a category already covered by a
permanent learning or Known Deviation.

### 6. Write the consolidated review

Two files are written.  Read the final keep list from
`plans/<project>/REVIEW_WORKING.md` — the code block at the bottom of the
5c output.  The checklist goes to `REVIEW_ITEMS.md`; the criteria and pass
log go to `REVIEW.md`.  After both files are written, remove
`REVIEW_WORKING.md`.

**Write the checklist to `plans/<project>/REVIEW_ITEMS.md`.**  Include a
header with the review date and summary counts:

```markdown
# Review Items

**Date:** <today's date>

## Summary

| State | Count |
|-------|-------|
| `[ ]` Open | <count> |
| `[!]` Re-opened | <count> |
| pruned-by-impact (Tier 3) | <count> |

## Pruned by Impact (consolidation decision)

The following items were valid findings but were pruned at consolidation
as Tier-3 — not worth the cost of fixing.  Review and veto if you disagree.

- <file>:<line> — <verbatim description of the pruned item>
  - Pruned by impact: <why it was not worth fixing>

## Open Items

- [ ] [TAG] <file>:<line> — <description>
- [ ] ...
```

Where `TAG` is one of:
- `[FIX]` — text/code is wrong; must be corrected, not just annotated.
- `[PONYTAIL]` — deliberate simplification; needs `ponytail:` documenting the ceiling.
- `[IMPL-NOTE]` — correct but incomplete; needs context for the implementer at coding time (only applies to plan/spec reviews, not source-code reviews).

Do not add commentary, summaries, or recommendations outside the checklist.

**Update `plans/<project>/REVIEW.md`.**  If it does not exist, create it
with the criteria and the first pass log line.  If it exists, preserve the
criteria and append one pass log line:

```markdown
# Special Review

**Criteria:** Review <what is being reviewed — e.g. the head of main (the code)> for compliance with <criteria document or standard>

## Pass Log

- <date> — <pass summary: files reviewed, N findings, N fixed, N pruned>
```

The `**Criteria:**` field must specify both key things: **what is being
reviewed** and **what it is reviewed against** (e.g. "Review the head of
`main` (the code) for compliance with plans/<project>/SPEC.md").

Each pass appends exactly one log line.  The `**Criteria:**` field is set
once at creation and does not change across passes unless the user redefines
the review scope.

### 7. Report to the user

Report:
- Number of `[x]` items verified and pruned (how many were still fixed)
- Number of `[~]` items verified and pruned (how many were still valid)
- Number of `[!]` items re-opened from prior `[x]` or `[~]` claims
- Number of new findings from fresh review
- Number of fresh findings removed because covered by `LEARNINGS.md`
- Number of items pruned by impact at consolidation — and list each one
  verbatim with its reason.  This is a decision the user must be able to
  see and veto.
- Final number of `[ ]` open items
- Number of pre-existing `[ ]` items carried forward (if any)
- Number of pre-existing `[!]` items carried forward (if any)
- Number of learnings added/updated in `LEARNINGS.md`
- Number of candidate patterns written to/bumped in `LEARNINGS.md`
- Number of learnings tightened during consolidation
- Output file paths: `REVIEW_ITEMS.md` (checklist) and `REVIEW.md`
  (criteria + pass log)

## Checklist format rules

Every item follows this structure:

```
- [<state>] [TAG] <location> — <description>
  - <detail line, if needed>
```

- `<state>` is one of ` ` (open), `x` (resolved), `~` (wont-fix), `!` (re-opened).
- `<TAG>` is one of `FIX`, `PONYTAIL`, `IMPL-NOTE` — classifies the kind of resolution needed:
  - `FIX` — the text/code is wrong and must be corrected.
  - `PONYTAIL` — deliberate simplification; add `ponytail:` documenting the ceiling.
  - `IMPL-NOTE` — correct but incomplete; add context for the implementer (only for plan/spec reviews).
- `<location>` is a file path with optional line number (`file.go:42`).
- `<description>` is a single sentence describing the divergence from
  criteria.  No severity words allowed (no "critical," "minor," "blocker,"
  "nice-to-have," etc.).
- Indented sub-lines carry extra detail, findings from a re-review, or
  wont-fix justifications.

## Wont-fix items

When an implementer marks an item as `- [~]`, they must add an indented
justification:

```
- [~] file.go:10 — Uses hand-rolled sort instead of sort.Slice.
  - Wont-fix: the custom sort is required because the comparison is
    stable-across-runs and sort.Slice is not guaranteed stable.
```

The reviewer checks whether the justification is still valid.  If the
justification no longer applies (e.g. the code was refactored and the
hand-rolled sort was replaced by something else entirely), the item is
re-opened.

## Re-opened items

When an `[x]` or `[~]` item is found to no longer be valid, it is changed
to `- [!]` with an explanation:

```
- [!] file.go:10 — Missing error check on os.Create.
  - Re-opened: the error check was present in v1 but was removed in a
    subsequent refactor (line 10 no longer has `if err != nil`).
```

## Boundaries

- This skill only reviews.  It does not fix anything.
- Reviewers are read-only: they never run `make build`, `make check-fix`,
  `make lint-fix`, `make proto`, or raw `go`/`buf` — no tree mutation.
  Running the read-only gate `make verify-check` is permitted to confirm
  the repo is green.
- This skill makes no severity judgements.  Every divergence from criteria
  is listed.  The implementer decides what to fix, what to defer, and what
  to mark wont-fix.
- The reviewer subagents are given the same instruction: no severity labels,
  no ranking, just divergences from criteria.
- Impact-sizing is done by a dedicated consolidation-reviewer subagent
  (step 5b).  The fresh-review reviewers (step 4) always produce the full
  flat list with no rankings.  The consolidation reviewer (5b) weighs
  whether an item is worth fixing and reports every pruned-by-impact item
  verbatim so the user can veto.
- If the user does not provide all three inputs (files, criteria, output
  path), ask for what's missing — do not guess.
- The checklist is always written to `REVIEW_ITEMS.md` in the project
  directory; the criteria and pass log to `REVIEW.md`.  These files may be
  gitignored (under `plans/`) or tracked — the skill does not commit.
- The companion `LEARNINGS.md` is written to the same directory as
  REVIEW.md and REVIEW_ITEMS.md.
