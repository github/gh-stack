package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeStack(trunk string, branches ...string) Stack {
	s := Stack{Trunk: BranchRef{Branch: trunk}}
	for _, b := range branches {
		s.Branches = append(s.Branches, BranchRef{Branch: b})
	}
	return s
}

func makeMergedBranch(name string, prNum int) BranchRef {
	return BranchRef{Branch: name, PullRequest: &PullRequestRef{Number: prNum, Merged: true}}
}

// --- ActiveBaseBranch: skipping merged ancestors for rebase ---

func TestActiveBaseBranch(t *testing.T) {
	tests := []struct {
		name     string
		stack    Stack
		branch   string
		expected string
	}{
		{
			name: "no merged ancestors returns previous branch",
			stack: Stack{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					{Branch: "b1"},
					{Branch: "b2"},
					{Branch: "b3"},
				},
			},
			branch:   "b3",
			expected: "b2",
		},
		{
			name: "immediate ancestor merged skips to next non-merged",
			stack: Stack{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					{Branch: "b1"},
					makeMergedBranch("b2", 10),
					{Branch: "b3"},
				},
			},
			branch:   "b3",
			expected: "b1",
		},
		{
			name: "all ancestors merged returns trunk",
			stack: Stack{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					makeMergedBranch("b1", 1),
					makeMergedBranch("b2", 2),
					{Branch: "b3"},
				},
			},
			branch:   "b3",
			expected: "main",
		},
		{
			name: "first branch always returns trunk",
			stack: Stack{
				Trunk:    BranchRef{Branch: "main"},
				Branches: []BranchRef{{Branch: "b1"}},
			},
			branch:   "b1",
			expected: "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.stack.ActiveBaseBranch(tt.branch))
		})
	}
}

// --- ActiveBranches / MergedBranches partition ---

func TestActiveBranches_And_MergedBranches(t *testing.T) {
	t.Run("all active", func(t *testing.T) {
		s := makeStack("main", "b1", "b2", "b3")
		assert.Len(t, s.ActiveBranches(), 3)
		assert.Empty(t, s.MergedBranches())
	})

	t.Run("some merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				{Branch: "b2"},
				makeMergedBranch("b3", 3),
			},
		}
		active := s.ActiveBranches()
		merged := s.MergedBranches()

		assert.Len(t, active, 1)
		assert.Equal(t, "b2", active[0].Branch)
		assert.Len(t, merged, 2)
		assert.Equal(t, "b1", merged[0].Branch)
		assert.Equal(t, "b3", merged[1].Branch)
	})

	t.Run("all merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				makeMergedBranch("b2", 2),
			},
		}
		assert.Empty(t, s.ActiveBranches())
		assert.Len(t, s.MergedBranches(), 2)
	})
}

// --- IsFullyMerged: blocks add on fully-merged stacks ---

func TestIsFullyMerged(t *testing.T) {
	t.Run("all merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				makeMergedBranch("b2", 2),
			},
		}
		assert.True(t, s.IsFullyMerged())
	})

	t.Run("some active", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				{Branch: "b2"},
			},
		}
		assert.False(t, s.IsFullyMerged())
	})

	t.Run("empty branches is not fully merged", func(t *testing.T) {
		s := Stack{Trunk: BranchRef{Branch: "main"}}
		assert.False(t, s.IsFullyMerged())
	})
}

// --- FirstActiveBranchIndex: navigation ---

func TestFirstActiveBranchIndex(t *testing.T) {
	t.Run("first is active", func(t *testing.T) {
		s := makeStack("main", "b1", "b2")
		assert.Equal(t, 0, s.FirstActiveBranchIndex())
	})

	t.Run("first two merged third active", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				makeMergedBranch("b2", 2),
				{Branch: "b3"},
			},
		}
		assert.Equal(t, 2, s.FirstActiveBranchIndex())
	})

	t.Run("all merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				makeMergedBranch("b2", 2),
			},
		}
		assert.Equal(t, -1, s.FirstActiveBranchIndex())
	})
}

// --- ActiveBranchIndices: navigation ---

func TestActiveBranchIndices(t *testing.T) {
	t.Run("all active", func(t *testing.T) {
		s := makeStack("main", "b1", "b2", "b3")
		assert.Equal(t, []int{0, 1, 2}, s.ActiveBranchIndices())
	})

	t.Run("some merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				{Branch: "b2"},
				makeMergedBranch("b3", 3),
				{Branch: "b4"},
			},
		}
		assert.Equal(t, []int{1, 3}, s.ActiveBranchIndices())
	})

	t.Run("all merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				makeMergedBranch("b2", 2),
			},
		}
		assert.Empty(t, s.ActiveBranchIndices())
	})
}

// --- Load / Save round-trip persistence ---

func TestLoad_Save_RoundTrip(t *testing.T) {
	t.Run("save and reload preserves all fields", func(t *testing.T) {
		dir := t.TempDir()
		original := &StackFile{
			Repository: "owner/repo",
			Stacks: []Stack{
				{
					ID:     "s1",
					Prefix: "feat",
					Trunk:  BranchRef{Branch: "main", Head: "abc123"},
					Branches: []BranchRef{
						{Branch: "b1", Head: "def456", Base: "abc123"},
						{Branch: "b2", PullRequest: &PullRequestRef{Number: 42, ID: "PR_id", URL: "https://example.com", Merged: true}},
					},
				},
			},
		}

		require.NoError(t, Save(dir, original))

		loaded, err := Load(dir)
		require.NoError(t, err)

		assert.Equal(t, schemaVersion, loaded.SchemaVersion)
		assert.Equal(t, original.Repository, loaded.Repository)
		require.Len(t, loaded.Stacks, 1)

		s := loaded.Stacks[0]
		assert.Equal(t, "s1", s.ID)
		assert.Equal(t, "feat", s.Prefix)
		assert.Equal(t, "main", s.Trunk.Branch)
		assert.Equal(t, "abc123", s.Trunk.Head)
		require.Len(t, s.Branches, 2)
		assert.Equal(t, "b1", s.Branches[0].Branch)
		assert.Equal(t, "def456", s.Branches[0].Head)
		assert.Equal(t, "abc123", s.Branches[0].Base)
		require.NotNil(t, s.Branches[1].PullRequest)
		assert.Equal(t, 42, s.Branches[1].PullRequest.Number)
		assert.True(t, s.Branches[1].PullRequest.Merged)
	})

	t.Run("missing file returns empty stack file", func(t *testing.T) {
		dir := t.TempDir()
		sf, err := Load(dir)
		require.NoError(t, err)
		assert.Equal(t, schemaVersion, sf.SchemaVersion)
		assert.Empty(t, sf.Stacks)
	})

	t.Run("future schema version returns error", func(t *testing.T) {
		dir := t.TempDir()
		data, _ := json.Marshal(StackFile{SchemaVersion: 999})
		require.NoError(t, os.WriteFile(filepath.Join(dir, stackFileName), data, 0644))

		_, err := Load(dir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "999")
	})

	t.Run("corrupt JSON returns error", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, stackFileName), []byte("{not json!"), 0644))

		_, err := Load(dir)
		assert.Error(t, err)
	})
}

// --- FindStackByPRNumber: used by checkout ---

func TestFindStackByPRNumber(t *testing.T) {
	sf := &StackFile{
		Stacks: []Stack{
			{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					{Branch: "b1", PullRequest: &PullRequestRef{Number: 10}},
					{Branch: "b2", PullRequest: &PullRequestRef{Number: 20}},
				},
			},
			{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					{Branch: "b3", PullRequest: &PullRequestRef{Number: 30}},
				},
			},
		},
	}

	t.Run("found", func(t *testing.T) {
		s, b := sf.FindStackByPRNumber(20)
		require.NotNil(t, s)
		require.NotNil(t, b)
		assert.Equal(t, "b2", b.Branch)
	})

	t.Run("found in second stack", func(t *testing.T) {
		s, b := sf.FindStackByPRNumber(30)
		require.NotNil(t, s)
		require.NotNil(t, b)
		assert.Equal(t, "b3", b.Branch)
	})

	t.Run("not found", func(t *testing.T) {
		s, b := sf.FindStackByPRNumber(999)
		assert.Nil(t, s)
		assert.Nil(t, b)
	})
}

// --- ValidateNoDuplicateBranch: guards against duplicates ---

func TestValidateNoDuplicateBranch(t *testing.T) {
	sf := &StackFile{
		Stacks: []Stack{
			makeStack("main", "b1", "b2"),
		},
	}

	t.Run("branch in stack returns error", func(t *testing.T) {
		assert.Error(t, sf.ValidateNoDuplicateBranch("b1"))
	})

	t.Run("trunk returns error because Contains checks trunk", func(t *testing.T) {
		assert.Error(t, sf.ValidateNoDuplicateBranch("main"))
	})

	t.Run("new branch returns nil", func(t *testing.T) {
		assert.NoError(t, sf.ValidateNoDuplicateBranch("new-branch"))
	})
}

// --- RemoveStackForBranch: used by unstack ---

func TestRemoveStackForBranch(t *testing.T) {
	t.Run("found and removed", func(t *testing.T) {
		sf := &StackFile{
			Stacks: []Stack{
				makeStack("main", "b1"),
				makeStack("main", "b2"),
			},
		}
		assert.True(t, sf.RemoveStackForBranch("b1"))
		require.Len(t, sf.Stacks, 1)
		assert.Equal(t, "b2", sf.Stacks[0].Branches[0].Branch)
	})

	t.Run("not found", func(t *testing.T) {
		sf := &StackFile{
			Stacks: []Stack{makeStack("main", "b1")},
		}
		assert.False(t, sf.RemoveStackForBranch("nonexistent"))
		assert.Len(t, sf.Stacks, 1)
	})
}
