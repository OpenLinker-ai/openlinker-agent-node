// Package provideroutput contains bounded provider output mechanisms without
// provider execution or product-specific diagnostic policy.
package provideroutput

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

// MaxBytes bounds a single retained provider output buffer.
const MaxBytes = 4 << 20

// Diagnostics must not kill a long-running provider just because it is verbose.
// Retain only the latest bytes; the model-facing error is separately redacted.
const MaxDiagnosticBytes = 64 << 10

// Tail retains the latest diagnostic bytes without stopping a verbose process.
// Its zero value is ready for use; callers own model-facing error redaction.
type Tail struct{ data []byte }

func (tail *Tail) Write(value []byte) (int, error) {
	n := len(value)
	if n >= MaxDiagnosticBytes {
		tail.data = append(tail.data[:0], value[n-MaxDiagnosticBytes:]...)
	} else {
		if excess := len(tail.data) + n - MaxDiagnosticBytes; excess > 0 {
			copy(tail.data, tail.data[excess:])
			tail.data = tail.data[:len(tail.data)-excess]
		}
		tail.data = append(tail.data, value...)
	}
	return n, nil
}

func (tail *Tail) String() string { return string(tail.data) }

// ErrTooLarge reports a write exceeding a LimitedBuffer's byte limit.
var ErrTooLarge = errors.New("provider output exceeded limit")

// LimitedBuffer retains at most MaxBytes and cancels its process on overflow.
// Use NewLimitedBuffer to attach cancellation; its zero value has no callback.
type LimitedBuffer struct {
	buf      bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func NewLimitedBuffer(cancel context.CancelFunc) *LimitedBuffer {
	return &LimitedBuffer{cancel: cancel}
}

func (buffer *LimitedBuffer) Write(value []byte) (int, error) {
	remaining := MaxBytes - buffer.buf.Len()
	if remaining > 0 {
		if len(value) <= remaining {
			_, _ = buffer.buf.Write(value)
		} else {
			_, _ = buffer.buf.Write(value[:remaining])
		}
	}
	if len(value) > remaining {
		buffer.exceeded = true
		if buffer.cancel != nil {
			buffer.cancel()
		}
		return 0, ErrTooLarge
	}
	return len(value), nil
}

func (buffer *LimitedBuffer) String() string { return buffer.buf.String() }

// LimitError reports overflow without including any retained output.
func LimitError(label string, buffers ...*LimitedBuffer) error {
	for _, buffer := range buffers {
		if buffer != nil && buffer.exceeded {
			return fmt.Errorf("%s output exceeded %d bytes", label, MaxBytes)
		}
	}
	return nil
}

// BoundedText trims value, substitutes fallback if empty, and truncates by rune
// count. The caller supplies a non-negative maximum and a safe fallback.
func BoundedText(value string, maximum int, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum])
	}
	return value
}
