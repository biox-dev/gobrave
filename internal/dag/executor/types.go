package executor

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type Action string

const (
	ActionRun  Action = "run"
	ActionStop Action = "stop"
)

type actionContextKey struct{}

type Result struct {
	Status          string
	ResolvedOutputs map[string]any
	ExitCode        int
	ErrorMessage    string
	Deferred        bool
}

type Executor interface {
	Execute(ctx context.Context, node *types.AnalysisNode) (*Result, error)
}

func WithAction(ctx context.Context, action Action) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if action == "" {
		action = ActionRun
	}
	return context.WithValue(ctx, actionContextKey{}, action)
}

func ActionFromContext(ctx context.Context) Action {
	if ctx == nil {
		return ActionRun
	}
	v := ctx.Value(actionContextKey{})
	a, ok := v.(Action)
	if !ok || a == "" {
		return ActionRun
	}
	return a
}
