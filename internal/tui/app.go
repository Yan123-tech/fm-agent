package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"fm-agent/internal/agent"
	"fm-agent/internal/repl"
	"fm-agent/internal/safety"
	"fm-agent/internal/session"
	"fm-agent/internal/term"
)

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockSys
)

// block is one transcript entry; assistant blocks stream incrementally.
type block struct {
	kind   blockKind
	lines  []string
	raw    string
	ms     *mdStream
	flush  int // bytes of raw already rendered into lines
	final  bool
	pend   bool // tool block: still running
	failed bool // tool block: finished with an error
}

// feed renders newly completed lines from raw.
func (b *block) feed() {
	rest := b.raw[b.flush:]
	for {
		i := strings.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		if l := b.ms.feedLine(rest[:i]); l != "" {
			b.lines = append(b.lines, l)
		}
		b.flush += i + 1
		rest = rest[i+1:]
	}
}

// view returns the renderable lines (including the in-progress partial).
func (b *block) view() []string {
	out := b.lines
	if b.kind == blockAssistant && !b.final {
		if p := b.ms.partial(b.raw[b.flush:]); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 && b.pend {
		return []string{ansiYellow + "●" + ansiReset}
	}
	return out
}

type confirmReq struct {
	desc string
	resp chan bool
}

type askReq struct {
	question string
	resp     chan string
}

// App is the full-screen terminal UI.
type App struct {
	ag          *agent.Agent
	store       *session.Store
	sessionName string
	out         io.Writer
	keys        <-chan Key

	// SizeFn returns the terminal size; replaceable in tests.
	SizeFn func() (int, int)

	ctx     context.Context
	wake    chan struct{}
	toolBuf []string

	mu       sync.Mutex
	blocks   []*block
	input    []rune
	cursor   int
	hist     []string
	histIdx  int
	scroll   int
	running  bool
	spinner  int
	cancel   context.CancelFunc
	runDone  chan struct{}
	runErr   error
	confirmQ []*confirmReq
	askQ     []*askReq
	quitting bool
}

// NewApp builds the full-screen UI. The agent's output writer and tool
// printers are wired separately via StreamWriter, ToolPrinter and
// ToolEventPrinter after construction.
func NewApp(ag *agent.Agent, store *session.Store, out io.Writer, keys <-chan Key, sessionName string) *App {
	if sessionName == "" {
		sessionName = repl.DefaultSessionName
	}
	return &App{
		ag:          ag,
		store:       store,
		sessionName: sessionName,
		out:         out,
		keys:        keys,
		SizeFn:      func() (int, int) { return term.Size(os.Stdout) },
		wake:        make(chan struct{}, 1),
		histIdx:     -1,
	}
}

// signal wakes the render loop for async events.
func (app *App) signal() {
	select {
	case app.wake <- struct{}{}:
	default:
	}
}

// sysf appends a dim system line.
func (app *App) sysf(format string, args ...any) {
	app.mu.Lock()
	defer app.mu.Unlock()
	for _, l := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		app.blocks = append(app.blocks, &block{kind: blockSys, lines: []string{ansiDim + l + ansiReset}})
	}
}

// StreamWriter returns the io.Writer the agent should stream into.
func (app *App) StreamWriter() io.Writer { return &streamWriter{app} }

type streamWriter struct{ app *App }

func (w *streamWriter) Write(p []byte) (int, error) {
	app := w.app
	app.mu.Lock()
	defer app.mu.Unlock()
	if n := len(app.blocks); n > 0 {
		if b := app.blocks[n-1]; b.kind == blockAssistant && !b.final {
			b.raw += string(p)
			b.feed()
			app.signal()
		}
	}
	return len(p), nil
}

// ToolPrinter returns the agent ToolPrinter callback: opens a tool block.
func (app *App) ToolPrinter() func(string) {
	return func(name string) {
		app.mu.Lock()
		defer app.mu.Unlock()
		app.blocks = append(app.blocks, &block{kind: blockTool, pend: true})
		app.toolBuf = []string{name}
		app.signal()
	}
}

// ToolEventPrinter returns the agent ToolEventPrinter callback: closes the
// tool block with its result.
func (app *App) ToolEventPrinter() func(name, result string) {
	return func(name, result string) {
		app.mu.Lock()
		defer app.mu.Unlock()
		if n := len(app.blocks); n > 0 && app.blocks[n-1].kind == blockTool {
			b := app.blocks[n-1]
			b.pend = false
			b.failed = strings.HasPrefix(result, "Error")
			mark := ansiGreen + "✓"
			if b.failed {
				mark = ansiRed + "✗"
			}
			b.lines = []string{fmt.Sprintf("%s %s%s %s%s", mark, ansiBold, name, ansiReset, ansiDim+oneLine(result)+ansiReset)}
		}
		app.signal()
	}
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 100
	if len(s) > max {
		s = s[:max] + "…"
	}
	if s == "" {
		return ""
	}
	return " " + s
}

// Prompter returns a safety.Prompter that renders in the UI.
func (app *App) Prompter() safety.Prompter { return &appPrompter{app} }

type appPrompter struct{ app *App }

func (p *appPrompter) Confirm(description string) bool {
	req := &confirmReq{desc: description, resp: make(chan bool, 1)}
	p.app.mu.Lock()
	p.app.confirmQ = append(p.app.confirmQ, req)
	p.app.mu.Unlock()
	p.app.signal()
	select {
	case b := <-req.resp:
		return b
	case <-p.app.ctx.Done():
		return false
	}
}

func (p *appPrompter) Ask(question string) string {
	req := &askReq{question: question, resp: make(chan string, 1)}
	p.app.mu.Lock()
	p.app.askQ = append(p.app.askQ, req)
	p.app.mu.Unlock()
	p.app.signal()
	select {
	case s := <-req.resp:
		return s
	case <-p.app.ctx.Done():
		return ""
	}
}

// Run starts the alternate-screen event loop.
func (app *App) Run(ctx context.Context) error {
	app.ctx = ctx
	fmt.Fprint(app.out, "\x1b[?1049h\x1b[?2004h") // alt screen + bracketed paste
	defer fmt.Fprint(app.out, "\x1b[?2004l\x1b[?1049l")
	app.sysf("fm-agent · model %s · session %s · /help for commands", app.ag.Model(), app.sessionName)

	tick := time.NewTicker(60 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case k, ok := <-app.keys:
			if !ok {
				return nil
			}
			app.handleKey(k)
		case <-app.wake:
		case <-tick.C:
			app.mu.Lock()
			if app.running {
				app.spinner++
			}
			app.mu.Unlock()
		case <-app.runDone:
			app.finishRun()
		case <-ctx.Done():
			return nil
		}
		if app.quitting {
			return nil
		}
		app.render()
	}
}

// finishRun finalizes the assistant block after the agent goroutine ends.
func (app *App) finishRun() {
	app.mu.Lock()
	defer app.mu.Unlock()
	if n := len(app.blocks); n > 0 && app.blocks[n-1].kind == blockAssistant {
		b := app.blocks[n-1]
		b.feed()
		b.final = true
	}
	if app.runErr != nil && !errors.Is(app.runErr, context.Canceled) {
		app.blocks = append(app.blocks, &block{kind: blockSys,
			lines: []string{ansiRed + "error: " + app.runErr.Error() + ansiReset}})
	}
	app.running = false
	app.runDone = nil
	app.cancel = nil
	_ = app.store.Save(app.sessionName, app.ag.History())
	app.signal()
}

// handleKey routes one key event.
func (app *App) handleKey(k Key) {
	app.mu.Lock()
	defer app.mu.Unlock()

	// Confirmation bar takes priority.
	if len(app.confirmQ) > 0 {
		req := app.confirmQ[0]
		switch k.Type {
		case KeyRune:
			switch k.R {
			case 'y', 'Y':
				app.confirmQ = app.confirmQ[1:]
				req.resp <- true
			case 'n', 'N':
				app.confirmQ = app.confirmQ[1:]
				req.resp <- false
			}
		case KeyEsc, KeyCtrlC:
			app.confirmQ = app.confirmQ[1:]
			req.resp <- false
		}
		return
	}

	switch k.Type {
	case KeyRune:
		if !app.running {
			app.input = append(app.input, 0)
			copy(app.input[app.cursor+1:], app.input[app.cursor:])
			app.input[app.cursor] = k.R
			app.cursor++
			app.histIdx = -1
		}
	case KeyAltEnter:
		app.insertNewline()
	case KeyEnter:
		if !app.running && len(app.input) > 0 {
			app.submitLocked()
		}
	case KeyBackspace:
		if app.cursor > 0 {
			app.input = append(app.input[:app.cursor-1], app.input[app.cursor:]...)
			app.cursor--
		}
	case KeyDelete:
		if app.cursor < len(app.input) {
			app.input = append(app.input[:app.cursor], app.input[app.cursor+1:]...)
		}
	case KeyLeft:
		if app.cursor > 0 {
			app.cursor--
		}
	case KeyRight:
		if app.cursor < len(app.input) {
			app.cursor++
		}
	case KeyHome:
		app.cursor = lineStart(app.input, app.cursor)
	case KeyEnd:
		app.cursor = lineEnd(app.input, app.cursor)
	case KeyUp:
		app.historyPrev()
	case KeyDown:
		app.historyNext()
	case KeyPgUp:
		app.scroll += app.pageSize() - 1
	case KeyPgDn:
		app.scroll -= app.pageSize() - 1
		if app.scroll < 0 {
			app.scroll = 0
		}
	case KeyEsc:
		if app.running && app.cancel != nil {
			app.cancel()
		} else if len(app.input) > 0 {
			app.input = nil
			app.cursor = 0
		}
	case KeyCtrlC:
		if app.running && app.cancel != nil {
			app.cancel()
		} else if len(app.input) > 0 {
			app.input = nil
			app.cursor = 0
		} else {
			app.quitting = true
		}
	case KeyCtrlD:
		if len(app.input) == 0 {
			app.quitting = true
		}
	case keyClearInput:
		app.input = nil
		app.cursor = 0
	case keyKillLine:
		app.input = app.input[:app.cursor]
	case keyDeleteWord:
		app.deleteWord()
	}
	if app.scroll < 0 {
		app.scroll = 0
	}
}

func (app *App) insertNewline() {
	app.input = append(app.input, 0)
	copy(app.input[app.cursor+1:], app.input[app.cursor:])
	app.input[app.cursor] = '\n'
	app.cursor++
}

func (app *App) deleteWord() {
	for app.cursor > 0 && app.input[app.cursor-1] == ' ' {
		app.input = app.input[:app.cursor-1]
		app.cursor--
	}
	for app.cursor > 0 && app.input[app.cursor-1] != ' ' {
		app.input = app.input[:app.cursor-1]
		app.cursor--
	}
}

func (app *App) historyPrev() {
	if len(app.hist) == 0 {
		return
	}
	if strings.Contains(string(app.input), "\n") {
		return // multiline: let cursor movement handle it
	}
	if app.histIdx == -1 {
		app.histIdx = len(app.hist) - 1
	} else if app.histIdx > 0 {
		app.histIdx--
	}
	app.input = []rune(app.hist[app.histIdx])
	app.cursor = len(app.input)
}

func (app *App) historyNext() {
	if app.histIdx == -1 {
		return
	}
	app.histIdx++
	if app.histIdx >= len(app.hist) {
		app.histIdx = -1
		app.input = nil
	} else {
		app.input = []rune(app.hist[app.histIdx])
	}
	app.cursor = len(app.input)
}

// pageSize estimates the transcript body height for scrolling.
func (app *App) pageSize() int {
	_, h := app.SizeFn()
	if h < 10 {
		return 2
	}
	return h * 2 / 3
}

// submitLocked sends the input line: slash command, pending ask answer, or
// a new agent turn. Must be called with mu held.
func (app *App) submitLocked() {
	text := string(app.input)
	app.input = nil
	app.cursor = 0
	app.histIdx = -1
	app.scroll = 0

	for i, l := range strings.Split(text, "\n") {
		prefix := ansiGreen + "❯" + ansiReset + " "
		if i > 0 {
			prefix = "  "
		}
		app.blocks = append(app.blocks, &block{kind: blockUser, lines: []string{prefix + l}})
	}

	// Pending ask_user answer?
	if len(app.askQ) > 0 {
		req := app.askQ[0]
		app.askQ = app.askQ[1:]
		req.resp <- text
		return
	}

	if strings.HasPrefix(text, "/") {
		var buf strings.Builder
		quit, err := repl.ExecCommand(app.ctx, app.ag, app.store, app.sessionName, text, &buf)
		for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if l != "" {
				app.blocks = append(app.blocks, &block{kind: blockSys, lines: []string{ansiDim + l + ansiReset}})
			}
		}
		if err != nil {
			app.blocks = append(app.blocks, &block{kind: blockSys, lines: []string{ansiRed + "error: " + err.Error() + ansiReset}})
		}
		_ = app.store.Save(app.sessionName, app.ag.History())
		if quit {
			app.quitting = true
		}
		return
	}

	app.hist = append(app.hist, text)
	app.blocks = append(app.blocks, &block{kind: blockAssistant, ms: &mdStream{}})

	runCtx, cancel := context.WithCancel(app.ctx)
	app.cancel = cancel
	app.running = true
	app.runDone = make(chan struct{})
	done := app.runDone
	ag := app.ag
	go func() {
		err := ag.Run(runCtx, text)
		app.mu.Lock()
		app.runErr = err
		app.mu.Unlock()
		close(done)
	}()
}

func lineStart(r []rune, cur int) int {
	for cur > 0 && r[cur-1] != '\n' {
		cur--
	}
	return cur
}

func lineEnd(r []rune, cur int) int {
	for cur < len(r) && r[cur] != '\n' {
		cur++
	}
	return cur
}
