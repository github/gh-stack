---
title: FAQ
description: Frequently asked questions about GitHub Stacked PRs.
---

## Creating Stacked PRs

### What is a Stacked PR? How is it different from a regular PR?

A Stacked PR is a pull request that is part of an ordered chain of PRs, where each PR targets the branch of the PR below it instead of targeting `main` directly. Each PR in the stack represents one focused layer of a larger change. Individually, each PR is still a regular pull request — it just has a different base branch, and GitHub understands the relationship between the PRs in the stack.

### How do I create a Stacked PR?

You can create a stack using the `gh stack` CLI:

```sh
gh stack init auth-layer
# ... make commits on the first branch ...
gh stack add api-routes
# ... make commits ...
gh stack add request-validation
# ... make commits ...
gh stack submit
```

You can also create stacks entirely from the GitHub UI — create the first PR normally, then when creating subsequent PRs, select the option to add them to a stack. See [Creating a Stack from the UI](/gh-stack/guides/ui/#creating-a-stack-from-the-ui) for a walkthrough.

### How do I add PRs to my stack?

Use `gh stack add <branch-name>` to add a new branch on top of the current stack. When you run `gh stack submit`, a PR is created for each branch, and they are linked together as a Stack on GitHub.

You can also add PRs to an existing stack from the GitHub UI. See [Adding to an Existing Stack](/gh-stack/guides/ui/#adding-to-an-existing-stack) for details.

### How can I modify my stack?

Reordering or inserting branches into the middle of a stack is not currently supported. To restructure a stack, use `gh stack unstack` to tear it down and then re-create it with `gh stack init --adopt`:

```sh
# 1. Remove the stack
gh stack unstack

# 2. Make structural changes (reorder, rename, delete branches)
git branch -m old-name new-name

# 3. Re-create the stack with the new structure
gh stack init --adopt branch-2 branch-1 branch-3
```

### How do I delete my stack?

**From the CLI** — Run `gh stack unstack` (or `gh stack delete`) to delete the stack on GitHub and remove local tracking. Use `--local` to only remove local tracking.

**From the UI** — You can unstack PRs from the GitHub UI — see [Unstacking](/gh-stack/guides/ui/#unstacking) for a walkthrough. This dissolves the association between PRs, turning them back into standard independent PRs.

### Can stacks be created across forks?

No, Stacked PRs currently require all branches to be in the same repository. Cross-fork stacks are not supported.

## Checks, Rules & Requirements

### How are branch protection rules evaluated for Stacked PRs?

Every PR in a stack is treated as if it is targeting the **base of the stack** (typically `main`), regardless of which branch it directly targets. This means:

- **Required reviews** are evaluated as if the PR is targeting the stack base.
- **Required status checks** are evaluated as if the PR is targeting the stack base.
- **CODEOWNERS** are evaluated from the stack base — changes in `CODEOWNERS` on a PR at the bottom of the stack will not affect PRs above it in the stack.
- **Code scanning workflows** are evaluated as if the PR is targeting the stack base.

### How do GitHub Actions work with Stacked PRs?

GitHub Actions workflows trigger as if each PR in the stack is targeting the base of the stack (e.g., `main`). If you have a workflow configured to run on `pull_request` events targeting `main`, it will run for **every PR in the stack** — not just the bottom one.

### How do I access stack metadata in my GitHub Actions workflow?

For advanced use cases, you can access the stack's base ref and base SHA in workflow expressions via `github.event.pull_request.stack`. This property is only present when the PR belongs to a stack.

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Show stack info
        if: github.event.pull_request.stack != null
        run: |
          echo "Stack base ref: ${{ github.event.pull_request.stack.base.ref }}"
          echo "Stack base SHA: ${{ github.event.pull_request.stack.base.sha }}"

      - name: Run a step only when the stack targets a release branch
        if: startsWith(github.event.pull_request.stack.base.ref, 'release/')
        run: echo "This stack targets a release branch"
```

| Expression | Description |
|------------|-------------|
| `github.event.pull_request.stack.base.ref` | The branch the entire stack ultimately targets (e.g., `main`). |
| `github.event.pull_request.stack.base.sha` | The HEAD SHA of that target branch at the time of the event. |

See the [Webhooks reference](/gh-stack/reference/webhooks/) for the full details on the `stack` object in webhook payloads.

### Do all previous PRs need to be passing checks before I can merge?

Yes. In order to merge a PR in the stack, **all PRs below it** must also have passing checks and meet all merge requirements. For example, in a stack of `main <- PR1 <- PR2 <- PR3`, if you want to merge PR #3, both PR #1 and PR #2 must have passing checks, required reviews, and satisfy all branch protection rules.

### Is a linear history required?

Yes. There must be a **fully linear history** between each of the branches in the stack. This is a strict requirement for merging.

If the stack is not linear (e.g., after changes were pushed to a lower branch), you can fix it in two ways:

- **From the CLI** — Run `gh stack rebase` to perform a cascading rebase locally and then push with `gh stack push`.
- **From the UI** — Click the **Rebase Stack** button in the merge box to trigger a cascading rebase across all branches in the stack.

## Merging Stacked PRs

### What conditions need to be met for a Stacked PR to be mergeable?

Every PR in a stack must meet the same merge requirements as a PR targeting the stack base (e.g., `main`): required reviews, passing CI checks, CODEOWNER approvals, and a linear history. All PRs below it must also meet these requirements. See the [Checks, Rules & Requirements](#checks-rules--requirements) section above for details.

### How does merging a stack of PRs differ from merging a regular PR?

Stacks must be merged **from the bottom up**. When you merge a PR, all non-merged PRs below it in the stack are also merged. After a PR is merged, the remaining stack is automatically rebased so the next PR targets `main` directly.

### What happens when you merge a PR in the middle of the stack?

You cannot merge a PR in the middle of the stack before the PRs below it are merged. PRs must be merged in order from the bottom up.

### How does squash merge work?

Squash merges are fully supported. Each PR in the stack produces one clean, squashed commit when merged. Merging `n` PRs will create `n` squashed commits on the base.

When a PR is squash-merged, the original commits disappear from the history, which can cause artificial merge conflicts during rebasing. Both the CLI and the server handle this using `git rebase --onto`:

```
git rebase --onto <new_commit_sha_generated_by_squash> <original_commit_sha_from_tip_of_merged_branch> <branch_name>
```

**Example:** Consider a stack with three PRs:

```
PR1: main ← A, B              (branch1)
PR2: main ← A, B, C, D        (branch2)
PR3: main ← A, B, C, D, E, F  (branch3)
```

When PR1 and PR2 are squash-merged, `main` now looks like:

```
S1 (squash of A+B), S2 (squash of C+D)
```

Then the following rebase is run:

```
git rebase --onto S2 D branch3
```

Which rewrites `branch3` to:

```
S1, S2, E, F
```

This moves the unique commits from the unmerged branch and replays them on top of the newly squashed commits on the base branch, avoiding any merge conflicts.

### How does merge commit work?

When you merge a stack using the merge commit strategy, it creates **one merge commit for the entire group** of PRs being merged. The full commit history of each PR is preserved within the merge commit.

### How does rebase merge work?

With rebase merge, all of the commits from each PR in the stack are replayed onto the base branch one at a time, creating a linear history without merge commits.

### Do all PRs get merged at once or one at a time?

PRs in a stack are merged sequentially, from the bottom up. When you initiate a merge, the bottom PR is merged first, and then the next PR above it, and so on. 

Commits are not all landed in a single atomic operation — each PR is merged individually in sequence.

### Can I merge only part of a stack? What happens to the remaining unmerged PRs?

Yes, partial stack merges are supported. After the merge, the lowest unmerged PR is updated to explicitly target the stack base (e.g., `main`). A cascading rebase is also automatically run to rebase the remaining unmerged branches.

### What happens if you close a PR in the middle of the stack?

Closing a PR in the middle of the stack will block all PRs above it from being mergeable. The stack relationship is preserved, so if you want to open a different PR or modify the stack, you will need to unstack and then re-create the stack.

### What happens when there is an error merging a PR in the middle of a stack?

If a merge fails (e.g., due to a failing check or merge conflict), the operation stops and no subsequent PRs are merged. You'll need to resolve the issue before continuing.

### Do Stacked PRs support merge queue?

Yes, Stacked PRs fully support merging via merge queue. When you merge a stack through the merge queue:

- **All PRs in the stack are added to the queue** in the correct order, ensuring a linear sequence.
- **If a PR is removed or ejected from the merge queue**, all PRs above it in the stack are also ejected and removed from the queue.
- **Stacks can be split across merge groups** in the merge queue — not all PRs in the stack need to be in the same merge group.

## Local Development

### Do you have a CLI to help manage stacks?

Yes! The `gh stack` CLI extension handles creating stacks, adding branches, rebasing, pushing, navigating, and syncing. Install it with:

```sh
gh extension install github/gh-stack
```

See the [CLI Reference](/gh-stack/reference/cli/) for the full command documentation.

### Do I need to use the GitHub CLI?

No. Stacked PRs are built on standard git branches and regular pull requests. You can create and manage them manually with `git` and the GitHub UI. The CLI just makes the workflow much simpler — especially for rebasing, pushing, and creating PRs with the correct base branches.

### Will this work with a different tool for stacking?

Yes, you can continue to use your tool of choice (e.g., jj, Sapling, git-town, etc.) to manage stacks locally and push up your branches to GitHub.

Stacked PRs on GitHub are based on the standard pull request model — any tool that creates PRs with the correct base branches can work with them. The `gh stack` CLI is purpose-built for the GitHub experience, but other tools that manage branch chains should be compatible.

You can also use the `gh stack link` command in conjunction with other tools to open your PRs as a stack:


```bash
# Create a stack of branches locally using jj
jj new main -m "first change"
jj bookmark create change1 --revision @
# ...

jj new -m "second change"
jj bookmark create change2 --revision @
# ...

jj new -m "third change"
jj bookmark create change3 --revision @
# ...

# Push branches and link them as a stack on GitHub
# (creates PRs automatically if they don't exist)
gh stack link change1 change2 change3
```

This doesn't create any local tracking and only hits the APIs to create Stacked PRs.

If the provided branches already have open PRs, `link` will use them. If not, it creates PRs with the correct base branch chaining.

To add more to the stack, run `link` again, but be sure to include the full list of PRs/branches in the stack:

```bash
gh stack link 123 124 125 change4 change5
```

You can also use `--base` to specify a different trunk branch and `--draft` to create PRs as drafts:

```bash
gh stack link --base develop --draft change1 change2 change3
```

Alternatively, if you want full local stack tracking (for commands like `rebase`, `sync`, and navigation), you can adopt existing branches to local tracking with `gh stack`:

```bash
gh stack init --adopt change1 change2 change3
gh stack submit
```
