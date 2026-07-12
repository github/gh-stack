package github

// ClientOps defines the interface for GitHub API operations.
// The concrete Client type satisfies this interface.
// Tests can substitute a MockClient.
type ClientOps interface {
	FindPRForBranch(branch string) (*PullRequest, error)
	FindPRByNumber(number int) (*PullRequest, error)
	FindPRDetailsForBranch(branch string) (*PRDetails, error)
	CreatePR(base, head, title, body string, draft bool) (*PullRequest, error)
	UpdatePRBase(number int, base string) error
	MarkPRReadyForReview(prID string) error
	DisableAutoMerge(prID string) error
	ListStacks() ([]RemoteStack, error)
	FindStackForPR(prNumber int) (*RemoteStack, error)
	GetStack(stackNumber int) (*RemoteStack, error)
	CreateStack(prNumbers []int) (*RemoteStack, error)
	AddToStack(stackNumber int, prNumbers []int) (*RemoteStack, error)
	Unstack(stackNumber int) (*RemoteStack, bool, error)
}

// Compile-time check that Client satisfies ClientOps.
var _ ClientOps = (*Client)(nil)
