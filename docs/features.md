# aimd Features

Reference documentation for each `aimd` command. Content mirrors the built-in
`aimd <command> --help` output.

Global flags available on every command: `--store` (store path, default
`~/.aimd/store`), `--machine` (machine name override, default system hostname),
`--dry-run` (show what would happen without making changes), and `--verbose`/`-v`.

---

## aimd init

Initialise the aimd store.

Clone an existing aimd store or create a new one at `~/.aimd/store/`.

If `<store-url>` is not provided, you will be prompted to enter a Git remote URL.

For a new (empty) remote, aimd will initialise a local store, scaffold the
registry, make an initial commit, and push to the remote.

```
aimd init [<store-url>]
```

| Flag | Description |
|---|---|
| `--yes`, `-y` | Skip all confirmation prompts |

## aimd track

Start tracking a file or directory in the private store.

Copy a file (or all files in a directory) into the private aimd store, replace
it with a symlink, and hide it from `git status` via `.git/info/exclude`.

Multiple paths may be given. Directories are walked recursively.

```
aimd track <path> [<path>...]
```

## aimd untrack

Stop tracking a file (or all tracked files in a directory) and optionally
restore or delete it.

By default the file is copied back from the store to the project directory, the
symlink is removed, and the overlay is deleted from the store.

A directory argument (such as `.`) is walked recursively: every tracked file
beneath it is untracked, while regular files and untracked symlinks are left
alone. A directory containing no tracked files is a no-op. This mirrors how
`aimd track` accepts a directory, so `aimd untrack .` tidies a whole project in
one shot.

With `--delete`, both the symlink and the overlay are deleted without restoring
file content. Use this flag carefully — content will be lost.

In both modes aimd prints what will happen and requires `--yes` to skip the
interactive confirmation prompt.

```
aimd untrack <path> [<path>...]
```

| Flag | Description |
|---|---|
| `--delete` | Remove symlink and overlay only (no restore); content will be lost |
| `--yes` | Skip confirmation prompt |

## aimd sync

Sync tracked files with the private store.

Sync the private aimd store with the remote origin.

By default, aimd syncs only the current project (detected from the working
directory). Use `--all` to sync all registered projects for this machine.

Sync handles four states:

| State | Behaviour |
|---|---|
| `UP_TO_DATE` | nothing to do, reports "store up to date" |
| `BEHIND` | fast-forward pull from origin |
| `AHEAD` | commits staged overlay changes and pushes |
| `DIVERGED` | rebases local commits on top of origin, then pushes (if clean) |
| `CONFLICT` | prints conflicted files and instructs user to resolve |

```
aimd sync
```

| Flag | Description |
|---|---|
| `--all` | Sync all registered projects |

## aimd restore

Restore tracked files as symlinks in the current project.

Pull the latest store state, then re-create symlinks for every tracked file
that belongs to the current project.

Use `--all` to restore every project already checked out on this machine in one
pass — handy when the symlinks have been cleared locally, instead of visiting
each project directory in turn. Only projects this machine has checked out before
are restored; one registered solely on another machine is skipped, since the
registry has no working-tree path for it here.

On a brand-new machine the store's projects aren't yet associated with this
hostname, so `--all` finds nothing. cd into each project and run a plain
`aimd restore` once to materialise its files and register the machine; `--all`
will pick those projects up afterwards.

By default restore warns and skips any destination that is an existing real
file. Use `--force` to replace real files with store overlays.

```
aimd restore
aimd restore --all
```

| Flag | Description |
|---|---|
| `--all` | Restore every project checked out on this machine, not just the current one |
| `--force` | Replace existing real files with store overlays |

## aimd status

Show the sync state of tracked AI context files.

Inspect tracked files and the store without modifying anything.

By default status reports the current project only. Use `--all` to report every
tracked project as a compact one-line-per-project roster (worst-state glyph plus
file count); add `-v`/`--verbose` to expand it to the full per-file detail.
Use `--all-machines` to also list the other machines tracking each reported
project. The header also shows whether an `aimd watch` is currently running.

status is read-only and offline by default: it compares the store against the
last-fetched `origin/main` without contacting the remote (the sync line says so).
Pass `--fetch` to refresh the remote-tracking ref first.

```
aimd status
aimd status --all       # compact roster
aimd status --all -v    # full per-file detail
```

| Flag | Description |
|---|---|
| `--all` | Report every tracked project (compact roster; `-v` for full detail) |
| `--all-machines` | List the other machines tracking each reported project |
| `--fetch` | Fetch `origin/main` before reporting store sync state |
| `--verbose`, `-v` | Expand the `--all` roster to the full per-file detail |

## aimd watch

Watch tracked files and sync after a quiet period.

Watch tracked AI context files and automatically sync each project to the
private store after a debounce window elapses.

By default watch follows only the current project (detected from the working
directory). Use `--all` to watch every registered project. Each change resets
that project's debounce timer; once the quiet period passes the project is
synced.

Press Ctrl-C to stop — any project with pending changes is synced before exit.

```
aimd watch
```

| Flag | Description |
|---|---|
| `--all` | Watch all registered projects, not just the current one |
| `--debounce` | Quiet period in seconds before syncing after a change (default 300) |

## aimd resolve

Resolve a sync conflict in the private store.

Resolve a rebase conflict left behind by a failed `aimd sync`.

When aimd sync rebases local store commits onto origin and a tracked overlay was
edited on two machines, the rebase stops with conflicts. aimd resolve drives the
resolution to completion and pushes the result.

Pass the conflicted file path exactly as aimd sync printed it (relative to the
store, e.g. `repos/<project-key>/CLAUDE.md`):

```
aimd resolve repos/github.com~org~app/CLAUDE.md
```

By default the file is opened in `$EDITOR` (or `$VISUAL`); after the editor
closes aimd verifies no conflict markers remain, then runs `git rebase
--continue` and pushes. With no editor configured, aimd prints the path and
instructions and you re-run the same command once the markers are gone.

| Flag | Description |
|---|---|
| `--keep-local` | Keep your version of the file — the local commit being replayed (`git checkout --theirs`) |
| `--keep-remote` | Keep the remote version — `origin/main`, which the rebase replays onto (`git checkout --ours`) |
| `--abort` | Abort the rebase and restore the store to its pre-sync state |

## aimd doctor

Diagnose the health of the store and tracked files.

Run a series of read-only health checks and report each one with a clear
✓ / ⚠ / ✗ status plus a suggested fix command for every failure.

Checks performed:

- store remote reachable (`git fetch --dry-run`)
- every tracked symlink resolves to its overlay
- every tracked file has a `.git/info/exclude` entry
- registry and store agree (each tracked file exists in the store)

By default doctor inspects the current project. Use `--all` to check every
tracked project. doctor never modifies anything; it exits non-zero when any
check fails so it can gate scripts and CI.

```
aimd doctor
```

| Flag | Description |
|---|---|
| `--all` | Check every tracked project, not just the current one |

## aimd log

Show the history of store changes for tracked files.

List past store changes — what verb ran, which files it touched, on which
machine, and how long ago.

By default log reports the current project only. Use `--all` to report history
across every registered project, and `--limit` to cap the number of entries.

log is read-only and offline: it reads structured fields from git trailers in
the store's commit history, never from the human-readable subject line.

```
aimd log
```

| Flag | Description |
|---|---|
| `--all` | Show history across all registered projects |
| `--limit` | Maximum number of entries to show, `0` = no limit (default 20) |

## aimd remove

Forget a project entirely — drop it from the store and registry.

Remove a project from aimd: drop its registry entry and delete its overlays
(`repos/<key>/`) and metadata from the store. This never touches the project's
working tree — it only cleans up aimd's own bookkeeping.

Unlike untrack (which is per-file), remove forgets the whole project. With no
argument it targets the current project; pass a project key or display name to
forget a project that is not checked out on this machine.

A project that still has tracked files is refused unless `--force`. remove
prints what it will do and requires `--yes` to skip the confirmation prompt.

```
aimd remove [<project>]
```

| Flag | Description |
|---|---|
| `--force` | Remove even if the project still has tracked files |
| `--yes` | Skip confirmation prompt |

## aimd reset

Restore every tracked file on this machine before uninstalling.

Tear aimd down on this machine: restore every tracked file in every project
checked out here back to a real file (overlay → file, remove the symlink, strip
the `.git/info/exclude` entry), then forget those projects from the registry and
store. It is the all-projects extension of `aimd untrack .`.

Run this immediately before uninstalling aimd. Homebrew's `--zap` only removes
`~/.aimd`; it cannot reach into your project directories, so without reset a zap
would leave broken symlinks and stale exclude entries behind.

reset does **not** push: the store and registry live under `~/.aimd` (which a
subsequent uninstall removes), so the remote and any other machines are left
untouched. Projects not checked out on this machine are skipped — reset them from
the machine where they live. Per-project failures are reported but do not abort
the rest.

reset prints what it will do and requires `--yes` to skip the confirmation prompt.

### Wiping the remote too

With `--remote`, reset also tears down the **shared remote store**: after restoring
this machine's files it removes every project everywhere and replaces the remote's
history with a single empty commit (force-push). This is a full decommission —
every other machine sees an empty store on its next sync.

`--remote` requires typing the remote URL to confirm; `--yes` does **not** bypass
that gate. If the local restore can't finish, reset aborts **before** touching the
remote, so the remote is never destroyed while local teardown is incomplete.

aimd can only restore files on the machine that runs reset. Projects checked out on
**other** machines are removed from the remote but left as broken symlinks there
until each of those machines runs its own `aimd reset` (then re-runs `aimd init`).

```
aimd reset
aimd reset --remote
```

| Flag | Description |
|---|---|
| `--yes` | Skip confirmation prompt (local reset only; ignored for `--remote`) |
| `--remote` | Also wipe the shared remote store and its history (decommission everywhere) |
