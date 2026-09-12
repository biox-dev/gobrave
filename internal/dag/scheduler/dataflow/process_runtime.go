package dataflow

import (
	"context"
	"sync"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// DataflowProcessRuntime is the bridge from operator decisions to concrete process launch.
type DataflowProcessRuntime interface {
	SubmitProcessInstance(ctx context.Context, req DataflowProcessRunRequest) error
}

type DataflowProcessRunRequest struct {
	AnalysisID int64
	NodeID     string
	Inputs     map[string]any
	Reason     string
}

type loggingDataflowRuntime struct {
	onSubmitted func(ctx context.Context, req DataflowProcessRunRequest) error
}

func (r *loggingDataflowRuntime) SubmitProcessInstance(ctx context.Context, req DataflowProcessRunRequest) error {
	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] submit process instance, analysis_id=%d node_id=%s reason=%s inputs=%d",
		req.AnalysisID,
		req.NodeID,
		req.Reason,
		len(req.Inputs),
	)
	if r.onSubmitted != nil {
		return r.onSubmitted(ctx, req)
	}
	return nil
}

type persistentDataflowRuntime struct {
	repo               interfaces.AnalysisRepository
	dispatcher         *dagruntime.NodeDispatcher
	projectID          int64
	workflowRepo       interfaces.WorkflowRepository
	dispatchFn         func(ctx context.Context, analysisNodeID int64) error
	onNodeSubmitChange func(nodeID string, delta int)
	onInstanceReused   func(ctx context.Context, node *types.AnalysisNode) error
	buildPersistParams func(req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool)
	// cachePolicies + fingerprinter drive the per-node reuse decision: what a cache
	// type means and the artifacts that decide it are both shared with V2.
	cachePolicies      *dagruntime.CachePolicyRegistry
	fingerprinter      dagruntime.NodeArtifactFingerprinter
	mu                 sync.Mutex
	inflightDispatches int
	inflightNodeIDs    map[int64]struct{}
}
