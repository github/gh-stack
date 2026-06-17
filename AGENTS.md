# gh-stack: Agent Instructions

A GitHub CLI (`gh`) extension for managing stacked branches and pull requests. Written in Go, it automates creating branches, keeping them rebased, setting PR base branches, and navigating between stack layers.

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
    gitops.go                # Ops interface (52 methods)
    mock_ops.go              # MockOps. Each method has a corresponding *Fn field.
  github/                    # github.ClientOps interface + real Client
    client_interface.go      # ClientOps interface (11 methods)
    mock_client.go           # MockClient. Uses function-pointer fields for testing.
  stack/                     # stack file (.git/gh-stack) management, JSON schema, locking
    schema.json              # JSON Schema for the stack file format
  config/                    # Config struct (I/O, colors, test overrides)
    testing.go               # NewTestConfig(). Returns *Config + stdout/stderr pipes.
  branch/                    # branch naming (Slugify, DateSlug, NextNumberedName)
  modify/                    # interactive stack modification state machine
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
| Remote operations | `submit`, `sync`, `rebase`, `push`, `link` |
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

Return these from `RunE`. Never call `os.Exit()` directly from commands. Check with `errors.As(err, &ExitError{})`.

### Testing patterns

- **Framework:** `stretchr/testify` (`assert`, `require`) for assertions.
- **Table-driven tests** are the norm. See `cmd/utils_test.go` for examples.
- **Config:** Use `config.NewTestConfig()` which returns `(*Config, stdoutReader, stderrReader)` with captured I/O and no-op color functions.
- **Git mocking:** Call `git.SetOps(&git.MockOps{...})`. It returns a restore function. Always `defer restore()` to prevent test pollution.
- **GitHub mocking:** Set `cfg.GitHubClientOverride = &github.MockClient{...}`.
- **Prompt mocking:** Set `cfg.SelectFn`, `cfg.ConfirmFn`, or `cfg.InputFn` on the config to simulate interactive input.
- **Stack file setup:** Use `stack.Load(dir)` after writing a stack file to get correct checksums for `Save`.

### Key interfaces

- **`git.Ops`** (`internal/git/gitops.go`): 52 methods wrapping git CLI calls. The production implementation uses `cli/go-gh`'s `client.Command()` via `run()` and `runSilent()` helpers. Package-level functions (e.g., `git.CurrentBranch()`) delegate to a swappable package-level `ops` variable.
- **`github.ClientOps`** (`internal/github/client_interface.go`): 11 methods for GitHub API (PRs, stacks). Injected via `cfg.GitHubClientOverride` in tests.
- **`config.Config`** (`internal/config/config.go`): Central configuration passed to all commands. Holds I/O streams, color functions, and test hook fields (`SelectFn`, `ConfirmFn`, `InputFn`, `TokenForHostFn`, `RepoOverride`).

### Stack file

- **Location:** `.git/gh-stack` (JSON format, schema version 1).
- **Schema:** `internal/stack/schema.json`.
- **Locking:** Exclusive file lock at `.git/gh-stack.lock` with 5-second timeout. Errors surface as `LockError`.
- **Staleness:** Concurrent modifications detected via `StaleError`.

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
- The `.gitignore` ignores `/gh-stack` and `/gh-stack.exe` (the built binary).
