---
title: CLI Commands
description: Complete reference for all gh stack commands.
---

## Installation

```sh
gh extension install github/gh-stack
```

Requires the [GitHub CLI](https://cli.github.com/) (`gh`) v2.0+.

:::note[Authentication]
The `gh stack` CLI uses your GitHub CLI authentication — run `gh auth login` if you haven't already.
:::

---

## Stack Management

### `gh stack init`

Initialize a new stack in the current repository.

```sh
gh stack init [flags] [branches...]
```

| Flag | Description |
|------|-------------|
| `-b, --base <branch>` | Trunk branch for the stack (defaults to the repository's default branch) |

Initializes a new stack locally. In interactive mode (no arguments), prompts for a branch name and offers to use the current branch as the first layer.

When explicit branch names are given, existing branches are adopted automatically and any missing branches are created. The trunk defaults to the repository's default branch unless overridden with `--base`.

Enables `git rerere` automatically so that conflict resolutions are remembered across rebases.

**Examples:**

```sh
# Interactive — prompts for branch names
gh stack init

# Non-interactive — specify first branch upfront
gh stack init feature-auth

# Use a different trunk branch
gh stack init --base develop feature-auth

# Adopt or create multiple branches at once
gh stack init feature-auth feature-api feature-ui
```

### `gh stack add`

Add a new branch on top of the current stack.

```sh
gh stack add [flags] [branch]
```

| Flag | Description |
|------|-------------|
| `-A, --all` | Stage all changes (including untracked files); requires `-m` |
| `-u, --update` | Stage changes to tracked files only; requires `-m` |
| `-m, --message <string>` | Create a commit with this message before creating the branch |

> **Note:** `-A` and `-u` are mutually exclusive.

Creates a new branch at the current HEAD, adds it to the top of the stack, and checks it out. Must be run while on the topmost branch of a stack. If no branch name is given, prompts for one.

You can optionally stage changes and create a commit as part of the `add` flow. When `-m` is provided without an explicit branch name, the branch name is auto-generated in date+slug format (e.g., `03-24-add_login`).

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

| Flag | Description |
|------|-------------|
| `-s, --short` | Compact output (branch names only) |
| `--json` | Output stack data as JSON |

Shows all branches in the stack, their ordering, PR links, and the most recent commit with a relative timestamp. Output is piped through a pager (respects `GIT_PAGER`, `PAGER`, or defaults to `less -R`).

**Examples:**

```sh
gh stack view
gh stack view --short
gh stack view --json
```

### `gh stack checkout`

Check out a stack by its stack number, a pull request number, a PR URL, or a branch name.

```sh
gh stack checkout [<stack-number> | <pr-number> | <pr-url> | <branch>]
```

A bare number is interpreted first as a stack or PR number (repo-scoped identifiers shown in the GitHub UI). If nothing matches the number, it is tried as a branch name.

When a remote stack is referenced, the command fetches the stack on GitHub, pulls the branches, and sets up the stack locally. If the stack already exists locally and matches, it switches to the branch. If the local and remote stacks have different compositions, you'll be prompted to resolve the conflict.

When a branch name is provided, the command resolves it against locally tracked stacks only.

When run without arguments in an interactive terminal, first checks whether the current branch is untracked locally but belongs to exactly one active stack on GitHub and offers to check it out. If there is no unique match or you decline, it opens a searchable picker listing every stack available to you — both the stacks tracked locally and the stacks that exist only on GitHub. Each row shows the stack number, its bottom and top branch, base branch, a status bar summarizing how many of its pull requests are merged, open, closed, or not yet pushed, and whether the stack is available locally or only on the remote. Filter with the All / Local / Remote tabs or type `/` to search; fully merged stacks are omitted. Selecting a remote-only stack clones it locally before switching to it.

**Examples:**

```sh
# Check out a stack by its stack number
gh stack checkout 7

# Check out a stack by PR number
gh stack checkout 42

# Check out a stack by PR URL
gh stack checkout https://github.com/owner/repo/pull/42

# Check out a stack by branch name (local only)
gh stack checkout feature-auth

# Interactive — pick from all available stacks (local and remote)
gh stack checkout
```

### `gh stack modify`

Interactively restructure the current stack.

```sh
gh stack modify [flags]
```

| Flag | Description |
|------|-------------|
| `--continue` | Continue after resolving conflicts |
| `--abort` | Abort the modify session and restore the stack to its pre-modify state |

Opens an interactive terminal UI for restructuring a stack. All changes are staged in the TUI and applied together when you press `Ctrl+S`. Branches from merged PRs cannot be modified.

**Preconditions:**

The command checks these conditions before opening the TUI:

1. Must have an active stack checked out locally
2. Working tree must be clean (no uncommitted changes)
3. No rebase in progress
4. No PR in the stack is queued for merge
5. Commit history must be linear (no merge commits, no diverged branches)

**Operations:**

| Operation | Key | Effect |
|-----------|-----|--------|
| Drop | `x` | Remove branch and its commits from stack. Local branch and associated PR are preserved. |
| Fold down | `d` | Absorb commits into branch below (toward trunk). Folded branch removed from stack. |
| Fold up | `u` | Absorb commits into branch above (away from trunk). Folded branch removed from stack. |
| Insert below | `i` | Insert a new empty branch below the cursor (toward trunk). |
| Insert above | `I` | Insert a new empty branch above the cursor (away from trunk). |
| Move down | `Shift+↓` | Reorder branch down (toward trunk) in the stack |
| Move up | `Shift+↑` | Reorder branch up (away from trunk) in the stack |
| Rename | `r` | Rename the branch (opens inline prompt) |
| Undo | `z` | Undo the last staged action |

**Apply phase:**

When you press `Ctrl+S`, the staged changes are applied by renaming branches, inserting new branches, folding/dropping branches, and running a cascading rebase to create a linear commit history with the desired stack state.

If a rebase conflict occurs, you can:
- Resolve conflicts, stage files, and run `gh stack modify --continue`
- Or run `gh stack modify --abort` to abort the operation and restore the stack to the pre-modify state

**After modifying:**

If a stack of PRs has been created on GitHub, run `gh stack submit` to push the updated branches and recreate the stack. The old stack is automatically replaced.

**Examples:**

```sh
# Open the interactive modify TUI
gh stack modify

# Continue after resolving a conflict
gh stack modify --continue

# Abort and restore to the previous state
gh stack modify --abort
```

### `gh stack unstack`

Remove a stack from local tracking and unstack it on GitHub. Also available as `gh stack delete`.

```sh
gh stack unstack [<stack-number>] [flags]
```

| Flag | Description |
|------|-------------|
| `--local` | Only remove the stack locally (keep it on GitHub) |

With no argument, the command targets the active stack — the one that contains the currently checked out branch — unstacking it on GitHub and removing local tracking.

Provide a stack number (the identifier shown in the github.com stack UI) to unstack a specific stack on GitHub. This works from anywhere in the repository, whether or not the stack is checked out locally — the stack is unstacked directly through the GitHub API. When the stack is also available locally, its local tracking is removed as well.

PRs that are merged, merging, or queued for merge cannot be removed from a stack on GitHub and are left part of the stack. When every pull request is removed, the stack is dissolved and any local tracking is removed; when some pull requests remain stacked, the stack is kept and local tracking, if any, is unchanged. Use `--local` to skip the remote operation and only remove local tracking.

This is useful when you need to restructure a stack — remove a branch, insert a branch, reorder branches, rename branches, or make other large changes. After unstacking, use `gh stack init` to re-create the stack with the desired structure — existing branches are adopted automatically.

**Examples:**

```sh
# Unstack the current stack on GitHub and remove local tracking
gh stack unstack

# Unstack a specific stack by its number
gh stack unstack 7

# Only remove local tracking
gh stack unstack --local
```

---

## Remote Operations

### `gh stack submit`

Push all branches and create/update PRs and the stack on GitHub.

```sh
gh stack submit [flags]
```

| Flag | Description |
|------|-------------|
| `--auto` | Skip the editor and use auto-generated PR titles |
| `--open` | Create new PRs as ready for review instead of drafts, and mark existing PRs as ready for review |
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

Creates a Stacked PR for every branch in the stack, pushing branches to the remote. After creating PRs, `submit` automatically creates a **Stack** on GitHub to link the PRs together. If the stack already exists on GitHub (e.g., from a previous submit), new PRs are added to the existing stack.

If every PR in the stack has already been merged, that stack is complete and can't be extended. In that case `submit` automatically starts a **new** stack rooted at the trunk for your unmerged branches and creates it on GitHub, leaving the merged stack untouched.

In an interactive terminal, `submit` opens a full-screen editor on a single screen:

- **Left panel** — every branch without a PR is **included by default**; deselect any you don't want to submit with <kbd>Ctrl</kbd>+<kbd>X</kbd>. Because each PR builds on the branch below it, deselecting a branch also deselects the ones stacked above it, and re-including a branch re-includes the ones below it that it depends on. Branches that already have a PR (open, draft, queued, or merged) are shown for context but are locked; edit those on the web.
- **Right panel** — for the focused branch, draft the title, description (pre-filled from your repo's PR template or commits, with a Glamour markdown preview and `$EDITOR` escape), and whether it opens ready for review or as a draft (a ready ↔ draft toggle). Focusing a locked branch shows a read-only card with a link to its PR (<kbd>o</kbd> to open in the browser).

Press <kbd>Ctrl</kbd>+<kbd>S</kbd> to submit all included PRs at once. The editor supports both keyboard and mouse input. Pass `--auto` (or run in a non-interactive terminal, such as CI) to skip the editor and use auto-generated titles.

If the branches already have open PRs but no stack exists on GitHub, you will have the option to link the PRs into a stack with <kbd>Ctrl</kbd>+<kbd>B</kbd>.

In the editor, new PRs default to **ready for review**; flip any PR to **draft** with the ready ↔ draft toggle. With `--auto`, new PRs are created as **drafts** unless you pass `--open`.

**Examples:**

```sh
gh stack submit
gh stack submit --auto
gh stack submit --open
```

### `gh stack sync`

Fetch, rebase, push, and sync PR state in a single command.

```sh
gh stack sync [flags]
```

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to fetch from and push to (defaults to auto-detected remote) |
| `--prune` | Delete local branches for merged PRs |

Performs a synchronization of the entire stack:

1. **Fetch** — fetches the latest changes from `origin`.
2. **Reconcile the remote stack** — mirrors the GitHub stack locally. When PRs have been added to the stack on GitHub (the remote is ahead of your local stack), their branches are pulled down and appended to your local stack automatically. When the local and remote stacks have genuinely diverged (for example, you added a branch locally while different PRs were added to the stack on GitHub), you are prompted to resolve (see **Diverged stacks** below). In a non-interactive terminal a divergence aborts the sync (nothing is pushed or updated).
3. **Fast-forward trunk** — fast-forwards the trunk branch to match the remote (skips if diverged).
4. **Cascade rebase** — rebases all stack branches onto their updated parents (only if trunk moved). If a conflict is detected, all branches are restored to their original state, and you are advised to run `gh stack rebase` to resolve conflicts interactively.
5. **Push** — pushes all branches (uses `--force-with-lease` if a rebase occurred).
6. **Sync PRs** — syncs PR state from GitHub and reports the status of each PR.
7. **Sync the stack** — links the stack's open PRs into a stack on GitHub, creating the remote stack object if it doesn't exist yet or updating it if it's partially formed. This only happens when two or more PRs exist; sync never opens PRs (use `gh stack submit` for that).
8. **Prune** — in interactive terminals, prompts to delete local branches for merged PRs. Use `--prune` to prune automatically.

A clean remote-ahead update (PRs added on top of your local stack) is pulled down automatically without prompting, so `sync` is safe to run in automation. Sync only prompts when the stacks have truly diverged.

**Diverged stacks**

When neither stack is a clean prefix of the other — for example, you added a branch locally while separate PRs were added to the same stack on GitHub — sync cannot merge the two automatically. In an interactive terminal it offers three choices:

- **Use the remote stack as the source of truth** — replaces your local stack composition with the remote's, pulling any missing branches. If you were on a branch that the remote stack no longer contains, you're moved to the nearest surviving branch. Requires a clean working state with no uncommitted changes.
- **Delete the stack on GitHub** — deletes the stack object on GitHub and stops the sync. Your PRs and local branches are untouched (only the stack on GitHub is removed); recreate the stack with `gh stack submit` (run `gh stack modify` first if you want to change its structure). This is the way to make GitHub match your local stack, because `submit` — unlike `sync` — also creates PRs for any branches you haven't submitted yet.
- **Cancel** — aborts the sync without pushing branches or updating any PRs.

In a non-interactive terminal, a divergence aborts the sync (exit success) without pushing branches or updating PRs; resolve it by unstacking and recreating the stack.

**Examples:**

```sh
gh stack sync

# Sync and automatically prune merged branches
gh stack sync --prune
```

### `gh stack rebase`

Pull from remote and do a cascading rebase across the stack.

```sh
gh stack rebase [flags] [branch]
```

| Flag | Description |
|------|-------------|
| `--downstack` | Only rebase branches from trunk to the current branch |
| `--upstack` | Only rebase branches from the current branch to the top |
| `--no-trunk` | Skip trunk — only rebase stack branches onto each other (no fetch, no trunk rebase) |
| `--continue` | Continue the rebase after resolving conflicts |
| `--abort` | Abort the rebase and restore all branches to their pre-rebase state |
| `--remote <name>` | Remote to fetch from (defaults to auto-detected remote) |
| `--committer-date-is-author-date` | Set the committer date to the author date during rebase. Alias: `--preserve-dates` |

| Argument | Description |
|----------|-------------|
| `[branch]` | Target branch (defaults to the current branch) |

Fetches the latest changes from `origin`, then ensures each branch in the stack has the tip of the previous layer in its commit history. Rebases branches in order from trunk upward.

If a branch's PR has been merged, the rebase automatically switches to `--onto` mode to correctly replay commits on top of the merge target.

If a rebase conflict occurs, the operation pauses and prints the conflicted files with line numbers. Resolve the conflicts, stage with `git add`, and continue with `--continue`. To undo the entire rebase, use `--abort` to restore all branches to their pre-rebase state.

**Examples:**

```sh
# Rebase the entire stack
gh stack rebase

# Only rebase branches below the current one
gh stack rebase --downstack

# Only rebase branches above the current one
gh stack rebase --upstack

# Rebase stack branches without pulling from or rebasing with trunk
gh stack rebase --no-trunk

# After resolving a conflict
gh stack rebase --continue

# Abort rebase and restore everything
gh stack rebase --abort

# Rebase and preserve committer date as author date
gh stack rebase --committer-date-is-author-date
```

### `gh stack push`

Push active branches in the current stack to the remote.

```sh
gh stack push [flags]
```

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

Pushes every active branch (excluding merged and queued branches) in one `git push` using explicit per-branch `--force-with-lease` checks. The update is not atomic: branches whose leases pass may update even if another branch is rejected. Fix the rejected branch and rerun the command; branches already updated will be unchanged. This command does not create or update pull requests — use `gh stack submit` for that.

**Examples:**

```sh
gh stack push
gh stack push --remote upstream
```

### `gh stack link`

Link PRs into a stack on GitHub without local tracking.

```sh
gh stack link [flags] <stack-number | branch-or-pr> <branch-or-pr> [...]
```

| Flag | Description |
|------|-------------|
| `--base <branch>` | Base branch for the bottom of the stack (defaults to the repository's default branch); ignored when adding to an existing stack |
| `--open` | Mark new and existing PRs as ready for review |
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

Creates or updates a stack on GitHub from branch names or PR numbers/URLs. This command does not create or modify any `gh-stack` local tracking state. It is designed for users who manage branches with other tools locally (e.g., jj, Sapling, git-town) and want to simply open a stack of PRs.

Arguments are provided in stack order (bottom to top). Branch arguments are automatically pushed to the remote before creating or looking up PRs. For branches that already have open PRs, those PRs are used. For branches without PRs, new PRs are created automatically with the correct base branch chaining. Existing PRs whose base branch doesn't match the expected chain are corrected automatically.

If the PRs are not yet in a stack, a new stack is created. If some of the PRs are already in a stack, the existing stack is updated to include the new PRs. Existing PRs are never removed from a stack — the update is additive only.

To grow an existing stack without re-listing its PRs, pass a stack number (the number shown in the GitHub stack UI) as the first argument. The remaining arguments are appended to the top of that stack. Arguments already in the stack are skipped, and arguments that belong to a different stack are rejected. Because stack and PR numbers never overlap, a numeric first argument is treated as a stack only when it matches an existing stack — otherwise it is treated as a PR or branch.

**Examples:**

```sh
# Link branches into a stack (pushes, creates PRs, creates stack)
gh stack link feature-auth feature-api feature-ui

# Link existing PRs by number
gh stack link 10 20 30

# Link existing PRs by URL
gh stack link https://github.com/owner/repo/pull/10 https://github.com/owner/repo/pull/20

# Add branches to an existing stack of PRs
gh stack link 42 43 feature-auth feature-ui

# Append to the top of an existing stack by its stack number (no need to
# re-list the PRs already in stack 7)
gh stack link 7 48 feature-ui

# Use a different base branch and mark PRs as ready for review
gh stack link --base develop --open feat-a feat-b feat-c
```

---

### `gh stack merge`

Merge one or multiple stacked PRs at once.

```sh
gh stack merge [<stack-number> | <pr-number>]
```

All members of the stack up to and including your chosen pull request are merged into the base branch in a single, all-or-nothing operation: if any PR can't be merged, none are.

With no argument, the current active local stack is used. Pass a stack number to merge a stack you don't have checked out (a purely remote operation), or a pull request number to merge directly up to that PR.

In an interactive terminal, a short wizard walks you through choosing which PRs to merge, picking the merge method, and confirming. In a non-interactive terminal, or with `--yes`, the whole stack (or everything up to the given PR) is merged without prompting, using your last-used merge method unless one is specified.

Only basic pull request state is checked before merging (open and not a draft); GitHub evaluates branch protection and repository rules when the merge runs, so any such failure is reported back to you. **Bypassing merge requirements is not supported** for stacked PR merges.

If the base branch uses a merge queue, the stack is added to the queue instead of merging directly. The queue chooses the merge method, so the wizard skips the method step and any `--merge-method` (or `--squash`/`--rebase`/`--merge`) flag is ignored with a warning. The selected pull requests are added to the queue together but merge as the queue processes them — they may land in separate groups rather than all at once.

Under the hood, this command uses the asynchronous [Merge API](/gh-stack/reference/merge-api/).

| Flag | Description |
|------|-------------|
| `--merge-method <method>` | Merge method to use: `merge`, `squash`, or `rebase` |
| `--merge` / `--squash` / `--rebase` | Shorthands for the corresponding merge method |
| `-y, --yes` | Merge without prompting for confirmation |

**Examples:**

```sh
# Merge the current stack (interactive picker)
gh stack merge

# Merge a stack you don't have checked out, by stack number
gh stack merge 7

# Merge everything up to and including PR #42
gh stack merge 42

# Merge the whole current stack without prompting, squashing
gh stack merge --yes --squash
```

---

## Navigation

Move between branches in the current stack without having to remember branch names. The **bottom** of the stack is the branch closest to the trunk, and the **top** is furthest from it. `up` moves away from trunk; `down` moves toward it.

All navigation commands clamp to the bounds of the stack — moving up from the top or down from the bottom is a no-op with a message.

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

### `gh stack trunk`

Jump to the trunk branch.

```sh
gh stack trunk
```

Checks out the trunk branch of the current stack (e.g., `main`). You must be on a branch that is part of a stack.

---

## Utilities

### `gh stack alias`

Create a short command alias so you can type less.

```sh
gh stack alias [flags] [name]
```

| Flag | Description |
|------|-------------|
| `--remove` | Remove a previously created alias |

Installs a small wrapper script into `~/.local/bin/` that forwards all arguments to `gh stack`. The default alias name is `gs`, but you can choose any name by passing it as an argument. After setup, you can run `gs push` instead of `gh stack push`.

On Windows, automatic alias creation is not supported — the command prints manual instructions for creating a batch file or PowerShell function.

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

## Environment Variables

| Variable | Values | Description |
|----------|--------|-------------|
| `GH_STACK_THEME` | `auto` (default), `light`, `dark` | Controls the color palette of the interactive screens (`submit`, `modify`, `view`) and all colored command output. Colors adapt to your terminal background automatically; set this to force the light or dark palette when a terminal doesn't report its background (some SSH or `tmux` setups). |

```sh
# Force the light palette for one command
GH_STACK_THEME=light gh stack view
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
| 10 | Modify session interrupted (recovery required) |
