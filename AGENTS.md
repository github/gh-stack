# gh-stack: Agent Instructions

A GitHub CLI (`gh`) extension for managing stacked branches and pull requests. Written in Go, it automates creating branches, keeping them rebased, setting PR base branches, and navigating between stack layers.

Repository operations require Git 2.36 or later.

## Build, test, and validate

```sh
go mod download                  # install dependencies
go build -o gh-stack .           # build (produces ./gh-stack binary)
go vet ./...                     # static analysis. Run before tests.
go test -race -count=1 ./...     # all tests with race detection
```

Always run `go vet` before `go test`. CI runs both on every push/PR across ubuntu, windows, and macOS (`test.yml`).

There is no Makefile, linter config, or code generation step. The standard Go toolchain is all that's needed.

### Install locally as a `gh` extension

```sh
go build -o gh-stack .
gh extension remove stack 2>/dev/null
gh extension install .
```

## Project structure

```
main.go                      # entrypoint. Calls cmd.Execute().
cmd/                         # Cobra commands (one file per command + tests)
  root.go                    # registers all subcommands in four groups
  utils.go                   # shared helpers, ExitError types, exit codes
internal/
  git/                       # git.Ops interface + defaultOps (exec-based)
    gitops.go                # Ops interface, including scoped worktree execution
    mock_ops.go              # MockOps. Each method has a corresponding *Fn field.
  github/                    # github.ClientOps interface + real Client
    client_interface.go      # ClientOps interface (18 methods)
    mock_client.go           # MockClient. Uses function-pointer fields for testing.
  stack/                     # common-directory catalog, JSON schema, migration, locking
    schema.json              # JSON Schema for the stack file format
  config/                    # Config struct (I/O, colors, test overrides)
    testing.go               # NewTestConfig(). Returns *Config + stdout/stderr pipes.
  branch/                    # branch naming (Slugify, DateSlug)
  modify/                    # interactive stack modification state machine
  worktree/                  # operation-owned worktree identities and ref recovery
  pr/                        # PR template discovery
  tui/                       # bubbletea/bubbles/lipgloss terminal UI
    stackview/               # interactive stack visualization
    modifyview/              # interactive modify session UI
    shared/                  # shared TUI types
docs/                        # Astro + Starlight documentation site
skills/                      # AI agent skill definition (SKILL.md)
```

### Command groups (registered in `cmd/root.go`)

| Group | Commands |
|-------|----------|
| Stack management | `init`, `add`, `view`, `checkout`, `modify`, `unstack` |
| Remote operations | `submit`, `sync`, `rebase`, `push`, `link`, `merge` |
| Navigation | `switch`, `up`, `down`, `top`, `bottom`, `trunk` |
| Utilities | `alias`, `feedback` |

## Coding patterns

### Command structure

Each command lives in its own file (`cmd/<name>.go`) and follows this pattern:

1. Define an `<name>Options` struct for flags/args.
2. Export a `<Name>Cmd(cfg *config.Config) *cobra.Command` constructor.
3. Implement logic in a private `run<Name>(cfg, opts, args)` function.
4. The `RunE` field on the command calls `run<Name>`.

### Error handling

Use typed exit codes defined in `cmd/utils.go`:

| Code | Sentinel | Meaning |
|------|----------|---------|
| 1 | `ErrSilent` | Error already printed |
| 2 | `ErrNotInStack` | Branch/stack not found |
| 3 | `ErrConflict` | Rebase conflict |
| 4 | `ErrAPIFailure` | GitHub API error |
| 5 | `ErrInvalidArgs` | Invalid arguments or flags |
| 6 | `ErrDisambiguate` | Multiple stacks/remotes, can't auto-select |
| 7 | `ErrRebaseActive` | Rebase already in progress |
| 8 | `ErrLockFailed` | Stack file lock contention |
| 9 | `ErrStacksUnavailable` | Stacked PRs not enabled for repository |
| 10 | `ErrModifyRecovery` | Modify session interrupted |

Return these from `RunE`. Never call `os.Exit()` directly from commands. Check with:

```go
var exitErr *ExitError
if errors.As(err, &exitErr) { ... }
```

### Testing patterns

- **Framework:** `stretchr/testify` (`assert`, `require`) for assertions.
- **Table-driven tests** are the norm. See `cmd/utils_test.go` for examples.
- **Config:** Use `config.NewTestConfig()` which returns `(*Config, stdoutReader, stderrReader)` with captured I/O and no-op color functions.
- **Git mocking:** Call `git.SetOps(&git.MockOps{...})`. It returns a restore function. Always `defer restore()` to prevent test pollution.
- **GitHub mocking:** Set `cfg.GitHubClientOverride = &github.MockClient{...}`.
- **Prompt mocking:** Set `cfg.SelectFn`, `cfg.ConfirmFn`, or `cfg.InputFn` on the config to simulate interactive input.
- **Stack file setup:** Use `stack.Load(dir)` after writing a stack file to get correct checksums for `Save`.

### Key interfaces

- **`git.Ops`** (`internal/git/gitops.go`): wraps git CLI calls. Package-level functions (e.g., `git.CurrentBranch()`) delegate to a swappable package-level `ops` variable. `git.ForWorktree(path)` returns an explicitly scoped executor for HEAD/index/working-file operations. Never switch production context with `os.Chdir` or `git.SetOps`; reserve `SetOps` for tests. `GitDir()` remains per-worktree; `CommonDir()` is repository-wide.
- **Scoped Git errors:** `ForWorktree(path)` returns `(Ops, error)` and no executor for invalid contexts. Each scoped operation rechecks directory identity. `BranchExists`, `HasStagedChanges`, `IsRebaseInProgress`, and `IsCherryPickInProgress` return `(bool, error)`; callers must handle lookup failures before mutating Git or recovery state, not treat them as absence.
- **`github.ClientOps`** (`internal/github/client_interface.go`): 18 methods for GitHub API (PRs, stacks, merges). Stack operations use the public Stacks REST API (`/repos/{owner}/{repo}/stacks`): `ListStacks`, `FindStackForPR`, `GetStack`, `CreateStack`, `AddToStack` (delta append), `Unstack`. Async stack merges use `RepoMergeConfig` (GraphQL: allowed merge methods + viewer's default), `BaseBranchUsesMergeQueue` (GraphQL: detects a base-branch merge queue to select the explicit `merge_action`), `MergeStackAsync`, and `GetAsyncMergeResult` (`/repos/{owner}/{repo}/pulls/{n}/merge-async`). Injected via `cfg.GitHubClientOverride` in tests.
- **`config.Config`** (`internal/config/config.go`): Central configuration passed to all commands. Holds I/O streams, color functions, and test hook fields (`SelectFn`, `ConfirmFn`, `InputFn`, `RepoOverride`).

### Stack file

- **Location:** `<common-dir>/gh-stack` (JSON format, schema version 1), shared by all linked worktrees. Use `stackStateDir(cfg)` for app storage; it also selects original-worktree catalogs during legacy recovery.
- **Schema:** `internal/stack/schema.json`.
- **Identity:** each stack stores GitHub's global `id` (string) and repo-scoped `number` (int, shown in the GitHub UI and used as the primary way to reference a stack, e.g. `gh stack checkout <number>`). `number` may be `0` for stack files created before it was tracked; it is backfilled from the API on the next stack operation.
- **Locking:** `<common-dir>/gh-stack.lock` protects short catalog saves; `<common-dir>/gh-stack-operation.lock` serializes clone-wide mutations. Acquire `beginStackMutation` before snapshots/preflight and defer its cleanup. Never hold a catalog lock across Git operations or call lock-taking `stack.Save` while already holding that lock. Errors surface as `LockError`.
- **Staleness:** Concurrent modifications detected via `StaleError`.
- **Migration:** Consolidate only nonconflicting legacy catalogs and preserve originals. Stop on conflicting definitions; finish legacy recovery in its original worktree before migrating. Do not mix old and new writers.
- **Recovery:** gh-stack journals live in the common directory and record the origin, stack identity, original refs, and progress. Native Git markers remain per-worktree. Rebase continue/abort currently requires the recorded original worktree; modify uses a scoped origin executor even when invoked elsewhere. Match stack identity (not catalog array position) and retain state on any partial restore or save failure.
- **External changes:** Mutation locks coordinate gh-stack, not arbitrary Git commands or editors. Keep affected worktrees quiescent during rewrites, except for requested conflict resolution while paused. Context-tracked modify calls `Context.Start(branch, expectedSHA)` before ref mutations, using the snapshot or last `Context.Touched` SHA. Do not adopt a freshly read tip as the operation's baseline during continuation.
- **Separate Git directories:** Native topology may report the administration directory as the main worktree path for `--separate-git-dir` repositories. A known origin remains usable, but a foreign main-owner root may be undiscoverable. Never infer a working directory from an administration path, emit it as a successful navigation target, or add a private registry/config mutation to guess ownership.
- **Core modify boundary:** Plain modify permits unoccupied branches and branches owned by its origin worktree, but rejects distributed stack membership before the TUI/apply. Trunk ownership alone does not block it. Full distributed modify is a separate layer.
- **Intermediate rebase/sync boundary:** Keep the existing origin-only execution engine. After prerequisite catalog migration, reject all foreign-owned member/rollback targets and any trunk that would be updated before requested mutations. Sync must check remote-added/replacement branches before importing or saving membership. Stack selection cannot eagerly checkout before this preflight. Multi-owner execution is deferred.
- **Journal transition:** New rebase/sync journals carry `executionMode: "origin-only"`. Their context identifies the origin, not distributed per-step progress. Future engines must recognize this marker before routing recovery and either use compatible origin-bound recovery or fail closed with matching-build instructions. Never reinterpret it as a distributed journal; this build likewise rejects unmarked non-null contexts and unknown modes. Legacy null-context journals retain their original-catalog route.

## CI workflows (`.github/workflows/`)

| Workflow | Trigger | What it does |
|----------|---------|-------------|
| `test.yml` | push to main, PRs | `go vet` + `go test -race -count=1 ./...` on 3 OS matrix |
| `release.yml` | `v*` tags | Cross-platform precompiled binaries via `cli/gh-extension-precompile` |
| `docs.yml` | push to main (docs/**) | Builds Astro/Starlight docs, deploys to GitHub Pages |

## Non-obvious things

- The `Queued` field on `BranchRef` is transient (populated from GitHub API, never persisted to the stack JSON file).
- `git.SetOps()` replaces the **package-level** ops variable. Forgetting `defer restore()` in a test will break every subsequent test in the package.
- Interrupt detection: Ctrl+C is caught as `terminal.InterruptErr`, wrapped into an `errInterrupt` sentinel, and printed with a friendly message before a silent exit.
- Rerere: on first rebase conflict, the user is prompted to enable `git rerere`. If declined, a flag file prevents future prompts. `tryAutoResolveRebase()` loops up to 1000 times auto-continuing when rerere resolves conflicts.
- Date-preserving rebase starts use the merge backend so Git persists the date setting across conflicts. Continuations use native saved settings, not start-only date flags.
- The `.gitignore` ignores `/gh-stack` and `/gh-stack.exe` (the built binary).
