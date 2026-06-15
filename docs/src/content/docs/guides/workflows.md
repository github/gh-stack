---
title: Typical Workflows
description: Common patterns and workflows for using Stacked PRs effectively.
---

This guide covers the most common workflows for day-to-day use of Stacked PRs, from the standard flow to advanced patterns.

## Standard Workflow

The basic flow: initialize a stack, add branches for each logical unit of work, commit, push, iterate on review feedback, and merge.

```sh
# 1. Start a stack (creates and checks out the first branch)
gh stack init

# 2. Work on the first layer
# ... write code, make commits ...

# 3. Add the next layer
gh stack add api-routes
# ... write code, make commits ...

# 4. Push everything and create Stacked PRs
gh stack submit

# 5. Reviewer requests changes on the first PR
gh stack bottom
# ... make changes, commit ...

# 6. Rebase the rest of the stack on top of your fix
gh stack rebase

# 7. Push the updated branches
gh stack push

# 8. Sync upstream changes as PRs get merged
gh stack sync
```

## Abbreviated Workflow

For speed, use a branch prefix with `--numbered` and the `-Am` flags to fold staging, committing, and branch creation into a single command. Branch names are auto-generated as `prefix/01`, `prefix/02`, etc.

```sh
# Alias `gh stack` as `gs` for easier use
gh stack alias

# 1. Start a stack with numbered branches
gs init -p feat --numbered
#    → creates feat/01 and checks it out

# 2. Write code for the first layer
# ... write code ...

# 3. Stage and commit on the current branch
gs add -Am "Auth middleware"
#    → feat/01 has no commits yet, so the commit lands here

# 4. Write code for the next layer
# ... write code ...

# 5. Create the next branch and commit
gs add -Am "API routes"
#    → feat/01 already has commits, so feat/02 is created

# 6. Keep going
# ... write code ...
gs add -Am "Frontend components"
#    → creates feat/03

# 7. Push everything and create PRs
gs submit
```

Each `gs add -Am "..."` stages all files, commits, and (if the current branch already has commits) creates a new branch — no separate `git add` or `git commit` needed.

## Making Mid-Stack Changes

When you're working on a higher layer and realize you need to change something lower in the stack — don't hack around it at the current layer. Navigate down, make the change where it belongs, and rebase.

```sh
# You're on feat/frontend but need an API change

# 1. Navigate to the API branch
gh stack down
# or: gh stack checkout api-routes

# 2. Make the change where it belongs
git add users_api.go
git commit -m "Add get-user endpoint"

# 3. Rebase everything above to pick up the change
gh stack rebase --upstack

# 4. Navigate back to where you were working
gh stack top
```

This keeps each branch focused on one concern and avoids muddying the diff for reviewers.

## Responding to Review Feedback

When a reviewer requests changes on a PR mid-stack:

```sh
# 1. Navigate to the branch that needs changes
gh stack checkout auth-middleware
# or: gh stack bottom, gh stack down, etc.

# 2. Make the fixes
git add .
git commit -m "Address review feedback"

# 3. Cascade the changes through the rest of the stack
gh stack rebase

# 4. Push the updated stack
gh stack push
```

The rebase ensures all branches above the changed one pick up the fixes. `gh stack push` uses `--force-with-lease` to safely update the rebased branches.

## Syncing After Merges

When a PR at the bottom of the stack is merged on GitHub, use `gh stack sync` to update your local state:

```sh
gh stack sync
```

This command:
1. Fetches the latest changes from the remote
2. Fast-forwards the trunk branch
3. Rebases all remaining stack branches onto the updated trunk
4. Pushes the updated branches
5. Syncs PR state from GitHub
6. Prompts to prune local branches for merged PRs (use `--prune` to prune automatically)

If a conflict is detected during the rebase, all branches are restored to their original state, and you're advised to run `gh stack rebase` to resolve conflicts interactively.

## Rebasing Your Stack

Stacked PRs rely on rebasing rather than merge commits to keep each branch's diff clean and reviewable. If you're coming from a merge-commit workflow, the key difference is: instead of merging upstream changes into your branch (which creates a merge commit with multiple parents), you replay your commits on top of the latest base. The result is a linear history where each PR shows only its specific changes.

### How rebasing works with stacks

When you run `gh stack rebase`, it performs a **cascading rebase**: each branch in the stack is rebased onto the tip of the branch below it, starting from the trunk. This ensures every branch has the latest changes from all lower layers.

```sh
# Rebase the entire stack (all branches, trunk to top)
gh stack rebase

# Only rebase from trunk up to the current branch
gh stack rebase --downstack

# Only rebase from the current branch up to the top
gh stack rebase --upstack

# Rebase stack branches without pulling from or rebasing with trunk
gh stack rebase --no-trunk
```

After rebasing, push the updated branches:

```sh
gh stack push
```

`gh stack push` uses `--force-with-lease` to safely update the rebased branches. This is a safe form of force push — it ensures you don't overwrite changes that someone else pushed since your last fetch. If the remote has unexpected changes, the push is rejected and you can investigate.

### Rebase from the CLI vs. the web UI

You can rebase stack branches from either the CLI or the GitHub web UI, but they behave differently:

| | CLI (`gh stack rebase`) | Web UI ("Rebase Stack" button) |
|---|---|---|
| **Runs where** | Locally, using your Git installation | On GitHub's servers |
| **Commit signing** | Commits are signed with your local Git committer config (GPG/SSH signing, if configured) | Commits retain the original author but the committer is set to whoever clicked the button — commits are **not** signed |
| **Conflict resolution** | Interactive — you resolve conflicts in your editor, then `gh stack rebase --continue` | Not available if there are conflicts — you must rebase locally |

:::note
If commit signing matters for your project (e.g., branch protection rules require signed commits), use the CLI for rebases.
:::

### Resolving conflicts

When a rebase encounters a conflict, `gh stack rebase` stops and tells you which files are conflicted:

```sh
gh stack rebase
# ✗ Conflict detected rebasing feat/api onto feat/auth
#   C api/routes.go (lines 12–18)
#
# Resolve conflicts on feat/api, then run: gh stack rebase --continue
# Or abort this operation with: gh stack rebase --abort
```

To resolve:

```sh
# 1. Open the conflicted files and resolve the markers
#    (<<<<<<< / ======= / >>>>>>>)
#    Use your editor of choice

# 2. Stage the resolved files
git add api/routes.go

# 3. Continue the rebase — remaining branches are rebased automatically
gh stack rebase --continue
```

If the conflict is too complex or you want to start over:

```sh
# Abort and restore all branches to their pre-rebase state
gh stack rebase --abort
```

### The rebase + force-push cycle

The typical cycle when updating a stack after making changes looks like this:

```sh
# 1. Make changes on a mid-stack branch
gh stack checkout feat/auth
git add .
git commit -m "Fix token validation"

# 2. Rebase everything above to incorporate the change
gh stack rebase --upstack

# 3. Push all updated branches (safe force push)
gh stack push
```

This is equivalent but distinct from updating your branch using a merge commit. The key difference is that after changing a lower branch, rebase maintains a linear commit history so the unique set of commits on each branch have clean diffs.

`gh stack push` then handles the force push safely via `--force-with-lease --atomic`, ensuring either all branches update or none do.

For a simpler all-in-one flow, `gh stack sync` combines fetch, rebase, and push into a single command — useful when you just need to pull in the latest upstream changes:

```sh
gh stack sync
```

## Existing Branches into a Stack

If you already have a set of branches that form a logical chain, you can organize them into a stack by passing them to `gh stack init`. Existing branches are adopted automatically — no special flags needed.

```sh
# Adopt three existing branches into a stack (bottom to top)
gh stack init feat/auth feat/api feat/ui
```

The order matters: branches are listed from bottom (closest to trunk) to top (furthest from trunk). Any PRs already open for these branches are detected and linked to the stack.

You can also mix existing and new branches in one command:

```sh
# feat/auth exists, feat/api-v2 will be created
gh stack init feat/auth feat/api-v2
```

After organizing branches into a stack, run `gh stack submit` to create a Stack on GitHub and link the PRs together.

```sh
# View the new stack
gh stack view

# Create/update PRs and link them as a Stack on GitHub
gh stack submit
```

## Structuring Your Stack

Think of a stack from the reviewer's perspective: the PRs should tell a **cohesive story**. A reviewer reading the PRs in sequence should understand the progression of changes.

### Dependency order

Plan your layers before writing code. Foundational changes go in lower branches, dependent changes go higher:

```
     ┌── tests          ← integration tests for the full stack
    ┌── frontend-ui      ← UI components that call the APIs
   ┌── api-endpoints     ← API routes that use the models
  ┌── data-models        ← shared types, database schema
main (trunk)
```

### When to create a new branch

Create a new branch (`gh stack add`) when you're starting a **different concern**:

- Switching from backend to frontend work
- Moving from core logic to tests or documentation
- The next changes have a different reviewer audience
- The current branch is already large enough to review

### One stack, one effort

All branches in a stack should be part of the same feature or project. If you need to work on something unrelated, start a separate stack with `gh stack init` or switch to an existing one with `gh stack checkout`.

## Restructuring a Stack

When you need to change the composition of a stack — remove a branch, combine branches, insert a new branch, change the order, or rename a branch — use `gh stack modify`:

```sh
# Open the modify TUI
gh stack modify

# In the TUI:
#   x     → drop a branch
#   d     → fold down (into branch below)
#   u     → fold up (into branch above)
#   i     → insert below
#   I     → insert above
#   Shift+↑/↓ → reorder
#   r     → rename
#   z     → undo
#   Ctrl+S → apply changes
#   q     → cancel

# After modifying, push changes to remote and recreate the stack on GitHub
gh stack submit
```

### Common restructuring scenarios

**Remove a branch and its unique commits from the stack:**
1. `gh stack modify`
2. Navigate to the branch, press `x` to mark it for drop
3. Press `Ctrl+S` to apply
4. `gh stack submit`

**Combine two branches into one:**
1. `gh stack modify`
2. Navigate to the branch you want to fold
3. Press `d` to fold its commits into the branch below, or `u` to fold into the branch above
4. Press `Ctrl+S` to apply
5. `gh stack submit`

**Reorder branches:**
1. `gh stack modify`
2. Navigate to the branch to move
3. Press `Shift+↑` to move up or `Shift+↓` to move down
4. Press `Ctrl+S` to apply
5. `gh stack submit`

For a comprehensive guide on all modify operations, see the [Restructuring Stacks](/gh-stack/guides/modify/) guide.

## Using AI Agents with Stacks

AI coding agents (like GitHub Copilot) can create and manage Stacked PRs on your behalf. Install the gh-stack skill to give them the context they need:

```sh
gh skill install github/gh-stack
```

Or if you prefer to use `npx skills`:

```sh
npx skills add github/gh-stack
```

With the skill installed, your agent can:
- Plan stack structure based on the work being done
- Create branches and commit changes in the right layers
- Navigate between branches to make mid-stack changes
- Push branches and create Stacked PRs
- Rebase after making changes to lower layers
