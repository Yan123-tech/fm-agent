// Package tui implements the fullscreen chat interface on the Charm
// stack (bubbletea + bubbles + lipgloss + glamour) — the same component
// set used by Crush, ccx-go and other Claude Code-style terminal agents.
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"fm-agent/internal/agent"
	"fm-agent/internal/repl"
	"fm-agent/internal/session"
)

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockSys
)

// block is one transcript entry.
type block struct {
	kind     blockKind
	raw      string // assistant markdown source
	rendered string // cached styled output
	name     string // tool name
	result   string
	pend     bool
	failed   bool
}

// Messages flowing from the agent goroutine into the event loop.
type deltaMsg string

type runDoneMsg struct{ err error }

type toolStartMsg struct{ name string }

type toolDoneMsg struct {
	name   string
	result string
}

type confirmMsg struct {
	desc string
	resp chan bool
}

type askMsg struct {
	question string
	resp     chan string
}

// snapshot is a consistent read of the model for tests and UIs.
type snapshot struct {
	blocks  []*block
	running bool
	confirm bool
	ask     bool
	model   string
	view    string
}

type queryMsg struct{ resp chan snapshot }

var (
	styleUser = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	styleDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleCyan = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// Model is the bubbletea model for the fullscreen agent UI.
// Update runs on the event loop while View is called concurrently by the
// renderer, so all model state is guarded by mu.
type Model struct {
	mu sync.Mutex

	prog        *tea.Program
	ag          *agent.Agent
	store       *session.Store
	sessionName string

	vp viewport.Model
	ta textarea.Model
	sp spinner.Model

	width, height int
	ready         bool

	blocks   []*block
	running  bool
	cancel   context.CancelFunc
	confirm  *confirmMsg
	ask      *askMsg
	status   string
	renderer *glamour.TermRenderer
}

// New builds the model. Wire the prompter and agent callbacks after
// creating the program with SetProgram.
func New(ag *agent.Agent, store *session.Store, sessionName string) *Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = styleUser.Render("❯ ") + " "
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "shift+enter", "ctrl+j"))
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleCyan

	m := &Model{
		ag:          ag,
		store:       store,
		sessionName: sessionName,
		ta:          ta,
		sp:          sp,
	}
	m.sysf("fm-agent · model %s · session %s · /help for commands", ag.Model(), sessionName)
	return m
}

// SetProgram links the running program so callbacks can post messages.
func (m *Model) SetProgram(p *tea.Program) { m.prog = p }

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.sp.Tick)
}

// sysf appends a dim system line.
func (m *Model) sysf(format string, args ...any) {
	for _, l := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		m.blocks = append(m.blocks, &block{kind: blockSys, rendered: styleDim.Render(l)})
	}
	m.refreshContent()
}

// --- tea.Model ---

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width < 2 || msg.Height < 2 {
			return m, nil // no usable geometry yet
		}
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		m.recreateRenderer()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case deltaMsg:
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockAssistant {
			b := m.blocks[n-1]
			b.raw += string(msg)
			if m.renderer != nil {
				if r, err := m.renderer.Render(b.raw); err == nil {
					b.rendered = strings.TrimRight(r, "\n")
					m.refreshContent()
				}
			}
		}
		return m, nil

	case toolStartMsg:
		m.blocks = append(m.blocks, &block{kind: blockTool, name: msg.name, pend: true})
		m.refreshContent()
		return m, nil

	case toolDoneMsg:
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockTool {
			b := m.blocks[n-1]
			b.pend = false
			b.result = msg.result
			b.failed = strings.HasPrefix(msg.result, "Error")
		}
		m.refreshContent()
		return m, nil

	case confirmMsg:
		m.confirm = &msg
		return m, nil

	case askMsg:
		m.ask = &msg
		return m, nil

	case runDoneMsg:
		m.running = false
		m.cancel = nil
		if msg.err != nil {
			m.status = styleErr.Render("error: " + msg.err.Error())
		} else {
			m.status = ""
		}
		_ = m.store.Save(m.sessionName, m.ag.History())
		m.refreshContent()
		return m, nil

	case queryMsg:
		blocks := make([]*block, len(m.blocks))
		for i, b := range m.blocks {
			cp := *b
			blocks[i] = &cp
		}
		msg.resp <- snapshot{
			blocks:  blocks,
			running: m.running,
			confirm: m.confirm != nil,
			ask:     m.ask != nil,
			model:   m.ag.Model(),
			view:    m.viewLocked(),
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Confirmation bar has priority.
	if m.confirm != nil {
		switch key {
		case "y", "Y":
			m.confirm.resp <- true
			m.confirm = nil
		case "n", "N", "esc", "ctrl+c":
			m.confirm.resp <- false
			m.confirm = nil
		}
		return m, nil
	}

	switch key {
	case "ctrl+c":
		if m.running && m.cancel != nil {
			m.cancel()
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		if m.running && m.cancel != nil {
			m.cancel()
		}
		return m, nil
	case "enter":
		// A pending ask_user question is answered by submitting input even
		// while the agent is still running.
		if !m.running || m.ask != nil {
			m.submit()
		}
		return m, nil
	case "pgup", "pgdown", "ctrl+u":
		if key == "ctrl+u" && m.ta.LineCount() > 1 {
			// let the textarea keep ctrl+u for its own editing
			var cmd tea.Cmd
			m.ta, cmd = m.ta.Update(msg)
			m.layout()
			return m, cmd
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.layout()
	return m, cmd
}

// submit sends the current input.
func (m *Model) submit() {
	text := strings.TrimRight(m.ta.Value(), "\n")
	if text == "" {
		return
	}
	m.ta.Reset()
	m.layout()

	for i, l := range strings.Split(text, "\n") {
		prefix := "❯ "
		if i > 0 {
			prefix = "  "
		}
		m.blocks = append(m.blocks, &block{kind: blockUser,
			rendered: styleUser.Render(prefix) + l})
	}

	// A pending ask_user tool consumes this input as its answer.
	if m.ask != nil {
		m.ask.resp <- text
		m.ask = nil
		m.refreshContent()
		return
	}

	if strings.HasPrefix(text, "/") {
		var buf strings.Builder
		quit, err := repl.ExecCommand(context.Background(), m.ag, m.store, m.sessionName, text, &buf)
		for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if l != "" {
				m.blocks = append(m.blocks, &block{kind: blockSys, rendered: styleDim.Render(l)})
			}
		}
		if err != nil {
			m.blocks = append(m.blocks, &block{kind: blockSys, rendered: styleErr.Render("error: " + err.Error())})
		}
		_ = m.store.Save(m.sessionName, m.ag.History())
		m.refreshContent()
		if quit {
			m.prog.Quit()
		}
		return
	}

	m.blocks = append(m.blocks, &block{kind: blockAssistant})
	m.refreshContent()
	m.running = true

	runCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go func() {
		err := m.ag.Run(runCtx, text)
		m.prog.Send(runDoneMsg{err: err})
	}()
}

// layout resizes viewport and textarea for the current window and content.
func (m *Model) layout() {
	if !m.ready {
		return
	}
	inputH := m.ta.LineCount() + 1
	if inputH < 3 {
		inputH = 3
	}
	if inputH > m.height/3 {
		inputH = m.height / 3
	}
	m.ta.SetWidth(m.width - 2)
	m.ta.SetHeight(inputH)
	bodyH := m.height - inputH - 1 // status bar
	if bodyH < 1 {
		bodyH = 1
	}
	m.vp.Width = m.width
	m.vp.Height = bodyH
}

// recreateRenderer rebuilds the markdown renderer for the current width.
func (m *Model) recreateRenderer() {
	if m.width <= 0 {
		return
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(max(m.width-4, 20)),
	)
	if err == nil {
		m.renderer = r
	}
}

// refreshContent rebuilds the viewport content and follows the bottom.
func (m *Model) refreshContent() {
	atBottom := m.vp.AtBottom()
	var sb strings.Builder
	for _, b := range m.blocks {
		sb.WriteString(b.text())
		sb.WriteString("\n")
	}
	m.vp.SetContent(sb.String())
	if atBottom {
		m.vp.GotoBottom()
	}
}

// text renders a block to its display form.
func (b *block) text() string {
	switch b.kind {
	case blockUser, blockSys:
		return b.rendered
	case blockAssistant:
		if b.rendered == "" && b.raw != "" {
			return styleDim.Render(b.raw)
		}
		return b.rendered
	case blockTool:
		if b.pend {
			return styleWarn.Render("● ") + b.name
		}
		mark := styleOK.Render("✓ ")
		if b.failed {
			mark = styleErr.Render("✗ ")
		}
		return mark + b.name + styleDim.Render(oneLine(b.result))
	}
	return ""
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

// View implements tea.Model.
func (m *Model) View() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

// viewLocked renders the frame; callers must hold m.mu.
func (m *Model) viewLocked() string {
	if !m.ready {
		return "loading…"
	}
	var sb strings.Builder
	sb.WriteString(m.vp.View())
	sb.WriteString("\n")
	sb.WriteString(styleDim.Render(strings.Repeat("─", m.width)))
	sb.WriteString("\n")
	sb.WriteString(m.ta.View())
	sb.WriteString("\n")
	sb.WriteString(m.statusLine())
	return sb.String()
}

func (m *Model) statusLine() string {
	var left string
	switch {
	case m.confirm != nil:
		left = styleWarn.Render("⚠ allow? " + m.confirm.desc + " [y/n]")
	case m.running:
		left = m.sp.View() + " thinking"
	default:
		left = styleOK.Render("● ready")
	}
	left += styleDim.Render(fmt.Sprintf("  %s · est %d/%d tok · %s",
		m.ag.Model(), m.ag.EstimateTokens(), m.ag.WindowTokens(), m.sessionName))
	right := styleDim.Render("enter send · alt+enter newline · esc interrupt · ctrl+c quit")
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if m.status != "" {
		line = m.status
	}
	return truncateLine(line, m.width)
}

func truncateLine(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
