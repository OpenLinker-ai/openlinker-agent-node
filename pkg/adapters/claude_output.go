package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerstream"
)

// Retain one bounded JSONL record and the final result, never the transcript.
// exec.Cmd calls Write serially and Result is called only after Cmd.Run returns.
type claudeResultStream struct {
	pending  []byte
	result   claudeResponse
	found    bool
	err      error
	cancel   context.CancelFunc
	observer *providerstream.Observer
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

func (s *claudeResultStream) Write(p []byte) (int, error) {
	n := len(p)
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
		} else if s.observer != nil {
			s.observer.ObserveLine(line)
		}
	}
	s.pending = s.pending[:0]
	return nil
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
