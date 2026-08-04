# gh-stack agent skill evals

This suite evaluates whether coding agents use `gh stack` correctly, efficiently, and
non-interactively. It runs agents against deterministic Git/GitHub fixtures, then grades the final
repository and GitHub state with objective assertions.

The suite is intentionally separate from the Go unit tests:

- It calls real language models through GitHub Copilot CLI.
- Several cases create real branches, pull requests, and GitHub Stack objects.
- Results are nondeterministic, slower, and consume Copilot requests.

## Cases

| Case | What it tests |
|---|---|
| `preemptive-feature` | Recognizing multi-part work before implementation |
| `read-state` | Using `gh stack view --json` without modifying the repo |
| `lower-layer-edit` | Editing the owning middle layer and rebasing upstack |
| `multi-remote-push` | Selecting the requested remote without a picker |
| `submit-prs` | Opening dependent, ready-for-review PRs in one Stack |
| `reorder-stack` | Rewriting Git ancestry before rebuilding stack metadata |
| `merge-stack` | Merging a PR cutoff and every PR below it |
| `split-worktree` | Splitting one uncommitted diff into dependency-ordered layers |
| `split-branch` | Splitting one large committed branch without losing changes |
| `split-open-pr` | Splitting an open PR while resolving its review history |
| `continue-stack` | Adding another top layer to an existing stack |
| `link-stack` | Linking externally managed branches without local tracking |

`preemptive-feature` is informational rather than blocking: skill selection before any stack
language appears remains model-dependent. All other cases are required for the `current`
configuration.

## What is measured

Each run records:

- Pass/fail against case-specific Git and GitHub assertions
- Copilot skill invocation and reference-file reads
- Tool and shell call counts
- Input/output tokens from the isolated Copilot session database
- Duration and timeout status
- Full JSONL agent transcript

Results are written under `evals/results/<run-id>/`. That directory is ignored by Git.

## Requirements

- Python 3.10+
- Git
- GitHub CLI (`gh`), authenticated
- GitHub Copilot CLI (`copilot`)
- Go toolchain, to build the local extension
- A **disposable** GitHub repository with Stacked PRs enabled
- A local checkout of that disposable repository

Never point the suite at a production repository. It creates and deletes branches and PRs.

## Authentication

Copilot CLI supports headless authentication with `COPILOT_GITHUB_TOKEN`. Use a fine-grained PAT
with the account-level **Copilot Requests** permission.

GitHub API and Git pushes use, in order:

1. `GH_STACK_EVAL_GITHUB_TOKEN`
2. `GH_TOKEN`
3. The token returned by `gh auth token`

The GitHub token needs read/write access to the disposable test repository. The two tokens may be
the same when one token has both permissions.

## Configuration

```bash
export GH_STACK_EVAL_SOURCE_REPO="$HOME/test"
export GH_STACK_EVAL_REPO="owner/test"
export COPILOT_GITHUB_TOKEN="github_pat_..."

# Optional when the checkout's origin is already correct:
export GH_STACK_EVAL_REMOTE_URL="https://github.com/owner/test.git"

# Optional; defaults to main:
export GH_STACK_EVAL_DEFAULT_BRANCH="main"
```

The runner creates a unique `eval-base/<run-id>` branch for every run. All PRs and merge tests
target that branch, so the test repository's default branch is never modified. Cleanup runs after
grading and removes the run's open PRs, branches, and base branch. Merge cases leave normal merged
PR history in the disposable repository.

## Run locally

Build the extension first:

```bash
go build -o gh-stack .
```

List cases:

```bash
python3 evals/runner.py --list
```

Run one case:

```bash
python3 evals/runner.py \
  --case lower-layer-edit \
  --arm current \
  --model mini \
  --iteration local
```

Run the full current-skill suite on both models:

```bash
python3 evals/run_suite.py \
  --cases all \
  --arms current \
  --models mini,sonnet \
  --repetitions 1 \
  --jobs 2 \
  --prefix local
```

Optionally compare the current skill with no skill:

```bash
python3 evals/run_suite.py \
  --cases all \
  --arms current,none \
  --models mini,sonnet \
  --repetitions 1 \
  --jobs 2 \
  --prefix benchmark
```

The `none` configuration installs no gh-stack skill. It is useful for diagnostics but is not part
of normal regression validation.

`--fail-on-failure` fails only required cases in the `current` configuration. Failures in `none`
or the informational trigger case remain visible without making a diagnostic comparison red.

Aggregate results:

```bash
python3 evals/aggregate.py --prefix benchmark
```

This writes `summary.md`, `summary.json`, `results.csv`, and `results.json` under `evals/results/`.

## Candidate skill from another directory

Use `--skill-path` with `runner.py` or `run_suite.py`:

```bash
python3 evals/run_suite.py \
  --cases read-state,lower-layer-edit \
  --arms current \
  --models mini \
  --skill-path /path/to/skill-directory \
  --prefix candidate
```

The directory must contain `SKILL.md` and any referenced files.

## Interpreting results

Agent runs are nondeterministic. Do not make release decisions from one close result.

- Run important comparisons at least three times per case/model.
- Treat any interactive hang as blocking.
- Inspect every current/no-skill divergence, not just the aggregate score.
- A reference file opened in most runs probably belongs inline.
- A reference file never opened is either poorly signposted or unnecessary.
- Preserve the transcripts and result JSON when reporting a regression.

## GitHub Actions

Two workflows support the suite:

- `evals-validate.yml` runs safe, offline validation on pull requests.
- `skill-evals.yml` runs live model/GitHub evals via `workflow_dispatch`.

The live workflow requires repository secrets:

- `COPILOT_GITHUB_TOKEN`
- `GH_STACK_EVAL_GITHUB_TOKEN`

And repository variables:

- `GH_STACK_EVAL_REPO` — for example `owner/test`
- `GH_STACK_EVAL_DEFAULT_BRANCH` — optional, defaults to `main`

It checks out the disposable test repository into the workspace, runs the requested matrix, writes
the Markdown summary to the Actions job summary, and uploads all result artifacts.

The live workflow is manual rather than a required pull-request check because it uses privileged
credentials, consumes Copilot requests, and executes nondeterministic agents. Once its flake rate
and cost are understood, maintainers can add a scheduled run or a protected label-triggered check.
