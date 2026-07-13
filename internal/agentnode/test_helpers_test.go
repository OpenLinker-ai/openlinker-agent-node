package agentnode

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	httpOK           = http.StatusOK
	testTimeout      = 3 * time.Second
	codexTestTimeout = 10 * time.Second
)

type testRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

func testJSONServer(t *testing.T, handler func(testRequest) (int, any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		status, response := handler(testRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header,
			Body:   body,
		})
		writeJSON(w, status, response)
	}))
}

func eventuallyForTest(t *testing.T, timeout time.Duration, condition func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}
