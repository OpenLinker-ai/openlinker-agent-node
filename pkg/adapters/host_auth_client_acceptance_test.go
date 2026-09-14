package adapters

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Real clients and synthetic model/auth prove admission before client startup.
// This does not reproduce OAuth rotation or coordinate external desktop clients.
func TestNativeHostAuthSerializesInstalledClients(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			bin := os.Getenv("OPENLINKER_TEST_NATIVE_" + strings.ToUpper(provider) + "_BIN")
			if bin == "" {
				t.Skip("set installed official client paths")
			}
			c := isolationConfig(t, provider)
			c.Bin, c.Timeout = bin, 20*time.Second
			home := t.TempDir()
			conf := filepath.Join(home, "."+provider)
			if err := os.Mkdir(conf, 0700); err != nil {
				t.Fatal(err)
			}
			entered, releaseFirst := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
			defer release()
			var calls, active, peak atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || strings.Contains(r.URL.Path, "count_tokens") {
					fmt.Fprint(w, `{"models":[],"input_tokens":20}`)
					return
				}
				n := calls.Add(1)
				current := active.Add(1)
				defer active.Add(-1)
				for prev := peak.Load(); current > prev && !peak.CompareAndSwap(prev, current); prev = peak.Load() {
				}
				if n == 1 {
					close(entered)
					select {
					case <-releaseFirst:
					case <-r.Context().Done():
						return
					}
				}
				if provider == "claude" {
					claudeFixtureEvents(w, int(n), map[string]any{"type": "text", "text": "fixture complete"}, false)
					return
				}
				item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "fixture complete", "annotations": []any{}}}}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []struct {
					kind string
					data any
				}{
					{"response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("r", n)}}},
					{"response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}},
					{"response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("r", n), "status": "completed", "output": []any{item}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 10, "total_tokens": 30}}}},
				} {
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.kind, jsonObject(event.data))
				}
			}))
			defer server.Close()
			// Release blocked HTTP handlers before server.Close on test failure.
			defer release()
			c.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "NO_PROXY=127.0.0.1,localhost,::1", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY="}
			write := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if provider == "codex" {
				c.Model = "gpt-5.4"
				c.Env = append(c.Env, "CODEX_HOME="+conf)
				write(filepath.Join(conf, "auth.json"), `{"OPENAI_API_KEY":"synthetic-auth-admission"}`)
				write(filepath.Join(conf, "config.toml"), "model_provider=\"fixture\"\n[model_providers.fixture]\nname=\"fixture\"\nbase_url="+jsonString(server.URL)+"\nwire_api=\"responses\"\nrequires_openai_auth=true\nsupports_websockets=false\n")
			} else {
				c.Model = "claude-sonnet-4-6"
				c.Env = append(c.Env, "CLAUDE_CONFIG_DIR="+conf, "ANTHROPIC_BASE_URL="+server.URL, "ANTHROPIC_API_KEY=synthetic-auth-admission")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := func(ctx context.Context, config ProviderConfig, key string, waited chan<- struct{}) <-chan error {
				done := make(chan error, 1)
				go func() {
					p, err := NewProvider(config)
					if err != nil {
						done <- err
						return
					}
					run := isolationRun(key)
					if waited != nil {
						run.Emit = func(_ string, payload any) error {
							if strings.Contains(jsonObject(payload), "host-auth serial policy") {
								waited <- struct{}{}
							}
							return nil
						}
					}
					_, err = p.Run(ctx, run)
					done <- err
				}()
				return done
			}
			adone := start(ctx, c, "a", nil)
			select {
			case <-entered:
			case err := <-adone:
				t.Fatal("first client did not call model:", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Another adapter/Node root still shares the provider admission group.
			other := c
			other.SessionIsolation.Root = filepath.Join(t.TempDir(), "another-node")
			bctx, stopB := context.WithCancel(ctx)
			defer stopB()
			waiting := make(chan struct{}, 1)
			bdone := start(bctx, other, "b", waiting)
			select {
			case <-waiting:
			case err := <-bdone:
				t.Fatal("second client did not wait:", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if calls.Load() != 1 {
				t.Fatal("waiting client reached model API")
			}
			stopB()
			if err := <-bdone; err == nil {
				t.Fatal("waiting cancellation succeeded unexpectedly")
			}
			select {
			case err := <-adone:
				t.Fatal("canceling waiter stopped active client:", err)
			default:
			}
			waiting = make(chan struct{}, 1)
			cdone := start(ctx, other, "c", waiting)
			select {
			case <-waiting:
			case err := <-cdone:
				t.Fatal("third client did not wait:", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			release()
			if err := <-adone; err != nil {
				t.Fatal(err)
			}
			if err := <-cdone; err != nil {
				t.Fatal(err)
			}
			if peak.Load() != 1 || calls.Load() != 2 {
				t.Fatalf("overlap or missing call: peak=%d calls=%d", peak.Load(), calls.Load())
			}
			t.Log("real clients serialized across different Node session roots; waiter canceled independently; next client admitted after completion")
		})
	}
}
