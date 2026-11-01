package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
)

// CommandTimeout bounds a single run_command execution.
const CommandTimeout = 60 * time.Second

// --- run_command ---

type runCommand struct {
	ws *safety.Workspace
}

// NewRunCommand builds the run_command tool.
func NewRunCommand(ws *safety.Workspace) Tool { return &runCommand{ws} }

func (t *runCommand) Spec() serve.Tool {
	return spec("run_command",
		"Run a shell command with bash in the workspace directory. Output is truncated. Do not start background processes.",
		map[string]any{
			"command": strProp("Shell command to execute"),
		}, nil)
}

type cmdArgs struct {
	Command string `json:"command"`
}

func (t *runCommand) Describe(args json.RawMessage) string {
	var a cmdArgs
	_ = decodeArgs(args, &a)
	return "run_command: " + a.Command
}

func (t *runCommand) NeedsConfirm() bool { return true }

func (t *runCommand) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a cmdArgs
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if a.Command == "" {
		return "", fmt.Errorf("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", a.Command)
	cmd.Dir = t.ws.Roots()[0]
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	if stderr.Len() > 0 {
		out += "\n[stderr]\n" + stderr.String()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("command timed out after %s", CommandTimeout)
	}
	if err != nil {
		if out != "" {
			out += "\n"
		}
		return out + "exit: " + err.Error(), nil
	}
	if out == "" {
		out = "(no output, exit 0)"
	}
	return out, nil
}

// --- ask_user ---

type askUser struct {
	prompter safety.Prompter
}

// NewAskUser builds the ask_user tool.
func NewAskUser(p safety.Prompter) Tool { return &askUser{p} }

func (t *askUser) Spec() serve.Tool {
	return spec("ask_user",
		"Ask the user a question and return their answer. Use when requirements are ambiguous or a decision is needed.",
		map[string]any{
			"question": strProp("The question to ask"),
		}, nil)
}

type askArgs struct {
	Question string `json:"question"`
}

func (t *askUser) Describe(args json.RawMessage) string { return "ask_user" }

func (t *askUser) NeedsConfirm() bool { return false }

func (t *askUser) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a askArgs
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if a.Question == "" {
		return "", fmt.Errorf("empty question")
	}
	answer := t.prompter.Ask(a.Question)
	if answer == "" {
		return "The user could not answer (non-interactive mode or no answer given). " +
			"Do not ask again; make a reasonable assumption, state it clearly, and proceed.", nil
	}
	return "User answered: " + answer, nil
}
