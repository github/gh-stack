package git

// MockOps is a test double for git operations.
// Each field is an optional function that, when set, handles the corresponding
// Ops method call. When nil, a reasonable default is returned.
type MockOps struct {
	GitDirFn              func() (string, error)
	CurrentBranchFn       func() (string, error)
	BranchExistsFn        func(string) bool
	CheckoutBranchFn      func(string) error
	FetchFn               func(string) error
	DefaultBranchFn       func() (string, error)
	CreateBranchFn        func(string, string) error
	PushFn                func(string, []string, bool, bool) error
	ResolveRemoteFn       func(string) (string, error)
	RebaseFn              func(string) error
	EnableRerereFn        func() error
	IsRerereEnabledFn     func() (bool, error)
	IsRerereDeclinedFn    func() (bool, error)
	SaveRerereDeclinedFn  func() error
	RebaseOntoFn          func(string, string, string) error
	RebaseContinueFn      func() error
	RebaseAbortFn         func() error
	IsRebaseInProgressFn  func() bool
	ConflictedFilesFn     func() ([]string, error)
	FindConflictMarkersFn func(string) (*ConflictMarkerInfo, error)
	IsAncestorFn          func(string, string) (bool, error)
	RevParseFn            func(string) (string, error)
	RevParseMultiFn       func([]string) ([]string, error)
	MergeBaseFn           func(string, string) (string, error)
	LogFn                 func(string, int) ([]CommitInfo, error)
	LogRangeFn            func(string, string) ([]CommitInfo, error)
	DiffStatRangeFn       func(string, string) (int, int, error)
	DiffStatFilesFn       func(string, string) ([]FileDiffStat, error)
	DeleteBranchFn        func(string, bool) error
	DeleteRemoteBranchFn  func(string, string) error
	ResetHardFn           func(string) error
	SetUpstreamTrackingFn func(string, string) error
	MergeFFFn             func(string) error
	UpdateBranchRefFn     func(string, string) error
	StageAllFn            func() error
	StageTrackedFn        func() error
	HasStagedChangesFn    func() bool
	CommitFn              func(string) (string, error)
	CommitInteractiveFn   func() (string, error)
	ValidateRefNameFn       func(string) error
	RenameBranchFn          func(string, string) error
	CherryPickFn            func([]string) error
	HasUncommittedChangesFn func() (bool, error)
	LogMergesFn             func(string, string) ([]CommitInfo, error)
}

var _ Ops = (*MockOps)(nil)

func (m *MockOps) GitDir() (string, error) {
	if m.GitDirFn != nil {
		return m.GitDirFn()
	}
	return "/tmp/fake-git-dir", nil
}

func (m *MockOps) CurrentBranch() (string, error) {
	if m.CurrentBranchFn != nil {
		return m.CurrentBranchFn()
	}
	return "main", nil
}

func (m *MockOps) BranchExists(name string) bool {
	if m.BranchExistsFn != nil {
		return m.BranchExistsFn(name)
	}
	return false
}

func (m *MockOps) CheckoutBranch(name string) error {
	if m.CheckoutBranchFn != nil {
		return m.CheckoutBranchFn(name)
	}
	return nil
}

func (m *MockOps) Fetch(remote string) error {
	if m.FetchFn != nil {
		return m.FetchFn(remote)
	}
	return nil
}

func (m *MockOps) DefaultBranch() (string, error) {
	if m.DefaultBranchFn != nil {
		return m.DefaultBranchFn()
	}
	return "main", nil
}

func (m *MockOps) CreateBranch(name, base string) error {
	if m.CreateBranchFn != nil {
		return m.CreateBranchFn(name, base)
	}
	return nil
}

func (m *MockOps) Push(remote string, branches []string, force, atomic bool) error {
	if m.PushFn != nil {
		return m.PushFn(remote, branches, force, atomic)
	}
	return nil
}

func (m *MockOps) ResolveRemote(branch string) (string, error) {
	if m.ResolveRemoteFn != nil {
		return m.ResolveRemoteFn(branch)
	}
	return "origin", nil
}

func (m *MockOps) Rebase(base string) error {
	if m.RebaseFn != nil {
		return m.RebaseFn(base)
	}
	return nil
}

func (m *MockOps) EnableRerere() error {
	if m.EnableRerereFn != nil {
		return m.EnableRerereFn()
	}
	return nil
}

func (m *MockOps) IsRerereEnabled() (bool, error) {
	if m.IsRerereEnabledFn != nil {
		return m.IsRerereEnabledFn()
	}
	return false, nil
}

func (m *MockOps) IsRerereDeclined() (bool, error) {
	if m.IsRerereDeclinedFn != nil {
		return m.IsRerereDeclinedFn()
	}
	return false, nil
}

func (m *MockOps) SaveRerereDeclined() error {
	if m.SaveRerereDeclinedFn != nil {
		return m.SaveRerereDeclinedFn()
	}
	return nil
}

func (m *MockOps) RebaseOnto(newBase, oldBase, branch string) error {
	if m.RebaseOntoFn != nil {
		return m.RebaseOntoFn(newBase, oldBase, branch)
	}
	return nil
}

func (m *MockOps) RebaseContinue() error {
	if m.RebaseContinueFn != nil {
		return m.RebaseContinueFn()
	}
	return nil
}

func (m *MockOps) RebaseAbort() error {
	if m.RebaseAbortFn != nil {
		return m.RebaseAbortFn()
	}
	return nil
}

func (m *MockOps) IsRebaseInProgress() bool {
	if m.IsRebaseInProgressFn != nil {
		return m.IsRebaseInProgressFn()
	}
	return false
}

func (m *MockOps) ConflictedFiles() ([]string, error) {
	if m.ConflictedFilesFn != nil {
		return m.ConflictedFilesFn()
	}
	return nil, nil
}

func (m *MockOps) FindConflictMarkers(filePath string) (*ConflictMarkerInfo, error) {
	if m.FindConflictMarkersFn != nil {
		return m.FindConflictMarkersFn(filePath)
	}
	return nil, nil
}

func (m *MockOps) IsAncestor(ancestor, descendant string) (bool, error) {
	if m.IsAncestorFn != nil {
		return m.IsAncestorFn(ancestor, descendant)
	}
	return false, nil
}

func (m *MockOps) RevParse(ref string) (string, error) {
	if m.RevParseFn != nil {
		return m.RevParseFn(ref)
	}
	return "", nil
}

func (m *MockOps) RevParseMulti(refs []string) ([]string, error) {
	if m.RevParseMultiFn != nil {
		return m.RevParseMultiFn(refs)
	}
	// Default: delegate to RevParse for each ref.
	shas := make([]string, len(refs))
	for i, ref := range refs {
		sha, err := m.RevParse(ref)
		if err != nil {
			return nil, err
		}
		shas[i] = sha
	}
	return shas, nil
}

func (m *MockOps) MergeBase(a, b string) (string, error) {
	if m.MergeBaseFn != nil {
		return m.MergeBaseFn(a, b)
	}
	return "", nil
}

func (m *MockOps) Log(ref string, maxCount int) ([]CommitInfo, error) {
	if m.LogFn != nil {
		return m.LogFn(ref, maxCount)
	}
	return nil, nil
}

func (m *MockOps) LogRange(base, head string) ([]CommitInfo, error) {
	if m.LogRangeFn != nil {
		return m.LogRangeFn(base, head)
	}
	return nil, nil
}

func (m *MockOps) DiffStatRange(base, head string) (int, int, error) {
	if m.DiffStatRangeFn != nil {
		return m.DiffStatRangeFn(base, head)
	}
	return 0, 0, nil
}

func (m *MockOps) DiffStatFiles(base, head string) ([]FileDiffStat, error) {
	if m.DiffStatFilesFn != nil {
		return m.DiffStatFilesFn(base, head)
	}
	return nil, nil
}

func (m *MockOps) DeleteBranch(name string, force bool) error {
	if m.DeleteBranchFn != nil {
		return m.DeleteBranchFn(name, force)
	}
	return nil
}

func (m *MockOps) DeleteRemoteBranch(remote, branch string) error {
	if m.DeleteRemoteBranchFn != nil {
		return m.DeleteRemoteBranchFn(remote, branch)
	}
	return nil
}

func (m *MockOps) ResetHard(ref string) error {
	if m.ResetHardFn != nil {
		return m.ResetHardFn(ref)
	}
	return nil
}

func (m *MockOps) SetUpstreamTracking(branch, remote string) error {
	if m.SetUpstreamTrackingFn != nil {
		return m.SetUpstreamTrackingFn(branch, remote)
	}
	return nil
}

func (m *MockOps) MergeFF(target string) error {
	if m.MergeFFFn != nil {
		return m.MergeFFFn(target)
	}
	return nil
}

func (m *MockOps) UpdateBranchRef(branch, sha string) error {
	if m.UpdateBranchRefFn != nil {
		return m.UpdateBranchRefFn(branch, sha)
	}
	return nil
}

func (m *MockOps) StageAll() error {
	if m.StageAllFn != nil {
		return m.StageAllFn()
	}
	return nil
}

func (m *MockOps) StageTracked() error {
	if m.StageTrackedFn != nil {
		return m.StageTrackedFn()
	}
	return nil
}

func (m *MockOps) HasStagedChanges() bool {
	if m.HasStagedChangesFn != nil {
		return m.HasStagedChangesFn()
	}
	return false
}

func (m *MockOps) Commit(message string) (string, error) {
	if m.CommitFn != nil {
		return m.CommitFn(message)
	}
	return "", nil
}

func (m *MockOps) CommitInteractive() (string, error) {
	if m.CommitInteractiveFn != nil {
		return m.CommitInteractiveFn()
	}
	return "", nil
}

func (m *MockOps) ValidateRefName(name string) error {
	if m.ValidateRefNameFn != nil {
		return m.ValidateRefNameFn(name)
	}
	return nil
}

func (m *MockOps) RenameBranch(oldName, newName string) error {
	if m.RenameBranchFn != nil {
		return m.RenameBranchFn(oldName, newName)
	}
	return nil
}

func (m *MockOps) CherryPick(commits []string) error {
	if m.CherryPickFn != nil {
		return m.CherryPickFn(commits)
	}
	return nil
}

func (m *MockOps) CherryPickAbort() error {
	return nil
}

func (m *MockOps) HasUncommittedChanges() (bool, error) {
	if m.HasUncommittedChangesFn != nil {
		return m.HasUncommittedChangesFn()
	}
	return false, nil
}

func (m *MockOps) LogMerges(base, head string) ([]CommitInfo, error) {
	if m.LogMergesFn != nil {
		return m.LogMergesFn(base, head)
	}
	return nil, nil
}
