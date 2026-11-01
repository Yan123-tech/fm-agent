# fm-agent

A local-first coding agent that wraps Apple's `fm` CLI (Apple Foundation
Models, macOS 27). It adds an agentic tool loop — file reading/writing/editing,
search, shell commands — on top of the fully on-device model, so your code
never leaves the machine.

Powered by `fm serve` (Chat Completions API over a Unix domain socket), with
native tool calling, streaming output, and automatic context compaction for
the model's small (~4K token) window.

## Requirements

- macOS 27+, Apple Silicon, Apple Intelligence enabled
- `fm` CLI available (`/usr/bin/fm`) with the license accepted (`sudo fm license`)
- Go ≥ 1.22

## Build & install

```sh
# build into the current directory
go build -o fm-agent ./cmd/fm-agent

# or install to ~/.local/bin (no sudo; already on PATH in most shells)
make install
fm-agent            # now runnable from any directory
```

If `~/.local/bin` is not on your PATH yet, add to your shell rc:
`export PATH="$HOME/.local/bin:$PATH"`. Override the location with
`make install PREFIX=/usr/local/bin`.

## Usage

```sh
# interactive session in the current directory
./fm-agent

# pick up a previous transcript
./fm-agent --resume my-session

# auto-approve write/edit/run actions (non-interactive use)
./fm-agent --yes

# extra allowed directory outside the current one
./fm-agent --allow-path ~/other/project
```

The agent confines all file and shell operations to the working directory
(plus `--allow-path` roots). `write_file`, `edit_file` and `run_command` ask
for confirmation unless `--yes` is given.

## Interface

In a terminal you get a full-screen, Claude Code style interface built on
the [Charm](https://charm.sh) stack — the same component set used by Crush,
ccx-go and other terminal agents:

- [bubbletea](https://github.com/charmbracelet/bubbletea) event loop with
  cell-diffed rendering (no flicker)
- [bubbles](https://github.com/charmbracelet/bubbles) viewport (PgUp/PgDn
  scrollback) and textarea (multiline input via alt+enter / shift+enter /
  ctrl+j, history via ↑/↓)
- [lipgloss](https://github.com/charmbracelet/lipgloss) styling and
  [glamour](https://github.com/charmbracelet/glamour) markdown rendering
  with syntax highlighting
- real-time streaming of the model's answer, styled tool-call blocks
  (● running / ✓ done / ✗ error)
- y/n answers tool confirmations with a single keypress (the action is
  shown in the status bar); Esc interrupts the model; Ctrl+C quits
- status bar with model, estimated context usage, and session

Flags: `--plain` forces the line-based interface (also used automatically
when stdin is piped).

### Commands inside the REPL

| command | effect |
|---|---|
| `/exit`, `/quit` | leave |
| `/clear` | drop conversation history |
| `/compact` | summarize older messages to free context |
| `/model system\|pcc` | switch model |
| `/save [name]` | save the transcript |
| `/help` | help |

Sessions autosave to `~/.fm-agent/sessions/<name>.jsonl`.

## Architecture

```
cmd/fm-agent      entry point: flags, env checks, fullscreen TUI / plain REPL
internal/serve    fm serve child process + Chat Completions client (UDS, SSE)
internal/agent    tool-call loop, untrusted-result marking, compaction
internal/tools    read_file, write_file, edit_file, list_files, search,
                  run_command, ask_user
internal/safety   workspace confinement, confirmation prompter
internal/tui      fullscreen UI (bubbletea/bubbles/lipgloss/glamour)
internal/repl     line-based fallback REPL, shared slash commands
internal/session  JSONL transcript persistence
```

## Notes and limitations

- The on-device model has a limited context window (8192 tokens on current
  devices, 4096 on older ones; `fm serve` does not report it). The agent
  starts at 8192, measures real prompt usage from the server, and on
  `context_length_exceeded` halves the window, compacts old turns and
  retries — tune with `--window` if needed. Tool results are truncated to
  fit. The model is a capable helper, not a frontier reasoner — small,
  verifiable steps work best.
- `fm serve` is beta software; behavior may change between macOS builds.
  Errors like `content_filter` stop the turn, transient service errors are
  retried with backoff.
- The `fm` license (FM1) restricts programmatic use of Apple models; `fm
  serve` is Apple's own server surface, but review the license terms and use
  at your own risk, locally.
- Prompt injection is not solved: tool outputs are marked untrusted in the
  transcript, but keep confirmations on for anything destructive.

## License

MIT, see [LICENSE](LICENSE).
