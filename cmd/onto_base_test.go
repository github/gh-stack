package cmd

import (
	"errors"
	"testing"

	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
)

// ancestryMock builds an IsAncestor func from an explicit linear history per
// branch: history[branch] lists the SHAs that branch contains, oldest first.
func ancestryMock(history map[string][]string) func(string, string) (bool, error) {
	index := func(list []string, sha string) int {
		for i, s := range list {
			if s == sha {
				return i
			}
		}
		return -1
	}
	return func(ancestor, descendant string) (bool, error) {
		// descendant may be a branch name or a SHA within some branch.
		for branch, list := range history {
			if branch != descendant && index(list, descendant) < 0 {
				continue
			}
			end := len(list)
			if i := index(list, descendant); i >= 0 {
				end = i + 1
			}
			if index(list[:end], ancestor) >= 0 {
				return true, nil
			}
		}
		return false, nil
	}
}

func TestResolveOntoOldBase(t *testing.T) {
	// b2 contains: trunk -> a-old -> b2-own
	// The parent (b1) has since been amended to a-new, which b2 does NOT have.
	history := map[string][]string{
		"b2": {"trunk", "a-old", "b2-own"},
		"b1": {"trunk", "a-new"},
	}

	t.Run("recorded upstream is used when the branch contains it", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{IsAncestorFn: ancestryMock(history)})
		defer restore()

		got := resolveOntoOldBase("a-old", "a-old", "b1", "b2")
		assert.Equal(t, "a-old", got)
	})

	// The #250 case: the parent was amended, so its current tip is not in the
	// branch's history. Falling back to a merge base would replay the parent's
	// superseded commit; the recorded metadata base is the correct boundary.
	t.Run("falls back to the metadata base when the parent was amended", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: ancestryMock(history),
			MergeBaseFn:  func(a, b string) (string, error) { return "trunk", nil },
		})
		defer restore()

		got := resolveOntoOldBase("a-new", "a-old", "b1", "b2")
		assert.Equal(t, "a-old", got,
			"should replay only b2's own commits, not the parent's superseded one")
	})

	t.Run("prefers the latest usable boundary", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: ancestryMock(history),
			MergeBaseFn:  func(a, b string) (string, error) { return "trunk", nil },
		})
		defer restore()

		// Both "trunk" and "a-old" are ancestors of b2; a-old replays fewer commits.
		got := resolveOntoOldBase("a-new", "a-old", "b1", "b2")
		assert.Equal(t, "a-old", got)
	})

	t.Run("falls back to a merge base when no metadata base is recorded", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: ancestryMock(history),
			MergeBaseFn:  func(a, b string) (string, error) { return "trunk", nil },
		})
		defer restore()

		got := resolveOntoOldBase("a-new", "", "b1", "b2")
		assert.Equal(t, "trunk", got)
	})

	t.Run("returns the recorded value when nothing is usable", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: func(string, string) (bool, error) { return false, nil },
			MergeBaseFn:  func(string, string) (string, error) { return "", errors.New("no merge base") },
		})
		defer restore()

		got := resolveOntoOldBase("a-new", "unrelated", "b1", "b2")
		assert.Equal(t, "a-new", got)
	})

	t.Run("ignores an unusable metadata base", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			IsAncestorFn: ancestryMock(history),
			MergeBaseFn:  func(a, b string) (string, error) { return "trunk", nil },
		})
		defer restore()

		// A metadata base from an unrelated history must not be trusted.
		got := resolveOntoOldBase("a-new", "bogus", "b1", "b2")
		assert.Equal(t, "trunk", got)
	})
}

func TestUpdateBaseSHAs(t *testing.T) {
	newStack := func(b2Base string) *stack.Stack {
		return &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1"},
				{Branch: "b2", Base: b2Base},
			},
		}
	}

	t.Run("advances the base when the branch really is stacked on the parent", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			RevParseFn:   func(ref string) (string, error) { return "sha-" + ref, nil },
			IsAncestorFn: func(string, string) (bool, error) { return true, nil },
		})
		defer restore()

		s := newStack("stale-base")
		updateBaseSHAs(s)
		assert.Equal(t, "sha-b1", s.Branches[1].Base)
		assert.Equal(t, "sha-b2", s.Branches[1].Head)
	})

	// The root cause of #250: `gh stack push` recorded the parent's amended tip
	// as the child's base even though the child had not been rebased onto it,
	// which made the next cascade replay the parent's superseded commits.
	t.Run("keeps the recorded base when the parent moved out of band", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			RevParseFn: func(ref string) (string, error) { return "sha-" + ref, nil },
			IsAncestorFn: func(ancestor, descendant string) (bool, error) {
				// b2 does not contain b1's amended tip.
				if ancestor == "sha-b1" && descendant == "b2" {
					return false, nil
				}
				return true, nil
			},
		})
		defer restore()

		s := newStack("b1-original-tip")
		updateBaseSHAs(s)
		assert.Equal(t, "b1-original-tip", s.Branches[1].Base,
			"the base must keep describing where b2 actually sits")
		assert.Equal(t, "sha-b2", s.Branches[1].Head, "head is always the branch's own tip")
	})

	t.Run("records a base when none was known yet", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			RevParseFn: func(ref string) (string, error) { return "sha-" + ref, nil },
			IsAncestorFn: func(string, string) (bool, error) {
				return false, nil
			},
		})
		defer restore()

		s := newStack("")
		updateBaseSHAs(s)
		assert.Equal(t, "sha-b1", s.Branches[1].Base,
			"with nothing recorded there is no truth to preserve")
	})

	t.Run("keeps the recorded base when ancestry cannot be determined", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			RevParseFn:   func(ref string) (string, error) { return "sha-" + ref, nil },
			IsAncestorFn: func(string, string) (bool, error) { return false, errors.New("boom") },
		})
		defer restore()

		s := newStack("known-base")
		updateBaseSHAs(s)
		assert.Equal(t, "known-base", s.Branches[1].Base)
	})

	t.Run("skips merged branches", func(t *testing.T) {
		restore := git.SetOps(&git.MockOps{
			RevParseFn:   func(ref string) (string, error) { return "sha-" + ref, nil },
			IsAncestorFn: func(string, string) (bool, error) { return true, nil },
		})
		defer restore()

		s := &stack.Stack{
			Trunk: stack.BranchRef{Branch: "main"},
			Branches: []stack.BranchRef{
				{Branch: "b1", Base: "frozen", PullRequest: &stack.PullRequestRef{Number: 1, Merged: true}},
				{Branch: "b2"},
			},
		}
		updateBaseSHAs(s)
		assert.Equal(t, "frozen", s.Branches[0].Base, "a merged branch's base is left alone")
		assert.Equal(t, "sha-main", s.Branches[1].Base, "b2 is measured against trunk")
	})
}
