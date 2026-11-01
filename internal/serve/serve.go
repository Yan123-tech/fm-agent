package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// APIError is a typed error returned by fm serve in the OpenAI error shape.
type APIError struct {
	Status  int
	Type    string
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("fm serve: %s (%s)", e.Message, e.Code)
	}
	return fmt.Sprintf("fm serve: %s", e.Message)
}

// Retryable reports whether the request may succeed if retried later.
func (e *APIError) Retryable() bool {
	switch e.Code {
	case "model_service_unavailable", "rate_limit_exceeded", "upstream_closed", "upstream_timeout":
		return true
	}
	return e.Status == http.StatusServiceUnavailable || e.Status >= 500
}

// Server owns the `fm serve` child process and a Chat Completions client
// bound to its Unix domain socket.
type Server struct {
	binPath    string
	socketPath string

	cmd    *exec.Cmd
	client *http.Client

	started bool
}

// NewServer prepares a manager for the fm binary. Start must be called
// before Chat/ChatStream.
func NewServer(binPath, socketPath string) *Server {
	s := &Server{binPath: binPath, socketPath: socketPath}
	s.client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", s.socketPath)
			},
		},
	}
	return s
}

// SocketPath returns the path the child process listens on.
func (s *Server) SocketPath() string { return s.socketPath }

// Start spawns `fm serve --socket <path>` and waits until /health answers.
func (s *Server) Start(ctx context.Context) error {
	if s.started {
		return nil
	}
	s.cmd = exec.CommandContext(ctx, s.binPath, "serve", "--socket", s.socketPath)
	var stderr bytes.Buffer
	s.cmd.Stderr = &stderr
	s.cmd.Stdout = io.Discard
	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("starting fm serve: %w", err)
	}
	s.started = true

	deadline := time.Now().Add(45 * time.Second)
	for {
		if time.Now().After(deadline) {
			s.Stop()
			return fmt.Errorf("fm serve did not become healthy at %s (stderr: %s)", s.socketPath, stderr.String())
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://fm/health", nil)
		if err != nil {
			return err
		}
		resp, err := s.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if s.cmd.ProcessState != nil && s.cmd.ProcessState.Exited() {
			s.started = false
			return fmt.Errorf("fm serve exited before becoming healthy: %s", strings.TrimSpace(stderr.String()))
		}
		select {
		case <-ctx.Done():
			s.Stop()
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Stop terminates the child process and removes the socket file.
func (s *Server) Stop() {
	if !s.started {
		return
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	s.started = false
	_ = os.Remove(s.socketPath)
}

// Alive reports whether the child process appears to be running.
func (s *Server) Alive() bool {
	return s.started && s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil
}

const (
	maxAttempts  = 4
	requestLimit = 5 * time.Minute
)

var (
	retryBase = time.Second
	retryCap  = 8 * time.Second
)

// Chat sends a non-streaming request and returns the assembled reply.
func (s *Server) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	return s.do(ctx, req, nil)
}

// ChatStream sends a streaming request, invoking onDelta for each content
// fragment, and returns the fully assembled reply (including tool calls).
func (s *Server) ChatStream(ctx context.Context, req *ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	return s.do(ctx, req, onDelta)
}

func (s *Server) do(ctx context.Context, req *ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, requestLimit)
	defer cancel()

	var lastErr error
	backoff := retryBase
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := s.once(attemptCtx, req, onDelta)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var apiErr *APIError
		connDead := errors.Is(err, os.ErrNotExist) || isConnError(err)
		if !connDead && !(errors.As(err, &apiErr) && apiErr.Retryable()) {
			return nil, err
		}
		if connDead {
			// The server process may have died: restart it once and retry.
			s.Stop()
			if startErr := s.Start(attemptCtx); startErr != nil {
				return nil, fmt.Errorf("%w (restart failed: %v)", err, startErr)
			}
		}
		select {
		case <-attemptCtx.Done():
			return nil, lastErr
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > retryCap {
			backoff = retryCap
		}
	}
	return nil, lastErr
}

func isConnError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) || errors.Is(err, io.EOF)
}

func (s *Server) once(ctx context.Context, req *ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	if onDelta != nil {
		req = cloneRequest(req)
		req.Stream = true
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fm/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeError(resp)
	}
	if onDelta == nil {
		var wr wireResponse
		if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}
		if len(wr.Choices) == 0 {
			return nil, &APIError{Status: resp.StatusCode, Message: "empty choices in response"}
		}
		return &ChatResponse{
			Message:      wr.Choices[0].Message,
			FinishReason: wr.Choices[0].FinishReason,
			Usage:        wr.Usage,
		}, nil
	}
	return readStream(resp.Body, onDelta)
}

func decodeError(resp *http.Response) error {
	var we wireError
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(data, &we); err != nil || we.Error.Message == "" {
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	return &APIError{
		Status:  resp.StatusCode,
		Type:    we.Error.Type,
		Code:    we.Error.Code,
		Message: we.Error.Message,
	}
}

// readStream consumes an SSE response and reconstructs the full message,
// accumulating fragmented tool calls by index.
func readStream(r io.Reader, onDelta func(string)) (*ChatResponse, error) {
	out := &ChatResponse{}
	var content strings.Builder
	var calls []ToolCall

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				out.Usage = chunk.Usage
			}
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			content.WriteString(ch.Delta.Content)
			onDelta(ch.Delta.Content)
		}
		for _, d := range ch.Delta.ToolCalls {
			for len(calls) <= d.Index {
				calls = append(calls, ToolCall{Type: "function"})
			}
			c := &calls[d.Index]
			if d.ID != "" {
				c.ID = d.ID
			}
			if d.Type != "" {
				c.Type = d.Type
			}
			if d.Function.Name != "" {
				c.Function.Name = d.Function.Name
			}
			c.Function.Arguments += d.Function.Arguments
		}
		if ch.FinishReason != "" {
			out.FinishReason = ch.FinishReason
		}
		if chunk.Usage != nil {
			out.Usage = chunk.Usage
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading stream: %w", err)
	}
	out.Message.Content = content.String()
	out.Message.ToolCalls = calls
	return out, nil
}

func cloneRequest(req *ChatRequest) *ChatRequest {
	cp := *req
	cp.Messages = append([]Message(nil), req.Messages...)
	cp.Tools = append([]Tool(nil), req.Tools...)
	return &cp
}
