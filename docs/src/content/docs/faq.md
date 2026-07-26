---
title: FAQ
description: Frequently asked questions about GitHub Stacked PRs.
---

## Creating Stacked PRs

### What is a Stacked PR? How is it different from a regular PR?

A Stacked PR is a pull request that is part of an ordered chain of PRs, where each PR targets the branch of the PR below it instead of targeting the merge target directly. Each PR in the stack represents one focused layer of a larger change. Individually, each PR is still a regular pull request — it just has a different base branch, and GitHub understands the relationship between the PRs in the stack.

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

If you already have open PRs whose branches line up, GitHub will detect and suggest turning them into a stack. See [Turning Existing PRs into a Stack](/gh-stack/guides/ui/#turning-existing-prs-into-a-stack).

### How do I add PRs to my stack?

Use `gh stack add <branch-name>` to add a new branch on top of the current stack. When you run `gh stack submit`, a PR is created for each branch, and they are linked together as a Stack on GitHub.

You can also add PRs to an existing stack from the GitHub UI — either a brand-new PR or an already-open PR (via the recommendation banner), added to the top of the stack. See [Adding to an Existing Stack](/gh-stack/guides/ui/#adding-to-an-existing-stack) for details.

### How many PRs can a stack contain?

A stack can contain up to **100 pull requests**. If your work requires more than 100 PRs, split it into multiple stacks.

### How can I modify my stack?

Use `gh stack modify` to restructure a stack. It opens an interactive terminal UI where you can reorder, drop, fold (combine), insert, and rename branches — then applies all changes at once. See the [Restructuring Stacks](/gh-stack/guides/modify/) guide for a full walkthrough.

Alternatively, you can manually tear down and re-create the stack with `gh stack unstack` and `gh stack init`:

```sh
# 1. Remove the stack
gh stack unstack

# 2. Make structural changes (reorder, rename, insert, delete branches)
git branch -m api-roots api-routes

# 3. Re-create the stack with the new structure
gh stack init db-migrations api-routes frontend
```

### How do I delete my stack?

**From the CLI** — Run `gh stack unstack` (or `gh stack delete`) to delete the stack on GitHub and remove local tracking. You can also unstack any stack by its number from anywhere in the repository — `gh stack unstack 7` — whether or not it's checked out locally. Use `--local` to only remove local tracking.

**From the UI** — You can unstack PRs from the GitHub UI — see [Unstacking](/gh-stack/guides/ui/#unstacking) for a walkthrough. This dissolves the association between the PRs, turning them back into standard independent PRs.

Unstacking only removes **open, draft, and closed** PRs from the stack. **Merged and queued PRs remain part of the stack** — once a PR has merged (or is queued for merge) as part of a stack, it can't be unstacked. A stack is fully dissolved only when none of its PRs have merged or are queued for merge; otherwise it persists with those PRs still in it.

### Can stacks be created across forks?

No, Stacked PRs currently require all branches to be in the same repository. Cross-fork stacks are not supported.

### Can a stack target a branch other than my default branch?

Yes. A stack's **trunk** (the base branch of the bottom PR) can be any branch in the repository, such as a release branch or a long-lived feature branch. It defaults to your repository's default branch (e.g., `main`), but you can pick a different one:

- **CLI** — pass `--base <branch>` to `gh stack init` or `gh stack link` (for example, `gh stack init --base release`).
- **Web** — create the bottom PR against whatever branch you want as the trunk; the rest of the stack chains on top of it.

The same behavior applies to whatever trunk your stack targets — branch protection rules, required checks, and CI are all evaluated against your stack's base branch.

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

For advanced use cases, you can access the stack's metadata in workflow expressions via `github.event.pull_request.stack`. This property is only present when the PR belongs to a stack.

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
          echo "PR ${{ github.event.pull_request.stack.position }} of ${{ github.event.pull_request.stack.size }} in the stack"

      - name: Run a step only when the stack targets a release branch
        if: github.event.pull_request.stack != null && startsWith(github.event.pull_request.stack.base.ref, 'release/')
        run: echo "This stack targets a release branch"
```

| Expression | Description |
|------------|-------------|
| `github.event.pull_request.stack.number` | The stack's number, scoped to the repository. |
| `github.event.pull_request.stack.size` | Total number of pull requests in the stack. |
| `github.event.pull_request.stack.position` | 1-based position of this PR within the stack (`1` is the bottom). |
| `github.event.pull_request.stack.base.ref` | The branch the entire stack ultimately targets (e.g., `main`). |
| `github.event.pull_request.stack.base.sha` | The HEAD SHA of the stack's base branch. |

See the [Webhooks reference](/gh-stack/reference/webhooks/) for the full details on the `stack` object in webhook payloads, or the [REST API reference](/gh-stack/reference/rest-api/) to read the same object on demand from a pull request.

### Why isn't the `stack` object in my `pull_request.opened` webhook?

A pull request is always **created before it's added to a stack**, so the `pull_request.opened` event never includes the `stack` object — at that moment the PR isn't part of any stack yet. The same is true for any other event that fires before the PR joins a stack.

To learn exactly when a PR becomes part of a stack, listen for the `pull_request` event with the **`stacked`** action. It fires when a PR is added to a stack and carries the `stack` object. See the [`stacked` event](/gh-stack/reference/webhooks/#the-stacked-event) in the Webhooks reference for the full payload.

### How can I optimize CI usage for a stack?

Because a workflow runs for every PR in a stack, a large stack can multiply your CI usage. You can use the `stack` fields to selectively run jobs based on the position of the current PR in the stack.

Two conditions are especially useful for deciding where a job should run:

- **Lowest unmerged PR** — the PR currently at the bottom of the remaining stack. Because it targets the stack base directly, `github.event.pull_request.stack.base.ref` equals `github.event.pull_request.base.ref`.
- **Top PR** — the last PR in the stack, containing the full set of changes. It's the PR where `github.event.pull_request.stack.position` equals `github.event.pull_request.stack.size`.

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Run for the lowest unmerged PR in the stack
        if: github.event.pull_request.stack != null && github.event.pull_request.stack.base.ref == github.event.pull_request.base.ref
        run: echo "Lowest unmerged PR in the stack"

      - name: Run for the top PR in the stack
        if: github.event.pull_request.stack != null && github.event.pull_request.stack.position == github.event.pull_request.stack.size
        run: echo "Top PR in the stack"
```

As PRs merge from the bottom up, the lowest unmerged PR changes: once the bottom PR lands, the next PR is rebased to target the stack base directly, so it becomes the new lowest unmerged PR on the following workflow run. You can also gate on the original bottom PR with `github.event.pull_request.stack.position == 1`, or on any specific layer using `position`.

### Do all previous PRs need to be passing checks before I can merge?

Yes. In order to merge a PR in the stack, **all PRs below it** must also have passing checks and meet all merge requirements. For example, in a stack of `main <- PR1 <- PR2 <- PR3`, if you want to merge PR #3, both PR #1 and PR #2 must have passing checks, required reviews, and satisfy all branch protection rules.

### Is a linear history required?

Yes. There must be a **fully linear history** between each of the branches in the stack. This is a strict requirement for merging.

If the stack is not linear (e.g., after changes were pushed to a lower branch), you can fix it in two ways:

- **From the CLI** — Run `gh stack rebase` to perform a cascading rebase locally and then push with `gh stack push`.
- **From the UI** — Click the **Rebase Stack** button in the merge box to trigger a server-side cascading rebase. This rebases the entire stack on top of the latest trunk, updates every unmerged branch on top of its base branch, and force-pushes the results. See [Rebasing from the UI](/gh-stack/guides/ui/#rebasing-from-the-ui) for details.

## Merging Stacked PRs

### What conditions need to be met for a Stacked PR to be mergeable?

Every PR in a stack must meet the same merge requirements as a PR targeting the stack base (e.g., `main`): required reviews, passing CI checks, CODEOWNER approvals, and a linear history. All PRs below it must also meet these requirements. See the [Checks, Rules & Requirements](#checks-rules--requirements) section above for details.

### Can I bypass the rules to merge a Stacked PR?

Not yet — bypassing rules is coming soon, but currently unavailable for stacked PRs. You can't bypass a stack's branch protection rules or rulesets to merge it before its requirements are met, so every PR in the stack must satisfy its rules and required checks before the stack can land.

### Can I enable auto-merge on a Stacked PR?

Not yet — auto-merge is coming soon, but currently unavailable for stacked PRs, for both direct merges and the merge queue. You can't set a PR in a stack to land automatically once its requirements are met. Until then, merge the stack (or the part you want to land) yourself once its PRs are ready.

### How does merging a stack of PRs differ from merging a regular PR?

Stacks merge from the bottom up. When you click merge on a PR in a stack, that PR and all unmerged PRs below it land on the base branch together; PRs above remain open, and the remaining stack is automatically rebased so the next PR targets your base branch directly. With a direct merge the group lands as a single atomic operation; through a merge queue the PRs enter the queue together and are evaluated individually, from the bottom up.

### What happens when you merge a PR in the middle of the stack?

When you click merge on a PR in the middle of the stack, that PR and all unmerged PRs below it land on the base branch together, ordered from the bottom up in the resulting history. PRs above the selected one remain open. After the merge, the lowest unmerged PR is updated to target the stack base directly, and a cascading rebase runs across the remaining branches.

It is not possible to merge a middle PR in isolation: the PRs below it always merge with it.

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

With rebase merge, the commits from each PR in the stack are replayed onto the base branch, creating a linear history without merge commits. The full set of commits lands on the base branch.

### Do all PRs get merged at once or one at a time?

It depends on the merge method:

- **Direct merge** — All the included PRs land in a single atomic operation. The selected PR and every unmerged PR below it are merged together onto the base branch at once, ordered from the bottom up. Either the whole group lands, or if any part fails, none of it does.
- **Merge queue** — The PRs enter the queue together and are evaluated individually, from the bottom up. If a PR fails while in the queue, it and all its descendants are ejected, while the PRs below it are unaffected.

### Can I merge only part of a stack? What happens to the remaining unmerged PRs?

Yes, partial stack merges are supported. After the merge, the lowest unmerged PR is updated to explicitly target the stack base (e.g., `main`). A cascading rebase is also automatically run to rebase the remaining unmerged branches.

### What happens if I add a new PR after the whole stack has merged?

Once every PR in a stack has merged, the stack is complete and can't be extended — a new PR on top would target the trunk directly, which no longer chains onto the merged PRs. When you run `gh stack submit` with new branches on top of a fully merged stack, the CLI automatically starts a **new** stack rooted at the trunk for those branches and creates it on GitHub. The original, fully merged stack is left untouched.

### What happens if you close a PR in the middle of the stack?

Closing a PR in the middle of the stack will block all PRs above it from being mergeable. The stack relationship is preserved, so if you want to open a different PR or modify the stack, you will need to unstack and then re-create the stack.

### What happens when there is an error merging a PR in the middle of a stack?

Pre-merge checks run before any merge attempt, but a merge can still fail (e.g., due to a merge conflict or intermittent failure). The behavior depends on the merge method:

- **Direct merge** — The merge is atomic. If any part fails, the entire operation is rolled back and nothing is merged.
- **Merge queue** — Because each PR is evaluated individually, a failure ejects that PR and all its descendants (the PRs stacked above it) from the queue, while the PRs below it are unaffected. You can fix the issue on the failed PR and requeue the ejected PRs.

### Do Stacked PRs support merge queue?

Yes, Stacked PRs fully support merging via GitHub merge queue. When you merge a stack through the merge queue:

- **All PRs in the stack enter the queue together** in the correct order and are evaluated individually, from the bottom up.
- **If a PR is ejected from the merge queue** (for example, because it fails), that PR and all its descendants are ejected too, while the PRs below it are unaffected.
- **The queue makes a best-effort attempt to keep the stack together** in a single merge group. If the stack is too large to fit, it lands across consecutive merge groups: as much of the stack as fits goes into the current group, and the remaining PRs continue in subsequent groups until the full stack has landed. The stack order is preserved, so downstack PRs are merged before upstack PRs.

### How do I merge a stack programmatically?

Stacks are merged through GitHub's asynchronous [Merge API](/gh-stack/reference/merge-api/). The legacy synchronous merge APIs (REST and GraphQL) do not support stack merges. You submit a merge request for a PR and poll for the result; every PR in the stack up to and including the one you request is merged into the base branch. The [`gh stack merge`](/gh-stack/reference/cli/#gh-stack-merge) CLI command is built on this API and is the easiest way to merge from the command line.

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

If the provided branches already have open PRs, `link` will use them. If not, it creates draft PRs by default with the correct base branch chaining.

To add more to the stack, pass the stack number (shown in the GitHub stack UI) as the first argument, followed by just the new PRs or branches — you no longer need to re-list the PRs already in the stack:

```bash
# 42 is the stack number; change4 and change5 are appended to its top
gh stack link 42 change4 change5
```

You can also pass the full list of PRs/branches (without a leading stack number) to create a stack or additively update an existing one:

```bash
gh stack link 123 124 125 change4 change5
```

You can also use `--base` to specify a different trunk branch and `--open` to mark PRs as ready for review:

```bash
gh stack link --base develop --open change1 change2 change3
```

Alternatively, if you want full local stack tracking (for commands like `rebase`, `sync`, and navigation), you can adopt existing branches to local tracking with `gh stack init`:

```bash
gh stack init change1 change2 change3
gh stack submit
```
