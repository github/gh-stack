package cmd

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/tui/mergeview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openStackPR(n int, ref string) github.RemoteStackPR {
	return github.RemoteStackPR{Number: n, State: "open", Head: github.RemoteStackPRHead{Ref: ref}}
}

func draftStackPR(n int, ref string) github.RemoteStackPR {
	return github.RemoteStackPR{Number: n, State: "open", Draft: true, Head: github.RemoteStackPRHead{Ref: ref}}
}

func closedStackPR(n int, ref string) github.RemoteStackPR {
	return github.RemoteStackPR{Number: n, State: "closed", Head: github.RemoteStackPRHead{Ref: ref}}
}

func mergedStackPR(n int, ref string) github.RemoteStackPR {
	at := "2026-01-01T00:00:00Z"
	return github.RemoteStackPR{Number: n, State: "closed", MergedAt: &at, Head: github.RemoteStackPRHead{Ref: ref}}
}

func remoteStack(number int, base string, prs ...github.RemoteStackPR) *github.RemoteStack {
	nums := make([]int, len(prs))
	for i, p := range prs {
		nums[i] = p.Number
	}
	return &github.RemoteStack{
		ID:           number,
		Number:       number,
		Base:         github.RemoteStackBase{Ref: base},
		Open:         true,
		PullRequests: nums,
		PRDetails:    prs,
	}
}

func notFoundErr() error { return &api.HTTPError{StatusCode: http.StatusNotFound} }

func fastOptions() *mergeOptions {
	return &mergeOptions{pollInterval: time.Millisecond, maxPolls: 5}
}

// setupLocalStack writes a single-stack file and mocks git so no-arg resolution
// finds it.
func setupLocalStack(t *testing.T, number int, currentBranch string, branches ...string) {
	t.Helper()
	gitDir := t.TempDir()
	restore := git.SetOps(&git.MockOps{
		GitDirFn:        func() (string, error) { return gitDir, nil },
		CurrentBranchFn: func() (string, error) { return currentBranch, nil },
	})
	t.Cleanup(restore)

	refs := make([]stack.BranchRef, len(branches))
	for i, b := range branches {
		refs[i] = stack.BranchRef{Branch: b}
	}
	writeStackFile(t, gitDir, stack.Stack{
		ID:       "s",
		Number:   number,
		Trunk:    stack.BranchRef{Branch: "main"},
		Branches: refs,
	})
}

func TestRunMerge_NoArg_MergesWholeStack(t *testing.T) {
	setupLocalStack(t, 100, "b2", "b1", "b2", "b3")

	var gotPR int
	var gotMethod string
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			assert.Equal(t, 100, n)
			return remoteStack(100, "main", openStackPR(1, "b1"), openStackPR(2, "b2"), openStackPR(3, "b3")), nil
		},
		RepoMergeConfigFn: func() (*github.RepoMergeConfig, error) {
			return &github.RepoMergeConfig{MergeAllowed: true, SquashAllowed: true, RebaseAllowed: true, DefaultMethod: "squash"}, nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			gotPR, gotMethod = pr, method
			return &github.AsyncMergeResult{Queued: true, Details: github.AsyncMergeDetails{UUID: "u"}}, nil
		},
		GetAsyncMergeResultFn: func(pr int, uuid string) (*github.AsyncMergeResult, error) {
			return &github.AsyncMergeResult{Merged: true, Details: github.AsyncMergeDetails{SHA: "abc1234"}}, nil
		},
	}

	err := runMerge(cfg, fastOptions(), nil)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 3, gotPR, "targets the top of the stack")
	assert.Equal(t, "squash", gotMethod, "uses the viewer default method")
	assert.Contains(t, output, "Merged #1, #2, #3 into main")
}

func TestRunMerge_StackNumberArg(t *testing.T) {
	var gotPR int
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			assert.Equal(t, 7, n)
			return remoteStack(7, "main", openStackPR(10, "a"), openStackPR(11, "b")), nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			gotPR = pr
			return &github.AsyncMergeResult{Queued: true, Details: github.AsyncMergeDetails{UUID: "u"}}, nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"7"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 11, gotPR)
	assert.Contains(t, output, "Merged #10, #11 into main")
}

func TestRunMerge_PRNumberArg(t *testing.T) {
	var gotPR int
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return nil, notFoundErr() // not a stack number
		},
		FindStackForPRFn: func(n int) (*github.RemoteStack, error) {
			assert.Equal(t, 2, n)
			return remoteStack(5, "main", openStackPR(1, "b1"), openStackPR(2, "b2"), openStackPR(3, "b3")), nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			gotPR = pr
			return &github.AsyncMergeResult{Queued: true, Details: github.AsyncMergeDetails{UUID: "u"}}, nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"2"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, 2, gotPR, "targets exactly the requested PR")
	assert.Contains(t, output, "Merged #1, #2 into main")
	assert.NotContains(t, output, "#3")
}

func TestRunMerge_SquashFlag(t *testing.T) {
	var gotMethod string
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(7, "main", openStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			gotMethod = method
			return &github.AsyncMergeResult{Queued: true, Details: github.AsyncMergeDetails{UUID: "u"}}, nil
		},
	}

	opts := fastOptions()
	opts.squash = true
	err := runMerge(cfg, opts, []string{"7"})
	_ = collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "squash", gotMethod)
}

func TestRunMerge_ConflictingMethodFlags(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	opts := fastOptions()
	opts.squash = true
	opts.rebase = true

	err := runMerge(cfg, opts, []string{"7"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "only one merge method")
}

func TestRunMerge_InvalidMergeMethod(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	opts := fastOptions()
	opts.mergeMethod = "fast-forward"

	err := runMerge(cfg, opts, []string{"7"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "invalid --merge-method")
}

func TestRunMerge_DisallowedMethod(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(7, "main", openStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
		RepoMergeConfigFn: func() (*github.RepoMergeConfig, error) {
			return &github.RepoMergeConfig{MergeAllowed: true, DefaultMethod: "merge"}, nil
		},
	}
	opts := fastOptions()
	opts.squash = true

	err := runMerge(cfg, opts, []string{"7"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "does not allow squash")
}

func TestRunMerge_DraftTarget(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) { return nil, notFoundErr() },
		FindStackForPRFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(5, "main", openStackPR(1, "b1"), openStackPR(2, "b2"), draftStackPR(3, "b3")), nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"3"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "#3 is a draft")
}

func TestRunMerge_BlockerBelowTarget(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) { return nil, notFoundErr() },
		FindStackForPRFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(5, "main", openStackPR(1, "b1"), draftStackPR(2, "b2"), openStackPR(3, "b3")), nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"3"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrInvalidArgs)
	assert.Contains(t, output, "#2 below it is a draft")
}

func TestRunMerge_AlreadyMergedTarget(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) { return nil, notFoundErr() },
		FindStackForPRFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(5, "main", mergedStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"1"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "#1 is already merged")
}

func TestRunMerge_NothingToMerge_AllMerged(t *testing.T) {
	setupLocalStack(t, 100, "b1", "b1", "b2")
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(100, "main", mergedStackPR(1, "b1"), mergedStackPR(2, "b2")), nil
		},
	}

	err := runMerge(cfg, fastOptions(), nil)
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "already fully merged")
}

func TestRunMerge_SubmitNotMergeable(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(7, "main", openStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			return nil, errors.New("the stack can no longer be merged as requested; refresh and try again")
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"7"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrAPIFailure)
	assert.Contains(t, output, "failed to start merge")
	assert.Contains(t, output, "can no longer be merged")
}

func TestRunMerge_PollFailedConflict(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(7, "main", openStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			return &github.AsyncMergeResult{Queued: true, Details: github.AsyncMergeDetails{UUID: "u"}}, nil
		},
		GetAsyncMergeResultFn: func(pr int, uuid string) (*github.AsyncMergeResult, error) {
			return &github.AsyncMergeResult{Queued: false, Merged: false, Details: github.AsyncMergeDetails{Message: "Merge conflict: could not merge."}}, nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"7"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, output, "merge failed: Merge conflict")
	assert.Contains(t, output, "nothing was merged")
}

func TestRunMerge_AlreadyMergedOnSubmit(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(7, "main", openStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			return &github.AsyncMergeResult{Merged: true, Details: github.AsyncMergeDetails{SHA: "abc"}}, nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"7"})
	output := collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Contains(t, output, "Merged #1, #2 into main")
}

func TestRunMerge_AsyncMergeUnavailable(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(7, "main", openStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			return nil, github.ErrAsyncMergeUnavailable
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"7"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "not available for this repository")
}

func TestRunMerge_StacksUnavailable(t *testing.T) {
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn:       func(n int) (*github.RemoteStack, error) { return nil, notFoundErr() },
		FindStackForPRFn: func(n int) (*github.RemoteStack, error) { return nil, notFoundErr() },
	}

	err := runMerge(cfg, fastOptions(), []string{"5"})
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrStacksUnavailable)
	assert.Contains(t, output, "not enabled for this repository")
}

func TestRunMerge_NoArg_NotInStack(t *testing.T) {
	setupLocalStack(t, 100, "other", "b1", "b2") // current branch not in stack
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{}

	err := runMerge(cfg, fastOptions(), nil)
	output := collectOutput(cfg, outR, errR)

	assert.ErrorIs(t, err, ErrNotInStack)
	assert.Contains(t, output, "not part of a stack")
}

func TestRunMerge_DefaultMethodFallsBackToAllowed(t *testing.T) {
	var gotMethod string
	cfg, outR, errR := config.NewTestConfig()
	cfg.GitHubClientOverride = &github.MockClient{
		GetStackFn: func(n int) (*github.RemoteStack, error) {
			return remoteStack(7, "main", openStackPR(1, "b1"), openStackPR(2, "b2")), nil
		},
		RepoMergeConfigFn: func() (*github.RepoMergeConfig, error) {
			// Viewer default is a method the repo no longer allows.
			return &github.RepoMergeConfig{SquashAllowed: true, DefaultMethod: "merge"}, nil
		},
		MergeStackAsyncFn: func(pr int, method string) (*github.AsyncMergeResult, error) {
			gotMethod = method
			return &github.AsyncMergeResult{Queued: true, Details: github.AsyncMergeDetails{UUID: "u"}}, nil
		},
	}

	err := runMerge(cfg, fastOptions(), []string{"7"})
	_ = collectOutput(cfg, outR, errR)

	require.NoError(t, err)
	assert.Equal(t, "squash", gotMethod, "falls back to the only allowed method")
}

func TestResolveMergeMethodFlag(t *testing.T) {
	tests := []struct {
		name    string
		opts    mergeOptions
		want    string
		wantErr bool
	}{
		{"none", mergeOptions{}, "", false},
		{"merge", mergeOptions{merge: true}, "merge", false},
		{"squash", mergeOptions{squash: true}, "squash", false},
		{"rebase", mergeOptions{rebase: true}, "rebase", false},
		{"merge-method", mergeOptions{mergeMethod: "SQUASH"}, "squash", false},
		{"redundant same", mergeOptions{squash: true, mergeMethod: "squash"}, "squash", false},
		{"conflicting bools", mergeOptions{squash: true, rebase: true}, "", true},
		{"conflicting flag+bool", mergeOptions{merge: true, mergeMethod: "squash"}, "", true},
		{"invalid", mergeOptions{mergeMethod: "ff"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMergeMethodFlag(&tt.opts)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMergeCandidates(t *testing.T) {
	t.Run("all open", func(t *testing.T) {
		items, blocker := mergeCandidates(remoteStack(1, "main", openStackPR(1, "a"), openStackPR(2, "b")))
		assert.Nil(t, blocker)
		require.Len(t, items, 2)
		assert.Equal(t, 1, items[0].Number)
	})
	t.Run("leading merged skipped", func(t *testing.T) {
		items, blocker := mergeCandidates(remoteStack(1, "main", mergedStackPR(1, "a"), openStackPR(2, "b"), openStackPR(3, "c")))
		assert.Nil(t, blocker)
		assert.Equal(t, []mergeview.PRItem{{Number: 2, Branch: "b"}, {Number: 3, Branch: "c"}}, items)
	})
	t.Run("draft blocks above", func(t *testing.T) {
		items, blocker := mergeCandidates(remoteStack(1, "main", openStackPR(1, "a"), draftStackPR(2, "b"), openStackPR(3, "c")))
		require.NotNil(t, blocker)
		assert.Equal(t, 2, blocker.Number)
		assert.Equal(t, []mergeview.PRItem{{Number: 1, Branch: "a"}}, items)
	})
	t.Run("closed blocks", func(t *testing.T) {
		items, blocker := mergeCandidates(remoteStack(1, "main", closedStackPR(1, "a"), openStackPR(2, "b")))
		require.NotNil(t, blocker)
		assert.Empty(t, items)
	})
}
