---
title: Restructuring Stacks
description: How to use `gh stack modify` to restructure a stack.
---

`gh stack modify` provides an interactive terminal UI for restructuring a stack locally. You can drop, fold, insert, rename, and reorder branches and then apply all your changes at once.

![The modify stack terminal UI](../../../assets/screenshots/modify-stack-tui.png)

## When to use modify

Use `modify` when you need to:
- **Remove** a branch from the stack
- **Combine** two branches into one
- **Insert** a new branch into the stack
- **Rename** a branch
- **Reorder** branches

## Prerequisites

Before running `modify`, ensure:
- You have an active stack checked out locally
- Your working tree is clean (no uncommitted changes)
- No rebase is in progress
- No PR in the stack is queued for merge
- Commit history is linear (run `gh stack rebase` first if needed)
- Git 2.36 or later
- Every stack branch is unoccupied or checked out in the invoking worktree

Linked worktrees are supported, but **distributed modify is temporarily rejected** before the TUI opens and rechecked before applying. A trunk checked out elsewhere does not block modify: trunk is only read. No worktrees are created, removed, detached, or automatically stashed.

This also applies when using a separate Git administration directory: modify can use its known main or linked origin, but discovering another main worktree's location may be unavailable. See [Separate Git administration directories](/gh-stack/guides/workflows/#separate-git-administration-directories).

## Opening the TUI

```sh
gh stack modify
```

The TUI shows your stack as a vertical list of branches with PR information, commits, and files changed. Merged branches appear as locked rows that cannot be modified. Press `?` for a help overlay describing all operations.

## Operations

### Drop (`x`)

Removes a branch and its commits from the stack. The local branch and any associated PR are preserved. Upstream branches are rebased to exclude the dropped branch's unique commits.

### Fold down (`d`)

Absorbs the selected branch's commits into the branch below it (toward trunk) via cherry-pick. The folded branch is removed from the stack.

### Fold up (`u`)

Absorbs the selected branch's commits into the branch above it (away from trunk). Since the branch above already contains the folded branch's commits in its history, this is handled by adjusting what is considered the first unique commit for the branch. The folded branch is removed from the stack.

### Insert below / above (`i` / `I`)

Inserts a new empty branch into the stack at the cursor position. Lowercase `i` inserts below the cursor (toward trunk); uppercase `I` inserts above the cursor (away from trunk). An inline prompt appears to enter the new branch name. The branch is created at apply time, pointing at the parent branch's tip.

### Rename (`r`)

Opens an inline prompt to enter a new name for the branch. The branch is renamed locally and in the stack metadata. On the next `submit`, the new branch name is pushed to GitHub.

### Reorder (`Shift+↓`/`Shift+↑`)

Moves the selected branch down (toward trunk) or up (away from trunk) in the stack. A cascading rebase adjusts all affected branches. Note: reordering and structural changes (drop/fold/insert/rename) cannot be mixed in the same session.

### Undo (`z`)

Reverses the most recent staged action. You can undo multiple times to step back through your changes.

## Applying changes

Press `Ctrl+S` to apply all staged changes. Nothing is modified until you save. The apply phase renames branches, inserts new branches, folds/drops branches, and runs a cascading rebase to create a linear commit history with the desired stack state.

### Handling conflicts

If a rebase conflict occurs during the apply phase, you have two options:

1. **Resolve and continue**: Fix the conflicts in your editor, stage with `git add`, then run `gh stack modify --continue` (you may need to do this multiple times)
2. **Abort**: Run `gh stack modify --abort` to abort the operation and restore the stack to the pre-modify state

If a second conflict occurs after continuing, the same options are available.

The conflict message identifies the originating worktree. Edit and stage the files **there**. You can invoke `--continue` or `--abort` from any linked worktree; Git operations still execute in the recorded origin without changing the invoking worktree's checkout.

If Git's recorded rebase or cherry-pick is no longer in progress, for example after an external `git rebase --abort`, `modify --continue` refuses and preserves the journal. Use `gh stack modify --abort` to recover through the saved state; continuation will not claim a new branch tip as completed modify work.

## After modifying

If a stack of PRs has been created on GitHub, run:

```sh
gh stack submit
```

This pushes the updated branches and updates their pull requests. With two or more PRs, the old stack is replaced; a single remaining PR is submitted without creating a new stack object.

The pending-modify journal is cleared only after all required PR submissions and updates succeed and the local catalog is saved. Failed updates or deselected branches without PRs leave it pending so you can complete the submission with `gh stack submit`.

## Aborting

If you want to discard all changes and restore the stack to its pre-modify state, run:

```sh
gh stack modify --abort
```

This also works if `modify` was interrupted (e.g., terminal crash). The shared `<common-dir>/gh-stack-modify-state` journal records the origin, original checkout, stack identity, and pre-modify snapshot before mutations. Git's native rebase/cherry-pick state stays in the origin's own Git directory.

Recovery restores changes made by this operation and the original checkout. If the owner is missing, refs were changed externally, or a restore/save fails, recovery stops and retains its journal instead of reporting success. Address the reported problem and retry `--abort`; do not delete the journal to bypass recovery. After a successful modify has reached pending-submit, `--abort` does not undo it and instead directs you to `submit`.

Clone-wide mutation serialization prevents another gh-stack mutation while modify is applying or paused; read-only views remain available. Pending-submit state is consumed only when submitting its matching stack, never an unrelated stack.

The mutation lock coordinates gh-stack processes only: arbitrary Git commands, editors, and other tools can still change refs or files. Keep affected worktrees idle during history rewrites. While paused, make only the requested conflict-resolution edits and staging in the reported worktree; do not add unrelated commits to branches that have not yet been processed.

Legacy journals must be continued or aborted in their original worktree before catalog migration. Nonconflicting legacy catalogs are consolidated with originals preserved; conflicts require reconciliation rather than choosing a definition automatically.

## Limitations

- Cannot modify merged branches (they are locked)
- Cannot split a branch into multiple branches
- Cannot move branches between different stacks
- Requires an interactive terminal
- Reordering and structural changes (drop/fold/insert/rename) cannot be mixed in the same session
- Distributed rename/fold/reorder support is deferred to separate work; all member branches must currently be available in one worktree
