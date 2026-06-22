<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/aimd-logo-dark.png">
    <img src="docs/assets/aimd-logo.png" alt="aimd" width="320">
  </picture>
</p>

<p align="center">
  <a href="https://github.com/CyberSecAuto-Labs/aimd/actions/workflows/ci.yml"><img src="https://github.com/CyberSecAuto-Labs/aimd/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/CyberSecAuto-Labs/aimd/releases/latest"><img src="https://img.shields.io/github/v/release/CyberSecAuto-Labs/aimd?sort=semver" alt="Latest release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/CyberSecAuto-Labs/aimd" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License"></a>
</p>

Keep your private AI context files (`CLAUDE.md`, `AGENTS.md`) out of shared repos, versioned and synced through your own Git store.

## Quick example

```bash
# Track a file — moves it to your private store, symlinks it back
aimd track CLAUDE.md

# On another machine — cd into a project and restore its symlinks
aimd restore

# Keep the store in sync
aimd sync
```

The tracked file stays available in your project directory (via symlink), is hidden from `git status` (via `.git/info/exclude`), and is versioned and synced through your own private Git remote.

## Why aimd

AI coding tools like Claude, Cursor, and GitHub Copilot read project-specific context files (`CLAUDE.md`, `.cursor/rules.md`, etc.) to understand your codebase. These files are valuable — but they often contain knowledge you can't or don't want to commit to a shared repository: client-specific notes, personal workflow preferences, or proprietary architectural context.

**aimd** lets you track those files in a private Git store and sync them across machines — without ever committing them to the project repository.

## Install

Install via Homebrew, a Linux package, or from source.

**Homebrew (macOS / Linux):**

```bash
brew install CyberSecAuto-Labs/tap/aimd
```

**Linux packages (Debian/Ubuntu `.deb`, Fedora/RHEL `.rpm`):**

Run the install script — it detects your architecture and package manager, downloads the
matching `.deb`/`.rpm` from the latest release, and installs it:

```bash
curl -fsSL https://raw.githubusercontent.com/CyberSecAuto-Labs/aimd/main/scripts/install.sh | sh
```

The packages install the `aimd` binary plus bash, zsh, and fish completions. Prebuilt
`tar.gz` archives, a `checksums.txt`, and per-archive CycloneDX SBOMs are also attached to
every release.

> **macOS note:** the binary is not yet signed/notarized with an Apple Developer ID, so
> Gatekeeper may block it with an "Apple could not verify 'aimd' is free of malware"
> prompt. Homebrew installs clear this automatically. If you download a `tar.gz` archive
> directly, strip the quarantine attribute after extracting:
>
> ```bash
> xattr -dr com.apple.quarantine ./aimd
> ```

**From source:**

Clone the repository, then build and install with the Go toolchain (Go 1.23+):

```bash
git clone https://github.com/CyberSecAuto-Labs/aimd.git
cd aimd
go install .
```

`go install` places the binary in `$GOBIN` (defaults to `~/go/bin` — make sure it's on
your `PATH`). To install the latest tagged version without cloning:

```bash
go install github.com/CyberSecAuto-Labs/aimd@latest
```

Shell completions are available via the built-in command, e.g. `aimd completion zsh`.

## Quickstart

Set up a store once, then track and sync files from any machine.

```bash
# 1. Initialise your private store (clones an existing remote, or creates a new one)
aimd init git@github.com:you/aimd-store.git

# 2. In a project, start tracking its AI context file
cd ~/code/my-project
aimd track CLAUDE.md          # moved to the store, symlinked back, hidden from git status

# 3. Push your changes to the store remote
aimd sync
```

On a second machine, after cloning the same project:

```bash
aimd init git@github.com:you/aimd-store.git   # once per machine
cd ~/code/my-project
aimd restore                  # re-creates the CLAUDE.md symlink from the store
```

Edit `CLAUDE.md` as usual on either machine; `aimd sync` (or `aimd watch` for automatic
syncing) keeps the store and every machine in step.

## How it works

1. `aimd track <file>` copies the file to a private Git store (`~/.aimd/store/`), creates a symlink in its place, and adds it to `.git/info/exclude` so it never appears in `git status`.
2. `aimd sync` commits changes and pushes to your private remote — or pulls and rebases if the remote has newer changes.
3. `aimd restore` recreates the symlink on any machine after a fresh clone.

No cloud dependency beyond a standard Git remote (GitHub, GitLab, Gitea, or self-hosted).

<p align="center">
  <img src="docs/assets/architecture.svg" alt="Architecture" width="300" />
</p>

## Commands

See [docs/features.md](docs/features.md) for full per-command documentation.

| Command | What it does |
|---|---|
| [`aimd init`](docs/features.md#aimd-init) | Clone or create your private context store |
| [`aimd track`](docs/features.md#aimd-track) | Move a file into the store and symlink it back |
| [`aimd untrack`](docs/features.md#aimd-untrack) | Stop tracking a file or directory, restoring or deleting it |
| [`aimd sync`](docs/features.md#aimd-sync) | Push, pull, or rebase overlay changes with the remote |
| [`aimd restore`](docs/features.md#aimd-restore) | Re-create tracked symlinks after a fresh clone |
| [`aimd status`](docs/features.md#aimd-status) | Show the sync state of tracked files |
| [`aimd watch`](docs/features.md#aimd-watch) | Auto-sync each project after a quiet period |
| [`aimd resolve`](docs/features.md#aimd-resolve) | Resolve a sync conflict in the store |
| [`aimd doctor`](docs/features.md#aimd-doctor) | Run read-only health checks on store and links |
| [`aimd log`](docs/features.md#aimd-log) | Show the history of store changes |
| [`aimd remove`](docs/features.md#aimd-remove) | Forget a project entirely from store and registry |
| [`aimd reset`](docs/features.md#aimd-reset) | Restore every tracked file on this machine before uninstalling |

## FAQ

**Will aimd pollute my project's `git status` or commit anything to the shared repo?**
No. Tracked files are added to `.git/info/exclude` (a local, uncommitted ignore list), so
they never show up in `git status` and are never committed to the project repo. The real
content lives in your separate private store.

**Do I need a special server?**
No. The store is just a Git repository. Any standard remote works — GitHub, GitLab, Gitea,
or a self-hosted box you can `git push` to.

**What happens to my file when I track it?**
The original is copied into the store and replaced in place with a symlink, so every tool
that reads it keeps working. `aimd untrack` reverses this (restoring is the default), putting
the real file back in your project.

**Can I edit the same file on two machines?**
Yes. `aimd sync` rebases divergent changes; if the same lines conflict, sync stops safely
and `aimd resolve` walks you through fixing it.

## License

[Apache 2.0](LICENSE)
