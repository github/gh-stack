---
title: Working with Stacked PRs
description: Practical guide for reviewing, merging, and managing stacked pull requests on GitHub.
---

This guide covers the practical day-to-day experience of working with Stacked PRs — how to review them, how merging works step by step, and how to keep things in sync from the CLI.

For an introduction to what stacks are and how GitHub supports them natively, see the [Overview](/gh-stack/introduction/overview/). For a visual walkthrough of the UI, see [Stacked PRs in the GitHub UI](/gh-stack/guides/ui/).

## Reviewing Stacked PRs

Each PR in a stack shows only the diff for its layer — the changes between its branch and the branch below it. This means:

- **Reviewers see focused diffs.** A PR for API routes only shows the API changes, not the auth middleware from the layer below.
- **Reviews are independent.** You can approve, request changes, or comment on any PR in the stack without affecting the others.
- **Context is preserved.** The stack map at the top always shows the full picture, so reviewers understand the progression.

### Tips for Reviewers

- **Read the stack in order** when you want the full story — start from the bottom PR and work up.
- **Review individual PRs** when you're focusing on a specific concern (e.g., reviewing only the API layer).
- **Use the stack map** to navigate between PRs without going back to the PR list.

## Merging from the Bottom Up

Stacks are merged **from the bottom up** — you can merge any number of PRs at once, as long as they form a contiguous group starting from the lowest unmerged PR. For example, in a stack of four PRs, you can merge just the bottom one, or the bottom three together, but you cannot merge only the second and third PRs while leaving the first unmerged. Mid-stack merges are not allowed.

1. When the lowest unmerged PR (and any PRs above it that you want to include) meet all merge requirements, merge them.
2. After the merge, the remaining stack is **automatically rebased** — the next unmerged PR's base is updated to target `main` directly.
3. The next unmerged PR is now at the bottom and can be reviewed, approved, and merged.
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
