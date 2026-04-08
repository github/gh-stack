package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
