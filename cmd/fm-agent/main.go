// Command fm-agent runs a local-first coding agent on top of Apple
// Foundation Models (the macOS 27 `fm` CLI).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"fm-agent/internal/agent"
	"fm-agent/internal/repl"
	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
	"fm-agent/internal/session"
	"fm-agent/internal/tools"
	"fm-agent/internal/tui"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		model      = flag.String("model", "system", "model to use (system, pcc)")
		yes        = flag.Bool("yes", false, "auto-approve write/edit/run actions")
		resume     = flag.String("resume", "", "resume a saved session by name")
		sessionFlg = flag.String("session", "", "session name for autosave (default: value of -resume or \"default\")")
		allowPaths multiFlag
		window     = flag.Int("window", agent.DefaultWindowTokens, "assumed context window in tokens (auto-shrinks on overflow)")
		plain      = flag.Bool("plain", false, "use the plain line-based interface instead of the TUI")
	)
	flag.Var(&allowPaths, "allow-path", "additional allowed directory (repeatable)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmBin, err := exec.LookPath("fm")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fm-agent: the 'fm' CLI was not found in PATH.")
		fmt.Fprintln(os.Stderr, "It ships with macOS 27; accept the license once with: sudo fm license")
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fm-agent:", err)
		return 1
	}
	roots := append([]string{cwd}, allowPaths...)
	ws, err := safety.NewWorkspace(roots...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fm-agent:", err)
		return 1
	}

	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("fm-agent-%d.sock", os.Getpid()))
	srv := serve.NewServer(fmBin, socketPath)
	if err := srv.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fm-agent: could not start 'fm serve':", err)
		fmt.Fprintln(os.Stderr, "Hint: Apple Intelligence must be enabled and the fm license accepted (sudo fm license).")
		return 1
	}
	defer srv.Stop()

	var prompter safety.Prompter
	if *yes {
		prompter = safety.AutoApprove{}
	} else {
		prompter = safety.NewIOPrompter(bufio.NewReader(os.Stdin), os.Stdout)
	}

	interactive := isTerminal(os.Stdin) && !*plain

	reg := tools.NewRegistry(ws, prompter)
	var opts agent.Options
	if interactive {
		// The TUI renders the final answer itself; streaming goes nowhere.
		opts = agent.Options{
			Model:        *model,
			Out:          io.Discard,
			WindowTokens: *window,
		}
	} else {
		opts = agent.Options{
			Model:        *model,
			Out:          os.Stdout,
			WindowTokens: *window,
		}
	}
	ag := agent.New(srv, reg, ws.Roots(), opts)

	store, err := session.NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fm-agent:", err)
		return 1
	}

	name := *sessionFlg
	if name == "" {
		name = *resume
	}
	if name == "" {
		name = repl.DefaultSessionName
	}
	if *resume != "" {
		msgs, err := store.Load(*resume)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fm-agent: cannot resume:", err)
			return 1
		}
		if len(msgs) > 0 && msgs[0].Role == "system" {
			msgs = msgs[1:]
		}
		ag.SetHistory(msgs)
		fmt.Fprintf(os.Stderr, "resumed session %q (%d messages)\n", *resume, len(msgs))
	}

	if interactive {
		ui := tui.New(ag, store, sharedReader(prompter), os.Stdout, name)
		// Stills the spinner while the user answers confirmations.
		pausing := &tui.PausingPrompter{Inner: prompter, Pause: ui.PauseFlag()}
		reg.SetPrompter(pausing)
		ag.SetToolPrinter(ui.ToolPrinter())
		if err := ui.Run(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "fm-agent:", err)
			return 1
		}
		return 0
	}

	r := repl.New(ag, store, sharedReader(prompter), os.Stdout, repl.Options{
		SessionName: name,
		Interactive: false,
	})
	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fm-agent:", err)
		return 1
	}
	return 0
}

// sharedReader returns the buffered reader behind the prompter so the REPL
// and confirmations consume the same input buffer. With --yes there is no
// interactive prompter and the REPL gets its own reader over stdin.
func sharedReader(p safety.Prompter) *bufio.Reader {
	if iop, ok := p.(*safety.IOPrompter); ok {
		return iop.In
	}
	return bufio.NewReader(os.Stdin)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
