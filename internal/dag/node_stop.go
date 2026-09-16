package dag

import (
	"context"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// ReasonStoppedByUser is the standard stop reason written to node error_message,
// shared so every scheduler reports the same text for a user-requested stop.
const ReasonStoppedByUser = "dag stopped by user"

// defaultNodeStopTimeout bounds one stop sweep so a wedged container runtime cannot
// block the caller, which is normally in the middle of writing the terminal status.
const defaultNodeStopTimeout = 30 * time.Second

// NodeStopSweeper owns "stop the nodes of one analysis", the single implementation
// shared by every scheduler (legacy dag, dynamic, dataflow).
//
// Before this type existed each orchestrator carried its own copy of the stop
// sequence, and they had already drifted apart (one wrote failed, another stopped,
// and none of them stopped the containers). Keeping one implementation means the
// meaning of "a stopped analysis" cannot depend on which scheduler happened to run.
//
// It is stateless apart from its timeout, so one instance can be shared across
// concurrent runs of different analyses.
type NodeStopSweeper struct {
	repo       interfaces.AnalysisRepository
	dispatcher *NodeDispatcher
	// timeout limits a single sweep. It is a field instead of a constant so tests
	// (and a future config knob) can shorten it.
	timeout time.Duration
}

// NewNodeStopSweeper builds the shared stop helper. A nil dispatcher is tolerated:
// the sweep then only latches node state instead of failing every call.
func NewNodeStopSweeper(repo interfaces.AnalysisRepository, dispatcher *NodeDispatcher) *NodeStopSweeper {
	return &NodeStopSweeper{repo: repo, dispatcher: dispatcher, timeout: defaultNodeStopTimeout}
}

// StopActiveNodes stops every node of the analysis that is still in flight: it first
// persists "stopping" (the stop latch), then asks the executor to stop the real
// process or container behind the node.
//
// The ordering matters. Persisting "stopping" first is what makes the stop convergent
// even if something is still dispatching: the claim query only ever selects ready
// rows, and a dispatch that already claimed the node fails the running transition
// instead of creating a container. Only then is it meaningful to stop containers,
// because from here on nothing new can appear behind our back.
//
// Only running/submitted nodes are handled: ready/pending nodes never reached an
// executor, so there is nothing to stop and they are left to MarkNodesStopped.
// A single failing node is logged, never propagated: the remaining nodes still have
// containers that must be told to stop.
func (s *NodeStopSweeper) StopActiveNodes(ctx context.Context, analysisID int64, reason string) {
	if s == nil || s.repo == nil || analysisID <= 0 {
		return
	}
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = ReasonStoppedByUser
	}

	sweepCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	nodes, err := s.repo.ListAnalysisNodesByAnalysisID(sweepCtx, analysisID)
	if err != nil {
		logger.Warnf(sweepCtx, "[NodeStopSweeper] list nodes for stop failed, analysis_id=%d err=%v", analysisID, err)
		return
	}
	for _, node := range nodes {
		if node == nil || strings.TrimSpace(node.AnalysisNodeID) == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(node.Status))
		if status != StatusRunning && status != StatusSubmitted {
			continue
		}
		if err := s.repo.UpdateAnalysisNodeByAnalysisNodeID(sweepCtx, node.AnalysisNodeID, map[string]any{
			"status":        StatusStopping,
			"server_status": "stopping",
			"error_message": reason,
			"updated_at":    time.Now().UTC(),
		}); err != nil {
			logger.Warnf(sweepCtx, "[NodeStopSweeper] mark node stopping failed, analysis_id=%d node_id=%s err=%v", analysisID, node.NodeID, err)
			continue
		}
		if s.dispatcher == nil {
			continue
		}
		// Stop only needs the persisted node id (the container executor maps it to
		// containerMgr.StopByOwner), and the node is already latched as stopping, so a
		// failure here degrades to "MarkNodesStopped will terminalize it".
		if _, err := s.dispatcher.Stop(sweepCtx, node); err != nil {
			logger.Warnf(sweepCtx, "[NodeStopSweeper] stop node failed, analysis_id=%d node_id=%s err=%v", analysisID, node.NodeID, err)
		}
	}
}

// MarkNodesStopped terminalizes every non-terminal node of the analysis.
//
// It is the mandatory last step of a stop: the analysis is about to be written as a
// terminal stopped state, and recovery only scans running/stopping analyses, so a node
// left in "stopping" would never be looked at again. Without this the node would wait
// forever for a container stop event that may never arrive.
func (s *NodeStopSweeper) MarkNodesStopped(ctx context.Context, analysisID int64, reason string) error {
	if s == nil || s.repo == nil || analysisID <= 0 {
		return nil
	}
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = ReasonStoppedByUser
	}

	nodes, err := s.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, node := range nodes {
		if node == nil || strings.TrimSpace(node.AnalysisNodeID) == "" {
			continue
		}
		if IsTerminalStatus(node.Status) {
			continue
		}
		if err := s.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
			"status":        StatusStopped,
			"server_status": "stopped",
			"error_message": reason,
			"finished_at":   now,
		}); err != nil {
			return err
		}
	}
	return nil
}
