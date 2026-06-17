# gh-stack: Copilot Instructions

A Go CLI extension (`gh stack`) for managing stacked branches and pull requests. Uses Cobra for commands, bubbletea/lipgloss for TUI, and `stretchr/testify` for tests.

## Build and validate

```sh
go mod download                  # install deps
go build -o gh-stack .           # build
go vet ./...                     # static analysis. Always run before tests.
go test -race -count=1 ./...     # tests with race detection
```

No Makefile, no code generation, no external linter config. Standard Go toolchain only.

## Project layout

- `cmd/`: One Cobra command per file. Each exports `<Name>Cmd(cfg *config.Config)` with logic in `run<Name>()`.
- `internal/git/`: `Ops` interface (52 methods) wrapping git CLI. `MockOps` for tests. Package-level functions delegate to swappable `ops` variable.
- `internal/github/`: `ClientOps` interface (11 methods) for GitHub API. `MockClient` for tests.
- `internal/config/`: `Config` struct passed to all commands. Holds I/O, colors, and test hooks (`SelectFn`, `ConfirmFn`, `InputFn`, `GitHubClientOverride`).
- `internal/stack/`: Stack file (`.git/gh-stack`, JSON) management with file locking.
- `internal/tui/`: bubbletea views (`stackview`, `modifyview`).

## Coding conventions

- Return typed `ExitError` sentinels (codes 1-10 in `cmd/utils.go`) from `RunE`. Never call `os.Exit()` directly.
- Check errors with `var exitErr *ExitError; errors.As(err, &exitErr)`.
- Table-driven tests with `t.Run()` subtests.
- Use `config.NewTestConfig()` for test configs with captured I/O.
- Mock git: `restore := git.SetOps(&git.MockOps{...}); defer restore()`. Always defer restore.
- Mock GitHub: `cfg.GitHubClientOverride = &github.MockClient{...}`.
- Mock prompts: set `cfg.SelectFn`, `cfg.ConfirmFn`, or `cfg.InputFn`.
- Load stack files with `stack.Load(dir)` after writing to get correct checksums.

For full architecture details, see [AGENTS.md](../AGENTS.md) in the repository root.
