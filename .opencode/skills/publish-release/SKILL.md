---
name: publish-release
description: Use when creating and publishing a new GitHub release with an auto-generated changelog and README review.
---

# Publish Release

Create a tagged release, generate a changelog, review the README, and publish to GitHub via the `gh` CLI. The workflow commits loose changes, runs the quality gate, builds, generates a `CHANGELOG.md`, reviews the `README.md`, tags, pushes, and creates a GitHub release.

## Workflow

### 1. Check for uncommitted changes

Run `git status --porcelain`. If there are uncommitted changes, ask the user whether to commit them. If they agree, stage all changes and create a commit with message `chore: checkpoint before release`.

If there are staged but uncommitted changes, ask whether to commit them with the same message.

### 2. Run the quality gate

Run the full quality gate:

```
go test ./... && make check-fix
```

If it fails, report the failure and stop — do not proceed to the release.

### 3. Build all binaries

Confirm the project compiles cleanly:

```
make build
```

If the build fails, report the failure and stop.

### 4. Determine the version

Ask the user for the new version string (e.g. `0.1.0`, `0.2.0`). Present a suggested version by looking at existing tags:

```
git tag --sort=-v:refname | head -5
```

If no tags exist, suggest `0.1.0`. Otherwise suggest a patch bump of the latest tag.

### 5. Generate CHANGELOG.md

Identify the previous release tag. Find the most recent tag matching `v*` with `git tag --sort=-v:refname | head -1`. If no tag exists, use the first commit as the range start (or collect all commits).

Collect commit messages between the previous tag and HEAD:

```
git log <prev-tag>..HEAD --oneline
```

If `CHANGELOG.md` does not exist, create it with a `# Changelog` heading. If it exists, update it.

Build a new `## [<version>] - <date>` section. Categorise commits by conventional-commit type:

| Prefix | Section |
|--------|---------|
| `feat:` / `feature:` | **Added** |
| `fix:` | **Fixed** |
| `chore:` / `refactor:` | **Changed** |
| `docs:` | **Changed** |
| `test:` | **Changed** |
| `remove:` | **Removed** |
| `break:` | **Breaking** |

For each category, list the commit messages as bullet points, stripping the commit hash and conventional-commit prefix. Skip merge commits (`Merge branch ...`).

Insert the new section at the top of `CHANGELOG.md`, immediately after the `# Changelog` heading. Follow consistent section formatting.

### 6. Review README.md

Read `README.md` in full. Do a high-level review:

- Are the prerequisites up to date?
- Are the getting-started steps still accurate?
- Does the repository structure table match the current directory layout?
- Are any headings or commands stale?
- Does the version badge or version reference (if any) need updating?

Report any issues found. Ask the user whether to fix them now or defer. If fixing, make the edits and stage `README.md`.

### 7. Commit the release

Stage `CHANGELOG.md` and `README.md` (if changed). Commit with message:

```
release: v<version>
```

### 8. Tag the release

Create a lightweight tag:

```
git tag v<version>
```

### 9. Push the commit and tag

```
git push origin HEAD
git push origin v<version>
```

If push fails (e.g. no upstream branch), ask the user how to proceed.

### 10. Create the GitHub release

Use the `gh` CLI to create the release. Build the release notes from the changelog section just created. Extract the `## [<version>]` section from `CHANGELOG.md` for the body:

```
gh release create v<version> --title "v<version>" --notes "<changelog-section>"
```

If a one-time auth code is needed, prompt the user.

Confirm the release was created by checking the exit code and the URL in the output.

### 11. Report the result

Report:
- The new version number
- The tag pushed
- The GitHub release URL
- The changelog categories and commit count
- Any README issues found (and whether they were fixed)

## Optional: Attach Binaries

If the user wants to attach build artefacts to the release, rebuild with platform-specific targets (if available in the Makefile) and upload:

```
gh release upload v<version> <file> [...]
```

Only do this if the user explicitly requests it.

## Common Mistakes

- **Skipping the quality gate.** The gate must pass before any release work begins.
- **Using an old changelog format.** Match the existing `CHANGELOG.md` style if one exists.
- **Forgetting to push the tag.** Tags must be pushed explicitly.
- **Missing `gh` CLI.** If `gh` is not installed or authenticated, stop and tell the user to set it up.
- **Committing unrelated changes.** Only `CHANGELOG.md` and `README.md` (if reviewed) should be in the release commit.
