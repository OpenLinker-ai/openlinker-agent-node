package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerstream"
)

// Retain one bounded JSONL record and the final result, never the transcript.
// exec.Cmd calls Write serially and Result is called only after Cmd.Run returns.
type claudeResultStream struct {
	pending     []byte
	result      claudeResponse
	found       bool
	err         error
	cancel      context.CancelFunc
	observer    *providerstream.Observer
	authFailure string
	authRetries int
	mu          sync.Mutex
	started     bool
	silent      bool
	stopped     bool
	timers      []*time.Timer
}

type claudeStreamSignal struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Error   json.RawMessage `json:"error"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

var claudeAuthFailureTexts = []string{
	"failed to authenticate",
	"authentication_failed",
	"authentication_error",
	"oauth session expired",
	"oauth token has expired",
	"invalid api key",
	"please run /login",
	"not logged in",
}

func claudeAuthFailureText(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range claudeAuthFailureTexts {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (signal claudeStreamSignal) errorKind() string {
	var kind string
	if json.Unmarshal(signal.Error, &kind) != nil {
		return ""
	}
	return kind
}

func (signal claudeStreamSignal) text() string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(signal.Message.Content, &blocks) != nil {
		return ""
	}
	parts := []string{}
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func newClaudeResultStream(cancel context.CancelFunc, observer *providerstream.Observer) *claudeResultStream {
	return &claudeResultStream{cancel: cancel, observer: observer}
}

func (s *claudeResultStream) fail(err error) error {
	s.err = err
	s.pending = nil
	if s.cancel != nil {
		s.cancel()
	}
	return err
}

func (s *claudeResultStream) watchStartup(deadline time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timers = append(s.timers, time.AfterFunc(deadline, func() {
		s.mu.Lock()
		expired := !s.started && !s.stopped
		s.silent = s.silent || expired
		s.mu.Unlock()
		if expired && s.cancel != nil {
			s.cancel()
		}
	}))
}

func (s *claudeResultStream) stopAfter(grace time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.cancel == nil {
		return
	}
	cancel := s.cancel
	s.timers = append(s.timers, time.AfterFunc(grace, func() {
		s.mu.Lock()
		active := !s.stopped
		s.mu.Unlock()
		if active {
			cancel()
		}
	}))
}

func (s *claudeResultStream) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	for _, timer := range s.timers {
		timer.Stop()
	}
}

func (s *claudeResultStream) silentStartup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.silent
}

func (s *claudeResultStream) failAuthentication(message string, grace time.Duration) {
	if s.authFailure == "" {
		s.authFailure = strings.TrimSpace(message)
	}
	s.stopAfter(grace)
}

func (s *claudeResultStream) Write(p []byte) (int, error) {
	n := len(p)
	if n > 0 {
		s.mu.Lock()
		s.started = true
		s.mu.Unlock()
	}
	if s.err != nil {
		return 0, s.err
	}
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		chunk := p
		if end >= 0 {
			chunk = p[:end]
		}
		if len(s.pending)+len(chunk) > maxProviderOutputBytes {
			return 0, s.fail(errors.New("Claude JSONL record exceeds 4 MiB limit"))
		}
		s.pending = append(s.pending, chunk...)
		if end < 0 {
			break
		}
		if err := s.consume(); err != nil {
			return 0, err
		}
		p = p[end+1:]
	}
	return n, nil
}

func (s *claudeResultStream) consume() error {
	line := bytes.TrimSpace(s.pending)
	if len(line) > 0 {
		var event claudeResponse
		if json.Unmarshal(line, &event) != nil {
			return s.fail(errors.New("Claude returned invalid JSONL output"))
		}
		if event.Type == "result" {
			s.result, s.found = event, true
			if failure := event.failureMessage(); failure != "" {
				if claudeAuthFailureText(failure) {
					s.failAuthentication(event.Result, claudeTerminalErrorGrace)
				}
				s.stopAfter(claudeTerminalErrorGrace)
			}
		} else {
			if s.observer != nil {
				s.observer.ObserveLine(line)
			}
			s.observeSignal(line)
		}
	}
	s.pending = s.pending[:0]
	return nil
}

func (s *claudeResultStream) observeSignal(line []byte) {
	var signal claudeStreamSignal
	if json.Unmarshal(line, &signal) != nil || signal.errorKind() != "authentication_failed" {
		return
	}
	switch {
	case signal.Type == "assistant":
		message := signal.text()
		if message == "" {
			message = "authentication_failed"
		}
		s.failAuthentication(message, claudeTerminalErrorGrace)
	case signal.Type == "system" && signal.Subtype == "api_retry":
		s.authRetries++
		if s.authRetries >= 2 {
			s.failAuthentication("Claude API authentication kept failing across retries (authentication_failed)", 0)
		}
	}
}

func (s *claudeResultStream) Result() (claudeResponse, error) {
	if s.err == nil {
		_ = s.consume()
	}
	if s.err != nil {
		return s.result, s.err
	}
	if !s.found {
		return s.result, errors.New("Claude completed without a result event")
	}
	return s.result, nil
}
