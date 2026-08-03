---
name: gh-stack
description: >
  Creates and manages stacked branches and pull requests with the gh-stack GitHub CLI
  extension. Use when working with stacked PRs, stacked diffs, dependent pull requests,
  or branch chains, when running any gh stack command, or before splitting a large
  change into a chain of dependent PRs.
metadata:
  author: github
  version: "0.1.0"
---

# gh-stack

`gh stack` is a [GitHub CLI](https://cli.github.com/) extension for stacked branches and pull
requests. A stack is an ordered chain of branches rooted on a trunk, where each branch has one PR
based on the branch below it, so a reviewer sees only that layer's diff.

`gh stack` prints a stack trunk-first, left to right:

```
(main) <- auth <- api <- frontend
```

Left is the **bottom**, right is the **top**. `auth` is based on `main` and merges first;
`frontend` merges last. `up` moves toward the top, away from trunk; `down` moves toward it.
Foundational work belongs at the bottom, code that depends on it above. For how to choose the
layers, read `references/stack-design.md`.

## Setup

```bash
gh extension install github/gh-stack
git config rerere.enabled true         # remember conflict resolutions
git config remote.pushDefault origin   # required if the repo has more than one remote
```

## Non-interactive use

`gh stack` branches on whether **stdout is a TTY**. Piped, most commands error cleanly or print
static text; under a PTY the same commands open a prompt or a full-screen TUI and block forever.
Agent harnesses differ, so always pass the flags below instead of relying on that detection.

| Always run | Never run bare | Why |
|---|---|---|
| `gh stack view --json` | `gh stack view` | opens a TUI under a PTY |
| `gh stack submit --auto` | `gh stack submit` | prompts for a title per new PR |
| `gh stack merge --yes` | `gh pr merge` | `gh pr merge` cannot merge a stack |
| `gh stack init <branch>...` | `gh stack init` | prompts for branch names |
| `gh stack add <branch>` | `gh stack add` | prompts for a name, and fails even when piped |
| `gh stack checkout <target>` | `gh stack checkout` | opens a selection menu |
| `gh stack up` / `down` / `top` / `bottom` | `gh stack switch` | `switch` is menu-only |
| — | `gh stack modify` | TUI-only, no non-interactive path |

- `view --short` is safe in both modes, but it is formatted for humans. Use `--json` to parse.
- **Multiple remotes:** set `remote.pushDefault`, or pass `--remote <name>` to `push`, `submit`,
  `sync`, `rebase`, and `link`. `checkout` and `trunk` have no `--remote` flag and rely on the
  config.
- **`checkout <pr>` when a different local stack already covers those branches** cannot be forced.
  Run `gh stack unstack --local` first (this keeps the stack on GitHub), then retry.

## Core loop

```bash
gh stack init auth              # create the stack and check out its branch
git add ... && git commit -m "Add auth middleware"
gh stack add api                # next layer, branched from the current one
git add ... && git commit -m "Add API routes"
gh stack submit --auto          # push every branch and open draft PRs
gh stack view --json            # confirm
```

Add `--open` to `submit` to create PRs ready for review instead of drafts. Branch names are used
verbatim — `gh stack add refactor/foo` creates `refactor/foo`.

## Changing a lower layer

Agents get this wrong most often. If work belongs in a lower layer, do not patch around it at the
current layer — the change lands in the wrong PR.

```bash
gh stack down                   # or: gh stack checkout api
git add ... && git commit -m "Add get-user endpoint"
gh stack rebase --upstack       # replay every branch above onto the change
gh stack top                    # return to where you were
gh stack push
```

## Staying in sync

```bash
gh stack sync                   # fetch, reconcile with GitHub, rebase, push, refresh PR state
gh stack sync --prune           # also delete local branches for merged PRs
```

Pruning never happens without `--prune` when non-interactive. If the local and remote stacks have
diverged, `sync` prints both chains, makes no changes, and exits 0 with `Sync aborted` — see
`references/troubleshooting.md`.

## Merging

```bash
gh stack merge --yes            # merge the whole current stack, bottom to top
gh stack merge 42 --yes         # merge up to and including PR #42
gh stack merge 7 --yes          # merge stack #7, no local checkout needed
gh stack merge --yes --squash   # or --merge, --rebase, --merge-method <method>
```

All-or-nothing: if any PR cannot merge, none do. Without a method flag the last-used method is
reused. If the base branch uses a merge queue, the stack is queued instead and the queue picks the
method, ignoring any flag you passed with a warning; queued PRs may land in separate groups.

## Reading state

`gh stack view --json` writes JSON to **stdout**. Status messages go to **stderr** — do not parse
them, branch on exit codes instead.

```
trunk           string
currentBranch   string
branches[]      name, head, base, isCurrent, isMerged, isQueued, needsRebase
branches[].pr   number, url, state ("OPEN" | "MERGED" | "QUEUED"); absent when no PR exists
```

`base` is the parent's HEAD SHA at the last sync. `needsRebase` is true when the base is no longer
an ancestor of the branch.

## Exit codes

| Code | Meaning | Recovery |
|---|---|---|
| 0 | Success | — |
| 1 | Generic error | Read stderr |
| 2 | Not in a stack | `gh stack init`, or `gh stack checkout <target>` |
| 3 | Rebase conflict | Resolve, `git add`, `gh stack rebase --continue`, or `--abort` |
| 4 | GitHub API failure | Check `gh auth status`, retry |
| 5 | Invalid arguments | Fix the invocation; see `<command> --help` |
| 6 | Disambiguation required | Branch is in several stacks; check out a non-shared branch |
| 7 | Rebase already in progress | `gh stack rebase --continue` or `--abort` |
| 8 | Stack file locked | Another `gh stack` process is writing; retry after ~5s |
| 9 | Stacked PRs unavailable | Not enabled on the repository; tell the user |
| 10 | Modify recovery required | `gh stack modify --abort` |

## Constraints

- Stacks are strictly linear: one parent, at most one child. Use separate stacks for parallel work.
- There is no non-interactive reorder or removal. Errors may suggest `gh stack modify`, but it is
  TUI-only — restructure with `unstack` then `init` instead.
- PR titles and bodies are auto-generated. Use `gh pr edit` afterwards to change them.
- `checkout <branch-name>` resolves against local stacks only. Use a stack or PR number to pull a
  stack down from GitHub.

## More detail

`gh stack <command> --help` is authoritative for flags and arguments. Note that
`gh stack help <command>` does **not** work — it prints the top-level help.

- `references/stack-design.md` — read before creating a stack, when deciding how many layers to
  use, what belongs in each one, or whether work belongs in a new stack.
- `references/commands.md` — read when a command fails unexpectedly or you need its preconditions,
  side effects, atomicity, or ordering guarantees.
- `references/troubleshooting.md` — read on a rebase conflict, after a squash-merge, on local and
  remote divergence, when restructuring a stack, or when driving stacks from another tool.
