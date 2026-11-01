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

## Build

```sh
go build -o fm-agent ./cmd/fm-agent
```

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
cmd/fm-agent      entry point: flags, env checks, wiring
internal/serve    fm serve child process + Chat Completions client (UDS, SSE)
internal/agent    tool-call loop, untrusted-result marking, compaction
internal/tools    read_file, write_file, edit_file, list_files, search,
                  run_command, ask_user
internal/safety   workspace confinement, confirmation prompter
internal/repl     prompt loop, slash commands
internal/session  JSONL transcript persistence
```

## Notes and limitations

- The on-device model has a ~4096-token context window; the agent compacts
  old turns automatically and truncates tool results. It is a capable helper,
  not a frontier model — small, verifiable steps work best.
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
