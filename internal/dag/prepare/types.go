package prepare

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type NodeRuntimePreparer interface {
	Prepare(ctx context.Context, node *types.AnalysisNode) error
}

// type NoopNodeRuntimePreparer struct{}

// func (NoopNodeRuntimePreparer) Prepare(context.Context, *types.AnalysisNode) error {
// 	return nil
// }
