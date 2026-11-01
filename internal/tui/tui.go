package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"fm-agent/internal/agent"
	"fm-agent/internal/repl"
	"fm-agent/internal/safety"
	"fm-agent/internal/session"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// TUI is the chat-style interactive interface.
type TUI struct {
	ag          *agent.Agent
	store       *session.Store
	in          *bufio.Reader
	out         io.Writer
	sessionName string

	pause *atomic.Bool // spinner holds still while confirmations are up
}

// New builds a TUI. in must be the same buffered reader that backs the
// safety prompter so input is shared.
func New(ag *agent.Agent, store *session.Store, in *bufio.Reader, out io.Writer, sessionName string) *TUI {
	if sessionName == "" {
		sessionName = repl.DefaultSessionName
	}
	return &TUI{
		ag:          ag,
		store:       store,
		in:          in,
		out:         out,
		sessionName: sessionName,
		pause:       &atomic.Bool{},
	}
}

// PauseFlag returns the flag the spinner honors; wire it around anything
// that prints while the model runs (confirmations, tool status lines).
func (t *TUI) PauseFlag() *atomic.Bool { return t.pause }

// Run serves the chat loop until /exit, EOF, or context cancellation.
func (t *TUI) Run(ctx context.Context) error {
	fmt.Fprintf(t.out, "%sfm-agent%s — local coding agent on Apple Foundation Models\n", ansiBold, ansiReset)
	fmt.Fprintf(t.out, "%smodel%s %s · %ssession%s %s · %swindow%s %d tokens\n",
		ansiDim, ansiReset, t.ag.Model(),
		ansiDim, ansiReset, t.sessionName,
		ansiDim, ansiReset, t.ag.WindowTokens())
	fmt.Fprintf(t.out, "%stype /help for commands%s\n\n", ansiDim, ansiReset)

	for {
		fmt.Fprintf(t.out, "%s%s%s %s❯%s ", ansiBold, t.ag.Model(), ansiReset, ansiGreen, ansiReset)
		line, err := t.in.ReadString('\n')
		text := strings.TrimSpace(line)
		if err != nil && text == "" {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if text == "" {
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}
		if strings.HasPrefix(text, "/") {
			quit, cmdErr := repl.ExecCommand(ctx, t.ag, t.store, t.sessionName, text, t.out)
			if cmdErr != nil {
				fmt.Fprintf(t.out, "%serror:%s %v\n", ansiRed, ansiReset, cmdErr)
			}
			if quit {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}

		t.userBubble(text)
		runErr := t.runTurn(ctx, text)
		fmt.Fprintln(t.out)
		if runErr != nil {
			fmt.Fprintf(t.out, "%serror:%s %v\n", ansiRed, ansiReset, runErr)
		}
		if saveErr := t.store.Save(t.sessionName, t.ag.History()); saveErr != nil {
			fmt.Fprintf(t.out, "%ssession not saved: %v%s\n", ansiDim, ansiReset, saveErr)
		}
		t.statusLine()
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (t *TUI) userBubble(_ string) {
	// The terminal already echoed the typed line after the prompt; just
	// separate it from the answer that follows.
	fmt.Fprintln(t.out)
}

// runTurn runs the agent with a spinner that holds still while
// confirmations or tool status lines are printed.
func (t *TUI) runTurn(ctx context.Context, text string) error {
	done := make(chan error, 1)
	go func() { done <- t.ag.Run(ctx, text) }()

	ticker := time.NewTicker(90 * time.Millisecond)
	defer ticker.Stop()
	frame := 0
	spinning := false
	for {
		select {
		case err := <-done:
			if spinning {
				fmt.Fprint(t.out, "\r\x1b[2K") // clear the spinner line
			}
			t.renderAnswer()
			return err
		case <-ticker.C:
			if t.pause.Load() {
				spinning = false
				continue
			}
			fmt.Fprintf(t.out, "\r%s%s%s thinking…", ansiDim, spinnerFrames[frame%len(spinnerFrames)], ansiReset)
			frame++
			spinning = true
		}
	}
}

// renderAnswer prints the final assistant message with markdown styling.
func (t *TUI) renderAnswer() {
	hist := t.ag.History()
	if len(hist) == 0 {
		return
	}
	last := hist[len(hist)-1]
	if last.Role != "assistant" || strings.TrimSpace(last.Content) == "" {
		return
	}
	fmt.Fprintf(t.out, "%sassistant%s\n%s\n\n", ansiBold+ansiGreen, ansiReset, RenderMarkdown(last.Content))
}

func (t *TUI) statusLine() {
	fmt.Fprintf(t.out, "%s ⏤ est %d/%d tokens · %s · /exit to quit%s\n",
		ansiGray, t.ag.EstimateTokens(), t.ag.WindowTokens(), t.ag.Model(), ansiReset)
}

// PausingPrompter wraps a Prompter and stills the spinner while the user
// reads and answers a prompt.
type PausingPrompter struct {
	Inner safety.Prompter
	Pause *atomic.Bool
}

// Confirm implements safety.Prompter.
func (p *PausingPrompter) Confirm(description string) bool {
	p.Pause.Store(true)
	defer p.Pause.Store(false)
	return p.Inner.Confirm(description)
}

// Ask implements safety.Prompter.
func (p *PausingPrompter) Ask(question string) string {
	p.Pause.Store(true)
	defer p.Pause.Store(false)
	return p.Inner.Ask(question)
}

// ToolPrinter returns an agent.ToolPrinter that stills the spinner, prints
// a styled tool line, then lets the spinner resume.
func (t *TUI) ToolPrinter() func(name string) {
	return func(name string) {
		t.pause.Store(true)
		defer t.pause.Store(false)
		fmt.Fprintf(t.out, "\r\x1b[2K%s⚒%s %s%s%s\n", ansiYellow, ansiReset, ansiDim, name, ansiReset)
	}
}
