package prepare

import (
	"context"

	"github.com/gobravedev/gobrave/internal/types"
)

type NodeRuntimePreparer interface {
	Prepare(ctx context.Context, node *types.AnalysisNode) error
}

// type NoopNodeRuntimePreparer struct{}

// func (NoopNodeRuntimePreparer) Prepare(context.Context, *types.AnalysisNode) error {
// 	return nil
// }
