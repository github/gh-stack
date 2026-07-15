package github

import (
	"encoding/json"
	"testing"

	graphql "github.com/cli/shurcooL-graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRURL(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		owner  string
		repo   string
		number int
		want   string
	}{
		{"github.com", "github.com", "owner", "repo", 42, "https://github.com/owner/repo/pull/42"},
		{"GHES host", "ghes.example.com", "myorg", "myrepo", 99, "https://ghes.example.com/myorg/myrepo/pull/99"},
		{"empty host defaults to github.com", "", "owner", "repo", 1, "https://github.com/owner/repo/pull/1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PRURL(tt.host, tt.owner, tt.repo, tt.number)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPullRequest_IsQueued(t *testing.T) {
	t.Run("not queued when MergeQueueEntry is nil", func(t *testing.T) {
		pr := &PullRequest{Number: 1}
		assert.False(t, pr.IsQueued())
	})

	t.Run("queued when MergeQueueEntry has ID", func(t *testing.T) {
		pr := &PullRequest{
			Number:          1,
			MergeQueueEntry: &MergeQueueEntry{ID: "MQE_123"},
		}
		assert.True(t, pr.IsQueued())
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		var pr *PullRequest
		assert.False(t, pr.IsQueued())
	})
}

func TestPullRequest_IsAutoMergeEnabled(t *testing.T) {
	t.Run("not enabled when AutoMergeRequest is nil", func(t *testing.T) {
		pr := &PullRequest{Number: 1}
		assert.False(t, pr.IsAutoMergeEnabled())
	})

	t.Run("enabled when AutoMergeRequest is present", func(t *testing.T) {
		pr := &PullRequest{
			Number:           1,
			AutoMergeRequest: &AutoMergeRequest{EnabledAt: "2024-01-01T00:00:00Z"},
		}
		assert.True(t, pr.IsAutoMergeEnabled())
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		var pr *PullRequest
		assert.False(t, pr.IsAutoMergeEnabled())
	})
}

func TestToGraphQLInt(t *testing.T) {
	t.Run("in range", func(t *testing.T) {
		got, err := toGraphQLInt(123)
		assert.NoError(t, err)
		assert.Equal(t, graphql.Int(123), got)
	})

	t.Run("out of range", func(t *testing.T) {
		_, err := toGraphQLInt(1 << 40)
		assert.Error(t, err)
	})
}

// TestRemoteStack_UnmarshalJSON verifies the custom decoder that flattens the
// Stacks REST API's pull_requests object array into ordered PullRequests
// numbers while preserving the full entries in PRDetails. This is the only path
// that populates those fields from the wire, so a shape regression here would
// silently break every stack endpoint.
func TestRemoteStack_UnmarshalJSON(t *testing.T) {
	payload := `{
		"id": 6154,
		"number": 360,
		"node_id": "S_kwABCD",
		"url": "https://api.github.com/repos/o/r/stacks/360",
		"base": {"ref": "main", "sha": "basesha"},
		"open": true,
		"created_at": "2026-01-01T00:00:00Z",
		"pull_requests": [
			{"number": 12, "state": "open", "draft": true, "merged_at": null, "head": {"ref": "feat-1", "sha": "sha1"}},
			{"number": 15, "state": "closed", "draft": false, "merged_at": "2026-01-02T00:00:00Z", "head": {"ref": "feat-2", "sha": "sha2"}}
		]
	}`

	var s RemoteStack
	require.NoError(t, json.Unmarshal([]byte(payload), &s))

	// Top-level metadata.
	assert.Equal(t, 6154, s.ID)
	assert.Equal(t, 360, s.Number)
	assert.Equal(t, "S_kwABCD", s.NodeID)
	assert.Equal(t, "https://api.github.com/repos/o/r/stacks/360", s.URL)
	assert.Equal(t, "main", s.Base.Ref)
	assert.Equal(t, "basesha", s.Base.Sha)
	assert.True(t, s.Open)
	assert.Equal(t, "2026-01-01T00:00:00Z", s.CreatedAt)

	// Ordered PR numbers (bottom to top) derived from the object array.
	assert.Equal(t, []int{12, 15}, s.PullRequests)
	assert.Equal(t, []int{12, 15}, s.PRNumbers())

	// Full PR entries preserved in PRDetails, including nullable merged_at.
	require.Len(t, s.PRDetails, 2)
	assert.Equal(t, 12, s.PRDetails[0].Number)
	assert.Equal(t, "open", s.PRDetails[0].State)
	assert.True(t, s.PRDetails[0].Draft)
	assert.Nil(t, s.PRDetails[0].MergedAt)
	assert.False(t, s.PRDetails[0].IsMerged())
	assert.Equal(t, "feat-1", s.PRDetails[0].Head.Ref)
	assert.Equal(t, "sha1", s.PRDetails[0].Head.Sha)

	assert.Equal(t, 15, s.PRDetails[1].Number)
	assert.Equal(t, "closed", s.PRDetails[1].State)
	require.NotNil(t, s.PRDetails[1].MergedAt)
	assert.Equal(t, "2026-01-02T00:00:00Z", *s.PRDetails[1].MergedAt)
	assert.True(t, s.PRDetails[1].IsMerged())
	assert.Equal(t, "feat-2", s.PRDetails[1].Head.Ref)
}

// TestRemoteStack_UnmarshalJSON_EmptyPRs ensures a stack with no pull_requests
// decodes to empty (non-nil) slices rather than panicking.
func TestRemoteStack_UnmarshalJSON_EmptyPRs(t *testing.T) {
	var s RemoteStack
	require.NoError(t, json.Unmarshal([]byte(`{"id": 1, "number": 2, "pull_requests": []}`), &s))
	assert.Equal(t, 1, s.ID)
	assert.Equal(t, 2, s.Number)
	assert.Empty(t, s.PullRequests)
	assert.Empty(t, s.PRDetails)
}
