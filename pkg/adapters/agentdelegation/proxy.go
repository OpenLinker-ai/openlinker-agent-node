package agentdelegation

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
)

// Proxy only transports MCP. The socket is supplied by the trusted parent,
// never by model tool arguments; no platform URL or token is accepted.
func Proxy(ctx context.Context, input io.Reader, output io.Writer, socket string) error {
	if !filepath.IsAbs(socket) {
		return errors.New("delegation proxy requires an absolute private socket path")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return errors.New("connect to delegation broker")
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	completed := make(chan error, 1)
	go func() {
		_, err := io.Copy(connection, input)
		if unix, ok := connection.(*net.UnixConn); ok {
			unix.CloseWrite()
		}
		if err != nil {
			connection.Close()
		}
	}()
	go func() { _, err := io.Copy(output, connection); completed <- err }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-completed:
		if err != nil {
			return errors.New("delegation proxy transport failed")
		}
		return nil
	}
}
