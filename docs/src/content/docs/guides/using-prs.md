---
title: Working with Stacked PRs
description: How stacked pull requests appear on GitHub, how to review them, and how to merge them.
---

This guide covers the GitHub pull request experience for stacked PRs — what authors and reviewers see, how rules and CI work, and how to merge.

## The Stack Map

When a pull request is part of a stack, a **stack map** appears at the top of the PR page. It shows:

- Every PR in the stack, listed in order
- The status of each PR (open, approved, merged, etc.)
- Navigation links to jump to any PR in the stack

This gives reviewers immediate context about where the current PR fits in the bigger picture and makes it easy to navigate the full set of changes.

## Reviewing Stacked PRs

Each PR in a stack shows only the diff for its layer — the changes between its branch and the branch below it. This means:

- **Reviewers see focused diffs.** A PR for API routes only shows the API changes, not the auth middleware from the layer below.
- **Reviews are independent.** You can approve, request changes, or comment on any PR in the stack without affecting the others.
- **Context is preserved.** The stack map at the top always shows the full picture, so reviewers understand the progression.

### Tips for Reviewers

- **Read the stack in order** when you want the full story — start from the bottom PR and work up.
- **Review individual PRs** when you're focusing on a specific concern (e.g., reviewing only the API layer).
- **Use the stack map** to navigate between PRs without going back to the PR list.

## Rules and CI Enforcement

The merge requirements for every PR in the stack are determined by the **final target branch** — typically `main`. This is different from how non-stacked PRs work, where rules are based on the direct target.

What this means in practice:

- **CODEOWNER approvals** required for `main` apply to all PRs in the stack, even mid-stack PRs that target another branch.
- **Required status checks** configured for `main` run on every PR in the stack.
- **CI workflows** triggered by pull requests to `main` also trigger for all stacked PRs.

This ensures that every layer of the stack meets the same quality bar before it can be merged.

## Merging Stacks

Stacks are merged **from the bottom up**. You cannot merge a PR in the middle of the stack before the PRs below it are merged.

### How It Works

1. When the bottom PR meets all merge requirements, merge it.
2. After the bottom PR is merged, the remaining stack is **automatically rebased** — the next PR's base is updated to target `main` directly.
3. The next PR is now at the bottom and can be reviewed, approved, and merged.
4. Repeat until the entire stack is landed.

### Merge Methods

- **Direct merge** — Merges a PR and all non-merged PRs below it in one operation, as long as all conditions are met.
- **Merge queue** — The merge queue is stack-aware. If the bottom PR is removed from the queue, all other PRs in the stack are also removed.

The resulting commit history is the same as merging each PR individually from the bottom up.

### Squash Merges

Stacks fully support **squash merges** — each PR in the stack produces one clean, squashed commit when merged. When a PR is squash-merged, the rebase engine detects this and uses `--onto` mode to correctly replay commits from the remaining branches on top of the squashed result.

## Simplified Rebasing

Rebasing is the trickiest part of working with stacked PRs. GitHub handles it in multiple ways:

### In the PR UI

A **rebase button** in the PR interface lets you trigger a cascading rebase across all branches in the stack. This updates every branch to include the latest changes from the branch below it.

### From the CLI

Run `gh stack rebase` to perform the same cascading rebase locally. This is useful when you've made changes to a lower branch and need the rest of the stack to pick them up.

### After Partial Merges

When you merge a PR at the bottom of the stack, the remaining branches are **automatically rebased**. The next PR's base is retargeted to `main`, and its branch is rebased on top of the updated trunk. This means you don't need to manually rebase after each merge — the stack stays in a clean state.

## Pushing Changes from the CLI

After making local changes or resolving conflicts, use the CLI to push and sync:

```sh
# Push all branches to the remote
gh stack push

# Create or update PRs and the Stack on GitHub
gh stack submit

# Or sync everything in one command (fetch, rebase, push, update PRs)
gh stack sync
```

`gh stack push` uses `--force-with-lease` to safely update rebased branches. Use `gh stack submit` when you need to create new PRs or update the Stack on GitHub.
