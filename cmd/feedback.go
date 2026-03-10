package cmd

import (
	"net/url"
	"strings"

	"github.com/cli/go-gh/v2/pkg/browser"
	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
)

const feedbackBaseURL = "https://github.com/github/gh-stack/discussions/new?category=feedback"

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
	feedbackURL := feedbackBaseURL

	if len(args) > 0 {
		title := strings.Join(args, " ")
		feedbackURL += "&title=" + url.QueryEscape(title)
	}

	b := browser.New("", cfg.Out, cfg.Err)
	if err := b.Browse(feedbackURL); err != nil {
		return err
	}

	cfg.Successf("Opening feedback form in your browser...\n")
	return nil
}
