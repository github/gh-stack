package github

// MockClient is a test double for GitHub API operations.
// Each field is an optional function that, when set, handles the corresponding
// ClientOps method call. When nil, a reasonable default is returned.
type MockClient struct {
	FindPRForBranchFn        func(string) (*PullRequest, error)
	FindPRByNumberFn         func(int) (*PullRequest, error)
	FindPRDetailsForBranchFn func(string) (*PRDetails, error)
	CreatePRFn               func(string, string, string, string, bool) (*PullRequest, error)
	UpdatePRBaseFn           func(int, string) error
	MarkPRReadyForReviewFn   func(string) error
	DisableAutoMergeFn       func(string) error
	ListStacksFn             func() ([]RemoteStack, error)
	FindStackForPRFn         func(int) (*RemoteStack, error)
	GetStackFn               func(int) (*RemoteStack, error)
	CreateStackFn            func([]int) (*RemoteStack, error)
	AddToStackFn             func(int, []int) (*RemoteStack, error)
	UnstackFn                func(int) (*RemoteStack, bool, error)
	RepoMergeConfigFn        func() (*RepoMergeConfig, error)
	MergeStackAsyncFn        func(int, string) (*AsyncMergeResult, error)
	GetAsyncMergeResultFn    func(int, string) (*AsyncMergeResult, error)
	PRTitlesFn               func([]int) (map[int]string, error)
	BaseBranchPolicyFn       func(string) (*BaseBranchPolicy, error)
}

// Compile-time check that MockClient satisfies ClientOps.
var _ ClientOps = (*MockClient)(nil)

func (m *MockClient) FindPRForBranch(branch string) (*PullRequest, error) {
	if m.FindPRForBranchFn != nil {
		return m.FindPRForBranchFn(branch)
	}
	return nil, nil
}

func (m *MockClient) FindPRByNumber(number int) (*PullRequest, error) {
	if m.FindPRByNumberFn != nil {
		return m.FindPRByNumberFn(number)
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

func (m *MockClient) MarkPRReadyForReview(prID string) error {
	if m.MarkPRReadyForReviewFn != nil {
		return m.MarkPRReadyForReviewFn(prID)
	}
	return nil
}

func (m *MockClient) DisableAutoMerge(prID string) error {
	if m.DisableAutoMergeFn != nil {
		return m.DisableAutoMergeFn(prID)
	}
	return nil
}

func (m *MockClient) ListStacks() ([]RemoteStack, error) {
	if m.ListStacksFn != nil {
		return m.ListStacksFn()
	}
	return nil, nil
}

func (m *MockClient) FindStackForPR(prNumber int) (*RemoteStack, error) {
	if m.FindStackForPRFn != nil {
		return m.FindStackForPRFn(prNumber)
	}
	return nil, nil
}

func (m *MockClient) GetStack(stackNumber int) (*RemoteStack, error) {
	if m.GetStackFn != nil {
		return m.GetStackFn(stackNumber)
	}
	return &RemoteStack{}, nil
}

func (m *MockClient) CreateStack(prNumbers []int) (*RemoteStack, error) {
	if m.CreateStackFn != nil {
		return m.CreateStackFn(prNumbers)
	}
	return &RemoteStack{}, nil
}

func (m *MockClient) AddToStack(stackNumber int, prNumbers []int) (*RemoteStack, error) {
	if m.AddToStackFn != nil {
		return m.AddToStackFn(stackNumber, prNumbers)
	}
	return &RemoteStack{}, nil
}

func (m *MockClient) Unstack(stackNumber int) (*RemoteStack, bool, error) {
	if m.UnstackFn != nil {
		return m.UnstackFn(stackNumber)
	}
	return nil, false, nil
}

func (m *MockClient) RepoMergeConfig() (*RepoMergeConfig, error) {
	if m.RepoMergeConfigFn != nil {
		return m.RepoMergeConfigFn()
	}
	return &RepoMergeConfig{
		MergeAllowed:  true,
		SquashAllowed: true,
		RebaseAllowed: true,
		DefaultMethod: MergeMethodMerge,
	}, nil
}

func (m *MockClient) MergeStackAsync(prNumber int, method string) (*AsyncMergeResult, error) {
	if m.MergeStackAsyncFn != nil {
		return m.MergeStackAsyncFn(prNumber, method)
	}
	return &AsyncMergeResult{
		Queued: true,
		Details: AsyncMergeDetails{
			Message:     "Merge request enqueued.",
			UUID:        "mock-uuid",
			MergeMethod: method,
		},
	}, nil
}

func (m *MockClient) GetAsyncMergeResult(prNumber int, uuid string) (*AsyncMergeResult, error) {
	if m.GetAsyncMergeResultFn != nil {
		return m.GetAsyncMergeResultFn(prNumber, uuid)
	}
	return &AsyncMergeResult{
		Merged: true,
		Details: AsyncMergeDetails{
			Message: "Pull request was merged.",
			SHA:     "mockmergesha",
		},
	}, nil
}

func (m *MockClient) PRTitles(numbers []int) (map[int]string, error) {
	if m.PRTitlesFn != nil {
		return m.PRTitlesFn(numbers)
	}
	return map[int]string{}, nil
}

func (m *MockClient) BaseBranchPolicy(baseRef string) (*BaseBranchPolicy, error) {
	if m.BaseBranchPolicyFn != nil {
		return m.BaseBranchPolicyFn(baseRef)
	}
	return &BaseBranchPolicy{}, nil
}
