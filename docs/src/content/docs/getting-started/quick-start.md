---
title: Quick Start
description: Install the gh stack CLI and create your first Stacked PR in minutes.
---

## Prerequisites

- [GitHub CLI](https://cli.github.com/) (`gh`) v2.0 or later, authenticated
- Git 2.20 or later
- A GitHub repository you can push to

## Install the CLI Extension

```sh
gh extension install github/gh-stack
```

## Set Up AI Agent Integration

If you use AI coding agents (like GitHub Copilot), install the gh-stack skill so they know how to work with Stacked PRs:

```sh
npx skills add github/gh-stack
```

This gives your AI agent the context it needs to create, manage, and navigate stacks on your behalf.

## Create Your First Stack

### 1. Initialize a stack

Navigate to your repository and initialize a new stack. This creates a tracking entry and your first branch:

```sh
cd my-project
gh stack init
```

You'll be prompted to name your first branch. The stack uses your repository's default branch (e.g., `main`) as the trunk.

### 2. Write code and commit

Work on your first branch as usual — write code, stage changes, and commit:

```sh
# ... write code ...
git add .
git commit -m "Add auth middleware"
```

### 3. Add more branches

When you're ready for the next logical unit of work, add a new branch to the top of the stack:

```sh
gh stack add api-routes
# ... write code ...
git add .
git commit -m "Add API routes"
```

### 4. Push branches

Push all branches to the remote:

```sh
gh stack push
```

### 5. Create PRs

Create pull requests and link them as a stack on GitHub:

```sh
gh stack submit
```

Each PR is created with the correct base branch — your first branch targets `main`, and `api-routes` targets the first branch — so reviewers see only the diff for that layer. The PRs are automatically linked together as a Stack on GitHub.

### 6. View the stack

See the full state of your stack at any time:

```sh
gh stack view
```

This shows all branches, their PR links, statuses, and the most recent commit on each.

## What's Next?

- [Working with Stacked PRs](/gh-stack/guides/stacked-prs/) — Learn about the PR review and merge experience
- [Typical Workflows](/gh-stack/guides/workflows/) — Common patterns for day-to-day use
- [CLI Reference](/gh-stack/reference/cli/) — Full command documentation
