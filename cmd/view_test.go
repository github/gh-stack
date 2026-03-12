package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTimeAgo(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"seconds", 30 * time.Second, "30 seconds ago"},
		{"one second", 1 * time.Second, "1 second ago"},
		{"minutes", 5 * time.Minute, "5 minutes ago"},
		{"one minute", 1 * time.Minute, "1 minute ago"},
		{"hours", 3 * time.Hour, "3 hours ago"},
		{"one hour", 1 * time.Hour, "1 hour ago"},
		{"days", 2 * 24 * time.Hour, "2 days ago"},
		{"one day", 24 * time.Hour, "1 day ago"},
		{"months", 60 * 24 * time.Hour, "2 months ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := timeAgo(time.Now().Add(-tt.duration))
			assert.Equal(t, tt.want, result)
		})
	}
}
