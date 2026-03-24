---
name: gh-stack
description: >
  Manage stacked branches and pull requests with the gh-stack GitHub CLI extension.
  Use when the user wants to create, push, rebase, sync, navigate, or view stacks of
  dependent PRs. Triggers on tasks involving stacked diffs, dependent pull requests,
  branch chains, or incremental code review workflows.
metadata:
  author: github
  version: "0.0.1"
---

# gh-stack

`gh stack` is a [GitHub CLI](https://cli.github.com/) extension for managing **stacked branches and pull requests**. A stack is an ordered list of branches where each branch builds on the one below it, rooted on a trunk branch (typically the repo's default branch). Each branch maps to one PR whose base is the branch below it, so reviewers see only the diff for that layer.

```
main (trunk)
 └── auth-layer     → PR #1 (base: main)          - bottom (closest to trunk)
  └── api-endpoints → PR #2 (base: auth-layer)
   └── frontend     → PR #3 (base: api-endpoints) - top (furthest from trunk)
```

The **bottom** of the stack is the branch closest to the trunk, and the **top** is the branch furthest from the trunk. Each branch inherits from the one below it. Navigation commands (`up`, `down`, `top`, `bottom`) follow this model: `up` moves away from trunk, `down` moves toward it.

## When to use this skill

Use this skill when the user wants to:

- Break a large change into a chain of small, reviewable PRs
- Create, rebase, push, or sync a stack of dependent branches
- Navigate between layers of a branch stack
- View the status of stacked PRs
- Clean up a stack after PRs are merged

## Prerequisites

The GitHub CLI (`gh`) v2.0+ must be installed and authenticated. Install the extension with:

```bash
gh extension install github/gh-stack
```

## Agent rules

1. **Always supply branch names as positional arguments** to `init`, `add`, and `checkout`.
2. **Always use `--auto` when pushing** to skip PR title prompts.
3. **Always use `--json` when viewing** to get structured output.
4. **Use `--remote <name>` when multiple remotes are configured**, or set `remote.pushDefault` in git config.
5. **Avoid branches shared across multiple stacks.** If a branch belongs to multiple stacks, commands exit with code 6. Check out a non-shared branch first.

## Quick reference

| Task | Command |
|------|---------|
| Create a stack | `gh stack init -p feat auth` |
| Create a stack with explicit branch names (no prefix) | `gh stack init branch-a` |
| Adopt existing branches | `gh stack init --adopt branch-a branch-b` |
| Set custom trunk | `gh stack init --base develop branch-a` |
| Add a branch to stack | `gh stack add branch-name` |
| Add branch + stage all + commit | `gh stack add -Am "message" new-branch` |
| Add branch + stage tracked + commit | `gh stack add -um "message" new-branch` |
| Push + create PRs | `gh stack push --auto` |
| Push as drafts | `gh stack push --auto --draft` |
| Push without creating PRs | `gh stack push --skip-prs` |
| Push to specific remote | `gh stack push --auto --remote origin` |
| Sync (fetch, rebase, push) | `gh stack sync` |
| Sync with specific remote | `gh stack sync --remote origin` |
| Rebase entire stack | `gh stack rebase` |
| Continue after conflict | `gh stack rebase --continue` |
| Abort rebase | `gh stack rebase --abort` |
| View stack details (JSON) | `gh stack view --json` |
| Switch branches up/down in stack | `gh stack up [n]` / `gh stack down [n]` |
| Switch to top/bottom branch | `gh stack top` / `gh stack bottom` |
| Check out by PR | `gh stack checkout 42` |
| Check out by branch | `gh stack checkout feature-auth` |
| Remove stack | `gh stack unstack --local` |

---

## Workflows

### End-to-end: create a stack from scratch

```bash
# 1. Initialize a stack with a prefix and first branch (feat/auth)
gh stack init -p feat auth

# 2. Write code and commit on the first branch
cat > auth.go << 'EOF'
package auth

func Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // verify token
        next.ServeHTTP(w, r)
    })
}
EOF
gh stack add -Am "Add auth middleware"
# → commits on feat/auth (no commits yet, so stays on current branch)

# 3. Write more code and create the next layer
cat > api.go << 'EOF'
package api

func RegisterRoutes(mux *http.ServeMux) {
    mux.HandleFunc("/users", handleUsers)
}
EOF
gh stack add -Am "Add API routes" api-routes
# → creates feat/api-routes, checks it out, commits there

# 4. Add a third layer
cat > api_test.go << 'EOF'
package api

func TestRegisterRoutes(t *testing.T) {
    // test routes
}
EOF
gh stack add -Am "Add API tests" api-tests
# → creates feat/api-tests, checks it out, commits there

# 5. Push everything and create draft PRs
gh stack push --auto --draft

# 6. Verify the stack
gh stack view --json
```

### Modify a mid-stack branch and sync

This is the most common agent task after initial creation: change a branch that isn't at the top, then rebase and push.

```bash
# 1. Navigate to the branch that needs changes
gh stack bottom
# or: gh stack checkout feat/auth
# or: gh stack checkout 42  (by PR number)

# 2. Make changes and commit
cat > auth.go << 'EOF'
package auth
// updated implementation
EOF
git add -A && git commit -m "Fix auth token validation"

# 3. Rebase everything above this branch
gh stack rebase --upstack

# 4. Push the updated stack
gh stack push --auto
```

### Routine sync after merges

```bash
# Single command: fetch, rebase, push, sync PR state
gh stack sync
```

### Squash-merge recovery

When a PR is squash-merged on GitHub, the original branch's commits no longer exist in the trunk history. `gh stack` detects this automatically and uses `git rebase --onto` to correctly replay remaining commits.

```bash
# After PR #1 (feat/auth) is squash-merged on GitHub:
gh stack sync
# → fetches latest, detects the merge, fast-forwards trunk
# → rebases feat/api-routes onto updated trunk using --onto (skips merged branch)
# → rebases feat/api-tests onto feat/api-routes
# → pushes updated branches
# → reports: "Merged: #1"

# Verify the result
gh stack view --json
# → feat/auth shows "isMerged": true, "state": "MERGED"
# → feat/api-routes and feat/api-tests show updated heads
```

If `sync` hits a conflict during this process, it restores all branches to their pre-rebase state and exits with code 3. See [Handle rebase conflicts](#handle-rebase-conflicts-agent-workflow) for the resolution workflow.

### Handle rebase conflicts (agent workflow)

```bash
# 1. Start the rebase
gh stack rebase

# 2. If exit code 3 (conflict):
#    - Parse stderr for conflicted file paths
#    - Read those files to find <<<<<<< / ======= / >>>>>>> markers
#    - Edit files to resolve conflicts
#    - Stage resolved files:
git add path/to/resolved-file.go

# 3. Continue the rebase
gh stack rebase --continue

# 4. If another conflict occurs, repeat steps 2-3

# 5. If unable to resolve, abort to restore everything
gh stack rebase --abort
```

### Parsing `--json` output

```bash
# Get stack state as JSON
output=$(gh stack view --json)

# Check if any branch needs a rebase
echo "$output" | jq '.branches[] | select(.needsRebase == true) | .name'

# Get all open PR URLs
echo "$output" | jq -r '.branches[] | select(.pr.state == "OPEN") | .pr.url'

# Find merged branches
echo "$output" | jq -r '.branches[] | select(.isMerged == true) | .name'

# Get the current branch
echo "$output" | jq -r '.currentBranch'

# Check if the stack is fully merged (all branches merged)
echo "$output" | jq '[.branches[] | .isMerged] | all'
```

### Clean up after all PRs are merged

```bash
gh stack unstack --local
```

---

## Commands

### Initialize a stack — `gh stack init`

Creates a new stack. Provide branch names as positional arguments.

```
gh stack init [branches...] [flags]
```

```bash
# Create a stack with new branches (branched from trunk)
gh stack init branch-a branch-b branch-c

# Use a different trunk branch
gh stack init --base develop branch-a branch-b

# Adopt existing branches into a stack
gh stack init --adopt branch-a branch-b branch-c

# Set a branch prefix for auto-naming (used by `add -m`)
gh stack init -p feat branch-a
```

| Flag | Description |
|------|-------------|
| `-b, --base <branch>` | Trunk branch (defaults to the repo's default branch) |
| `-a, --adopt` | Adopt existing branches instead of creating new ones |
| `-p, --prefix <string>` | Set a branch name prefix for auto-generated names |

**Behavior:**

- Creates any branches that don't already exist (branching from the trunk branch)
- In `--adopt` mode: validates all branches exist, rejects if any is already in a stack or has an existing PR
- Checks out the last branch in the list
- Enables `git rerere` so conflict resolutions are remembered across rebases

---

### Add a branch — `gh stack add`

Add a new branch on top of the current stack. Must be run while on the topmost branch (or the trunk if the stack has no branches yet). Always provide an explicit branch name.

```
gh stack add [branch] [flags]
```

```bash
# Create a new branch and switch to it
gh stack add api-routes

# Create a new branch, stage all changes, and commit
gh stack add -Am "Add API routes" api-routes

# Create a new branch, stage tracked files only, and commit
gh stack add -um "Fix auth bug" auth-fix
```

You can also create the branch first, then use regular git to make changes:

```bash
gh stack add api-routes
# ... write code ...
git add -A && git commit -m "Add API routes"
```

| Flag | Description |
|------|-------------|
| `-m, --message <string>` | Create a commit with this message |
| `-A, --all` | Stage all changes including untracked files (requires `-m`) |
| `-u, --update` | Stage tracked files only (requires `-m`) |

**Behavior notes:**

- `-A` and `-u` are mutually exclusive.
- When the current branch has no commits (e.g., right after `init`), `add -Am` commits directly on the current branch instead of creating a new one.
- If a prefix was set during `init`, the prefix is applied to branch names: `prefix/branch-name`.
- If called from a branch that is not the topmost in the stack, exits with code 5: `"can only add branches on top of the stack"`. Use `gh stack top` to switch first.

---

### Push branches and create PRs — `gh stack push`

Push all stack branches and create/update PRs.

```
gh stack push [flags]
```

```bash
# Push and auto-title new PRs
gh stack push --auto

# Push and create PRs as drafts
gh stack push --auto --draft

# Push branches only, no PR creation
gh stack push --skip-prs
```

| Flag | Description |
|------|-------------|
| `--auto` | Auto-generate PR titles without prompting |
| `--draft` | Create new PRs as drafts |
| `--skip-prs` | Push branches without creating or updating PRs |
| `--remote <name>` | Remote to push to (use if multiple remotes exist) |

**Behavior:**

- Pushes all active (non-merged) branches atomically (`--force-with-lease --atomic`)
- Creates a new PR for each branch that doesn't have one (base set to the first non-merged ancestor branch)
- Syncs PR metadata for branches that already have PRs

**PR title auto-generation (`--auto`):**

- Single commit on branch → uses the commit subject as the PR title, commit body as PR body
- Multiple commits on branch → humanizes the branch name (hyphens/underscores → spaces) as the title

**Output (stderr):**

- `Created PR #N for <branch>` for each newly created PR
- `PR #N for <branch> is up to date` for existing PRs
- `Pushed and synced N branches` summary

---

### Sync the stack — `gh stack sync`

Fetch, rebase, push, and sync PR state in a single command. This is the recommended command for routine synchronization.

```
gh stack sync [flags]
```

| Flag | Description |
|------|-------------|
| `--remote <name>` | Remote to fetch from and push to (use if multiple remotes exist) |

**What it does (in order):**

1. **Fetch** latest changes from the remote
2. **Fast-forward trunk** to match remote (skips if already up to date, warns if diverged)
3. **Cascade rebase** all stack branches onto their updated parents (only if trunk moved). Handles squash-merged PRs automatically with `--onto`. If a conflict is detected, **all branches are restored** to their pre-rebase state and the command exits with code 3 — see [Handle rebase conflicts](#handle-rebase-conflicts-agent-workflow) for the resolution workflow
4. **Push** all active branches atomically
5. **Sync PR state** from GitHub and report the status of each PR

**Output (stderr):**

- `✓ Fetched latest changes from origin`
- `✓ Trunk main fast-forwarded to <sha>` or `✓ Trunk main is already up to date`
- `✓ Rebased <branch> onto <base>` per branch (if base moved)
- `✓ Pushed N branches`
- `✓ PR #N (<branch>) — Open` per branch
- `Merged: #N, #M` for merged branches
- `✓ Stack synced`

---

### Rebase the stack — `gh stack rebase`

Pull from remote and cascade-rebase stack branches. Use this when `sync` reports a conflict or when you need finer control (e.g., rebase only part of the stack).

```
gh stack rebase [branch] [flags]
```

```bash
# Rebase the entire stack
gh stack rebase

# Rebase only branches from trunk to current branch
gh stack rebase --downstack

# Rebase only branches from current branch to top
gh stack rebase --upstack

# After resolving a conflict: stage files with `git add`, then:
gh stack rebase --continue

# Abort and restore all branches to pre-rebase state
gh stack rebase --abort
```

| Flag | Description |
|------|-------------|
| `--downstack` | Only rebase branches from trunk to the current branch |
| `--upstack` | Only rebase branches from the current branch to the top |
| `--continue` | Continue after resolving conflicts |
| `--abort` | Abort and restore all branches |
| `--remote <name>` | Remote to fetch from (use if multiple remotes exist) |

| Argument | Description |
|----------|-------------|
| `[branch]` | Target branch (defaults to the current branch) |

**Conflict handling:** See [Handle rebase conflicts](#handle-rebase-conflicts-agent-workflow) in the Workflows section for the full resolution workflow.

**Squash-merge detection:** If a branch's PR was squash-merged on GitHub, the rebase automatically uses `git rebase --onto` to correctly replay commits on top of the merge target. This is handled transparently.

**Rerere (conflict memory):** `git rerere` is enabled by `init` so previously resolved conflicts are auto-resolved in future rebases.

---

### View the stack — `gh stack view`

Display the current stack's branches, PR status, and recent commits. Use `--json` for structured output.

```
gh stack view [flags]
```

```bash
# Structured JSON output (recommended)
gh stack view --json
```

| Flag | Description |
|------|-------------|
| `--json` | Output stack data as JSON to stdout |

**`--json` output format:**

```json
{
  "trunk": "main",
  "prefix": "feat",
  "currentBranch": "feat/api-routes",
  "branches": [
    {
      "name": "feat/auth",
      "head": "abc1234...",
      "base": "def5678...",
      "isCurrent": false,
      "isMerged": true,
      "needsRebase": false,
      "pr": {
        "number": 42,
        "url": "https://github.com/owner/repo/pull/42",
        "state": "MERGED"
      }
    },
    {
      "name": "feat/api-routes",
      "head": "789abcd...",
      "base": "abc1234...",
      "isCurrent": true,
      "isMerged": false,
      "needsRebase": false,
      "pr": {
        "number": 43,
        "url": "https://github.com/owner/repo/pull/43",
        "state": "OPEN"
      }
    }
  ]
}
```

Fields per branch:
- `name` — branch name
- `head` — current HEAD SHA
- `base` — parent branch's HEAD SHA at last sync
- `isCurrent` — whether this is the checked-out branch
- `isMerged` — whether the PR has been merged
- `needsRebase` — whether the base branch is not an ancestor (non-linear history)
- `pr` — PR metadata (omitted if no PR exists). `state` is `"OPEN"` or `"MERGED"`.

> **Note:** `--short` outputs a compact text view with box-drawing characters and status icons. Prefer `--json` for programmatic use.

---

### Navigate the stack

Move between branches without remembering branch names. These commands are fully non-interactive.

```bash
gh stack up          # Move up one branch (further from trunk)
gh stack up 3        # Move up three branches
gh stack down        # Move down one branch (closer to trunk)
gh stack down 2      # Move down two branches
gh stack top         # Jump to the top of the stack (furthest from trunk)
gh stack bottom      # Jump to the bottom (first non-merged branch above trunk)
```

Navigation clamps to stack bounds. Merged branches are skipped when navigating from active branches.

---

### Check out a stack — `gh stack checkout`

Check out a locally tracked stack by PR number or branch name. Always provide the target as an argument.

```
gh stack checkout <pr-or-branch>
```

```bash
# By PR number
gh stack checkout 42

# By branch name
gh stack checkout feature-auth
```

Resolves the target against locally tracked stacks. Accepts a PR number, PR URL, or branch name. Checks out the matching branch.

> **Note:** This command only works with stacks that have been created locally (via `gh stack init`). Server-side stack discovery is not yet implemented.

---

### Remove a stack — `gh stack unstack`

Remove a stack from local tracking. Use `--local` to avoid warnings about unsupported server-side deletion.

```
gh stack unstack [branch] [flags]
```

```bash
# Remove from local tracking
gh stack unstack --local

# Specify a branch to identify which stack
gh stack unstack feature-auth --local
```

| Flag | Description |
|------|-------------|
| `--local` | Only delete the stack locally (recommended) |

| Argument | Description |
|----------|-------------|
| `[branch]` | A branch in the stack (defaults to the current branch) |

---

## Output conventions

- **Status messages** go to **stderr** with emoji prefixes: `✓` (success), `✗` (error), `⚠` (warning), `ℹ` (info).
- **Data output** (e.g., `view --json`) goes to **stdout**.
- When piping output, use `2>/dev/null` to suppress status messages if only data output is needed.

## Exit codes and error recovery

| Code | Meaning | Agent action |
|------|---------|-------------|
| 0 | Success | Proceed normally |
| 1 | Generic error | Read stderr for details; may indicate commit/push failure |
| 2 | Not in a stack | Run `gh stack init` to create a stack first |
| 3 | Rebase conflict | Parse stderr for conflicted file paths, resolve conflicts, run `gh stack rebase --continue` |
| 4 | GitHub API failure | Check `gh auth status`, retry the command |
| 5 | Invalid arguments | Fix the command invocation (check flags and arguments) |
| 6 | Disambiguation required | A branch belongs to multiple stacks. Run `gh stack checkout <specific-branch>` to switch to a non-shared branch first |
| 7 | Rebase already in progress | Run `gh stack rebase --continue` (after resolving conflicts) or `gh stack rebase --abort` to start over |

## Known limitations

1. **Stack disambiguation cannot be bypassed.** If the current branch is the trunk of multiple stacks, commands error with code 6. Check out a non-shared branch first.
2. **Multiple remotes require `--remote` or config.** If more than one remote is configured, pass `--remote <name>` or set `remote.pushDefault` in git config before running `push`, `sync`, or `rebase`.
3. **Merging PRs:** Merging Stacked PRs from the CLI is not supported yet. Direct users to open the PR URL in a browser to merge PRs.
4. **Server-side stack deletion is not supported.** Use `unstack --local`.
5. **Server-side stack discovery is not supported.** `checkout` only works with locally tracked stacks.
6. **PR title and body are auto-generated.** There is no flag to set a custom PR title or body during `push`. The title and body are generated from commit messages plus a footer. Use `gh pr edit` to modify PR title and body after creation.
