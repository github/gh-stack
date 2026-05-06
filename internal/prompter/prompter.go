// Package prompter provides interactive prompts using charm.land/huh/v2.
//
// This replaces the go-gh/v2/pkg/prompter (survey/v2-based) to avoid
// terminal scrolling issues in modern terminals. The API mirrors go-gh's
// prompter so call-sites only need an import-path swap.
//
// Modeled after cli/cli's internal/prompter/huh_prompter.go.
package prompter

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// ErrInterrupt is returned when the user cancels a prompt (e.g. Ctrl+C).
var ErrInterrupt = errors.New("prompt interrupted")

// FileWriter provides a minimal writable interface for stdout/stderr.
type FileWriter interface {
	io.Writer
	Fd() uintptr
}

// FileReader provides a minimal readable interface for stdin.
type FileReader interface {
	io.Reader
	Fd() uintptr
}

var (
	cyanStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// surveyTheme returns a huh theme that visually matches the survey/v2 prompt
// style: no box border, green "?" prefix, inline layout.
func surveyTheme(isDark bool) *huh.Styles {
	t := huh.ThemeBase(isDark)

	cyan := lipgloss.Color("6")

	// No border, no padding — survey prompts are flush left.
	t.Focused.Base = lipgloss.NewStyle()
	t.Focused.Card = t.Focused.Base

	// Title has no forced color so embedded ANSI (green "?") is preserved.
	t.Focused.Title = lipgloss.NewStyle().Bold(true)
	t.Focused.Description = lipgloss.NewStyle()

	// Select options.
	t.Focused.SelectSelector = lipgloss.NewStyle().Foreground(cyan).SetString("> ")
	t.Focused.Option = lipgloss.NewStyle()
	t.Focused.NextIndicator = lipgloss.NewStyle().MarginLeft(1).SetString("↓")
	t.Focused.PrevIndicator = lipgloss.NewStyle().MarginRight(1).SetString("↑")

	// Text input cursor/prompt.
	t.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(cyan)
	t.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(cyan)
	t.Focused.TextInput.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	t.Focused.TextInput.Text = lipgloss.NewStyle().Foreground(cyan)

	// Confirm buttons — minimal style to match survey's (Y/n) look.
	t.Focused.FocusedButton = lipgloss.NewStyle().Bold(true).Foreground(cyan).
		Padding(0, 1)
	t.Focused.BlurredButton = lipgloss.NewStyle().
		Padding(0, 1)

	// Blurred (non-focused) fields are identical but without border.
	t.Blurred = t.Focused
	t.Blurred.Base = lipgloss.NewStyle()
	t.Blurred.Card = t.Blurred.Base

	t.FieldSeparator = lipgloss.NewStyle().SetString("\n")

	return t
}

// Prompter provides interactive prompt methods backed by huh.
type Prompter struct {
	stdin  FileReader
	stdout FileWriter
	stderr FileWriter
}

// New creates a Prompter that reads from stdin and writes to stdout/stderr.
func New(stdin FileReader, stdout FileWriter, stderr FileWriter) *Prompter {
	return &Prompter{stdin: stdin, stdout: stdout, stderr: stderr}
}

func (p *Prompter) newForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).
		WithTheme(huh.ThemeFunc(surveyTheme)).
		WithShowHelp(false).
		WithInput(p.stdin).
		WithOutput(p.stdout)
}

func (p *Prompter) runForm(form *huh.Form) error {
	err := form.Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrInterrupt
	}
	return err
}

// printResult re-prints the prompt after huh clears its rendering,
// matching survey's behavior where the completed prompt stays visible.
func (p *Prompter) printResult(prompt, answer string) {
	q := cyanStyle.Render("?")
	if answer == "" {
		fmt.Fprintf(p.stderr, "%s %s\n", q, prompt)
	} else {
		fmt.Fprintf(p.stderr, "%s %s %s\n", q, prompt, cyanStyle.Render(answer))
	}
}

// surveyTitle prepends the cyan "?" icon to match survey/v2 prompt style.
func surveyTitle(prompt string) string {
	q := cyanStyle.Render("?")
	return q + " " + prompt
}

// selectPageSize is the maximum number of visible options in a select prompt.
const selectPageSize = 20

// Select prompts the user to select an option from a list of options.
func (p *Prompter) Select(prompt, defaultValue string, options []string) (int, error) {
	var result int

	if !slices.Contains(options, defaultValue) {
		defaultValue = ""
	}

	formOptions := make([]huh.Option[int], len(options))
	for i, o := range options {
		if defaultValue == o {
			result = i
		}
		formOptions[i] = huh.NewOption(o, i)
	}

	form := p.newForm(
		huh.NewGroup(
			huh.NewSelect[int]().
				Title(surveyTitle(prompt)).
				Value(&result).
				Options(formOptions...),
		),
	)

	err := p.runForm(form)

	// Clear residual select rendering — huh's bubbletea renderer may leave
	// lines behind. Move cursor up past title + visible options, then clear.
	visible := len(options)
	if visible > selectPageSize {
		visible = selectPageSize
	}
	lines := 1 + visible // 1 title + N options
	fmt.Fprintf(p.stderr, "\033[%dA\033[J", lines)

	if err != nil {
		p.printResult(prompt, "")
		return result, err
	}
	if result >= 0 && result < len(options) {
		p.printResult(prompt, options[result])
	}
	return result, nil
}

// Input prompts the user to input a single-line string.
func (p *Prompter) Input(prompt, defaultValue string) (string, error) {
	result := defaultValue
	form := p.newForm(
		huh.NewGroup(
			huh.NewInput().
				Title(surveyTitle(prompt)).
				Prompt(" ").
				Inline(true).
				Value(&result),
		),
	)

	err := p.runForm(form)
	p.printResult(prompt, result)
	return result, err
}

// Confirm prompts the user to confirm a yes/no question.
// Uses a simple line-based prompt matching survey/v2's (Y/n) format.
func (p *Prompter) Confirm(prompt string, defaultValue bool) (bool, error) {
	hint := "(y/N)"
	if defaultValue {
		hint = "(Y/n)"
	}
	q := cyanStyle.Render("?")
	fmt.Fprintf(p.stderr, "%s %s %s ", q, prompt, hint)

	reader := bufio.NewReader(p.stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		// EOF or read error — treat as interrupt.
		fmt.Fprintln(p.stderr)
		return defaultValue, ErrInterrupt
	}

	val := strings.TrimSpace(line)
	var answer bool
	switch strings.ToLower(val) {
	case "y", "yes":
		answer = true
	case "n", "no":
		answer = false
	case "":
		answer = defaultValue
	default:
		answer = defaultValue
	}

	// Overwrite the prompt line with the final answer (survey Cleanup style).
	answerText := "No"
	if answer {
		answerText = "Yes"
	}
	// Move up one line (ReadString echoed \n), rewrite, clear rest.
	fmt.Fprintf(p.stderr, "\033[1A\r%s %s %s\033[K\n", q, prompt, cyanStyle.Render(answerText))

	return answer, nil
}
