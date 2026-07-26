// Package mergeview implements the interactive wizard used by `gh stack merge`.
//
// The wizard walks the user through three selection steps — choosing how far up
// the stack to merge (a bottom-anchored checkbox list), picking the merge
// method, and confirming — then shows a live progress view while the
// asynchronous merge runs on GitHub. Because a stack merge is atomic, the
// progress view reports a single aggregate outcome: all selected PRs merge, or
// none do.
//
// The async merge submit/poll calls are injected as SubmitFunc/PollFunc so the
// wizard stays decoupled from the GitHub client and is easy to test.
package mergeview

import "time"

// Step identifies the current stage of the wizard.
type Step int

const (
	// StepSelectPRs is the bottom-anchored checkbox list choosing how far up
	// the stack to merge.
	StepSelectPRs Step = iota
	// StepMethod is the merge-method picker.
	StepMethod
	// StepConfirm is the confirmation summary.
	StepConfirm
	// StepProgress shows the live async merge status.
	StepProgress
	// StepDone is the terminal state after success, failure, or cancel.
	StepDone
)

// PRItem is a selectable pull request in the merge picker, ordered bottom to top
// of the stack.
type PRItem struct {
	Number int
	Title  string
	Branch string
}

// Status is the async-merge state, mirroring the API's `status` field.
type Status string

const (
	// StatusPending means the merge is still running in the background.
	StatusPending Status = "pending"
	// StatusMerged means the merge completed successfully.
	StatusMerged Status = "merged"
	// StatusEnqueued means the stack was added to the base branch's merge queue.
	StatusEnqueued Status = "enqueued"
	// StatusFailed means the merge was attempted but did not complete.
	StatusFailed Status = "failed"
)

// MergeStatus is the minimal async-merge result the progress view consumes,
// mapped by the caller from the API response.
type MergeStatus struct {
	// Status is the current merge state.
	Status Status
	// Message is the human-readable status or failure reason.
	Message string
	// UUID identifies an in-flight merge request, used for polling.
	UUID string
	// SHA is the resulting merge commit on success.
	SHA string
}

// SubmitFunc submits the async merge for the chosen target PR and method and
// returns the initial status.
type SubmitFunc func(targetPR int, method string) (MergeStatus, error)

// PollFunc fetches the latest status for an in-flight merge request UUID on the
// given target PR.
type PollFunc func(targetPR int, uuid string) (MergeStatus, error)

// Options configures the wizard model.
type Options struct {
	// PRs are the selectable (open, mergeable) pull requests ordered bottom to
	// top of the stack.
	PRs []PRItem
	// StackNumber is the repo-scoped stack number, shown in the header.
	StackNumber int
	// BaseRef is the branch the stack merges into (for display).
	BaseRef string
	// RepoSlug is owner/repo, for display.
	RepoSlug string
	// AllowedMethods are the repo's enabled merge methods in display order
	// (subset of "merge", "squash", "rebase").
	AllowedMethods []string
	// DefaultMethod is the method preselected in the picker (the viewer's
	// last-used method).
	DefaultMethod string
	// PreselectTopIndex, when >= 0, preselects PRs[0..PreselectTopIndex] and
	// skips the PR-selection step (PR-number mode).
	PreselectTopIndex int
	// Submit and Poll perform the async merge; injected by the command.
	Submit SubmitFunc
	Poll   PollFunc
	// PollInterval is the delay between status polls. Defaults to one second.
	PollInterval time.Duration
}

// Outcome is the result the command reads back from the finished wizard.
type Outcome struct {
	// Cancelled reports the user quit before the merge was submitted.
	Cancelled bool
	// Submitted reports a merge request was sent to GitHub.
	Submitted bool
	// Merged reports the merge completed successfully.
	Merged bool
	// Enqueued reports the stack was added to the base branch's merge queue
	// (it will merge once the queue processes it).
	Enqueued bool
	// Failed reports the merge was attempted but did not complete (conflict,
	// rule failure, or not mergeable).
	Failed bool
	// WatchStopped reports the user stopped watching an in-flight merge (ctrl+c
	// during progress); the merge continues on GitHub.
	WatchStopped bool
	// Message is the final status or failure message.
	Message string
	// TargetPR is the topmost selected PR (the merge high-water mark).
	TargetPR int
	// Method is the chosen merge method.
	Method string
	// MergedPRs are the PR numbers included in the merge.
	MergedPRs []int
	// SHA is the resulting merge commit on success.
	SHA string
	// Err is a transport/API error encountered during submit or polling.
	Err error
}
