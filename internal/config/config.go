package config

import (
	"fmt"
	"io"
	"os"

	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/mgutz/ansi"

	ghapi "github.com/github/gh-stack/internal/github"
)

// Config holds shared state for all commands.
type Config struct {
	Terminal term.Term
	Out      io.Writer
	Err      io.Writer
	In       io.Reader

	ColorSuccess func(string) string
	ColorError   func(string) string
	ColorWarning func(string) string
	ColorBold    func(string) string
	ColorBlue    func(string) string
	ColorMagenta func(string) string
	ColorCyan    func(string) string
	ColorGray    func(string) string
}

// New creates a new Config with terminal-aware output and color support.
func New() *Config {
	terminal := term.FromEnv()
	cfg := &Config{
		Terminal: terminal,
		Out:      terminal.Out(),
		Err:      terminal.ErrOut(),
		In:       os.Stdin,
	}

	if terminal.IsColorEnabled() {
		cfg.ColorSuccess = ansi.ColorFunc("green")
		cfg.ColorError = ansi.ColorFunc("red")
		cfg.ColorWarning = ansi.ColorFunc("yellow")
		cfg.ColorBold = ansi.ColorFunc("default+b")
		cfg.ColorBlue = ansi.ColorFunc("blue")
		cfg.ColorMagenta = ansi.ColorFunc("magenta")
		cfg.ColorCyan = ansi.ColorFunc("cyan")
		cfg.ColorGray = ansi.ColorFunc("default+d")
	} else {
		noop := func(s string) string { return s }
		cfg.ColorSuccess = noop
		cfg.ColorError = noop
		cfg.ColorWarning = noop
		cfg.ColorBold = noop
		cfg.ColorBlue = noop
		cfg.ColorMagenta = noop
		cfg.ColorCyan = noop
		cfg.ColorGray = noop
	}

	return cfg
}

func (c *Config) Successf(format string, args ...any) {
	fmt.Fprintf(c.Err, "%s %s\n", c.ColorSuccess("\u2713"), fmt.Sprintf(format, args...))
}

func (c *Config) Errorf(format string, args ...any) {
	fmt.Fprintf(c.Err, "%s %s\n", c.ColorError("\u2717"), fmt.Sprintf(format, args...))
}

func (c *Config) Warningf(format string, args ...any) {
	fmt.Fprintf(c.Err, "%s %s\n", c.ColorWarning("\u26a0"), fmt.Sprintf(format, args...))
}

func (c *Config) Infof(format string, args ...any) {
	fmt.Fprintf(c.Err, "%s %s\n", c.ColorCyan("\u2139"), fmt.Sprintf(format, args...))
}

func (c *Config) Printf(format string, args ...any) {
	fmt.Fprintf(c.Err, format+"\n", args...)
}

func (c *Config) Outf(format string, args ...any) {
	fmt.Fprintf(c.Out, format, args...)
}

func (c *Config) IsInteractive() bool {
	return c.Terminal.IsTerminalOutput()
}

func (c *Config) Repo() (repository.Repository, error) {
	return repository.Current()
}

func (c *Config) GitHubClient() (*ghapi.Client, error) {
	repo, err := c.Repo()
	if err != nil {
		return nil, fmt.Errorf("determining repository: %w", err)
	}
	return ghapi.NewClient(repo.Owner, repo.Name)
}
