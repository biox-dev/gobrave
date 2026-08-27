package executor

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type NextflowExecutor struct {
	// fallback Executor
}

func NewNextflowExecutor() *NextflowExecutor {
	return &NextflowExecutor{}
}

func (e *NextflowExecutor) Execute(ctx context.Context, node *types.AnalysisNode) (*Result, error) {
	// Implement Nextflow execution logic here
	return nil, nil
}
