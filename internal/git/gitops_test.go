package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitCommitMessage(t *testing.T) {
	tests := []struct {
		name        string
		msg         string
		wantSubject string
		wantBody    string
	}{
		{
			name:        "single line",
			msg:         "Fix the bug",
			wantSubject: "Fix the bug",
			wantBody:    "",
		},
		{
			name:        "subject and body with blank separator",
			msg:         "Fix the bug\n\nMore details about the fix.",
			wantSubject: "Fix the bug",
			wantBody:    "More details about the fix.",
		},
		{
			name:        "multi-line without blank separator",
			msg:         "Fix the bug\nMore details\nEven more",
			wantSubject: "Fix the bug",
			wantBody:    "More details\nEven more",
		},
		{
			name:        "body with leading and trailing blank lines trimmed",
			msg:         "Fix the bug\n\n\nSome body text\n\n",
			wantSubject: "Fix the bug",
			wantBody:    "Some body text",
		},
		{
			name:        "whitespace-only body",
			msg:         "Fix the bug\n\n   \n\n",
			wantSubject: "Fix the bug",
			wantBody:    "",
		},
		{
			name:        "leading whitespace on message trimmed",
			msg:         "\n  Fix the bug\n\nBody here",
			wantSubject: "Fix the bug",
			wantBody:    "Body here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject, body := splitCommitMessage(tt.msg)
			assert.Equal(t, tt.wantSubject, subject)
			assert.Equal(t, tt.wantBody, body)
		})
	}
}
