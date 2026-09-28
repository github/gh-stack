# gh-stack: Copilot Instructions

A Go CLI extension (`gh stack`) for managing stacked branches and pull requests. Uses Cobra for commands, bubbletea/lipgloss for TUI, and `stretchr/testify` for tests.

Repository operations require Git 2.36+.

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
- `internal/git/`: `Ops` interface wrapping git CLI. `MockOps` for tests. Use `ForWorktree(path)` for scoped execution, `CommonDir()` for shared storage, and `GitDir()` for native per-worktree state. Never use production `os.Chdir` or `SetOps` to switch context.
- `internal/github/`: `ClientOps` interface (18 methods) for GitHub API. `MockClient` for tests. Stack operations use the public Stacks REST API (`/repos/{owner}/{repo}/stacks`); merges use the async merge API (`/repos/{owner}/{repo}/pulls/{n}/merge-async`) with an explicit `merge_action` (`direct_merge` or `merge_queue`) chosen from the base branch's merge-queue detection. `merge_action` is optional — omitting it (or sending `default`) lets the server auto-route (merge queue if one is configured, else direct merge) — but the CLI sends it explicitly so a wrong detection fails loudly instead of silently merging directly.
- `internal/config/`: `Config` struct passed to all commands. Holds I/O, colors, and test hooks (`SelectFn`, `ConfirmFn`, `InputFn`, `GitHubClientOverride`).
- `internal/stack/`: Shared catalog (`<common-dir>/gh-stack`, JSON), conservative legacy migration, atomic saves, and short catalog locks.
- `internal/worktree/`: Origin/owner identities, cleanliness preflight, scoped operations, and touched-ref recovery.
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
- Use `stackStateDir(cfg)` for application state and `beginStackMutation` before mutation snapshots; defer cleanup. The clone-wide operation lock is separate from short catalog saves.
- Recovery must match stack identity, execute in the recorded worktree, and retain journals on partial failures. Native Git markers stay per-worktree.
- Mutation locks coordinate gh-stack only, not Git commands/editors. Keep affected worktrees quiescent during rewrites. Context-tracked modify passes the snapshot SHA (or prior `Context.Touched` SHA) to `Context.Start`; never claim an external commit as this operation's work during continuation.
- Rebase/sync currently reject foreign-owned members and writable trunks after prerequisite migration but before requested mutations, including sync reconciliation. Their existing engine remains origin-only; rebase recovery must be invoked in its recorded origin.
- Core modify rejects distributed stack branches before TUI/apply; foreign trunk ownership alone is allowed. Do not enable distributed modify until its dependent layer is implemented.

For full architecture details, see [AGENTS.md](../AGENTS.md) in the repository root.
