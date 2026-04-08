package github

// MockClient is a test double for GitHub API operations.
// Each field is an optional function that, when set, handles the corresponding
// ClientOps method call. When nil, a reasonable default is returned.
type MockClient struct {
	FindPRForBranchFn        func(string) (*PullRequest, error)
	FindAnyPRForBranchFn     func(string) (*PullRequest, error)
	FindPRDetailsForBranchFn func(string) (*PRDetails, error)
	CreatePRFn               func(string, string, string, string, bool) (*PullRequest, error)
	UpdatePRBaseFn           func(int, string) error
	CreateStackFn            func([]int) (int, error)
	UpdateStackFn            func(string, []int) error
	DeleteStackFn            func(string) error
}

// Compile-time check that MockClient satisfies ClientOps.
var _ ClientOps = (*MockClient)(nil)

func (m *MockClient) FindPRForBranch(branch string) (*PullRequest, error) {
	if m.FindPRForBranchFn != nil {
		return m.FindPRForBranchFn(branch)
	}
	return nil, nil
}

func (m *MockClient) FindAnyPRForBranch(branch string) (*PullRequest, error) {
	if m.FindAnyPRForBranchFn != nil {
		return m.FindAnyPRForBranchFn(branch)
	}
	return nil, nil
}

func (m *MockClient) FindPRDetailsForBranch(branch string) (*PRDetails, error) {
	if m.FindPRDetailsForBranchFn != nil {
		return m.FindPRDetailsForBranchFn(branch)
	}
	return nil, nil
}

func (m *MockClient) CreatePR(base, head, title, body string, draft bool) (*PullRequest, error) {
	if m.CreatePRFn != nil {
		return m.CreatePRFn(base, head, title, body, draft)
	}
	return nil, nil
}

func (m *MockClient) UpdatePRBase(number int, base string) error {
	if m.UpdatePRBaseFn != nil {
		return m.UpdatePRBaseFn(number, base)
	}
	return nil
}

func (m *MockClient) CreateStack(prNumbers []int) (int, error) {
	if m.CreateStackFn != nil {
		return m.CreateStackFn(prNumbers)
	}
	return 0, nil
}

func (m *MockClient) UpdateStack(stackID string, prNumbers []int) error {
	if m.UpdateStackFn != nil {
		return m.UpdateStackFn(stackID, prNumbers)
	}
	return nil
}

func (m *MockClient) DeleteStack(stackID string) error {
	if m.DeleteStackFn != nil {
		return m.DeleteStackFn(stackID)
	}
	return nil
}
