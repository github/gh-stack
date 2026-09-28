package cmd

import (
	"errors"
	"fmt"
	"slices"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
)

func rebaseBranchNames(branches []stack.BranchRef) []string {
	var names []string
	for _, branch := range branches {
		if !branch.IsSkipped() {
			names = append(names, branch.Branch)
		}
	}
	return names
}

func newWorktreeRebaseState(s *stack.Stack, ctx *worktree.Context, originalBranch string, refs map[string]string, trunk trunkTarget, start, end int) *rebaseState {
	snapshot := *s
	snapshot.Branches = append([]stack.BranchRef{}, s.Branches...)
	return &rebaseState{
		Phase:              "applying",
		Worktrees:          ctx,
		StackID:            s.ID,
		StackTrunk:         s.Trunk.Branch,
		StackBranches:      s.BranchNames(),
		OriginalStack:      &snapshot,
		OriginalBranch:     originalBranch,
		OriginalRefs:       refs,
		CurrentBranchIndex: start,
		StartIndex:         start,
		EndIndex:           end,
		TrunkRef:           trunk.Ref,
		TrunkSHA:           trunk.SHA,
	}
}

func printWorktreeConflict(cfg *config.Config, state *rebaseState, base string) {
	branch := state.Worktrees.Pending
	if branch == "" {
		branch = state.ConflictBranch
	}
	ops, err := state.Worktrees.Ops(branch)
	if err != nil {
		cfg.Errorf("%s", err)
		return
	}
	printConflictDetailsAt(cfg, ops, state.Worktrees.Location(branch).Path, base, "gh stack rebase --continue")
}

func rollbackWorktreeRebase(cfg *config.Config, dir string, state *rebaseState) error {
	ctx := state.Worktrees
	if _, err := ctx.OriginOps(); err != nil {
		cfg.Errorf("%s", err)
		return err
	}
	branches := make([]string, 0, len(ctx.Touched)+1)
	for branch := range ctx.Touched {
		branches = append(branches, branch)
	}
	if ctx.Pending != "" {
		branches = append(branches, ctx.Pending)
	}
	var pendingOps git.Ops
	var inProgress bool
	for _, branch := range branches {
		ops, err := ctx.Ops(branch)
		if err != nil {
			cfg.Errorf("%s", err)
			return err
		}
		if _, err := ops.BranchExists(branch); err != nil {
			cfg.Errorf("checking branch %s before restoring: %s", branch, err)
			return fmt.Errorf("checking branch %s before restoring: %w", branch, err)
		}
		if branch == ctx.Pending {
			pendingOps = ops
			inProgress, err = ops.IsRebaseInProgress()
			if err != nil {
				cfg.Errorf("checking rebase state: %s", err)
				return fmt.Errorf("checking rebase state: %w", err)
			}
		}
	}
	state.Phase = "restoring"
	var err error
	if inProgress {
		err = pendingOps.RebaseAbort()
	}
	if err == nil {
		err = errors.Join(ctx.Restore(state.OriginalRefs), ctx.RestoreOrigin(state.OriginalBranch))
	}
	if err == nil && state.OriginalStack != nil {
		err = restoreWorktreeRebaseMetadata(dir, state)
	}
	if err != nil {
		if saveErr := saveRebaseState(dir, state); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
		cfg.Errorf("Could not fully restore the stack; recovery state was retained: %v", err)
		return err
	}
	if err := clearRebaseState(dir); err != nil {
		cfg.Errorf("Branches restored, but could not clear recovery state: %v", err)
		return err
	}
	return nil
}

func restoreWorktreeRebaseMetadata(dir string, state *rebaseState) error {
	sf, err := stack.Load(dir)
	if err != nil {
		return err
	}
	target, err := rebaseStackFromState(sf, state)
	if err != nil {
		return err
	}
	if !slices.Equal(state.OriginalStack.BranchNames(), target.BranchNames()) {
		return fmt.Errorf("original stack snapshot does not match the recovery target")
	}
	target.Trunk.Head = state.OriginalStack.Trunk.Head
	for i, before := range state.OriginalStack.Branches {
		target.Branches[i].Base = before.Base
		target.Branches[i].Head = before.Head
		if sha, err := git.RevParse(before.Branch); err == nil {
			target.Branches[i].Head = sha
		} else if !before.IsMerged() {
			return fmt.Errorf("reading restored branch %s: %w", before.Branch, err)
		}
	}
	return stack.Save(dir, sf)
}

func rebaseStackFromState(sf *stack.StackFile, state *rebaseState) (*stack.Stack, error) {
	switch state.Phase {
	case "applying", "conflict", "complete", "restoring":
	default:
		return nil, fmt.Errorf("unknown saved rebase phase %q", state.Phase)
	}
	var target *stack.Stack
	for i := range sf.Stacks {
		s := &sf.Stacks[i]
		if s.Trunk.Branch != state.StackTrunk || !slices.Equal(s.BranchNames(), state.StackBranches) {
			continue
		}
		if (state.Phase == "applying" || state.Phase == "conflict") && state.StackID != "" && s.ID != "" && state.StackID != s.ID {
			continue
		}
		if target != nil {
			return nil, fmt.Errorf("saved rebase matches multiple stacks; resolve the catalog before continuing")
		}
		target = s
	}
	if target == nil {
		return nil, fmt.Errorf("the stack changed since this rebase started; restore its original membership or run gh stack rebase --abort")
	}
	if state.StartIndex < 0 || state.EndIndex > len(target.Branches) ||
		state.StartIndex > state.EndIndex || state.CurrentBranchIndex < state.StartIndex ||
		state.CurrentBranchIndex > state.EndIndex {
		return nil, fmt.Errorf("invalid branch range in saved rebase state")
	}
	return target, nil
}

func continueWorktreeRebase(cfg *config.Config, dir string, state *rebaseState) error {
	if state.Phase == "restoring" {
		return fmt.Errorf("restoration is incomplete; run gh stack rebase --abort to finish restoring the stack")
	}
	sf, err := stack.Load(dir)
	if err != nil {
		return err
	}
	s, err := rebaseStackFromState(sf, state)
	if err != nil {
		return err
	}
	ctx := state.Worktrees
	_ = syncStackPRs(cfg, s)
	if state.Phase != "complete" {
		remainingStart := state.CurrentBranchIndex
		if ctx.Pending != "" {
			remainingStart++
		}
		if remainingStart > state.EndIndex {
			return fmt.Errorf("invalid pending branch in saved rebase state")
		}
		required := rebaseBranchNames(s.Branches[remainingStart:state.EndIndex])
		if ctx.Pending != "" {
			if _, err := ctx.Ops(ctx.Pending); err != nil {
				return err
			}
			pendingPath := ctx.Location(ctx.Pending).Path
			otherWorktrees := required[:0]
			for _, branch := range required {
				if _, err := ctx.Ops(branch); err != nil {
					return err
				}
				if !worktree.SamePath(ctx.Location(branch).Path, pendingPath) {
					otherWorktrees = append(otherWorktrees, branch)
				}
			}
			required = otherWorktrees
		}
		// The pending worktree is intentionally busy. Its remaining branches
		// are checked by Prepare after native continuation has completed.
		if err := ctx.Preflight(required); err != nil {
			return err
		}
		if ctx.Pending != "" {
			branch := ctx.Pending
			if state.CurrentBranchIndex >= len(s.Branches) || s.Branches[state.CurrentBranchIndex].Branch != branch {
				return fmt.Errorf("saved conflict branch does not match the stack")
			}
			ops, err := ctx.Ops(branch)
			if err != nil {
				return err
			}
			retry := false
			inProgress, err := ops.IsRebaseInProgress()
			if err != nil {
				return fmt.Errorf("checking rebase state: %w", err)
			}
			if inProgress {
				if err := ops.RebaseContinue(git.RebaseOpts{CommitterDateIsAuthorDate: state.CommitterDateIsAuthorDate}); err != nil {
					cfg.Errorf("rebase continue failed: %v", err)
					printWorktreeConflict(cfg, state, state.RebaseBase)
					return ErrConflict
				}
			} else {
				sha, err := ops.RevParse(branch)
				if err != nil {
					return err
				}
				retry = state.Phase == "applying" && sha == ctx.PendingBefore
			}
			if retry {
				opts := savedCascadeOpts(cfg, dir, state, s)
				conflicted, err := rebaseStep(opts, branch, state.CurrentBranchIndex, state.RebaseBase, state.RebaseOldBase, state.RebaseOnto, state.UseOnto)
				if err != nil {
					cfg.Errorf("%s", err)
					if conflicted {
						printWorktreeConflict(cfg, state, state.RebaseBase)
						return ErrConflict
					}
					return ErrSilent
				}
			} else {
				containsBase, err := ops.IsAncestor(state.RebaseBase, branch)
				if err != nil || !containsBase {
					return fmt.Errorf("%s does not contain its saved rebase target; the Git rebase may have been aborted; run gh stack rebase --abort", branch)
				}
				if err := ctx.Record(branch); err != nil {
					return err
				}
				state.ConflictBranch = ""
				state.CurrentBranchIndex++
				state.Phase = "applying"
				if err := saveRebaseState(dir, state); err != nil {
					return err
				}
			}
			cfg.Successf("Rebased %s", branch)
		}
		result := cascadeRebase(savedCascadeOpts(cfg, dir, state, s))
		if result.Err != nil {
			cfg.Errorf("%s", result.Err)
			if err := rollbackWorktreeRebase(cfg, dir, state); err != nil {
				return errors.Join(ErrSilent, err)
			}
			return ErrSilent
		}
		if result.Conflicted {
			printWorktreeConflict(cfg, state, result.ConflictBase)
			return ErrConflict
		}
	}
	if err := finishWorktreeRebase(cfg, dir, state, sf, s); err != nil {
		return err
	}
	cfg.Successf("All branches in stack rebased locally")
	cfg.Printf("To push your changes, run `%s`", cfg.ColorCyan("gh stack push"))
	return nil
}

func savedCascadeOpts(cfg *config.Config, dir string, state *rebaseState, s *stack.Stack) cascadeRebaseOpts {
	return cascadeRebaseOpts{
		Cfg:                       cfg,
		Stack:                     s,
		Branches:                  s.Branches[state.CurrentBranchIndex:state.EndIndex],
		StartAbsIdx:               state.CurrentBranchIndex,
		OriginalRefs:              state.OriginalRefs,
		NeedsOnto:                 state.UseOnto,
		OntoOldBase:               state.OntoOldBase,
		CommitterDateIsAuthorDate: state.CommitterDateIsAuthorDate,
		TrunkRef:                  state.TrunkRef,
		TrunkSHA:                  state.TrunkSHA,
		Worktrees:                 state.Worktrees,
		State:                     state,
		StateDir:                  dir,
	}
}

func finishWorktreeRebase(cfg *config.Config, dir string, state *rebaseState, sf *stack.StackFile, s *stack.Stack) error {
	if err := state.Worktrees.RestoreOrigin(state.OriginalBranch); err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	target := state.TrunkSHA
	if target == "" {
		target = state.TrunkRef
	}
	if unstacked := verifyStacked(s, target, state.StartIndex, state.EndIndex); len(unstacked) > 0 {
		reportUnstacked(cfg, state.TrunkRef, unstacked)
		if err := rollbackWorktreeRebase(cfg, dir, state); err != nil {
			return errors.Join(ErrSilent, err)
		}
		return ErrSilent
	}
	_ = syncStackPRs(cfg, s)
	return publishCompletedRebase(cfg, dir, state, sf, s)
}

func publishCompletedRebase(cfg *config.Config, dir string, state *rebaseState, sf *stack.StackFile, s *stack.Stack) error {
	updateBaseSHAsWithTrunk(s, state.TrunkSHA)
	state.Phase = "complete"
	state.CurrentBranchIndex = state.EndIndex
	if err := saveRebaseState(dir, state); err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	if err := stack.Save(dir, sf); err != nil {
		return handleSaveError(cfg, err)
	}
	if err := clearRebaseState(dir); err != nil {
		cfg.Errorf("rebase completed but recovery state could not be cleared: %v", err)
		return ErrSilent
	}
	return nil
}
