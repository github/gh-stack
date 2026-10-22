## Contributing

[fork]: https://github.com/github/gh-stack/fork
[pr]: https://github.com/github/gh-stack/compare

Hi there! We're thrilled that you'd like to contribute to this project. Your help is essential for keeping it great.

Contributions to this project are [released](https://help.github.com/articles/github-terms-of-service/#6-contributions-under-repository-license) to the public under the [project's open source license](LICENSE).

Please note that this project is released with a [Contributor Code of Conduct](CODE_OF_CONDUCT.md). By participating in this project you agree to abide by its terms.

## Prerequisites

- [Go](https://go.dev/doc/install) (version specified in `go.mod`)
- [GitHub CLI](https://cli.github.com/) (`gh`) v2.0+

## Local setup

```sh
# Clone the repository (or your fork)
git clone https://github.com/github/gh-stack.git
cd gh-stack

# Download dependencies
go mod download
```

## Build

```sh
go build ./...
```

This produces a `gh-stack` binary in the current directory.

## Test

Run static analysis first, then choose **one** full-suite command.

```sh
go vet ./...
```

For process-isolated parallel execution:

```sh
go run ./scripts/test -workers 4
```

Alternatively, use the canonical Go command:

```sh
go test -race -count=1 -timeout=30m ./...
```

The parallel runner executes the complete platform-applicable suite, including
native Windows regressions. It compiles race-enabled test binaries once and
isolates Git mocks, environment changes, and working directories between workers.
Tests within a worker still need to restore their state. `-workers` bounds process
concurrency; each child uses `-test.parallel=1`. Choose fewer workers on constrained
machines. External deadlines also cover initialization and `TestMain`, with a
five-second grace period for native timeout diagnostics and race-runtime shutdown.

Results are saved in a printed temporary directory, or under the directory supplied
with `-output`. Each run contains JSON event logs and a `report.json` with the test
inventory, assignments, results, runner/toolchain metadata, and build/test timings.
Top-level timings include their subtests; do not add both together.

To run a package or a specific regression:

```sh
go vet ./internal/modify
go run ./scripts/test -workers 4 ./internal/modify

go vet ./cmd
go test -race -count=1 -timeout=30m -run '^TestRebase_Worktrees' ./cmd
```

CI partitions the full suite across Windows and macOS jobs. The `Tests complete`
check verifies that every platform has a complete, consistent set of successful
shards. Missing results, failures, or cancelled shards fail the check. Test
membership comes from Go's test binaries, not the advisory scheduling weights in
`scripts/test/timings.go`; newly added tests are included automatically.

## Vet

```sh
go vet ./...
```

## Install locally as a `gh` extension

To test your local build as a `gh` CLI extension:

```sh
# Build the binary
go build -o gh-stack .

# Remove any existing installation
gh extension remove stack

# Install from the local directory
gh extension install .
```

You can now run `gh stack` and it will use your locally built version.

## Docs site

The documentation site lives in `docs/` and uses [Astro](https://astro.build/) + [Starlight](https://starlight.astro.build/).

```sh
cd docs
npm install
npm run dev       # Start dev server at localhost:4321
npm run build     # Production build to docs/dist/
```

## Submitting a pull request

1. [Fork][fork] and clone the repository
1. Create a new branch: `git checkout -b my-branch-name`
1. Make your change and add tests
1. Make sure vet passes: `go vet ./...`
1. Make sure tests pass: `go run ./scripts/test -workers 4` (or the canonical command above)
1. Push to your fork and [submit a pull request][pr]

Here are a few things you can do that will increase the likelihood of your pull request being accepted:

- Write tests.
- Keep your change as focused as possible. If there are multiple changes you would like to make that are not dependent upon each other, consider submitting them as separate pull requests.
- Write a [good commit message](http://tbaggery.com/2008/04/19/a-note-about-git-commit-messages.html).

## Resources

- [How to Contribute to Open Source](https://opensource.guide/how-to-contribute/)
- [Using Pull Requests](https://help.github.com/articles/about-pull-requests/)
- [GitHub Help](https://help.github.com)
