package executor

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
)

type LocalExecutor struct{}

func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

func (e *LocalExecutor) Execute(ctx context.Context, node *types.AnalysisNode) (*Result, error) {
	// Keep local executor deterministic and lightweight for now.
	select {
	case <-ctx.Done():
		return &Result{Status: "failed", ExitCode: 1, ErrorMessage: ctx.Err().Error()}, ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}

	outputs := map[string]any{}
	if node.ResolvedOutputs != nil {
		outputs = map[string]any(node.ResolvedOutputs)
	}

	return &Result{
		Status:          "done",
		ResolvedOutputs: outputs,
		ExitCode:        0,
	}, nil
}
