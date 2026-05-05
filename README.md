# GitHub Stacked PRs

A GitHub CLI extension for managing stacked branches and pull requests.

Stacked PRs break large changes into a chain of small, reviewable pull requests that build on each other. `gh stack` automates the tedious parts — creating branches, keeping them rebased, setting correct PR base branches, and navigating between layers.

> [!NOTE]
> Stacked PRs is currently in private preview. This CLI and the referenced functionality will not work unless the feature has been enabled for your repository.
> You can sign up for the waitlist at [gh.io/stacksbeta](https://gh.io/stacksbeta).

## Installation

```sh
gh extension install github/gh-stack
```

Requires the [GitHub CLI](https://cli.github.com/) (`gh`) v2.0+.

## AI agent integration

Install the gh-stack skill so your AI coding agent knows how to work with stacked PRs and the `gh stack` CLI:

```sh
gh skill install github/gh-stack
```

## Quick start

```sh
# Start a new stack (creates and checks out the first branch)
gh stack init

# ... make commits on the first branch ...

# Add another branch on top
gh stack add api-endpoints
# ... make commits ...

# Push all branches
gh stack push

# View the stack
gh stack view

# Open a stack of PRs
gh stack submit
```

## How it works

A **stack** is an ordered list of branches where each branch builds on the one below it. The **bottom** of the stack is based on a **trunk** branch (typically `main`).

```
frontend      → PR #3 (base: api-endpoints) ← top
api-endpoints → PR #2 (base: auth-layer)
auth-layer    → PR #1 (base: main)          ← bottom
─────────────
main (trunk)
```

The **bottom** of the stack is the branch closest to the trunk, and the **top** is the branch furthest from it. Each branch inherits from the one below it. Navigation commands (`up`, `down`, `top`, `bottom`) follow this model: `up` moves away from trunk, `down` moves toward it.

When you submit, `gh stack` creates one PR per branch and links them together as a **Stack** on GitHub. Each PR's base is set to the branch below it in the stack, so reviewers see only the diff for that layer.

### Local tracking

Stack metadata is stored in `.git/gh-stack` (a JSON file, not committed to the repo). This tracks which branches belong to which stack and their ordering. Rebase state during interrupted rebases is stored separately in `.git/gh-stack-rebase-state`.

## Commands

### `gh stack init`

Initialize a new stack in the current repository.

```
gh stack init [flags] [branches...]
```

Creates an entry in `.git/gh-stack` to track stack state. In interactive mode (no arguments), prompts you to name branches and offers to use the current branch as the first layer. In interactive mode, you'll also be prompted to set an optional branch prefix (unless adopting existing branches). When a prefix is set, branch names you enter are automatically prefixed. When explicit branch names are given, creates any that don't already exist (branching from the trunk). The trunk defaults to the repository's default branch unless overridden with `--base`.

Use `--numbered` with `--prefix` to enable auto-incrementing numbered branch names (`prefix/01`, `prefix/02`, …). Without `--numbered`, you'll always be prompted to provide a meaningful branch name.

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

# Set a prefix — you'll be prompted for a branch name
gh stack init -p feat
#    → prompts "Enter a name for the first branch (will be prefixed with feat/)"
#    → type "auth" → creates feat/auth

# Use numbered auto-incrementing branch names
gh stack init -p feat --numbered
#    → creates feat/01 automatically
```

### `gh stack add`

Add a new branch on top of the current stack.

```
gh stack add [flags] [branch]
```

Creates a new branch at the current HEAD, adds it to the top of the stack, and checks it out. Must be run while on the topmost branch of a stack. If no branch name is given, prompts for one.

You can optionally stage changes and create a commit as part of the `add` flow. When `-m` is provided without an explicit branch name, the branch name is auto-generated. If the stack was created with `--numbered`, auto-generated names use numbered format (`prefix/01`, `prefix/02`); otherwise, date+slug format is used (e.g., `prefix/2025-03-24-add-login`).

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

# Commit already-staged changes and use an explicit branch name
gh stack add -m "Refactor utils" cleanup-layer
```

### `gh stack checkout`

Check out a stack from a pull request number or branch name.

```
gh stack checkout [<pr-number> | <branch>]
```

When a PR number is provided (e.g. `123`), the command fetches the stack on GitHub, pulls the branches, and sets up the stack locally. If the stack already exists locally and matches, it switches to the branch. If the local and remote stacks have different compositions, you'll be prompted to resolve the conflict.

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

### `gh stack rebase`

Pull from remote and do a cascading rebase across the stack.

```
gh stack rebase [flags] [branch]
```

Fetches the latest changes from `origin`, then ensures each branch in the stack has the tip of the previous layer in its commit history. Rebases branches in order from trunk upward. If a branch's PR has been merged, the rebase automatically switches to `--onto` mode to correctly replay commits on top of the merge target.

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

### `gh stack modify`

Interactively restructure the current stack.

```
gh stack modify [flags]
```

Opens a terminal UI for restructuring a stack. You can rename, drop, reorder, and fold branches into adjacent ones. All the changes are staged during the preview and applied at once on save.

If the stack of PRs has been created on GitHub, run `gh stack submit` afterwards to push the changes and recreate the stack.

| Flag | Description |
|------|-------------|
| `--continue` | Continue after resolving conflicts |
| `--abort` | Abort the modify session and restore the stack to its pre-modify state |

**Operations:**

- **Drop** (`x`): Remove a branch and its commits from the stack. Local branch and associated PR are preserved.
- **Fold down** (`d`): Absorb a branch's commits into the branch below (toward trunk). Folded branch removed from stack.
- **Fold up** (`u`): Absorb a branch's commits into the branch above (away from trunk). Folded branch removed from stack.
- **Reorder** (`Shift+↑`/`Shift+↓`): Move a branch up (away from trunk) or down (toward trunk) in the stack.
- **Rename** (`r`): Rename a branch locally and in the stack metadata.
- **Undo** (`z`): Undo the last staged action.

**Keybindings:**

| Key | Action |
|-----|--------|
| `↑`/`↓` | Navigate branch list |
| `f` | View files changed |
| `c` | View commits |
| `x` | Drop branch |
| `r` | Rename branch |
| `u/d` | Fold branch up/down |
| `Shift+↑`/`Shift+↓` | Move branch up/down |
| `z` | Undo last action |
| `Ctrl+S` | Apply all changes |
| `q`/`Esc` | Cancel and exit |
| `?` | Help |

**Preconditions:**
- Must have an active stack checked out locally
- Working tree must be clean
- No rebase in progress
- No PR in the stack is queued for merge
- Commit history must be linear

**Examples:**

```sh
# Open the modify TUI
gh stack modify

# Continue after resolving a conflict
gh stack modify --continue

# Abort and restore to the previous state
gh stack modify --abort
```

### `gh stack sync`

Fetch, rebase, push, and sync PR state in a single command.

```
gh stack sync [flags]
```

Performs a safe, non-interactive synchronization of the entire stack:

1. **Fetch** — fetches the latest changes from `origin`
2. **Fast-forward trunk** — fast-forwards the trunk branch to match the remote (skips if diverged)
3. **Cascade rebase** — rebases all stack branches onto their updated parents (only if trunk moved). If a conflict is detected, all branches are restored to their original state and you are advised to run `gh stack rebase` to resolve conflicts interactively
4. **Push** — pushes all branches (uses `--force-with-lease` if a rebase occurred)
5. **Sync PRs** — syncs PR state from GitHub and reports the status of each PR

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to fetch from and push to (defaults to auto-detected remote) |

**Examples:**

```sh
gh stack sync
```

### `gh stack push`

Push all branches in the current stack to the remote.

```
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

### `gh stack submit`

Push all branches and create/update PRs and the stack on GitHub.

```
gh stack submit [flags]
```

Creates a Stacked PR for every branch in the stack, pushing branches to the remote.

After creating PRs, `submit` automatically creates a **Stack** on GitHub to link the PRs together. If the stack already exists on GitHub (e.g., from a previous submit), new PRs will be added to the top of the stack.

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

### `gh stack link`

Link PRs into a stack on GitHub without local tracking.

```
gh stack link [flags] <branch-or-pr> <branch-or-pr> [...]
```

Creates or updates a stack on GitHub from branch names or PR numbers. This command does not store or modify any `gh stack` local tracking state. It is designed for users who manage branches with other tools locally (e.g., jj, Sapling, git-town) and want to simply open a stack of PRs.

Arguments are provided in stack order (bottom to top). Branch arguments are automatically pushed to the remote before creating or looking up PRs. For branches that already have open PRs, those PRs are used. For branches without PRs, new PRs are created automatically with the correct base branch chaining. Existing PRs whose base branch doesn't match the expected chain are corrected automatically.

If the PRs are not yet in a stack, a new stack is created. If some of the PRs are already in a stack, the existing stack is updated to include the new PRs. Existing PRs are never removed from a stack — the update is additive only.

| Flag | Description |
|------|-------------|
| `--base <branch>` | Base branch for the bottom of the stack (default: `main`) |
| `--draft` | Create new PRs as drafts |
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

**Examples:**

```sh
# Link branches into a stack (pushes branches, creates PRs, creates stack)
gh stack link feature-auth feature-api feature-ui

# Link existing PRs by number
gh stack link 10 20 30

# Add branches to an existing stack of PRs
gh stack link 42 43 feature-auth feature-ui

# Use a different base branch and create PRs as drafts
gh stack link --base develop --draft feat-a feat-b feat-c
```

### `gh stack view`

View the current stack.

```
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

### `gh stack unstack`

Remove a stack from local tracking and delete it on GitHub. Also available as `gh stack delete`.

```
gh stack unstack [flags]
```

You must have an active stack checkout out locally. The command targets the active stack — the one that contains the currently checked out branch.

Deletes the stack on GitHub first, if it exists, then removes local tracking. Use `--local` to only remove from local tracking.

| Flag | Description |
|------|-------------|
| `--local` | Only delete the stack locally (keep it on GitHub) |

**Examples:**

```sh
# Remove the stack from local tracking and GitHub
gh stack unstack

# Only remove local tracking
gh stack unstack --local
```

### `gh stack merge`

Merge a stack of PRs.

```
gh stack merge <pr>
```

Merges the specified PR and all PRs below it in the stack.

> **Note:** This command is not yet implemented. Running it prints a notice.

### Navigation

Move between branches in the current stack without having to remember branch names.

```sh
gh stack up [n]      # Move up n branches (default 1)
gh stack down [n]    # Move down n branches (default 1)
gh stack top         # Jump to the top of the stack
gh stack bottom      # Jump to the bottom of the stack
gh stack switch      # Interactively pick a branch to switch to
```

Navigation commands clamp to the bounds of the stack — moving up from the top or down from the bottom is a no-op with a message. If you're on the trunk branch, `up` moves to the first stack branch.

**Examples:**

```sh
gh stack up          # move up one layer
gh stack up 3        # move up three layers
gh stack down
gh stack top
gh stack bottom
gh stack switch      # shows an interactive picker
```

### `gh stack feedback`

Share feedback about gh-stack.

```
gh stack feedback [title]
```

Opens a GitHub Discussion in the [gh-stack repository](https://github.com/github/gh-stack) to submit feedback. Optionally provide a title for the discussion post.

**Examples:**

```sh
gh stack feedback
gh stack feedback "Support for reordering branches"
```

### `gh stack alias`

Create a short command alias so you can type less.

```
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
gh stack alias gst --remove
```

## Typical workflow

```sh
# 1. Start a stack (creates and checks out the first branch)
gh stack init

# 2. Work on the first layer
#    ... write code, make commits ...

# 3. Add the next layer
gh stack add api-routes
#    ... write code, make commits ...

# 4. Push everything and create Stacked PRs
gh stack submit

# 5. Reviewer requests changes on the first PR
gh stack bottom
#    ... make changes, commit ...

# 6. Rebase the rest of the stack on top of your fix
gh stack rebase

# 7. Push the updated branches
gh stack push

# 8. When the first PR is merged, sync the stack
gh stack sync
```

## Abbreviated workflow

If you want to minimize keystrokes, use a branch prefix with `--numbered` and the `-Am` flags to fold staging, committing, and branch creation into a single command. Branch names are auto-generated as `prefix/01`, `prefix/02`, etc.

When a branch has no commits yet (e.g., right after `init`), `add -Am` stages and commits directly on that branch instead of creating a new one. Once a branch has commits, `add -Am` creates a new branch, checks it out, and commits there.

```sh
# 1. Start a stack with a prefix and numbered branches
gh stack init -p feat --numbered
#    → creates feat/01 and checks it out

# 2. Write code for the first layer
#    ... write code ...

# 3. Stage and commit on the current branch
gh stack add -Am "Auth middleware"
#    → feat/01 has no commits yet, so the commit lands here
#      (no new branch is created)

# 4. Write code for the next layer
#    ... write code ...

# 5. Create the next branch and commit
gh stack add -Am "API routes"
#    → feat/01 already has commits, so a new branch feat/02 is
#      created, checked out, and the commit lands there

# 6. Keep going
#    ... write code ...

gh stack add -Am "Frontend components"
#    → feat/02 already has commits, creates feat/03 and commits there

# 7. Push everything and create PRs
gh stack submit
```

Compared to the typical workflow, there's no need to name branches, run `git add`, or run `git commit` separately. Each `gh stack add -Am "..."` does it all.

## Exit codes

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

## License 

This project is licensed under the terms of the MIT open source license. Please refer to the [LICENSE](LICENSE) file for the full terms.

## Maintainers 

See [CODEOWNERS](CODEOWNERS)

## Support

See [SUPPORT.md](SUPPORT.md)

Please note that the Stacked PRs feature is currently in private preview and **gh-stack** will not work without that feature enabled.
