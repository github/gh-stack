package cmd

import (
	"net/url"
	"strings"

	"github.com/cli/go-gh/v2/pkg/browser"
	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
)

const (
	feedbackURL     = "https://gh.io/stacks-feedback"
	feedbackFormURL = "https://gh.io/stacks-feedback-form"
)

func FeedbackCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feedback [title]",
		Short: "Submit feedback for gh-stack",
		Long:  "Opens a GitHub Discussion in the gh-stack repository to submit feedback. Optionally provide a title for the discussion post.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeedback(cfg, args)
		},
	}

	return cmd
}

func runFeedback(cfg *config.Config, args []string) error {
	targetURL := feedbackURL

	if len(args) > 0 {
		title := strings.Join(args, " ")
		targetURL = feedbackFormURL + "?title=" + url.QueryEscape(title)
	}

	b := browser.New("", cfg.Out, cfg.Err)
	if err := b.Browse(targetURL); err != nil {
		return err
	}

	cfg.Successf("Opening feedback form in your browser...")
	return nil
}
