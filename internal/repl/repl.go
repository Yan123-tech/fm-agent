// Package repl provides the interactive prompt loop for fm-agent.
package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"fm-agent/internal/agent"
	"fm-agent/internal/session"
)

// DefaultSessionName is used when the user does not pick one.
const DefaultSessionName = "default"

// REPL drives the interactive conversation.
type REPL struct {
	ag          *agent.Agent
	store       *session.Store
	in          *bufio.Reader
	out         io.Writer
	interactive bool

	sessionName string
}

// Options configures a REPL.
type Options struct {
	// SessionName selects the JSONL transcript to autosave into.
	SessionName string
	// Interactive enables prompt rendering; disable when stdin is piped.
	Interactive bool
}

// New builds a REPL. The same in reader should back the safety prompter so
// buffered input is shared between confirmations and the prompt loop.
func New(ag *agent.Agent, store *session.Store, in io.Reader, out io.Writer, opts Options) *REPL {
	if opts.SessionName == "" {
		opts.SessionName = DefaultSessionName
	}
	return &REPL{
		ag:          ag,
		store:       store,
		in:          bufio.NewReader(in),
		out:         out,
		interactive: opts.Interactive,
		sessionName: opts.SessionName,
	}
}

// ErrExit is returned by Run when the user asks to leave.
var ErrExit = errors.New("exit requested")

// Run serves prompts until /exit, EOF, or context cancellation.
func (r *REPL) Run(ctx context.Context) error {
	if r.interactive {
		fmt.Fprintf(r.out, "fm-agent (model: %s) — /help for commands, /exit to quit\n", r.ag.Model())
	}
	for {
		if r.interactive {
			fmt.Fprintf(r.out, "%s ❯ ", r.ag.Model())
		}
		line, err := r.in.ReadString('\n')
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
			quit, cmdErr := r.command(ctx, text)
			if cmdErr != nil {
				fmt.Fprintf(r.out, "error: %v\n", cmdErr)
			}
			if quit {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}
		runErr := r.ag.Run(ctx, text)
		fmt.Fprintln(r.out)
		if runErr != nil {
			fmt.Fprintf(r.out, "error: %v\n", runErr)
		}
		if saveErr := r.autosave(); saveErr != nil && r.interactive {
			fmt.Fprintf(r.out, "(session not saved: %v)\n", saveErr)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// command handles slash commands; it reports whether the loop should exit.
func (r *REPL) command(ctx context.Context, text string) (bool, error) {
	fields := strings.Fields(text)
	name := fields[0]
	args := fields[1:]

	switch name {
	case "/exit", "/quit":
		return true, nil
	case "/help":
		fmt.Fprint(r.out, `commands:
  /exit, /quit   leave the session
  /clear         drop conversation history
  /compact       summarize older messages to free context
  /model <name>  switch model (system, pcc)
  /save [name]   save transcript (default: current session)
  /help          this help
`)
		return false, nil
	case "/clear":
		r.ag.Clear()
		return false, r.autosave()
	case "/compact":
		if err := r.ag.Compact(ctx); err != nil {
			return false, err
		}
		fmt.Fprintf(r.out, "compacted (est. %d tokens)\n", r.ag.EstimateTokens())
		return false, r.autosave()
	case "/model":
		if len(args) != 1 {
			return false, fmt.Errorf("usage: /model <system|pcc>")
		}
		r.ag.SetModel(args[0])
		fmt.Fprintf(r.out, "model set to %s\n", args[0])
		return false, nil
	case "/save":
		name := r.sessionName
		if len(args) == 1 {
			name = args[0]
		} else if len(args) > 1 {
			return false, fmt.Errorf("usage: /save [name]")
		}
		if err := r.store.Save(name, r.ag.History()); err != nil {
			return false, err
		}
		fmt.Fprintf(r.out, "saved to %s\n", r.store.Path(name))
		return false, nil
	default:
		return false, fmt.Errorf("unknown command %q (try /help)", name)
	}
}

func (r *REPL) autosave() error {
	return r.store.Save(r.sessionName, r.ag.History())
}
