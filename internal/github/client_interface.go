package github

// ClientOps defines the interface for GitHub API operations.
// The concrete Client type satisfies this interface.
// Tests can substitute a MockClient.
type ClientOps interface {
	FindPRForBranch(branch string) (*PullRequest, error)
	FindAnyPRForBranch(branch string) (*PullRequest, error)
	FindPRDetailsForBranch(branch string) (*PRDetails, error)
	CreatePR(base, head, title, body string, draft bool) (*PullRequest, error)
	UpdatePRBase(number int, base string) error
	CreateStack(prNumbers []int) (int, error)
	UpdateStack(stackID string, prNumbers []int) error
	DeleteStack(stackID string) error
}

// Compile-time check that Client satisfies ClientOps.
var _ ClientOps = (*Client)(nil)
