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

## Merging a Stack

Merging is driven by a single action: **click Merge on the highest PR you want to land, and that PR plus every unmerged PR below it are merged together, from the bottom up.** You do not need to merge PRs one at a time, unless you choose to.

- **To land the whole stack**, merge the **top** PR — every PR below it lands with it in a single step.
- **To land part of the stack**, merge a lower PR — the PRs below it come along, and the PRs above stay open.
- **To land a single PR**, merge the **bottom** PR - only that PR will be merged, and the rest of the PRs stay open.

You can merge any contiguous group, as long as it starts from the lowest unmerged PR. In a stack of four PRs you can land just the bottom one, or the bottom three together, but you can't merge only the second and third while leaving the first unmerged — a PR always brings the unmerged PRs below it along. Merging a stacked PR always merges all the unmerged PRs below it as well.

When you land only part of a stack, the remaining PRs are **automatically rebased** and retargeted so the next unmerged PR targets your base branch directly and is immediately ready to review and merge.

Once the entire stack has landed, it is complete and can't be extended. If you add new branches on top and run `gh stack submit`, the CLI automatically starts a **new** stack rooted at the trunk for those branches (a new PR on a fully merged stack would target the trunk directly rather than chaining onto the merged PRs).

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
- **`gh stack sync`** is the all-in-one command: fetch, rebase, push, sync stack/PR state, link open PRs into a Stack on GitHub, and optionally prune local branches for merged PRs. If there is a divergence between local and remote stacks, you will be prompted to resolve.
