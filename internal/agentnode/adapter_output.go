package agentnode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxAdapterOutputBytes = 4 << 20

var errAdapterOutputTooLarge = errors.New("adapter output exceeded limit")

type limitedOutputBuffer struct {
	buf      bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func newLimitedOutputBuffer(cancel context.CancelFunc) *limitedOutputBuffer {
	return &limitedOutputBuffer{cancel: cancel}
}

func (w *limitedOutputBuffer) Write(p []byte) (int, error) {
	remaining := maxAdapterOutputBytes - w.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = w.buf.Write(p)
		} else {
			_, _ = w.buf.Write(p[:remaining])
		}
	}
	if len(p) > remaining {
		w.exceeded = true
		if w.cancel != nil {
			w.cancel()
		}
		return 0, errAdapterOutputTooLarge
	}
	return len(p), nil
}

func (w *limitedOutputBuffer) String() string {
	return w.buf.String()
}

func adapterOutputLimitError(label string, stdout, stderr *limitedOutputBuffer) error {
	var streams []string
	if stdout != nil && stdout.exceeded {
		streams = append(streams, "stdout")
	}
	if stderr != nil && stderr.exceeded {
		streams = append(streams, "stderr")
	}
	if len(streams) == 0 {
		return nil
	}
	return fmt.Errorf("%s output exceeded %d bytes (%s)", label, maxAdapterOutputBytes, strings.Join(streams, ", "))
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	// #nosec G304 -- caller supplies a generated adapter output path and this reader enforces a byte limit.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errAdapterOutputTooLarge
	}
	return data, nil
}
