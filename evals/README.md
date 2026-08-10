# gh-stack skill evals

This suite measures whether coding agents use the repository's `gh-stack` skill correctly and
non-interactively. Agents run against prepared Git/GitHub scenarios; deterministic graders inspect
the final branch graph, file ownership, pull requests, and Stack metadata. No LLM judges are used.

These are live agent evaluations, not Go unit tests. They consume Copilot requests and some cases
create real branches and pull requests in a disposable repository.

## Layout

```text
evals/
├── cases/<number-name>/
│   ├── case.json          # contract, timeout, fixture, assertions
│   └── prompt.md          # exact instruction given to the agent
├── runner.py              # one isolated trial
├── run_suite.py           # repeated/matrix execution
├── aggregate.py           # Markdown, JSON, and CSV reports
├── cleanup.py             # recovery after interrupted batches
└── tests/                 # offline harness validation
```

Each case is self-contained and reviewable without reading the runner. Its `network` field is
descriptive metadata included in results; all trials currently use GitHub to create their isolated
base branch.

## Scenarios

| Case | Contract |
|---|---|
| `preemptive-feature` | Recognize multi-part work before implementation (informational) |
| `read-state` | Read state through `view --json` without mutations |
| `lower-layer-edit` | Commit an API change in its owning middle layer and restack |
| `multi-remote-push` | Push the complete stack to the requested remote |
| `submit-prs` | Open dependent, ready-for-review PRs in one Stack |
| `reorder-stack` | Rewrite ancestry and stack metadata without losing content |
| `merge-stack` | Merge a PR cutoff and every PR below it |
| `split-worktree` | Split one dirty worktree into ordered layers |
| `split-branch` | Split one large committed branch with exact content parity |
| `split-open-pr` | Split an open PR while preserving or superseding its identity |
| `continue-stack` | Add another top layer without rewriting existing layers |
| `link-stack` | Link externally managed branches without local tracking |

`preemptive-feature` is non-blocking because skill selection without stack vocabulary remains
model-dependent. Every other current-skill case is required.

## Requirements

- Python 3.9+
- Git and GitHub CLI (`gh`)
- GitHub Copilot CLI (`copilot`)
- Go, to build the local extension
- A **disposable** GitHub repository with Stacked PRs enabled
- A local checkout of that repository

Never target a production repository.

```bash
export GH_STACK_EVAL_SOURCE_REPO="$HOME/test"
export GH_STACK_EVAL_REPO="owner/test"
# Optional:
export COPILOT_GITHUB_TOKEN="github_pat_..."
export GH_STACK_EVAL_GITHUB_TOKEN="github_pat_..."
export GH_STACK_EVAL_REMOTE_URL="https://github.com/owner/test.git"
export GH_STACK_EVAL_DEFAULT_BRANCH="main"
export GH_STACK_EVAL_SEED_REF="eval-fixture-v1"
export GH_STACK_EVAL_SEED_SHA="<expected commit>"
```

When set, `COPILOT_GITHUB_TOKEN` needs the account-level **Copilot Requests** permission. If it is
unset, Copilot CLI uses `GH_STACK_EVAL_GITHUB_TOKEN`, `GH_TOKEN`, or `gh auth token`; that fallback
token must also be valid for Copilot requests. GitHub operations use the same fallback order.

Every trial creates a unique `eval-base/<run-id>` branch. PRs and merges target that branch, so the
test repository's default branch is not modified. Cleanup removes open PRs, Stack metadata, feature
branches, and the base branch. Merge scenarios leave normal merged-PR history.

For published comparisons, point `GH_STACK_EVAL_SEED_REF` at an immutable fixture branch and set
`GH_STACK_EVAL_SEED_SHA`; the runner fails before setup if the seed moved.

## Run

Build the extension:

```bash
go build -o gh-stack .
```

List scenarios:

```bash
python3 evals/runner.py --list
```

Run one trial:

```bash
python3 evals/runner.py \
  --case lower-layer-edit \
  --arm current \
  --model mini \
  --iteration local
```

Run the complete suite:

```bash
python3 evals/run_suite.py \
  --cases all \
  --arms current \
  --models mini,sonnet \
  --repetitions 3 \
  --jobs 2 \
  --prefix local
```

`run_suite.py` writes its deterministic shuffled schedule before starting. Supply
`--shuffle-seed <value>` to reuse the same order.

Aggregate a batch:

```bash
python3 evals/aggregate.py --prefix local
```

Generated artifacts live in `evals/results/` and are ignored by Git.

After an interrupted batch, remove and audit its remote state:

```bash
python3 evals/cleanup.py \
  --repo owner/test \
  --prefix local \
  --source-repo "$GH_STACK_EVAL_SOURCE_REPO" \
  --audit-path evals/results/local-cleanup-audit.json
```

## Evaluate another skill revision

Load `skills/gh-stack` from any local commit, tag, or branch:

```bash
git fetch origin <commit>
python3 evals/run_suite.py \
  --cases all \
  --arms current \
  --models mini,sonnet \
  --repetitions 3 \
  --skill-ref <commit> \
  --prefix commit-<short-sha>
```

The runner exports the skill directly from Git without changing the working tree. Each result
records the resolved commit, Git tree, skill-directory SHA-256, repository state, model, CLI
versions, platform, and case-contract hash.

For uncommitted candidates, use `--skill-path /path/to/skill-directory`. The optional `none`
configuration installs no skill and is diagnostic only.

## Results and metrics

Each `result.json` contains:

- Objective assertions, failed assertions, and a failure class
- Full Copilot JSONL transcript and issued shell commands
- Skill invocation and reference-file reads
- Model calls, all tool calls, failed tool calls, and VC-related shell calls
- Tool-output bytes, duration, timeouts, and token usage
- Skill, binary, model, CLI, platform, and scenario provenance

`input_tokens` is **cumulative across all model requests in the trial**, not the size of one prompt
or context window. `tool_calls` counts all agent-issued tools; `vc_shell_calls` counts shell-tool
invocations containing `git`, `gh stack`, or `gh pr` and is not an internal-subprocess trace.
Infrastructure failures are reported separately and excluded from pass-rate denominators.

Correctness is the gate. Compare efficiency only among correct runs, and repeat important cells:

- Use at least three trials per case/model.
- Treat interactive hangs as blocking.
- Inspect every divergent run and its failed assertions.
- Keep the same shuffle seed for paired comparisons.
- Preserve result JSON and transcripts when reporting a regression.

To publish a batch, include its saved plan, summary Markdown/JSON, results CSV, selected failing
transcripts, and the repository commit or `--skill-ref`. Review transcripts for sensitive data
before committing them.

## Add a scenario

1. Add `evals/cases/<number-name>/case.json` and `prompt.md`.
2. Add or reuse a fixture in `runner.py`.
3. Add objective grading assertions; avoid judging prose or command style.
4. Add the case name to `test_public_cases_cover_core_workflows` when it is a core contract.
5. Run `python3 -m unittest discover -s evals/tests -v`.
6. Smoke-test both models against the disposable repository.
