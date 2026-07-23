# GitHub Stacked PRs

A GitHub CLI extension for managing stacked branches and pull requests.

Stacked PRs break large changes into a chain of small, reviewable pull requests that build on each other. `gh stack` automates the tedious parts — creating branches, keeping them rebased, setting correct PR base branches, and navigating between layers.

> [!NOTE]
> Stacked PRs is currently in private preview. This CLI and the referenced functionality will not work unless the feature has been enabled for your repository.
> You can sign up for the waitlist at [gh.io/stacksbeta](https://gh.io/stacksbeta).

## Installation

```sh
gh extension install github/gh-stack
```

Requires the [GitHub CLI](https://cli.github.com/) (`gh`) v2.0+.

## AI agent integration

Install the gh-stack skill so your AI coding agent knows how to work with stacked PRs and the `gh stack` CLI:

```sh
gh skill install github/gh-stack
```

## Quick start

```sh
# Start a new stack (creates and checks out the first branch)
gh stack init

# ... make commits on the first branch ...

# Add another branch on top
gh stack add api-endpoints
# ... make commits ...

# Push all branches
gh stack push

# View the stack
gh stack view

# Open a stack of PRs
gh stack submit
```

## How it works

A **stack** is an ordered list of branches where each branch builds on the one below it. The **bottom** of the stack is based on a **trunk** branch (typically `main`).

```
frontend      → PR #3 (base: api-endpoints) ← top
api-endpoints → PR #2 (base: auth-layer)
auth-layer    → PR #1 (base: main)          ← bottom
─────────────
main (trunk)
```

The **bottom** of the stack is the branch closest to the trunk, and the **top** is the branch furthest from it. Each branch inherits from the one below it. Navigation commands (`up`, `down`, `top`, `bottom`) follow this model: `up` moves away from trunk, `down` moves toward it.

When you submit, `gh stack` creates one PR per branch and links them together as a **Stack** on GitHub. Each PR's base is set to the branch below it in the stack, so reviewers see only the diff for that layer.

### Local tracking

Stack metadata is stored in `.git/gh-stack` (a JSON file, not committed to the repo). This tracks which branches belong to which stack and their ordering. Rebase state during interrupted rebases is stored separately in `.git/gh-stack-rebase-state`.

## Commands

### `gh stack init`

Initialize a new stack in the current repository.

```
gh stack init [flags] [branches...]
```

Initializes a new stack locally. In interactive mode (no arguments), prompts for a branch name and offers to use the current branch as the first layer.

When explicit branch names are given, existing branches are adopted automatically and any missing branches are created. The trunk defaults to the repository's default branch unless overridden with `--base`.

Enables `git rerere` automatically so that conflict resolutions are remembered across rebases.

| Flag | Description |
|------|-------------|
| `-b, --base <branch>` | Trunk branch for the stack (defaults to the repository's default branch) |

**Examples:**

```sh
# Interactive — prompts for branch names
gh stack init

# Non-interactive — specify branches upfront
gh stack init feature-auth feature-api feature-ui

# Use a different trunk branch
gh stack init --base develop feature-auth

# Adopt existing branches into a stack
gh stack init feature-auth feature-api
```

### `gh stack add`

Add a new branch on top of the current stack.

```
gh stack add [flags] [branch]
```

Creates a new branch at the current HEAD, adds it to the top of the stack, and checks it out. Must be run while on the topmost branch of a stack. If no branch name is given, prompts for one.

You can optionally stage changes and create a commit as part of the `add` flow. When `-m` is provided without an explicit branch name, the branch name is auto-generated in date+slug format (e.g., `03-24-add_login`).

| Flag | Description |
|------|-------------|
| `-A, --all` | Stage all changes (including untracked files); requires `-m` |
| `-u, --update` | Stage changes to tracked files only; requires `-m` |
| `-m, --message <string>` | Create a commit with this message before creating the branch |

> **Note:** `-A` and `-u` are mutually exclusive.

**Examples:**

```sh
# Create a branch by name
gh stack add api-routes

# Prompt for a branch name interactively
gh stack add

# Stage all changes, commit, and auto-generate the branch name
gh stack add -Am "Add login endpoint"

# Stage only tracked files, commit, and auto-generate the branch name
gh stack add -um "Fix auth bug"

# Commit already-staged changes and auto-generate the branch name
gh stack add -m "Add user model"

# Stage all changes, commit, and use an explicit branch name
gh stack add -Am "Add tests" test-layer

# Stage only tracked files, commit, and use an explicit branch name
gh stack add -um "Update docs" docs-layer

# Commit already-staged changes and use an explicit branch name
gh stack add -m "Refactor utils" cleanup-layer
```

### `gh stack checkout`

Check out a stack by its stack number, a pull request number, a PR URL, or a branch name.

```
gh stack checkout [<stack-number> | <pr-number> | <pr-url> | <branch>]
```

A bare number is interpreted first as a stack or PR number (repo-scoped identifiers shown in the GitHub UI). If nothing matches the number, it is tried as a branch name.

When a remote stack is referenced, the command fetches the stack on GitHub, pulls the branches, and sets up the stack locally. If the stack already exists locally and matches, it switches to the branch. If the local and remote stacks have different compositions, you'll be prompted to resolve the conflict.

When a branch name is provided, the command resolves it against locally tracked stacks only.

When run without arguments in an interactive terminal, opens a searchable picker listing every stack available to you — both the stacks tracked locally and the stacks that exist only on GitHub. Each row shows the stack number, its bottom and top branch, base branch, a status bar summarizing how many of its pull requests are merged, open, closed, or not yet pushed, and whether the stack is available locally or only on the remote. Filter with the All / Local / Remote tabs or type `/` to search; fully merged stacks are omitted. Selecting a remote-only stack clones it locally before switching to it.

**Examples:**

```sh
# Check out a stack by its stack number
gh stack checkout 7

# Check out a stack by PR number
gh stack checkout 42

# Check out a stack by PR URL
gh stack checkout https://github.com/owner/repo/pull/42

# Check out a stack by branch name (local only)
gh stack checkout feature-auth

# Interactive — pick from all available stacks (local and remote)
gh stack checkout
```

### `gh stack rebase`

Pull from remote and do a cascading rebase across the stack.

```
gh stack rebase [flags] [branch]
```

Fetches the latest changes from `origin`, then ensures each branch in the stack has the tip of the previous layer in its commit history. Rebases branches in order from trunk upward. If a branch's PR has been merged, the rebase automatically switches to `--onto` mode to correctly replay commits on top of the merge target.

If a rebase conflict occurs, the operation pauses and prints the conflicted files with line numbers. Resolve the conflicts, stage with `git add`, and continue with `--continue`. To undo the entire rebase, use `--abort` to restore all branches to their pre-rebase state.

| Flag | Description |
|------|-------------|
| `--downstack` | Only rebase branches from trunk to the current branch |
| `--upstack` | Only rebase branches from the current branch to the top |
| `--no-trunk` | Skip trunk — only rebase stack branches onto each other (no fetch, no trunk rebase) |
| `--continue` | Continue the rebase after resolving conflicts |
| `--abort` | Abort the rebase and restore all branches to their pre-rebase state |
| `--remote <name>` | Remote to fetch from (defaults to auto-detected remote) |
| `--committer-date-is-author-date` | Set the committer date to the author date during rebase. Alias: `--preserve-dates` |

| Argument | Description |
|----------|-------------|
| `[branch]` | Target branch (defaults to the current branch) |

**Examples:**

```sh
# Rebase the entire stack
gh stack rebase

# Only rebase branches below the current one
gh stack rebase --downstack

# Only rebase branches above the current one
gh stack rebase --upstack

# Rebase stack branches without pulling from or rebasing with trunk
gh stack rebase --no-trunk

# After resolving a conflict
gh stack rebase --continue

# Abort rebase and restore everything
gh stack rebase --abort

# Rebase and preserve committer date as author date
gh stack rebase --committer-date-is-author-date
```

### `gh stack modify`

Interactively restructure the current stack.

```
gh stack modify [flags]
```

Opens a terminal UI for restructuring a stack. You can drop, fold, insert, rename, and reorder branches. All the changes are staged during the preview and applied at once on save.

If the stack of PRs has been created on GitHub, run `gh stack submit` afterwards to push the changes and recreate the stack.

| Flag | Description |
|------|-------------|
| `--continue` | Continue after resolving conflicts |
| `--abort` | Abort the modify session and restore the stack to its pre-modify state |

**Operations:**

- **Drop** (`x`): Remove a branch and its commits from the stack. Local branch and associated PR are preserved.
- **Fold down** (`d`): Absorb a branch's commits into the branch below (toward trunk). Folded branch removed from stack.
- **Fold up** (`u`): Absorb a branch's commits into the branch above (away from trunk). Folded branch removed from stack.
- **Insert** (`i`/`I`): Insert a new empty branch into the stack. `i` inserts below the cursor; `I` inserts above.
- **Reorder** (`Shift+↑`/`Shift+↓`): Move a branch up (away from trunk) or down (toward trunk) in the stack.
- **Rename** (`r`): Rename a branch locally and in the stack metadata.
- **Undo** (`z`): Undo the last staged action.

**Keybindings:**

| Key | Action |
|-----|--------|
| `↑`/`↓` | Navigate branch list |
| `f` | View files changed |
| `c` | View commits |
| `x` | Drop branch |
| `r` | Rename branch |
| `i/I` | Insert branch below/above |
| `d/u` | Fold branch down/up |
| `Shift+↑`/`Shift+↓` | Move branch up/down |
| `z` | Undo last action |
| `Ctrl+S` | Apply all changes |
| `q`/`Esc` | Cancel and exit |
| `?` | Help |

**Preconditions:**
- Must have an active stack checked out locally
- Working tree must be clean
- No rebase in progress
- No PR in the stack is queued for merge
- Commit history must be linear

**Examples:**

```sh
# Open the modify TUI
gh stack modify

# Continue after resolving a conflict
gh stack modify --continue

# Abort and restore to the previous state
gh stack modify --abort
```

### `gh stack sync`

Fetch, rebase, push, and sync PR state in a single command.

```
gh stack sync [flags]
```

Performs a synchronization of the entire stack:

1. **Fetch** — fetches the latest changes from `origin`.
2. **Reconcile the remote stack** — mirrors the GitHub stack locally. When PRs have been added to the stack on GitHub (the remote is ahead of your local stack), their branches are pulled down and appended to your local stack automatically. When the local and remote stacks have genuinely diverged (for example, you added a branch locally while different PRs were added to the stack on GitHub), you are prompted to resolve (see [Diverged stacks](#diverged-stacks) below). In a non-interactive terminal a divergence aborts the sync (nothing is pushed or updated).
3. **Fast-forward trunk** — fast-forwards the trunk branch to match the remote (skips if diverged).
4. **Cascade rebase** — rebases all stack branches onto their updated parents (only if trunk moved). If a conflict is detected, all branches are restored to their original state, and you are advised to run `gh stack rebase` to resolve conflicts interactively.
5. **Push** — pushes all branches (uses `--force-with-lease` if a rebase occurred).
6. **Sync PRs** — syncs PR state from GitHub and reports the status of each PR.
7. **Sync the stack** — links the stack's open PRs into a stack on GitHub, creating the remote stack object if it doesn't exist yet or updating it if it's partially formed. This only happens when two or more PRs exist; sync never opens PRs (use `gh stack submit` for that).
8. **Prune** — in interactive terminals, prompts to delete local branches for merged PRs. Use `--prune` to prune automatically.

A clean remote-ahead update (PRs added on top of your local stack) is pulled down automatically without prompting, so `sync` is safe to run in automation. Sync only prompts when the stacks have truly diverged.

#### Diverged stacks

When neither stack is a clean prefix of the other — for example, you added a branch locally while separate PRs were added to the same stack on GitHub — sync cannot merge the two automatically. In an interactive terminal it offers three choices:

- **Use the remote stack as the source of truth** — replaces your local stack composition with the remote's, pulling any missing branches. If you were on a branch that the remote stack no longer contains, you're moved to the nearest surviving branch. Requires a clean working state with no uncommitted changes.
- **Delete the stack on GitHub** — deletes the stack object on GitHub and stops the sync. Your PRs and local branches are untouched (only the stack on GitHub is removed); recreate the stack with `gh stack submit` (run `gh stack modify` first if you want to change its structure). This is the way to make GitHub match your local stack, because `submit` — unlike `sync` — also creates PRs for any branches you haven't submitted yet.
- **Cancel** — aborts the sync without pushing branches or updating any PRs.

In a non-interactive terminal, a divergence aborts the sync (exit success) without pushing branches or updating PRs; resolve it by unstacking and recreating the stack.

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to fetch from and push to (defaults to auto-detected remote) |
| `--prune` | Delete local branches for merged PRs |

**Examples:**

```sh
gh stack sync

# Sync and automatically prune merged branches
gh stack sync --prune
```

### `gh stack push`

Push active branches in the current stack to the remote.

```
gh stack push [flags]
```

Pushes every active branch (excluding merged and queued branches) in one `git push` using explicit per-branch `--force-with-lease` checks. The update is not atomic: branches whose leases pass may update even if another branch is rejected. Fix the rejected branch and rerun the command; branches already updated will be unchanged. This command does not create or update pull requests — use `gh stack submit` for that.

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

**Examples:**

```sh
gh stack push
gh stack push --remote upstream
```

### `gh stack submit`

Push all branches and create/update PRs and the stack on GitHub.

```
gh stack submit [flags]
```

Creates a Stacked PR for every branch in the stack, pushing branches to the remote.

After creating PRs, `submit` automatically creates a **Stack** on GitHub to link the PRs together. If the stack already exists on GitHub (e.g., from a previous submit), new PRs will be added to the top of the stack.

If every PR in the stack has already been merged, that stack is complete and can't be extended — a new PR on top would target the trunk directly rather than chaining onto the merged PRs. In that case `submit` automatically starts a **new** stack rooted at the trunk for your unmerged branches and creates it on GitHub, leaving the merged stack untouched.

In an interactive terminal, `submit` opens a full-screen, mouse- and keyboard-driven editor on a single screen. Every branch without a PR is included by default — deselect any you don't want on the left panel (<kbd>Ctrl</kbd>+<kbd>X</kbd>). Because each PR builds on the branch below it, deselecting a branch also deselects the ones stacked above it, and re-including a branch re-includes the ones below it. Draft each PR's title, description (with a markdown preview and `$EDITOR` escape), and choose ready-for-review or draft on the right, then submit them all at once with <kbd>Ctrl</kbd>+<kbd>S</kbd>. Pass `--auto` (or run in CI) to skip the editor and use auto-generated titles.

If the branches already have open PRs but no stack exists on GitHub, you will have the option to link the PRs into a stack with <kbd>Ctrl</kbd>+<kbd>B</kbd>.

In the editor, new PRs default to ready for review; flip any PR to draft with the ready ↔ draft toggle. With `--auto`, new PRs are created as drafts unless you pass `--open`.

| Flag | Description |
|------|-------------|
| `--auto` | Skip the editor and use auto-generated PR titles |
| `--open` | Mark new and existing PRs as ready for review |
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

**Examples:**

```sh
gh stack submit
gh stack submit --auto
gh stack submit --open
```

### `gh stack link`

Link PRs into a stack on GitHub without local tracking.

```
gh stack link [flags] <branch-or-pr> <branch-or-pr> [...]
```

Creates or updates a stack on GitHub from branch names or PR numbers/URLs. This command does not store or modify any `gh stack` local tracking state. It is designed for users who manage branches with other tools locally (e.g., jj, Sapling, git-town) and want to simply open a stack of PRs.

Arguments are provided in stack order (bottom to top). Branch arguments are automatically pushed to the remote before creating or looking up PRs. For branches that already have open PRs, those PRs are used. For branches without PRs, new PRs are created automatically with the correct base branch chaining. Existing PRs whose base branch doesn't match the expected chain are corrected automatically.

If the PRs are not yet in a stack, a new stack is created. If some of the PRs are already in a stack, the existing stack is updated to include the new PRs. Existing PRs are never removed from a stack — the update is additive only.

| Flag | Description |
|------|-------------|
| `--base <branch>` | Base branch for the bottom of the stack (defaults to the repository's default branch) |
| `--open` | Mark new and existing PRs as ready for review |
| `--remote <name>` | Remote to push to (defaults to auto-detected remote) |

**Examples:**

```sh
# Link branches into a stack (pushes branches, creates PRs, creates stack)
gh stack link feature-auth feature-api feature-ui

# Link existing PRs by number
gh stack link 10 20 30

# Link existing PRs by URL
gh stack link https://github.com/owner/repo/pull/10 https://github.com/owner/repo/pull/20

# Add branches to an existing stack of PRs
gh stack link 42 43 feature-auth feature-ui

# Use a different base branch and mark PRs as ready for review
gh stack link --base develop --open feat-a feat-b feat-c
```

### `gh stack merge`

Merge one or multiple stacked PRs at once.

```
gh stack merge [<stack-number> | <pr-number>]
```

All members of the stack up to and including your chosen pull request are merged into the base branch in a single, all-or-nothing operation: if any PR can't be merged, none are.

With no argument, the current active local stack is used. Pass a stack number to merge a stack you don't have checked out (a purely remote operation), or a pull request number to merge directly up to that PR.

In an interactive terminal, a short wizard walks you through three steps — choose which PRs to merge, pick the merge method, and confirm. In a non-interactive terminal, or with `--yes`, the whole stack (or everything up to the given PR) is merged without prompting, using your last-used merge method unless one is specified.

Only basic pull request state is checked before merging (open and not a draft); GitHub evaluates branch protection and repository rules when the merge runs, so any such failure is reported back to you. **Admin bypass is not supported** for stacked PR merges at this time.

Note that this command does not work with merge queues. If the stack's base branch uses a merge queue, use `gh pr merge` instead.

| Flag | Description |
|------|-------------|
| `--merge-method <method>` | Merge method to use: `merge`, `squash`, or `rebase` |
| `--merge` / `--squash` / `--rebase` | Shorthands for the corresponding merge method |
| `-y, --yes` | Merge without prompting for confirmation |

**Examples:**

```sh
# Merge the current stack (interactive picker)
gh stack merge

# Merge a stack you don't have checked out, by stack number
gh stack merge 7

# Merge everything up to and including PR #42
gh stack merge 42

# Merge the whole current stack without prompting, squashing
gh stack merge --yes --squash
```

### `gh stack view`

View the current stack.

```
gh stack view [flags]
```

Shows all branches in the stack, their ordering, PR links, and the most recent commit with a relative timestamp. Output is piped through a pager (respects `GIT_PAGER`, `PAGER`, or defaults to `less -R`).

| Flag | Description |
|------|-------------|
| `-s, --short` | Compact output (branch names only) |
| `--json` | Output stack data as JSON |

**Examples:**

```sh
gh stack view
gh stack view --short
gh stack view --json
```

### `gh stack unstack`

Remove a stack from local tracking and unstack it on GitHub. Also available as `gh stack delete`.

```
gh stack unstack [<stack-number>] [flags]
```

With no argument, the command targets the active stack — the one that contains the currently checked out branch — unstacking it on GitHub and removing local tracking.

Provide a stack number (the identifier shown in the github.com stack UI) to unstack a specific stack on GitHub. This works from anywhere in the repository, whether or not the stack is checked out locally — the number is unstacked directly through the GitHub API. If the stack is also tracked locally, its local tracking is removed as well.

Use `--local` to only remove local tracking without contacting GitHub.

GitHub decides which pull requests can be unstacked: PRs that are queued for merge or have auto-merge enabled are left stacked. When some pull requests remain stacked, the stack is kept (and local tracking, if any, is unchanged).

| Flag | Description |
|------|-------------|
| `--local` | Only remove the stack locally (keep it on GitHub) |

**Examples:**

```sh
# Remove the current stack from local tracking and GitHub
gh stack unstack

# Unstack a specific stack by its number
gh stack unstack 7

# Only remove local tracking
gh stack unstack --local
```

### Navigation

Move between branches in the current stack without having to remember branch names.

```sh
gh stack up [n]      # Move up n branches (default 1)
gh stack down [n]    # Move down n branches (default 1)
gh stack top         # Jump to the top of the stack
gh stack bottom      # Jump to the bottom of the stack
gh stack trunk       # Jump to the trunk branch
gh stack switch      # Interactively pick a branch to switch to
```

Navigation commands clamp to the bounds of the stack — moving up from the top or down from the bottom is a no-op with a message. If you're on the trunk branch, `up` moves to the first stack branch.

**Examples:**

```sh
gh stack up          # move up one layer
gh stack up 3        # move up three layers
gh stack down
gh stack top
gh stack bottom
gh stack trunk       # jump to the trunk branch (e.g., main)
gh stack switch      # shows an interactive picker
```

### `gh stack feedback`

Share feedback about gh-stack.

```
gh stack feedback [title]
```

Opens a GitHub Discussion in the [gh-stack repository](https://github.com/github/gh-stack) to submit feedback. Optionally provide a title for the discussion post.

**Examples:**

```sh
gh stack feedback
gh stack feedback "Support for reordering branches"
```

### `gh stack alias`

Create a short command alias so you can type less.

```
gh stack alias [flags] [name]
```

Installs a small wrapper script into `~/.local/bin/` that forwards all arguments to `gh stack`. The default alias name is `gs`, but you can choose any name by passing it as an argument. After setup, you can run `gs push` instead of `gh stack push`.

On Windows, automatic alias creation is not supported — the command prints manual instructions for creating a batch file or PowerShell function.

| Flag | Description |
|------|-------------|
| `--remove` | Remove a previously created alias |

**Examples:**

```sh
# Create the default alias (gs)
gh stack alias
#    → now "gs push", "gs view", etc. all work

# Create a custom alias
gh stack alias gst

# Remove an alias
gh stack alias --remove
gh stack alias gst --remove
```

## Typical workflow

```sh
# 1. Start a stack (creates and checks out the first branch)
gh stack init

# 2. Work on the first layer
#    ... write code, make commits ...

# 3. Add the next layer
gh stack add api-routes
#    ... write code, make commits ...

# 4. Push everything and create Stacked PRs
gh stack submit

# 5. Reviewer requests changes on the first PR
gh stack bottom
#    ... make changes, commit ...

# 6. Rebase the rest of the stack on top of your fix
gh stack rebase

# 7. Push the updated branches
gh stack push

# 8. When the first PR is merged, sync the stack
gh stack sync
# → prompts to prune merged branches (or use --prune to prune automatically and avoid the prompt)
```

## Abbreviated workflow

If you want to minimize keystrokes, use the `-Am` flags to fold staging, committing, and branch creation into a single command. When you don't pass a branch name, one is auto-generated from the commit message in date+slug format (e.g., `03-24-auth_middleware`).

When a branch has no commits yet (e.g., right after `init`), `add -Am` stages and commits directly on that branch instead of creating a new one. Once a branch has commits, `add -Am` creates a new branch, checks it out, and commits there.

```sh
# 1. Start a stack
gh stack init auth
#    → creates auth and checks it out

# 2. Write code for the first layer
#    ... write code ...

# 3. Stage and commit on the current branch
gh stack add -Am "Auth middleware"
#    → auth has no commits yet, so the commit lands here
#      (no new branch is created)

# 4. Write code for the next layer
#    ... write code ...

# 5. Create the next branch and commit
gh stack add -Am "API routes"
#    → auth already has commits, so a new branch is created from the
#      commit message, checked out, and the commit lands there

# 6. Keep going
#    ... write code ...

gh stack add -Am "Frontend components"
#    → creates another branch and commits there

# 7. Push everything and create PRs
gh stack submit
```

Compared to the typical workflow, there's no need to name branches, run `git add`, or run `git commit` separately. Each `gh stack add -Am "..."` does it all. Pass an explicit branch name any time you want to control it: `gh stack add -Am "API routes" api-routes`.

## Terminal theme

The interactive screens (`submit`, `modify`, and `view`) and all colored command output (status messages, prompts) automatically adapt their colors to your terminal's background, so they're readable on both dark and light themes. The background is detected from the terminal; if a terminal doesn't report it (some SSH or `tmux` setups), the dark palette is used.

Set `GH_STACK_THEME` to force a palette if detection is wrong:

| Value | Behavior |
|-------|----------|
| `auto` (default) | Detect from the terminal background |
| `light` | Force the light palette |
| `dark` | Force the dark palette |

```bash
# Force the light palette
export GH_STACK_THEME=light && gh stack view
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic error |
| 2 | Not in a stack / stack not found |
| 3 | Rebase conflict |
| 4 | GitHub API failure |
| 5 | Invalid arguments or flags |
| 6 | Disambiguation required (branch belongs to multiple stacks) |
| 7 | Rebase already in progress |
| 8 | Stack is locked by another process |

## License 

This project is licensed under the terms of the MIT open source license. Please refer to the [LICENSE](LICENSE) file for the full terms.

## Maintainers 

See [CODEOWNERS](CODEOWNERS)

## Support

See [SUPPORT.md](SUPPORT.md)

Please note that the Stacked PRs feature is currently in private preview and **gh-stack** will not work without that feature enabled.
