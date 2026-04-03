---
title: FAQ
description: Frequently asked questions about GitHub Stacked PRs.
---

:::note
This FAQ is a work-in-progress. Detailed answers will be added soon.
:::

## Creating Stacked PRs

### What is a stacked PR? How is it different from a regular PR?

A stacked PR is a pull request that is part of an ordered chain of PRs, where each PR targets the branch of the PR below it instead of targeting `main` directly. Each PR in the stack represents one focused layer of a larger change. Individually, each PR is still a regular pull request — it just has a different base branch and GitHub understands the relationship between the PRs in the stack.

### How do I create a stacked PR?

You can create a stack using the `gh stack` CLI:

```sh
gh stack init
# ... make commits on the first branch ...
gh stack add auth-layer
# ... make commits ...
gh stack add api-routes
# ... make commits ...
gh stack submit
```

Or you can create stacked PRs manually by setting each PR's base branch to the branch of the PR below it.

### How do I add PRs to my stack?

Use `gh stack add <branch-name>` to add a new branch on top of the current stack. When you run `gh stack submit`, a PR is created for each branch and they are linked together as a Stack on GitHub.

### How can I modify my stack?

Reordering or inserting branches into the middle of a stack is not currently supported. To restructure a stack, you need to delete it and recreate it with the desired order.

### How do I delete my stack?

<!-- TODO: Update once stack deletion workflow is finalized -->

You can unstack a stacked of PRs from the PRs UI on the web. Functionality to do this from the CLI will be coming soon.

### Can stacks be created across forks?

<!-- TODO: Add answer once fork support is confirmed -->

No, stacked PRs currently require all branches to be in the same repository. Cross-fork stacks are not supported.

## Merging Stacked PRs

### What conditions need to be met for a stacked PR to be mergeable?

The same conditions as any PR targeting the final target branch (e.g., `main`) — required reviews, passing CI checks, CODEOWNER approvals, etc. These rules are evaluated against the final target branch, not the direct base branch of the PR.

### How does merging a stack of PRs differ from merging a regular PR?

Stacks must be merged **from the bottom up**. You can merge the bottom PR (and all non-merged PRs below it) in a single operation. After a PR is merged, the remaining stack is automatically rebased so the next PR targets `main` directly.

### What happens when you merge a PR in the middle of the stack?

You cannot merge a PR in the middle of the stack before the PRs below it are merged. PRs must be merged in order from the bottom up.

### How does squash merge work?

Squash merges are fully supported. Each PR in the stack produces one clean, squashed commit when merged. The rebase engine automatically detects squash-merged PRs and uses `--onto` mode to correctly replay commits from the remaining branches.

### What happens if you close a PR in the middle of the stack?

<!-- TODO: Add specific behavior details -->

Closing a PR in the middle of the stack does not automatically affect the other PRs. However, the stack relationship is preserved, so the PRs above the closed PR will still target the closed PR's branch.

### What happens when there is an error merging a PR in the middle of a stack?

<!-- TODO: Add specific error handling details -->

If a merge fails (e.g., due to a failing check or merge conflict), the operation stops and no subsequent PRs are merged. You'll need to resolve the issue before continuing.

### What happens if auto-delete branches is enabled for PRs?

<!-- TODO: Add specific behavior details -->

When a PR is merged and its branch is auto-deleted, the remaining stack is rebased so the next PR targets the appropriate base branch.

## Local Development

### Do you have a CLI to help manage stacks?

Yes! The `gh stack` CLI extension handles creating stacks, adding branches, rebasing, pushing, navigating, and syncing. Install it with:

```sh
gh extension install github/gh-stack
```

See the [CLI Reference](/gh-stack/reference/cli/) for the full command documentation.

### Do I need to use the GitHub CLI?

No. Stacked PRs are built on standard git branches and regular pull requests. You can create and manage them manually with `git` and the GitHub UI. The CLI just makes the workflow much simpler — especially for rebasing, pushing, and creating PRs with the correct base branches.

### Will this work with a different tool for stacking (jj / Sapling / ghstack / git-town, etc.)?

<!-- TODO: Add compatibility details -->

Stacked PRs on GitHub are based on the standard pull request model — any tool that creates PRs with the correct base branches can work with them. The `gh stack` CLI is purpose-built for the GitHub experience, but other tools that manage branch chains should be compatible.

<!-- ## Miscellaneous -->

<!-- ### Do you have a VS Code extension? -->

<!-- TODO: Add answer -->

<!-- ### Will stacked PRs work with the GitHub mobile app? -->

<!-- TODO: Add answer -->

<!-- ### Will stacked PRs work with the GitHub Desktop app? -->

<!-- TODO: Add answer -->

<!-- ### What happens if I merge via the REST or GraphQL APIs? -->

<!-- TODO: Add answer -->
