package agentnode

import "context"

// Test seams reuse the production configuration and runtime-handler paths.
type Env map[string]string

func NewFromEnvMap(env Env) (*Node, error) {
	return NewFromLookup(func(key string) string {
		return env[key]
	})
}

type AdapterFunc func(ctx context.Context, input any, runCtx RunContext) (any, error)

func (f AdapterFunc) Run(ctx context.Context, input any, runCtx RunContext) (any, error) {
	return f(ctx, input, runCtx)
}
