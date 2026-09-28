package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
					Number: 7,
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
		assert.Equal(t, 7, s.Number)
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

// --- IndexOfStack: locating a stack by identity ---

func TestIndexOfStack(t *testing.T) {
	sf := &StackFile{
		Stacks: []Stack{
			makeStack("main", "a1"),
			makeStack("main", "b1"),
			makeStack("main", "c1"),
		},
	}

	assert.Equal(t, 0, sf.IndexOfStack(&sf.Stacks[0]))
	assert.Equal(t, 2, sf.IndexOfStack(&sf.Stacks[2]))

	t.Run("not part of the file", func(t *testing.T) {
		other := makeStack("main", "z1")
		assert.Equal(t, -1, sf.IndexOfStack(&other))
	})
}

// --- Queued state: transient merge queue support ---

func makeQueuedBranch(name string, prNum int) BranchRef {
	return BranchRef{
		Branch:      name,
		PullRequest: &PullRequestRef{Number: prNum},
		Queued:      true,
	}
}

func TestIsQueued(t *testing.T) {
	t.Run("queued branch", func(t *testing.T) {
		b := makeQueuedBranch("b1", 1)
		assert.True(t, b.IsQueued())
		assert.False(t, b.IsMerged())
		assert.True(t, b.IsSkipped())
	})

	t.Run("merged branch", func(t *testing.T) {
		b := makeMergedBranch("b1", 1)
		assert.False(t, b.IsQueued())
		assert.True(t, b.IsMerged())
		assert.True(t, b.IsSkipped())
	})

	t.Run("active branch", func(t *testing.T) {
		b := BranchRef{Branch: "b1"}
		assert.False(t, b.IsQueued())
		assert.False(t, b.IsMerged())
		assert.False(t, b.IsSkipped())
	})
}

func TestQueuedBranches(t *testing.T) {
	s := Stack{
		Trunk: BranchRef{Branch: "main"},
		Branches: []BranchRef{
			{Branch: "b1"},
			makeQueuedBranch("b2", 2),
			{Branch: "b3"},
			makeQueuedBranch("b4", 4),
		},
	}
	queued := s.QueuedBranches()
	assert.Len(t, queued, 2)
	assert.Equal(t, "b2", queued[0].Branch)
	assert.Equal(t, "b4", queued[1].Branch)
}

func TestActiveBranches_ExcludesQueued(t *testing.T) {
	s := Stack{
		Trunk: BranchRef{Branch: "main"},
		Branches: []BranchRef{
			makeQueuedBranch("b1", 1),
			{Branch: "b2"},
			makeMergedBranch("b3", 3),
			{Branch: "b4"},
		},
	}
	active := s.ActiveBranches()
	assert.Len(t, active, 2)
	assert.Equal(t, "b2", active[0].Branch)
	assert.Equal(t, "b4", active[1].Branch)
}

func TestFirstActiveBranchIndex_SkipsQueued(t *testing.T) {
	t.Run("queued first, then active", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeQueuedBranch("b1", 1),
				{Branch: "b2"},
			},
		}
		assert.Equal(t, 1, s.FirstActiveBranchIndex())
	})

	t.Run("all queued", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeQueuedBranch("b1", 1),
				makeQueuedBranch("b2", 2),
			},
		}
		assert.Equal(t, -1, s.FirstActiveBranchIndex())
	})

	t.Run("merged then queued then active", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				makeQueuedBranch("b2", 2),
				{Branch: "b3"},
			},
		}
		assert.Equal(t, 2, s.FirstActiveBranchIndex())
	})
}

func TestActiveBranchIndices_SkipsQueued(t *testing.T) {
	s := Stack{
		Trunk: BranchRef{Branch: "main"},
		Branches: []BranchRef{
			makeQueuedBranch("b1", 1),
			{Branch: "b2"},
			makeMergedBranch("b3", 3),
			{Branch: "b4"},
			makeQueuedBranch("b5", 5),
		},
	}
	assert.Equal(t, []int{1, 3}, s.ActiveBranchIndices())
}

func TestActiveBaseBranch_SkipsQueued(t *testing.T) {
	tests := []struct {
		name     string
		stack    Stack
		branch   string
		expected string
	}{
		{
			name: "queued ancestor skipped to trunk",
			stack: Stack{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					makeQueuedBranch("b1", 1),
					{Branch: "b2"},
				},
			},
			branch:   "b2",
			expected: "main",
		},
		{
			name: "queued ancestor skipped to active sibling",
			stack: Stack{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					{Branch: "b1"},
					makeQueuedBranch("b2", 2),
					{Branch: "b3"},
				},
			},
			branch:   "b3",
			expected: "b1",
		},
		{
			name: "mixed merged and queued ancestors skip to trunk",
			stack: Stack{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					makeMergedBranch("b1", 1),
					makeQueuedBranch("b2", 2),
					{Branch: "b3"},
				},
			},
			branch:   "b3",
			expected: "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.stack.ActiveBaseBranch(tt.branch))
		})
	}
}

func TestQueuedState_NotPersisted(t *testing.T) {
	dir := t.TempDir()
	original := &StackFile{
		Repository: "owner/repo",
		Stacks: []Stack{
			{
				Trunk: BranchRef{Branch: "main"},
				Branches: []BranchRef{
					{
						Branch:      "b1",
						PullRequest: &PullRequestRef{Number: 1},
						Queued:      true, // set transient state
					},
				},
			},
		},
	}

	require.NoError(t, Save(dir, original))

	loaded, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, loaded.Stacks, 1)
	require.Len(t, loaded.Stacks[0].Branches, 1)

	// Queued state should NOT be persisted (json:"-")
	assert.False(t, loaded.Stacks[0].Branches[0].Queued)
	assert.False(t, loaded.Stacks[0].Branches[0].IsQueued())
}

func TestIsFullyMerged_NotAffectedByQueued(t *testing.T) {
	t.Run("all queued is not fully merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeQueuedBranch("b1", 1),
				makeQueuedBranch("b2", 2),
			},
		}
		assert.False(t, s.IsFullyMerged())
	})

	t.Run("merged and queued is not fully merged", func(t *testing.T) {
		s := Stack{
			Trunk: BranchRef{Branch: "main"},
			Branches: []BranchRef{
				makeMergedBranch("b1", 1),
				makeQueuedBranch("b2", 2),
			},
		}
		assert.False(t, s.IsFullyMerged())
	})
}

func TestNearestSurvivingBranch(t *testing.T) {
	// survivesIn returns a predicate reporting membership in the given set.
	survivesIn := func(names ...string) func(string) bool {
		set := make(map[string]bool, len(names))
		for _, n := range names {
			set[n] = true
		}
		return func(name string) bool { return set[name] }
	}

	tests := []struct {
		name     string
		order    []string
		target   string
		survives func(string) bool
		want     string
	}{
		{"prefers neighbor above", []string{"a", "b", "c"}, "b", survivesIn("a", "c"), "c"},
		{"falls to neighbor below", []string{"a", "b", "c"}, "c", survivesIn("a", "b"), "b"},
		{"skips dead neighbors above", []string{"a", "b", "c", "d"}, "b", survivesIn("a", "d"), "d"},
		{"target not in order", []string{"a", "b"}, "z", survivesIn("a", "b"), ""},
		{"no other survives", []string{"a", "b", "c"}, "b", survivesIn("b"), ""},
		{"empty order", nil, "a", survivesIn("a"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NearestSurvivingBranch(tt.order, tt.target, tt.survives))
		})
	}
}

func migrationTestFile(stacks ...Stack) StackFile {
	if stacks == nil {
		stacks = []Stack{}
	}
	return StackFile{SchemaVersion: schemaVersion, Repository: "github.com:owner/repo", Stacks: stacks}
}

func migrationTestStack() Stack {
	return Stack{
		ID:     "stack-global-id",
		Number: 17,
		Trunk:  BranchRef{Branch: "main", Head: "trunk-head", Base: "trunk-base"},
		Branches: []BranchRef{
			{
				Branch: "feature/one", Head: "first-head", Base: "first-base",
				PullRequest: &PullRequestRef{Number: 21, ID: "PR_one", URL: "https://example.com/pull/21", Merged: true},
			},
			{
				Branch: "feature/two", Head: "second-head", Base: "second-base",
				PullRequest: &PullRequestRef{Number: 22, ID: "PR_two", URL: "https://example.com/pull/22"},
			},
		},
	}
}

func writeMigrationTestData(t *testing.T, dir, relative string, data []byte) migrationCatalog {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, data, 0644))
	info, err := os.Stat(path)
	require.NoError(t, err)
	return migrationCatalog{Path: relative, Data: data, Mode: info.Mode().Perm()}
}

func writeMigrationTestCatalog(t *testing.T, dir, relative string, sf StackFile) migrationCatalog {
	t.Helper()
	data, err := json.MarshalIndent(sf, "", "    ")
	require.NoError(t, err)
	return writeMigrationTestData(t, dir, relative, append(data, '\n'))
}

func migrateWithTestOperationLock(t *testing.T, dir string) error {
	t.Helper()
	lock, err := LockOperation(dir)
	require.NoError(t, err)
	defer lock.Unlock()
	return MigrateLegacyState(dir)
}

func assertMigrationOriginals(t *testing.T, dir string, catalogs []migrationCatalog, migrated bool) {
	t.Helper()
	for _, catalog := range catalogs {
		path := filepath.Join(dir, filepath.FromSlash(catalog.Path))
		if migrated {
			if catalog.Path != stackFileName {
				assert.NoFileExists(t, path)
			}
			path += migrationBackupSuffix
		} else {
			assert.NoFileExists(t, path+migrationBackupSuffix)
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, catalog.Data, data, "original bytes at %s", path)
	}
	assert.NoFileExists(t, filepath.Join(dir, migrationFileName))
}

func TestMigrateLegacyState_Merge(t *testing.T) {
	detailed := migrationTestStack()
	other := makeStack("main", "other")
	other.Trunk.Head = "a-different-recorded-trunk-head"
	empty := makeStack("main")
	emptySlice := Stack{Trunk: BranchRef{Branch: "main"}, Branches: []BranchRef{}}
	tests := []struct {
		name   string
		common []Stack
		legacy [][]Stack
		want   []Stack
	}{
		{"absent common catalog", nil, [][]Stack{{detailed}, {other}}, []Stack{detailed, other}},
		{"disjoint common and linked catalogs", []Stack{detailed}, [][]Stack{{other}}, []Stack{detailed, other}},
		{"equivalent common and linked catalogs", []Stack{detailed}, [][]Stack{{detailed}}, []Stack{detailed}},
		{"equivalent linked catalogs", nil, [][]Stack{{detailed}, {detailed}}, []Stack{detailed}},
		{"deduplicate individual stacks", []Stack{detailed}, [][]Stack{{other, detailed}, {other}}, []Stack{detailed, other}},
		{"shared trunks with differing recorded heads", nil, [][]Stack{{detailed}, {other}}, []Stack{detailed, other}},
		{"a member can be another stacks trunk", []Stack{makeStack("main", "base")}, [][]Stack{{makeStack("base", "top")}}, []Stack{makeStack("main", "base"), makeStack("base", "top")}},
		{"equivalent empty branch lists", []Stack{empty}, [][]Stack{{emptySlice}}, []Stack{empty}},
		{"empty legacy catalog", nil, [][]Stack{{}}, []Stack{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			var catalogs []migrationCatalog
			if tt.common != nil {
				catalogs = append(catalogs, writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(tt.common...)))
			}
			for i, stacks := range tt.legacy {
				relative := fmt.Sprintf("worktrees/admin id %d/gh-stack", i)
				catalogs = append(catalogs, writeMigrationTestCatalog(t, dir, relative, migrationTestFile(stacks...)))
			}
			has, err := HasLegacyState(dir)
			require.NoError(t, err)
			require.True(t, has)
			require.NoError(t, migrateWithTestOperationLock(t, dir))

			got, err := Load(dir)
			require.NoError(t, err)
			assert.Equal(t, schemaVersion, got.SchemaVersion)
			assert.Equal(t, "github.com:owner/repo", got.Repository)
			assert.Equal(t, tt.want, got.Stacks)
			assertMigrationOriginals(t, dir, catalogs, true)
			if tt.common == nil {
				assert.NoFileExists(t, stackFilePath(dir)+migrationBackupSuffix)
			}
			first, err := os.ReadFile(stackFilePath(dir))
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(first, &fields))
			assert.Len(t, fields, 3, "the shared catalog retains its ordinary schema")
			has, err = HasLegacyState(dir)
			require.NoError(t, err)
			assert.False(t, has)
			require.NoError(t, migrateWithTestOperationLock(t, dir))
			second, err := os.ReadFile(stackFilePath(dir))
			require.NoError(t, err)
			assert.Equal(t, first, second)
			assertMigrationOriginals(t, dir, catalogs, true)
		})
	}
}

func TestMigrateLegacyState_Conflicts(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Stack)
	}{
		{"same ID different branches", func(s *Stack) { s.Branches = []BranchRef{{Branch: "different"}} }},
		{"same number different ID and branches", func(s *Stack) {
			s.ID = "different-id"
			s.Branches = []BranchRef{{Branch: "different"}}
		}},
		{"same branches different identities", func(s *Stack) { s.ID, s.Number = "different-id", 18 }},
		{"missing versus known identity", func(s *Stack) { s.ID, s.Number = "", 0 }},
		{"different recorded base", func(s *Stack) { s.Branches[0].Base = "new-base" }},
		{"different recorded head", func(s *Stack) { s.Branches[0].Head = "new-head" }},
		{"different trunk head", func(s *Stack) { s.Trunk.Head = "new-trunk-head" }},
		{"different trunk base", func(s *Stack) { s.Trunk.Base = "new-trunk-base" }},
		{"different PR number", func(s *Stack) { s.Branches[0].PullRequest.Number++ }},
		{"different PR ID", func(s *Stack) { s.Branches[0].PullRequest.ID = "new-PR-id" }},
		{"different PR URL", func(s *Stack) { s.Branches[0].PullRequest.URL = "https://example.com/new" }},
		{"different merge state", func(s *Stack) { s.Branches[0].PullRequest.Merged = false }},
		{"longer stack must not win", func(s *Stack) { s.Branches = append(s.Branches, BranchRef{Branch: "extra"}) }},
		{"different order", func(s *Stack) { s.Branches[0], s.Branches[1] = s.Branches[1], s.Branches[0] }},
	}
	for _, tt := range tests {
		for _, withCommon := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/common=%t", tt.name, withCommon), func(t *testing.T) {
				dir := t.TempDir()
				first := migrationTestStack()
				second := migrationTestStack()
				tt.change(&second)
				firstPath := "worktrees/first/gh-stack"
				if withCommon {
					firstPath = stackFileName
				}
				catalogs := []migrationCatalog{
					writeMigrationTestCatalog(t, dir, firstPath, migrationTestFile(first)),
					writeMigrationTestCatalog(t, dir, "worktrees/second/gh-stack", migrationTestFile(second)),
				}
				err := migrateWithTestOperationLock(t, dir)
				var conflict *MigrationConflictError
				require.ErrorAs(t, err, &conflict)
				assert.ElementsMatch(t, []string{filepath.Join(dir, filepath.FromSlash(firstPath)), filepath.Join(dir, "worktrees", "second", stackFileName)}, conflict.Sources)
				assert.Contains(t, conflict.Branches, "feature/one")
				assert.Contains(t, err.Error(), "reconcile or recreate")
				assertMigrationOriginals(t, dir, catalogs, false)
			})
		}
	}

	t.Run("overlapping local stacks without IDs", func(t *testing.T) {
		dir := t.TempDir()
		catalogs := []migrationCatalog{
			writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(makeStack("main", "shared", "one"))),
			writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "shared", "two"))),
		}
		var conflict *MigrationConflictError
		require.ErrorAs(t, migrateWithTestOperationLock(t, dir), &conflict)
		assert.Equal(t, []string{"shared"}, conflict.Branches)
		assertMigrationOriginals(t, dir, catalogs, false)
	})

	t.Run("duplicate branch within one stack", func(t *testing.T) {
		dir := t.TempDir()
		catalogs := []migrationCatalog{
			writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "duplicate", "duplicate"))),
		}
		var conflict *MigrationConflictError
		require.ErrorAs(t, migrateWithTestOperationLock(t, dir), &conflict)
		assert.Equal(t, []string{"duplicate"}, conflict.Branches)
		assert.Equal(t, []string{filepath.Join(dir, "worktrees", "linked", stackFileName)}, conflict.Sources)
		assertMigrationOriginals(t, dir, catalogs, false)
	})
}

func TestMigrateLegacyState_RepositoryIdentity(t *testing.T) {
	for _, repositories := range [][2]string{
		{"github.com:owner/repo", "github.com:other/repo"},
		{"", "github.com:owner/repo"},
		{"github.com:owner/repo", ""},
		{"", ""},
	} {
		t.Run(fmt.Sprintf("%q and %q", repositories[0], repositories[1]), func(t *testing.T) {
			dir := t.TempDir()
			first, second := migrationTestFile(makeStack("main", "one")), migrationTestFile(makeStack("main", "two"))
			first.Repository, second.Repository = repositories[0], repositories[1]
			catalogs := []migrationCatalog{
				writeMigrationTestCatalog(t, dir, stackFileName, first),
				writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", second),
			}
			err := migrateWithTestOperationLock(t, dir)
			if repositories[0] != "" && repositories[1] != "" && repositories[0] != repositories[1] {
				var conflict *MigrationConflictError
				require.ErrorAs(t, err, &conflict)
				assert.Contains(t, conflict.Reason, "repository")
				assertMigrationOriginals(t, dir, catalogs, false)
				return
			}
			require.NoError(t, err)
			sf, err := Load(dir)
			require.NoError(t, err)
			want := repositories[0]
			if want == "" {
				want = repositories[1]
			}
			assert.Equal(t, want, sf.Repository)
			assertMigrationOriginals(t, dir, catalogs, true)
		})
	}
}

func TestMigrateLegacyState_InvalidCatalogs(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"malformed JSON", "{not JSON"},
		{"null catalog", "null"},
		{"array catalog", "[]"},
		{"future schema", `{"schemaVersion":999,"stacks":[]}`},
		{"negative schema", `{"schemaVersion":-1,"stacks":[]}`},
		{"missing schema", `{"stacks":[]}`},
		{"missing stacks", `{"schemaVersion":1}`},
		{"null stacks", `{"schemaVersion":1,"stacks":null}`},
		{"unknown metadata", `{"schemaVersion":1,"stacks":[],"futureMetadata":true}`},
		{"unnamed trunk", `{"schemaVersion":1,"stacks":[{"trunk":{},"branches":[]}]}`},
		{"missing branches", `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"}}]}`},
		{"unnamed branch", `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{}]}]}`},
		{"unknown branch metadata", `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"a","unknown":"value"}]}]}`},
		{"invalid PR type", `{"schemaVersion":1,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"a","pullRequest":{"number":"21"}}]}]}`},
	}
	for _, tt := range tests {
		for _, invalidCommon := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/common=%t", tt.name, invalidCommon), func(t *testing.T) {
				dir := t.TempDir()
				invalid, valid := "worktrees/invalid/gh-stack", stackFileName
				if invalidCommon {
					invalid, valid = stackFileName, "worktrees/valid/gh-stack"
				}
				catalogs := []migrationCatalog{
					writeMigrationTestData(t, dir, invalid, []byte(tt.data)),
					writeMigrationTestCatalog(t, dir, valid, migrationTestFile(makeStack("main", "valid"))),
				}
				err := migrateWithTestOperationLock(t, dir)
				require.Error(t, err)
				assert.Contains(t, err.Error(), fmt.Sprintf("%q", filepath.Join(dir, filepath.FromSlash(invalid))))
				assertMigrationOriginals(t, dir, catalogs, false)
			})
		}
	}

	t.Run("older schema keeps existing load compatibility", func(t *testing.T) {
		dir := t.TempDir()
		catalog := writeMigrationTestData(t, dir, "worktrees/older/gh-stack", []byte(`{"schemaVersion":0,"stacks":[{"trunk":{"branch":"main"},"branches":[{"branch":"old"}]}]}`))
		require.NoError(t, migrateWithTestOperationLock(t, dir))
		sf, err := Load(dir)
		require.NoError(t, err)
		assert.Equal(t, schemaVersion, sf.SchemaVersion)
		assert.Equal(t, []Stack{makeStack("main", "old")}, sf.Stacks)
		assertMigrationOriginals(t, dir, []migrationCatalog{catalog}, true)
	})
}

func TestMigrateLegacyState_RecoveryBlocks(t *testing.T) {
	for _, location := range []string{".", "worktrees/linked"} {
		for _, name := range []string{"gh-stack-rebase-state", "gh-stack-modify-state"} {
			t.Run(location+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				catalogs := []migrationCatalog{
					writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(makeStack("main", "common"))),
					writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "linked"))),
				}
				recoveryPath := filepath.Join(dir, filepath.FromSlash(location), name)
				require.NoError(t, os.WriteFile(recoveryPath, []byte("{even a damaged recovery record blocks migration"), 0600))
				err := migrateWithTestOperationLock(t, dir)
				var blocked *MigrationBlockedError
				require.ErrorAs(t, err, &blocked)
				assert.Equal(t, []string{recoveryPath}, blocked.RecoveryPaths)
				assert.Contains(t, err.Error(), "original worktree")
				assertMigrationOriginals(t, dir, catalogs, false)
				assert.FileExists(t, recoveryPath)
				require.NoError(t, os.Remove(recoveryPath))
				require.NoError(t, migrateWithTestOperationLock(t, dir))
				assertMigrationOriginals(t, dir, catalogs, true)
			})
		}
	}

	t.Run("common journal blocks even with no common catalog", func(t *testing.T) {
		dir := t.TempDir()
		catalog := writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "linked")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "gh-stack-rebase-state"), []byte("{}"), 0600))
		var blocked *MigrationBlockedError
		require.ErrorAs(t, migrateWithTestOperationLock(t, dir), &blocked)
		assert.NoFileExists(t, stackFilePath(dir))
		assertMigrationOriginals(t, dir, []migrationCatalog{catalog}, false)
	})

	t.Run("unrelated worktree without a catalog is not touched", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "linked")))
		recovery := writeMigrationTestData(t, dir, "worktrees/unrelated/gh-stack-modify-state", []byte("{}"))
		require.NoError(t, migrateWithTestOperationLock(t, dir))
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(recovery.Path)))
		require.NoError(t, err)
		assert.Equal(t, recovery.Data, data)
	})
}

func TestHasLegacyState_RetainedAdministrationDirectories(t *testing.T) {
	dir := t.TempDir()
	has, err := HasLegacyState(dir)
	require.NoError(t, err)
	assert.False(t, has)
	common := writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(makeStack("main", "common")))
	has, err = HasLegacyState(dir)
	require.NoError(t, err)
	assert.False(t, has)
	require.NoError(t, migrateWithTestOperationLock(t, dir))
	assertMigrationOriginals(t, dir, []migrationCatalog{common}, false)

	legacy := writeMigrationTestCatalog(t, dir, "worktrees/unrelated-admin-id/gh-stack", migrationTestFile(makeStack("main", "linked")))
	adminDir := filepath.Dir(filepath.Join(dir, filepath.FromSlash(legacy.Path)))
	gitdir := []byte(filepath.Join(dir, "missing worktree with different basename", ".git") + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(adminDir, "gitdir"), gitdir, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(adminDir, "locked"), []byte("retained worktree"), 0644))
	has, err = HasLegacyState(dir)
	require.NoError(t, err)
	assert.True(t, has)
	require.NoError(t, migrateWithTestOperationLock(t, dir))
	assertMigrationOriginals(t, dir, []migrationCatalog{common, legacy}, true)
	data, err := os.ReadFile(filepath.Join(adminDir, "gitdir"))
	require.NoError(t, err)
	assert.Equal(t, gitdir, data)
	assert.FileExists(t, filepath.Join(adminDir, "locked"))
	assert.DirExists(t, adminDir)
	has, err = HasLegacyState(dir)
	require.NoError(t, err)
	assert.False(t, has, "archived catalogs do not trigger re-import")

	require.NoError(t, os.WriteFile(filepath.Join(dir, migrationFileName), []byte("{}"), 0600))
	has, err = HasLegacyState(dir)
	require.NoError(t, err)
	assert.True(t, has, "a pending journal must be detected even after every catalog was archived")
}

func TestLegacyState_MetadataErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"worktrees is a file", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "worktrees"), []byte("not a directory"), 0600))
		}},
		{"admin entry is a file", func(t *testing.T, dir string) {
			writeMigrationTestData(t, dir, "worktrees/not-a-directory", []byte("metadata"))
		}},
		{"catalog is a directory", func(t *testing.T, dir string) {
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "worktrees", "linked", stackFileName), 0755))
		}},
		{"catalog is a dangling symlink", func(t *testing.T, dir string) {
			admin := filepath.Join(dir, "worktrees", "linked")
			require.NoError(t, os.MkdirAll(admin, 0755))
			if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(admin, stackFileName)); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("symlinks unavailable: %v", err)
				}
				require.NoError(t, err)
			}
		}},
		{"symlinked admin is not silently skipped", func(t *testing.T, dir string) {
			target := filepath.Join(dir, "target")
			require.NoError(t, os.MkdirAll(target, 0755))
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "worktrees"), 0755))
			if err := os.Symlink(target, filepath.Join(dir, "worktrees", "linked")); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("symlinks unavailable: %v", err)
				}
				require.NoError(t, err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			_, err := HasLegacyState(dir)
			require.Error(t, err)
			require.Error(t, migrateWithTestOperationLock(t, dir))
			assert.NoFileExists(t, stackFilePath(dir))
			assert.NoFileExists(t, filepath.Join(dir, migrationFileName))
		})
	}

	t.Run("missing common directory", func(t *testing.T) {
		_, err := HasLegacyState(filepath.Join(t.TempDir(), "missing"))
		require.Error(t, err)
	})

	for _, inaccessible := range []string{"admin directory", "catalog"} {
		t.Run("inaccessible "+inaccessible, func(t *testing.T) {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("requires Unix permission enforcement")
			}
			dir := t.TempDir()
			writeMigrationTestCatalog(t, dir, "worktrees/a-readable/gh-stack", migrationTestFile(makeStack("main", "readable")))
			catalog := writeMigrationTestCatalog(t, dir, "worktrees/z-inaccessible/gh-stack", migrationTestFile(makeStack("main", "hidden")))
			path := filepath.Join(dir, filepath.FromSlash(catalog.Path))
			if inaccessible == "admin directory" {
				path = filepath.Dir(path)
			}
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(path, 0))
			t.Cleanup(func() { assert.NoError(t, os.Chmod(path, info.Mode().Perm())) })
			_, err = HasLegacyState(dir)
			require.Error(t, err, "a readable catalog must not hide a later access failure")
			require.Error(t, migrateWithTestOperationLock(t, dir))
			assert.NoFileExists(t, stackFilePath(dir))
		})
	}
}

func TestMigrateLegacyState_BackupCollisions(t *testing.T) {
	for _, relative := range []string{stackFileName, "worktrees/linked/gh-stack"} {
		for _, kind := range []string{"equivalent bytes", "different bytes", "directory", "symlink"} {
			t.Run(relative+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				catalogs := []migrationCatalog{
					writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(makeStack("main", "common"))),
					writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "linked"))),
				}
				path := filepath.Join(dir, filepath.FromSlash(relative)) + migrationBackupSuffix
				original := catalogs[0].Data
				if relative != stackFileName {
					original = catalogs[1].Data
				}
				switch kind {
				case "equivalent bytes":
					require.NoError(t, os.WriteFile(path, original, 0600))
				case "different bytes":
					require.NoError(t, os.WriteFile(path, []byte("do not overwrite this backup"), 0600))
				case "directory":
					require.NoError(t, os.Mkdir(path, 0755))
				case "symlink":
					if err := os.Symlink(filepath.Join(dir, filepath.FromSlash(relative)), path); err != nil {
						if runtime.GOOS == "windows" {
							t.Skipf("symlinks unavailable: %v", err)
						}
						require.NoError(t, err)
					}
				}
				err := migrateWithTestOperationLock(t, dir)
				if kind == "equivalent bytes" {
					require.NoError(t, err)
					assertMigrationOriginals(t, dir, catalogs, true)
					return
				}
				require.Error(t, err)
				assert.Contains(t, err.Error(), fmt.Sprintf("%q", path))
				if kind == "different bytes" {
					var conflict *MigrationConflictError
					require.ErrorAs(t, err, &conflict)
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, "do not overwrite this backup", string(data))
				} else if kind == "directory" {
					assert.DirExists(t, path)
				} else {
					info, err := os.Lstat(path)
					require.NoError(t, err)
					assert.NotZero(t, info.Mode()&os.ModeSymlink)
				}
				for _, catalog := range catalogs {
					data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(catalog.Path)))
					require.NoError(t, err)
					assert.Equal(t, catalog.Data, data)
				}
				assert.NoFileExists(t, filepath.Join(dir, migrationFileName))
			})
		}
	}
}

func TestMigrateLegacyState_InterruptedMigration(t *testing.T) {
	tests := []struct {
		name      string
		common    bool
		published bool
		backups   int
		archived  int
	}{
		{"before publication", true, false, 0, 0},
		{"after publication before backups", true, true, 0, 0},
		{"after common backup", true, true, 1, 0},
		{"after linked backup before archive", true, true, 2, 0},
		{"after partial archival", true, true, 2, 1},
		{"after all archival before journal removal", true, true, 3, 2},
		{"absent common before publication", false, false, 0, 0},
		{"absent common after publication", false, true, 0, 0},
		{"absent common partially archived", false, true, 1, 1},
		{"absent common fully archived", false, true, 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			var catalogs []migrationCatalog
			want := migrationTestFile()
			if tt.common {
				common := migrationTestStack()
				catalogs = append(catalogs, writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(common)))
				want.Stacks = append(want.Stacks, common)
			}
			for i := range 2 {
				s := makeStack("main", fmt.Sprintf("linked-%d", i))
				catalogs = append(catalogs, writeMigrationTestCatalog(t, dir, fmt.Sprintf("worktrees/linked-%d/gh-stack", i), migrationTestFile(s)))
				want.Stacks = append(want.Stacks, s)
			}
			journal, err := json.Marshal(migrationState{Version: migrationVersion, Catalogs: catalogs})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, migrationFileName), journal, 0600))
			merged, err := json.MarshalIndent(want, "", "  ")
			require.NoError(t, err)
			if tt.published {
				require.NoError(t, os.WriteFile(stackFilePath(dir), merged, 0644))
			}
			for _, catalog := range catalogs[:tt.backups] {
				require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(catalog.Path))+migrationBackupSuffix, catalog.Data, catalog.Mode))
			}
			archived := 0
			for _, catalog := range catalogs {
				if archived == tt.archived {
					break
				}
				if catalog.Path != stackFileName {
					require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(catalog.Path))))
					archived++
				}
			}
			has, err := HasLegacyState(dir)
			require.NoError(t, err)
			require.True(t, has)
			require.NoError(t, migrateWithTestOperationLock(t, dir))
			got, err := Load(dir)
			require.NoError(t, err)
			assert.Equal(t, want.Stacks, got.Stacks)
			assert.Equal(t, want.Repository, got.Repository)
			assertMigrationOriginals(t, dir, catalogs, true)
			require.NoError(t, migrateWithTestOperationLock(t, dir))
			assertMigrationOriginals(t, dir, catalogs, true)
		})
	}
}

func TestMigrateLegacyState_InterruptedMigrationRefusesChanges(t *testing.T) {
	for _, kind := range []string{"common modified", "legacy modified", "legacy disappeared", "new catalog", "backup conflict", "archive before publication", "recovery appeared"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			catalogs := []migrationCatalog{
				writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(makeStack("main", "common"))),
				writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "linked"))),
			}
			journal, err := json.Marshal(migrationState{Version: migrationVersion, Catalogs: catalogs})
			require.NoError(t, err)
			journalPath := filepath.Join(dir, migrationFileName)
			require.NoError(t, os.WriteFile(journalPath, journal, 0600))
			merged, err := json.MarshalIndent(migrationTestFile(makeStack("main", "common"), makeStack("main", "linked")), "", "  ")
			require.NoError(t, err)
			if kind != "archive before publication" {
				require.NoError(t, os.WriteFile(stackFilePath(dir), merged, 0644))
			}
			legacyPath := filepath.Join(dir, "worktrees", "linked", stackFileName)
			switch kind {
			case "common modified":
				require.NoError(t, os.WriteFile(stackFilePath(dir), []byte("externally changed common catalog"), 0644))
			case "legacy modified":
				require.NoError(t, os.WriteFile(legacyPath, []byte("externally changed legacy catalog"), 0644))
			case "legacy disappeared":
				require.NoError(t, os.Remove(legacyPath))
			case "new catalog":
				writeMigrationTestCatalog(t, dir, "worktrees/new/gh-stack", migrationTestFile(makeStack("main", "new")))
			case "backup conflict":
				require.NoError(t, os.WriteFile(legacyPath+migrationBackupSuffix, []byte("existing unrelated backup"), 0644))
			case "archive before publication":
				require.NoError(t, os.WriteFile(legacyPath+migrationBackupSuffix, catalogs[1].Data, 0644))
				require.NoError(t, os.Remove(legacyPath))
			case "recovery appeared":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "gh-stack-rebase-state"), []byte("{}"), 0600))
			}
			before, err := os.ReadFile(stackFilePath(dir))
			require.NoError(t, err)
			err = migrateWithTestOperationLock(t, dir)
			if kind == "recovery appeared" {
				var blocked *MigrationBlockedError
				require.ErrorAs(t, err, &blocked)
			} else {
				var conflict *MigrationConflictError
				require.ErrorAs(t, err, &conflict)
			}
			after, err := os.ReadFile(stackFilePath(dir))
			require.NoError(t, err)
			assert.Equal(t, before, after)
			afterJournal, err := os.ReadFile(journalPath)
			require.NoError(t, err)
			assert.Equal(t, journal, afterJournal, "recovery snapshots must remain available")
			assert.NoFileExists(t, stackFilePath(dir)+migrationBackupSuffix)
		})
	}
}

func TestMigrateLegacyState_InvalidJournals(t *testing.T) {
	for _, kind := range []string{"invalid JSON", "null", "new version", "missing version", "missing catalogs", "unsafe path", "duplicate path", "invalid mode"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			catalog := writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "linked")))
			state := migrationState{Version: migrationVersion, Catalogs: []migrationCatalog{catalog}}
			switch kind {
			case "new version":
				state.Version++
			case "missing version":
				state.Version = 0
			case "missing catalogs":
				state.Catalogs = nil
			case "unsafe path":
				state.Catalogs[0].Path = "../gh-stack"
			case "duplicate path":
				state.Catalogs = append(state.Catalogs, catalog)
			case "invalid mode":
				state.Catalogs[0].Mode = os.ModeSymlink
			}
			journal, err := json.Marshal(state)
			require.NoError(t, err)
			if kind == "invalid JSON" {
				journal = []byte("{incomplete")
			} else if kind == "null" {
				journal = []byte("null")
			} else if kind == "missing version" {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(journal, &fields))
				delete(fields, "version")
				journal, err = json.Marshal(fields)
				require.NoError(t, err)
			}
			journalPath := filepath.Join(dir, migrationFileName)
			require.NoError(t, os.WriteFile(journalPath, journal, 0600))
			require.Error(t, migrateWithTestOperationLock(t, dir))
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(catalog.Path)))
			require.NoError(t, err)
			assert.Equal(t, catalog.Data, data)
			assert.FileExists(t, journalPath)
			assert.NoFileExists(t, stackFilePath(dir))
			assert.NoFileExists(t, filepath.Join(dir, filepath.FromSlash(catalog.Path))+migrationBackupSuffix)
		})
	}
}

func TestMigrateLegacyState_PreservesStaleDetection(t *testing.T) {
	dir := t.TempDir()
	writeMigrationTestCatalog(t, dir, stackFileName, migrationTestFile(makeStack("main", "common")))
	writeMigrationTestCatalog(t, dir, "worktrees/linked/gh-stack", migrationTestFile(makeStack("main", "linked")))
	before, err := Load(dir)
	require.NoError(t, err)
	operation, err := LockOperation(dir)
	require.NoError(t, err)
	defer operation.Unlock()
	require.NoError(t, MigrateLegacyState(dir))
	err = Save(dir, before)
	var stale *StaleError
	require.True(t, errors.As(err, &stale))
	after, err := Load(dir)
	require.NoError(t, err)
	after.AddStack(makeStack("main", "new"))
	require.NoError(t, Save(dir, after))
	require.NoError(t, Save(dir, after), "atomic publication must refresh the load checksum")
}
