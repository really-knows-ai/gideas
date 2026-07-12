# flowctl Release Process

This document describes how to build, release, and distribute `flowctl` via
GoReleaser and Homebrew.

## Quick Start: Cut a Release

1. Ensure the working tree is clean (`git status` shows no uncommitted changes)
   and all tests pass (`cd tools && go test ./flowctl/...`).

2. Tag the release:
   ```bash
   git tag flowctl/v1.0.0
   git push origin flowctl/v1.0.0
   ```

3. The GitHub Actions workflow `.github/workflows/release-flowctl.yml` triggers
   automatically. Watch the run at:
   `https://github.com/gideas/platform/actions/workflows/release-flowctl.yml`

4. After the workflow succeeds (usually 3-5 minutes):
   - A GitHub Release is created at
     `https://github.com/gideas/platform/releases/tag/flowctl/v1.0.0`
     with archives for darwin/amd64, darwin/arm64, linux/amd64, linux/arm64,
     and a `checksums.txt` file.
   - The Homebrew formula at `gideas/homebrew-tap` is updated automatically
     to point to the new release assets.

5. Users can install with:
   ```bash
   brew tap gideas/tap
   brew install flowctl
   ```

## How GoReleaser Builds and Publishes

### The pipeline (`.goreleaser.yaml`)

When `goreleaser release --clean` runs, it:

1. **Prepares**: Runs `go mod tidy` and `go vet ./...` inside `tools/flowctl/`.
2. **Builds**: Cross-compiles `flowctl` for four platform/arch combinations:
   - darwin/amd64
   - darwin/arm64
   - linux/amd64
   - linux/arm64
   Each binary is stripped (`-s -w`) and has version metadata injected via
   `ldflags`.
3. **Archives**: Each binary is packaged into a `.tar.gz` archive. The archive
   contains only the binary (no README, no license file — the formula carries
   metadata). Archive naming:
   - `flowctl_1.0.0_macOS_x86_64.tar.gz`
   - `flowctl_1.0.0_macOS_arm64.tar.gz`
   - `flowctl_1.0.0_linux_x86_64.tar.gz`
   - `flowctl_1.0.0_linux_arm64.tar.gz`
4. **Checksums**: Generates `checksums.txt` with SHA-256 hashes of all archives.
5. **Publishes Release**: Creates a GitHub Release on `gideas/platform` with
   all archives and the checksum file. The release body includes an auto-generated
   changelog from conventional commits.
6. **Updates Homebrew**: Generates `Formula/flowctl.rb` and pushes it to the
   `gideas/homebrew-tap` repository. The formula contains the correct archive
   URL, SHA-256 hash, version, and install instructions.

### Version resolution

GoReleaser determines the version from the git tag. The tag format is
`flowctl/v<semver>`. The `v` prefix and `flowctl/` scope are stripped by
GoReleaser, producing version `1.0.0` from tag `flowctl/v1.0.0`.

### Snapshot builds

For testing the pipeline locally without pushing a tag:

```bash
cd tools/flowctl
goreleaser release --snapshot --clean
```

This produces builds in `tools/flowctl/dist/` with a `-SNAPSHOT-<commit>`
version suffix and does not publish anything. Use this to verify the
configuration before committing.

## How the Homebrew Formula Is Updated

GoReleaser's `brews` section in `.goreleaser.yaml` handles formula generation
and push automatically:

1. **Formula generation**: GoReleaser creates `Formula/flowctl.rb` with:
   - `version` set to the release semver.
   - `url` pointing to the correct archive on GitHub Releases.
   - `sha256` matching the archive's checksum.
   - `homepage`, `description`, `license` from the config.
   - `install` method: `bin.install "flowctl"`.
   - `test` method: `system "#{bin}/flowctl", "--help"`.

2. **Push**: GoReleaser clones `gideas/homebrew-tap`, writes the formula file
   to `Formula/flowctl.rb`, commits with author `foundry-bot <bot@gideas.io>`,
   and pushes to the `main` branch using the provided token.

3. **No manual formula updates**: Every release automatically updates the
   formula. The old formula is overwritten — there is no version-pinning in
   the tap repo (Homebrew handles version switching).

### If the formula push fails

- Check that the GitHub App is installed on `gideas/homebrew-tap` with
  `contents: write` permission.
- Verify the `HOMEBREW_TAP_TOKEN` in the workflow has not expired (it is
  generated fresh each run, so expiry is not an issue — but the underlying
  App installation must be valid).
- Re-run the workflow from the GitHub Actions UI. GoReleaser skips the release
  creation if the tag already has a Release, but re-runs the formula push.

## Testing the Formula Locally Before Release

To test that the formula installs correctly before cutting a release:

1. Build a snapshot:
   ```bash
   cd tools/flowctl
   goreleaser release --snapshot --clean
   ```

2. Upload the desired archive to a temporary location (or use a local file
   URL).

3. Create a local formula for testing:
   ```bash
   brew create --set-name flowctl <url-or-path-to-archive>
   ```

4. Install from the local formula:
   ```bash
   brew install --build-from-source Formula/flowctl.rb
   ```

5. Verify the binary works:
   ```bash
   flowctl --help
   ```

6. Remove the test formula:
   ```bash
   brew uninstall flowctl
   brew untap gideas/tap  # if you tapped during testing
   ```

## GitHub App Setup

The release pipeline uses a GitHub App for cross-repo authentication. This
avoids Personal Access Tokens (which expire) and shared secrets.

### 1. Create the GitHub App

1. Go to `https://github.com/settings/apps/new` (GitHub organization or
   personal account settings — use `gideas` organization).
2. **GitHub App name**: `gideas-release-bot` (or any descriptive name).
3. **Homepage URL**: `https://github.com/gideas/platform`.
4. **Webhook**: Deselect "Active" — webhooks are not needed.
5. **Permissions**:
   - `Contents: Read & write` (for creating releases and pushing formula updates).
   - `Metadata: Read-only` (required for all apps).
6. **Where can this app be installed?**: "Any account" (or restrict to `gideas`
   organization).
7. Click "Create GitHub App".

### 2. Generate a Private Key

1. On the app settings page, scroll to "Private keys".
2. Click "Generate a private key".
3. Download the `.pem` file. This is the `RELEASE_APP_PRIVATE_KEY` secret.
   **Store it securely** — it cannot be downloaded again.

### 3. Install the App on Both Repositories

1. Go to the app's "Install App" section (left sidebar).
2. Click "Install" next to the `gideas` organization.
3. Select **Only select repositories**.
4. Choose **`platform`** and **`homebrew-tap`**.
5. Click "Install".
6. Note the **installation ID** from the URL after installation
   (`https://github.com/settings/installations/<INSTALLATION_ID>`). This is
   not needed as a secret — `actions/create-github-app-token` derives it
   automatically.

### 4. Configure GitHub Actions Secrets

In `https://github.com/gideas/platform/settings/secrets/actions`:

| Secret | Value |
|--------|-------|
| `RELEASE_APP_ID` | The App ID (numeric, from the app settings page) |
| `RELEASE_APP_PRIVATE_KEY` | The full contents of the downloaded `.pem` file |

These are the only secrets needed. No PATs, no token rotation, no expiry dates.

### 5. Verify the Setup

Create a test tag and push it (or trigger the workflow manually with
`workflow_dispatch`). If the workflow fails with a 401 or 403:

- Verify the App is installed on both repositories.
- Verify the App has `Contents: write` permission.
- Verify `RELEASE_APP_ID` and `RELEASE_APP_PRIVATE_KEY` are set correctly.
- Check that the App's private key PEM starts with `-----BEGIN RSA PRIVATE KEY-----`.

## Version Variables in main.go

For `ldflags` injection to work, `main.go` must declare the version variables:

```go
package main

var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)
```

If these variables are missing, GoReleaser will still build successfully, but
`flowctl --version` will show `dev` instead of the release version. Add the
`version` subcommand or flag at your convenience — the variables are optional.

## The `gideas/homebrew-tap` Repository

This is a separate GitHub repository (`gideas/homebrew-tap`) that serves as the
Homebrew tap. Its structure is minimal:

```
homebrew-tap/
├── Formula/
│   └── flowctl.rb        # Generated by GoReleaser on each release
├── README.md             # Manual: explains how to use the tap
└── .gitignore            # Standard Go/Homebrew ignores
```

The `README.md` should contain:

```markdown
# gideas Homebrew Tap

Formulas for Foundry Flow tooling.

## Usage

```bash
brew tap gideas/tap
brew install flowctl
```
```

The `Formula/flowctl.rb` file is fully managed by GoReleaser — never edit it
manually. If you need to test formula changes, use the local testing procedure
described above.

## Troubleshooting

| Problem | Likely cause | Fix |
|---------|-------------|-----|
| Homebrew formula not updated | Token lacks `homebrew-tap` scope | Verify GitHub App is installed on `gideas/homebrew-tap` |
| `brew install flowctl` downloads wrong architecture | Homebrew selects the wrong archive | Update formula: `brew upgrade flowctl` or `brew reinstall flowctl` |
| Release created but no formula push | GoReleaser's `brew` section misconfigured | Check `.goreleaser.yaml` `brews.repository.name` and `token` |
| `actions/create-github-app-token` fails with 401 | Private key or App ID incorrect | Re-download private key, verify App ID |
| `go vet` fails in `before.hooks` | Code quality issue in `flowctl/` | Fix the vet warning and re-tag |
| `go test` fails in workflow but passes locally | Environment mismatch (Go version, platform) | Check Go version in `setup-go` matches local version |
| Formula refers to non-existent release asset | Release was deleted or tag moved | Ensure release exists before re-running formula push |
| Snapshot archive names differ from release archives | Snapshot uses `-SNAPSHOT-<commit>` suffix | Expected — snapshot builds are not published |
