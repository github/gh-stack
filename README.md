# gh-stack

A GitHub CLI extension for managing stacked branches and pull requests.

Stacked PRs break large changes into a chain of small, reviewable pull requests that build on each other. `gh stack` automates the tedious parts — creating branches, keeping them rebased, setting correct PR base branches, and navigating between layers.

## Installation

```sh
gh extension install github/gh-stack
```

Requires the [GitHub CLI](https://cli.github.com/) (`gh`) v2.0+.

## Quick start

```sh
# Start a new stack from the default branch
gh stack init

# Create the first branch and start working
gh stack add auth-layer
# ... make commits ...

# Add another branch on top
gh stack add api-endpoints
# ... make commits ...

# Push all branches and create/update PRs
gh stack push

# View the stack
gh stack view
```

## How it works

A **stack** is an ordered list of branches where each branch builds on the one below it. The bottom of the stack is based on a **trunk** branch (typically `main`).

```
main (trunk)
 └── auth-layer        → PR #1 (base: main)
      └── api-endpoints → PR #2 (base: auth-layer)
```

When you push, `gh stack` creates one PR per branch. Each PR's base is set to the branch below it in the stack (**branch-chaining**), so reviewers see only the diff for that layer.

### Local tracking

Stack metadata is stored in `.git/gh-stack` (a JSON file, not committed to the repo). This tracks which branches belong to which stack and their ordering. Rebase state during interrupted rebases is stored separately in `.git/gh-stack-rebase-state`.

## Commands

### `gh stack init`

Initialize a new stack in the current repository.

```
gh stack init [branches...] [flags]
```

Creates an entry in `.git/gh-stack` to track stack state. In interactive mode (no arguments), prompts you to name branches and offers to use the current branch as the first layer. When explicit branch names are given, creates any that don't already exist (branching from the trunk). The trunk defaults to the repository's default branch unless overridden with `--base`.

Enables `git rerere` automatically so that conflict resolutions are remembered across rebases.

| Flag | Description |
|------|-------------|
| `-b, --base <branch>` | Trunk branch for the stack (defaults to the repository's default branch) |
| `-a, --adopt` | Adopt existing branches into a stack instead of creating new ones |

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
```

### `gh stack add`

Add a new branch on top of the current stack.

```
gh stack add [branch]
```

Creates a new branch at the current HEAD, adds it to the top of the stack, and checks it out. Must be run while on the topmost branch of a stack. If no branch name is given, prompts for one.

**Examples:**

```sh
gh stack add api-routes
gh stack add  # prompts for name
```

### `gh stack checkout`

Discover and check out an entire stack from a pull request or branch.

```
gh stack checkout <pr-or-branch> [flags]
```

Accepts a PR number, PR URL, or branch name. Traces the chain of PRs to discover the full stack, fetches all branches, and saves the stack to local tracking.

> **Note:** This command is not yet implemented. Running it prints a notice.

| Flag | Description |
|------|-------------|
| `--no-switch` | Fetch and track the stack without switching to the target branch |

**Examples:**

```sh
gh stack checkout 42
gh stack checkout feature-auth
gh stack checkout https://github.com/owner/repo/pull/42
```

### `gh stack rebase`

Pull from remote and do a cascading rebase across the stack.

```
gh stack rebase [branch] [flags]
```

Fetches the latest changes from `origin`, then ensures each branch in the stack has the tip of the previous layer in its commit history. Rebases branches in order from trunk upward. If a branch's PR has been squash-merged, the rebase automatically switches to `--onto` mode to correctly replay commits on top of the merge target.

If a rebase conflict occurs, the operation pauses and prints the conflicted files with line numbers. Resolve the conflicts, stage with `git add`, and continue with `--continue`. To undo the entire rebase, use `--abort` to restore all branches to their pre-rebase state.

| Flag | Description |
|------|-------------|
| `--downstack` | Only rebase branches from trunk to the current branch |
| `--upstack` | Only rebase branches from the current branch to the top |
| `--continue` | Continue the rebase after resolving conflicts |
| `--abort` | Abort the rebase and restore all branches to their pre-rebase state |

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

# Give up and restore everything
gh stack rebase --abort
```

### `gh stack sync`

Fetch, rebase, push, and sync PR state in a single command.

```
gh stack sync
```

Performs a safe, non-interactive synchronization of the entire stack:

1. **Fetch** — fetches the latest changes from `origin`
2. **Fast-forward trunk** — fast-forwards the trunk branch to match the remote (skips if diverged)
3. **Cascade rebase** — rebases all stack branches onto their updated parents (only if trunk moved). If a conflict is detected, all branches are restored to their original state and you are advised to run `gh stack rebase` to resolve conflicts interactively
4. **Push** — pushes all branches (uses `--force-with-lease` if a rebase occurred)
5. **Sync PRs** — syncs PR state from GitHub and reports the status of each PR

**Examples:**

```sh
gh stack sync
```

### `gh stack push`

Push all branches in the current stack and create or update pull requests.

```
gh stack push [flags]
```

Pushes every branch to the remote, then for each branch either creates a new PR (with the correct base branch) or updates the base of an existing PR if it has changed.

| Flag | Description |
|------|-------------|
| `-f, --force` | Force-push branches |
| `--draft` | Create new PRs as drafts |
| `--dry-run` | Show what would be pushed without actually pushing |

**Examples:**

```sh
gh stack push
gh stack push --force
gh stack push --draft
gh stack push --dry-run
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
| `-w, --web` | Open all associated PRs in the browser |

**Examples:**

```sh
gh stack view
gh stack view --short
gh stack view --web
```

### `gh stack unstack`

Remove a stack from local tracking and optionally delete it on GitHub.

```
gh stack unstack [branch] [flags]
```

If no branch is specified, uses the current branch to find the stack. By default, the stack is removed from both local tracking and GitHub. Use `--local` to only remove the local tracking entry.

| Flag | Description |
|------|-------------|
| `--local` | Only delete the stack locally (keep it on GitHub) |

| Argument | Description |
|----------|-------------|
| `[branch]` | A branch in the stack to delete (defaults to the current branch) |

**Examples:**

```sh
# Remove the stack from local tracking and GitHub
gh stack unstack

# Only remove local tracking
gh stack unstack --local

# Specify a branch to identify the stack
gh stack unstack feature-auth
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
```

Navigation commands clamp to the bounds of the stack — moving up from the top or down from the bottom is a no-op with a message. If you're on the trunk branch, `up` moves to the first stack branch.

**Examples:**

```sh
gh stack up          # move up one layer
gh stack up 3        # move up three layers
gh stack down
gh stack top
gh stack bottom
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

### Placeholder commands

The following commands are planned but not yet implemented. Running them prints a notice and suggests using `gh stack feedback` to share your interest.

`remove` · `modify` · `reorder` · `move` · `fold` · `squash` · `rename` · `split`

## Typical workflow

```sh
# 1. Start a stack
gh stack init
gh stack add auth-middleware

# 2. Work on the first layer
#    ... write code, make commits ...

# 3. Add the next layer
gh stack add api-routes
#    ... write code, make commits ...

# 4. Push everything and create PRs
gh stack push

# 5. Reviewer requests changes on the first PR
gh stack bottom
#    ... make changes, commit ...

# 6. Rebase the rest of the stack on top of your fix
gh stack rebase

# 7. Force-push the updated stack
gh stack push --force

# 8. When the first PR is merged, sync the stack
gh stack sync
```
