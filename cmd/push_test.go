package cmd

import (
	"testing"

	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
)

func TestGeneratePRBody(t *testing.T) {
	tests := []struct {
		name          string
		stack         *stack.Stack
		currentBranch string
		wantContains  []string
		wantAbsent    []string
	}{
		{
			name: "single branch stack",
			stack: &stack.Stack{
				Trunk:    stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{{Branch: "feature"}},
			},
			currentBranch: "feature",
			wantContains: []string{
				"---",
				"**Stacked Pull Requests**",
				"- `feature` ← *this PR*",
				"- `main` (base)",
				"GitHub Stacks CLI",
				feedbackBaseURL,
			},
		},
		{
			name: "multi-branch current is topmost",
			stack: &stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "part-1", PullRequest: &stack.PullRequestRef{Number: 1, URL: "https://github.com/org/repo/pull/1"}},
					{Branch: "part-2", PullRequest: &stack.PullRequestRef{Number: 2, URL: "https://github.com/org/repo/pull/2"}},
					{Branch: "part-3"},
				},
			},
			currentBranch: "part-3",
			wantContains: []string{
				"- `part-3` ← *this PR*",
				"- `part-2` https://github.com/org/repo/pull/2",
				"- `part-1` https://github.com/org/repo/pull/1",
				"- `main` (base)",
			},
		},
		{
			name: "current is in the middle excludes upstack",
			stack: &stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "part-1", PullRequest: &stack.PullRequestRef{Number: 1, URL: "https://github.com/org/repo/pull/1"}},
					{Branch: "part-2"},
					{Branch: "part-3"},
				},
			},
			currentBranch: "part-2",
			wantContains: []string{
				"- `part-2` ← *this PR*",
				"- `part-1` https://github.com/org/repo/pull/1",
				"- `main` (base)",
			},
			wantAbsent: []string{
				"part-3",
			},
		},
		{
			name: "merged branches are skipped",
			stack: &stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "part-1", PullRequest: &stack.PullRequestRef{Number: 1, URL: "https://github.com/org/repo/pull/1", Merged: true}},
					{Branch: "part-2", PullRequest: &stack.PullRequestRef{Number: 2, URL: "https://github.com/org/repo/pull/2"}},
					{Branch: "part-3"},
				},
			},
			currentBranch: "part-3",
			wantContains: []string{
				"- `part-3` ← *this PR*",
				"- `part-2` https://github.com/org/repo/pull/2",
				"- `main` (base)",
			},
			wantAbsent: []string{
				"part-1",
			},
		},
		{
			name: "downstack branch without PR shows branch name only",
			stack: &stack.Stack{
				Trunk: stack.BranchRef{Branch: "main"},
				Branches: []stack.BranchRef{
					{Branch: "part-1"},
					{Branch: "part-2"},
				},
			},
			currentBranch: "part-2",
			wantContains: []string{
				"- `part-2` ← *this PR*",
				"- `part-1`",
				"- `main` (base)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generatePRBody(tt.stack, tt.currentBranch)
			for _, want := range tt.wantContains {
				assert.Contains(t, got, want)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, got, absent)
			}
		})
	}
}
