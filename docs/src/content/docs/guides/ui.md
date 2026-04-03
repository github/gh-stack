---
title: Stacked PRs in the GitHub UI
description: A visual walkthrough of creating, navigating, merging, and managing stacked PRs directly in the GitHub pull request UI.
---

This guide walks through the key UI components and workflows for working with Stacked PRs on GitHub.

## Navigating Stacked PRs

When a pull request is part of a stack, a **stack navigator** appears in the PR header. This component gives you an at-a-glance view of the entire stack and lets you jump between PRs.

The stack navigator shows:

- All PRs in the stack, listed in order from top to bottom
- Which PR you're currently viewing (highlighted)
- Clickable links to navigate directly to any PR in the stack
- Link to Add to Stack, where you can create a new PR that targets the head of the topmost PR
- Unstack option to dissolve the association between PRs, turning them back into standard PRs

![The stack navigator in a PR header](../../../assets/screenshots/stack-navigator.png)

## Creating a Stack from the UI

You can create Stacked PRs entirely from the GitHub UI.

### Step 1: Create the first PR normally

Create a pull request as you normally would. The first PR in the stack targets `main` (or whatever you want as your trunk branch). There's nothing special about this step.

![Creating the first PR targeting main](../../../assets/screenshots/create-first-pr.png)

### Step 2: Create subsequent PRs and add them to the stack

When you create the next PR, set its base branch to the first PR's branch. You'll see a checkbox or option to **Create stack**. Select this to link this PR to the previous one, starting a new stack.

![Creating a second PR with the stack checkbox](../../../assets/screenshots/create-second-pr-stack-checkbox.png)

### Step 3: Confirm the stack

After creating the PR, you'll see the stack navigator appear in the header, showing both PRs linked together.

![The stack navigator showing the newly created stack](../../../assets/screenshots/newly-created-stack.png)

Repeat this process for each additional PR in the stack — each one targets the branch of the PR before it.

## Adding to an Existing Stack

If a stack already exists and you want to add a new PR to it:

1. Open a PR in the stack, click the stack icon in the header, and click **Add**.

![Adding a new PR to an existing stack](../../../assets/screenshots/add-to-existing-stack.png)

2. On the following page, the base branch is automatically set to the head of the top-most PR. Select the head branch for your new PR and click **Create pull request**.

![Selecting branch to add to stack](../../../assets/screenshots/selecting-branch-to-add.png)

3. Select the checkbox for **Add to existing stack** and create your pull request.

![Creating a PR to add to an existing stack](../../../assets/screenshots/create-pr-add-to-stack.png)

The new PR will be added to the top of the stack, which you will see on the PRs page.

![Stack of Pull Requests](../../../assets/screenshots/stacked-prs.png)

## The Merge Box

The merge box for a Stacked PR works differently from a regular PR. It shows not just the current PR's merge status, but the status of the entire stack.

### Merge Requirements

Before a PR in the stack can be merged, the following conditions must be met:

- **All PRs below it** must be approved and have passing checks
- **The stack must be fully rebased** with a linear history
- **The current PR itself** must meet all branch protection requirements for the stack base

![Merge box showing failing stack requirements](../../../assets/screenshots/merge-box-failing-stack-requirements.png)

## Unstacking

If you want to reorder or reorganize the PRs in a stack, you must first dissolve the stack and then recreate it. You can unstack PRs from the UI.

### Dissolving the Entire Stack

To dissolve the stack entirely (turning all Stacked PRs back into independent PRs), use the unstack option on the stack itself.

![Dissolving an entire stack](../../../assets/screenshots/unstack-entire-stack.png)

After unstacking, each PR retains its current base branch but is no longer linked to the other PRs. The stack navigator and stack-related merge requirements disappear from all affected PRs.
