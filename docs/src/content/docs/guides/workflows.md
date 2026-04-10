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

If a conflict is detected during the rebase, all branches are restored to their original state, and you're advised to run `gh stack rebase` to resolve conflicts interactively.

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

When you need to remove a branch, reorder branches, or rename branches, tear down the stack and rebuild it:

```sh
# 1. Remove the stack on GitHub and locally
gh stack unstack

# 2. Make structural changes
git branch -m old-branch-1 new-branch-1  # rename a branch
git branch -D branch-3                   # delete a branch

# 3. Re-create the stack with the new order
gh stack init --adopt new-branch-1 branch-2 branch-4

# 4. Push and sync the new stack
gh stack submit
```

The `unstack` command deletes the stack on GitHub first, then removes local tracking. Your branches and PRs are not affected — only the stack relationship is removed. After `init --adopt`, any existing open PRs are automatically re-associated with the new stack.

## Using AI Agents with Stacks

AI coding agents (like GitHub Copilot) can create and manage Stacked PRs on your behalf. Install the gh-stack skill to give them the context they need:

```sh
npx skills add github/gh-stack
```

With the skill installed, your agent can:
- Plan stack structure based on the work being done
- Create branches and commit changes in the right layers
- Navigate between branches to make mid-stack changes
- Push branches and create Stacked PRs
- Rebase after making changes to lower layers
