---
title: Working with Stacked PRs
description: Practical guide for reviewing, merging, and managing stacked pull requests on GitHub.
---

This guide covers the practical day-to-day experience of working with Stacked PRs — how to review them, how merging works step by step, and how to keep things in sync from the CLI.

For an introduction to what stacks are and how GitHub supports them natively, see the [Overview](/gh-stack/introduction/overview/). For a visual walkthrough of the UI, see [Using Stacks in the GitHub UI](/gh-stack/guides/ui/).

## Reviewing Stacked PRs

Each PR in a stack shows only the diff for its layer — the changes between its branch and the branch below it. This means:

- **Reviewers see focused diffs.** A PR for API routes only shows the API changes, not the auth middleware from the layer below.
- **Reviews are independent.** You can approve, request changes, or comment on any PR in the stack without affecting the others.
- **Context is preserved.** The stack map at the top always shows the full picture, so reviewers understand the progression.

### Tips for Reviewers

- **Read the stack in order** when you want the full story — start from the bottom PR and work up.
- **Review individual PRs** when you're focusing on a specific concern (e.g., reviewing only the API layer).
- **Use the stack map** to navigate between PRs without going back to the PR list.

## Merging Step by Step

Stacks are merged **from the bottom up**. You cannot merge a PR in the middle of the stack before the PRs below it are merged.

1. When the bottom PR meets all merge requirements, merge it.
2. After the bottom PR is merged, the remaining stack is **automatically rebased** — the next PR's base is updated to target `main` directly.
3. The next PR is now at the bottom and can be reviewed, approved, and merged.
4. Repeat until the entire stack is landed.

For details on merge methods (squash, merge commit, rebase) and merge requirements, see [Merging Stacks](/gh-stack/introduction/overview/#merging-stacks) in the Overview.

## Pushing and Syncing from the CLI

After making local changes or resolving conflicts, use the CLI to push and sync:

```sh
# Push all branches to the remote
gh stack push

# Create or update PRs and the Stack on GitHub
gh stack submit

# Or sync everything in one command (fetch, rebase, push, update PRs)
gh stack sync
```

- **`gh stack push`** pushes branches only (uses `--force-with-lease` for safety). It does not create or update PRs.
- **`gh stack submit`** pushes branches and creates or updates PRs, linking them as a Stack on GitHub.
- **`gh stack sync`** is the all-in-one command: fetch, rebase, push, and sync PR state.
