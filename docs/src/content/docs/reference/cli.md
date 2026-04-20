---
title: CLI Commands
description: Complete reference for all gh stack commands.
---

## Installation

```sh
gh extension install github/gh-stack
```

Requires the [GitHub CLI](https://cli.github.com/) (`gh`) v2.0+.

---

## Stack Management

### `gh stack init`

Initialize a new stack in the current repository.

```sh
gh stack init [flags] [branches...]
```

Creates an entry in `.git/gh-stack` to track stack state. In interactive mode (no arguments), prompts you to name branches and offers to use the current branch as the first layer. You'll also be prompted to set an optional branch prefix. When a prefix is set, branch names you enter are automatically prefixed.

When explicit branch names are given, creates any that don't already exist (branching from the trunk). The trunk defaults to the repository's default branch unless overridden with `--base`.

Use `--numbered` with `--prefix` to enable auto-incrementing branch names (`prefix/01`, `prefix/02`, …).

Enables `git rerere` automatically so that conflict resolutions are remembered across rebases.

| Flag | Description |
|------|-------------|
| `-b, --base <branch>` | Trunk branch for the stack (defaults to the repository's default branch) |
| `-a, --adopt` | Adopt existing branches into a stack instead of creating new ones |
| `-p, --prefix <string>` | Set a branch name prefix for the stack |
| `-n, --numbered` | Use auto-incrementing numbered branch names (requires `--prefix`) |

**Examples:**

```sh
# Interactive — prompts for branch names
gh stack init

# Non-interactive — specify branches upfront
gh stack init feature-auth feature-api feature-ui

# Use a different trunk branch
gh stack init --base develop feature-auth

# Adopt existing branches into a stack
gh stack init --adopt feature-auth feature-api

# Set a prefix — prompts for a branch name suffix
gh stack init -p feat
#    → type "auth" → creates feat/auth

# Use numbered auto-incrementing branch names
gh stack init -p feat --numbered
#    → creates feat/01 automatically
```

### `gh stack add`

Add a new branch on top of the current stack.

```sh
gh stack add [flags] [branch]
```

Creates a new branch at the current HEAD, adds it to the top of the stack, and checks it out. Must be run while on the topmost branch of a stack. If no branch name is given, prompts for one.

You can optionally stage changes and create a commit as part of the `add` flow. When `-m` is provided without an explicit branch name, the branch name is auto-generated. If the stack was created with `--numbered`, auto-generated names use numbered format (`prefix/01`, `prefix/02`); otherwise, date+slug format is used.

| Flag | Description |
|------|-------------|
| `-A, --all` | Stage all changes (including untracked files); requires `-m` |
| `-u, --update` | Stage changes to tracked files only; requires `-m` |
| `-m, --message <string>` | Create a commit with this message before creating the branch |

> **Note:** `-A` and `-u` are mutually exclusive.

**Examples:**

```sh
# Create a branch by name
gh stack add api-routes

# Prompt for a branch name interactively
gh stack add

# Stage all changes, commit, and auto-generate the branch name
gh stack add -Am "Add login endpoint"

# Stage only tracked files, commit, and auto-generate the branch name
gh stack add -um "Fix auth bug"

# Commit already-staged changes and auto-generate the branch name
gh stack add -m "Add user model"

# Stage all changes, commit, and use an explicit branch name
gh stack add -Am "Add tests" test-layer

# Stage only tracked files, commit, and use an explicit branch name
gh stack add -um "Update docs" docs-layer
```

### `gh stack view`

View the current stack.

```sh
gh stack view [flags]
```

Shows all branches in the stack, their ordering, PR links, and the most recent commit with a relative timestamp. Output is piped through a pager (respects `GIT_PAGER`, `PAGER`, or defaults to `less -R`).

| Flag | Description |
|------|-------------|
| `-s, --short` | Compact output (branch names only) |
| `--json` | Output stack data as JSON |

**Examples:**

```sh
gh stack view
gh stack view --short
gh stack view --json
```

### `gh stack checkout`

Check out a stack from a pull request number or branch name.

```sh
gh stack checkout [<pr-number> | <branch>]
```

When a PR number is provided (e.g., `123`), the command fetches the stack on GitHub, pulls the branches, and sets up the stack locally. If the stack already exists locally and matches, it switches to the branch. If the local and remote stacks have different compositions, you'll be prompted to resolve the conflict.

When a branch name is provided, the command resolves it against locally tracked stacks only.

When run without arguments in an interactive terminal, shows a menu of all locally available stacks to choose from.

**Examples:**

```sh
# Check out a stack by PR number
gh stack checkout 42

# Check out a stack by branch name (local only)
gh stack checkout feature-auth

# Interactive — select from locally tracked stacks
gh stack checkout
```

---

## Remote Operations

### `gh stack submit`

Push all branches and create/update PRs and the stack on GitHub.

```sh
gh stack submit [flags]
```

Creates a Stacked PR for every branch in the stack, pushing branches to the remote. After creating PRs, `submit` automatically creates a **Stack** on GitHub to link the PRs together. If the stack already exists on GitHub (e.g., from a previous submit), new PRs are added to the existing stack.

When creating new PRs, you will be prompted to enter a title for each one. Press Enter to accept the default (branch name), or use `--auto` to skip prompting entirely.

| Flag | Description |
|------|-------------|
| `--auto` | Use auto-generated PR titles without prompting |
| `--draft` | Create new PRs as drafts |
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

**Examples:**

```sh
gh stack submit
gh stack submit --auto
gh stack submit --draft
```

### `gh stack sync`

Fetch, rebase, push, and sync PR state in a single command.

```sh
gh stack sync [flags]
```

Performs a safe, non-interactive synchronization of the entire stack:

1. **Fetch** — fetches the latest changes from `origin`.
2. **Fast-forward trunk** — fast-forwards the trunk branch to match the remote (skips if diverged).
3. **Cascade rebase** — rebases all stack branches onto their updated parents (only if trunk moved). If a conflict is detected, all branches are restored to their original state, and you are advised to run `gh stack rebase` to resolve conflicts interactively.
4. **Push** — pushes all branches (uses `--force-with-lease` if a rebase occurred).
5. **Sync PRs** — syncs PR state from GitHub and reports the status of each PR.

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to fetch from and push to (defaults to auto-detected remote) |

**Examples:**

```sh
gh stack sync
```

### `gh stack rebase`

Pull from remote and do a cascading rebase across the stack.

```sh
gh stack rebase [flags] [branch]
```

Fetches the latest changes from `origin`, then ensures each branch in the stack has the tip of the previous layer in its commit history. Rebases branches in order from trunk upward.

If a branch's PR has been merged, the rebase automatically switches to `--onto` mode to correctly replay commits on top of the merge target.

If a rebase conflict occurs, the operation pauses and prints the conflicted files with line numbers. Resolve the conflicts, stage with `git add`, and continue with `--continue`. To undo the entire rebase, use `--abort` to restore all branches to their pre-rebase state.

| Flag | Description |
|------|-------------|
| `--downstack` | Only rebase branches from trunk to the current branch |
| `--upstack` | Only rebase branches from the current branch to the top |
| `--continue` | Continue the rebase after resolving conflicts |
| `--abort` | Abort the rebase and restore all branches to their pre-rebase state |
| `--remote <name>` | Remote to fetch from (defaults to auto-detected remote) |

| Argument | Description |
|----------|-------------|
| `[branch]` | Target branch (defaults to the current branch) |

**Examples:**

```sh
# Rebase the entire stack
gh stack rebase

# Only rebase branches below the current one
gh stack rebase --downstack

# Only rebase branches above the current one
gh stack rebase --upstack

# After resolving a conflict
gh stack rebase --continue

# Abort rebase and restore everything
gh stack rebase --abort
```

### `gh stack push`

Push all branches in the current stack to the remote.

```sh
gh stack push [flags]
```

Pushes every branch to the remote using `--force-with-lease --atomic`. This is a lightweight wrapper around `git push` that knows about all branches in the stack. It does not create or update pull requests — use `gh stack submit` for that.

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

**Examples:**

```sh
gh stack push
gh stack push --remote upstream
```

### `gh stack unstack`

Remove a stack from local tracking and delete it on GitHub. Also available as `gh stack delete`.

```sh
gh stack unstack [flags] [branch]
```

Deletes the stack on GitHub first, then removes it from local tracking. If the remote deletion fails, the local state is left untouched so you can retry. Use `--local` to skip the remote deletion and only remove local tracking.

This is useful when you need to restructure a stack — remove a branch, reorder branches, rename branches, or make other large changes. After unstacking, use `gh stack init --adopt` to re-create the stack with the desired structure.

| Flag | Description |
|------|-------------|
| `--local` | Only delete the stack locally (keep it on GitHub) |

| Argument | Description |
|----------|-------------|
| `[branch]` | A branch in the stack to identify which stack to delete (defaults to the current branch) |

**Examples:**

```sh
# Delete the stack on GitHub and remove local tracking
gh stack unstack

# Only remove local tracking
gh stack unstack --local

# Specify a branch to identify which stack
gh stack unstack feature-auth
```

### `gh stack link`

Link PRs into a stack on GitHub without local tracking.

```sh
gh stack link [flags] <branch-or-pr> <branch-or-pr> [...]
```

Creates or updates a stack on GitHub from branch names or PR numbers. This command does not create or modify any `gh-stack` local tracking state. It is designed for users who manage branches with other tools locally (e.g., jj, Sapling, git-town) and want to simply open a stack of PRs.

Arguments are provided in stack order (bottom to top). Branch arguments are automatically pushed to the remote before creating or looking up PRs. For branches that already have open PRs, those PRs are used. For branches without PRs, new PRs are created automatically with the correct base branch chaining. Existing PRs whose base branch doesn't match the expected chain are corrected automatically.

If the PRs are not yet in a stack, a new stack is created. If some of the PRs are already in a stack, the existing stack is updated to include the new PRs. Existing PRs are never removed from a stack — the update is additive only.

| Flag | Description |
|------|-------------|
| `--base <branch>` | Base branch for the bottom of the stack (default: `main`) |
| `--draft` | Create new PRs as drafts |
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

**Examples:**

```sh
# Link branches into a stack (pushes, creates PRs, creates stack)
gh stack link feature-auth feature-api feature-ui

# Link existing PRs by number
gh stack link 10 20 30

# Add branches to an existing stack of PRs
gh stack link 42 43 feature-auth feature-ui

# Use a different base branch and create PRs as drafts
gh stack link --base develop --draft feat-a feat-b feat-c
```

---

## Navigation

Move between branches in the current stack without having to remember branch names. The **bottom** of the stack is the branch closest to the trunk, and the **top** is furthest from it. `up` moves away from trunk; `down` moves toward it.

All navigation commands clamp to the bounds of the stack — moving up from the top or down from the bottom is a no-op with a message.

### `gh stack up`

Move up toward the top of the stack (away from trunk).

```sh
gh stack up [n]
```

Moves up `n` branches (default 1). If you're on the trunk branch, `up` moves to the first stack branch.

**Examples:**

```sh
gh stack up          # move up one layer
gh stack up 3        # move up three layers
```

### `gh stack down`

Move down toward the bottom of the stack (toward trunk).

```sh
gh stack down [n]
```

Moves down `n` branches (default 1).

**Examples:**

```sh
gh stack down        # move down one layer
gh stack down 2      # move down two layers
```

### `gh stack top`

Jump to the top of the stack.

```sh
gh stack top
```

Checks out the branch furthest from the trunk.

### `gh stack bottom`

Jump to the bottom of the stack.

```sh
gh stack bottom
```

Checks out the branch closest to the trunk.

### `gh stack switch`

Interactively switch to another branch in the stack.

```sh
gh stack switch
```

Shows an interactive picker listing all branches in the current stack, ordered from top (furthest from trunk) to bottom (closest to trunk) with their position number. Select a branch to check it out.

Requires an interactive terminal.

**Examples:**

```sh
gh stack switch
#    → Select a branch in the stack to switch to
#      5. frontend
#      4. api-endpoints
#      3. auth-layer
#      2. db-schema
#      1. config-setup
```

---

## Utilities

### `gh stack alias`

Create a short command alias so you can type less.

```sh
gh stack alias [flags] [name]
```

Installs a small wrapper script into `~/.local/bin/` that forwards all arguments to `gh stack`. The default alias name is `gs`, but you can choose any name by passing it as an argument. After setup, you can run `gs push` instead of `gh stack push`.

On Windows, automatic alias creation is not supported — the command prints manual instructions for creating a batch file or PowerShell function.

| Flag | Description |
|------|-------------|
| `--remove` | Remove a previously created alias |

**Examples:**

```sh
# Create the default alias (gs)
gh stack alias
#    → now "gs push", "gs view", etc. all work

# Create a custom alias
gh stack alias gst

# Remove an alias
gh stack alias --remove
gh stack alias --remove gst
```

### `gh stack feedback`

Share feedback about gh-stack.

```sh
gh stack feedback [title]
```

Opens a GitHub Discussion in the [gh-stack repository](https://github.com/github/gh-stack) to submit feedback. Optionally provide a title for the discussion post.

**Examples:**

```sh
gh stack feedback
gh stack feedback "Support for reordering branches"
```

---

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic error |
| 2 | Not in a stack / stack not found |
| 3 | Rebase conflict |
| 4 | GitHub API failure |
| 5 | Invalid arguments or flags |
| 6 | Disambiguation required (branch belongs to multiple stacks) |
| 7 | Rebase already in progress |
| 8 | Stack is locked by another process |
| 9 | Stacked PRs not enabled for this repository |
