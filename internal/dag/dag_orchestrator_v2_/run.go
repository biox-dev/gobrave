package orchestratorv2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// errRunStopped marks a loop exit caused by a stop request rather than by a
// scheduling failure. The supervisor maps it to the "stopped" status.
var errRunStopped = errors.New("dag run stopped")

// runExecution is the mutable state of one scheduling loop.
type runExecution struct {
	analysis   *types.Analysis
	graph      *Graph
	tracker    *DependencyTracker
	reconciler *Reconciler
}

// execute runs the full scheduling loop for one analysis and returns when the
// run either completed, failed or was asked to stop.
//
// Loop shape:
//
//	prepare  -> reset cached rows, persist edges, seed dependency state
//	schedule -> materialize ready templates, claim + dispatch ready nodes
//	wait     -> react to runtime events, stop flags and the watchdog ticker
func (o *Orchestrator) execute(ctx context.Context, analysisID int64, graph *Graph) error {
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return fmt.Errorf("load analysis failed: %w", err)
	}
	if analysis == nil {
		return fmt.Errorf("analysis %d not found", analysisID)
	}

	if err := o.resetForRerun(ctx, analysis); err != nil {
		return err
	}
	if err := o.persistEdges(ctx, graph); err != nil {
		return err
	}

	exec, err := o.newRunExecution(ctx, analysis, graph)
	if err != nil {
		return err
	}

	events, unsubscribe := o.events.Subscribe(analysis.ID, o.opts.EventBuffer)
	defer unsubscribe()

	runtime := dagruntime.NewRuntimeEngine(o.repo)
	pool := dagruntime.NewWorkerPool(o.dispatcher, o.opts.Workers, o.opts.ReadyQueueSize)
	pool.Start(ctx)
	defer pool.Stop()

	if err := exec.reconciler.Reconcile(ctx, exec.tracker.Runnable()); err != nil {
		return err
	}
	if err := o.dispatchReady(ctx, runtime, pool, analysis.ID); err != nil {
		return err
	}

	stopTicker := time.NewTicker(o.opts.StopCheckInterval)
	defer stopTicker.Stop()
	watchdog := time.NewTicker(o.opts.WatchdogInterval)
	defer watchdog.Stop()

	for {
		done, err := o.isRunComplete(ctx, exec, runtime, pool)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		select {
		case <-ctx.Done():
			// Cancellation always means "stop asked", never "run finished".
			return errRunStopped
		case <-stopTicker.C:
			if stopping, err := o.stopRequested(ctx, analysis.ID); err == nil && stopping {
				return errRunStopped
			}
		case <-watchdog.C:
			// Safety net: a dropped event can never stall the run.
			if err := exec.reconciler.Reconcile(ctx, exec.tracker.Runnable()); err != nil {
				return err
			}
			if err := o.dispatchReady(ctx, runtime, pool, analysis.ID); err != nil {
				return err
			}
		case evt, ok := <-events:
			if !ok {
				return errRunStopped
			}
			if err := o.consumeEvent(ctx, exec, evt); err != nil {
				return err
			}
			if err := o.dispatchReady(ctx, runtime, pool, analysis.ID); err != nil {
				return err
			}
		}
	}
}

// newRunExecution builds the per-run collaborators and seeds their state.
func (o *Orchestrator) newRunExecution(ctx context.Context, analysis *types.Analysis, graph *Graph) (*runExecution, error) {
	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysis.ID)
	if err != nil {
		return nil, fmt.Errorf("list analysis nodes failed: %w", err)
	}

	existing := indexNodesByNodeID(nodes)
	tracker := NewDependencyTracker(graph)
	tracker.Seed(existing)

	preparer := prepare.NewFileSystemNodeRuntimePreparer(
		o.repo,
		o.workflowRepo,
		o.projectRepo,
		o.workflowService,
		o.cfg,
		o.runScriptBuilders,
	)

	return &runExecution{
		analysis: analysis,
		graph:    graph,
		tracker:  tracker,
		reconciler: NewReconciler(ReconcilerDeps{
			Analyses:      o.repo,
			Workflows:     o.workflowRepo,
			Analysis:      analysis,
			Graph:         graph,
			Tracker:       tracker,
			Builder:       NewNodeBuilder(),
			Fingerprinter: NewPreparerFingerprinter(preparer),
			Policies:      o.opts.CachePolicies,
			Existing:      existing,
		}),
	}, nil
}

// consumeEvent advances the dependency tracker and re-materializes the
// templates affected by the runtime event.
func (o *Orchestrator) consumeEvent(ctx context.Context, exec *runExecution, evt dagruntime.RuntimeEvent) error {
	nodeID := strings.TrimSpace(evt.NodeID)
	if nodeID == "" {
		return nil
	}

	switch strings.TrimSpace(evt.Name) {
	case dagruntime.EventNodeCompleted:
		candidates := exec.tracker.OnSuccess(nodeID)
		exec.reconciler.Reopen(candidates)
		return exec.reconciler.Reconcile(ctx, candidates)

	case dagruntime.EventNodeFailed:
		if node, ok := exec.reconciler.Known(nodeID); ok && o.opts.Retry.ShouldRetry(node) {
			if err := exec.reconciler.ScheduleRetry(ctx, node); err != nil {
				logger.Warnf(ctx, "[DagOrchestratorV2] schedule retry failed, analysis_id=%d node_id=%s err=%v", evt.AnalysisID, nodeID, err)
			} else {
				logger.Infof(ctx, "[DagOrchestratorV2] retrying node, analysis_id=%d node_id=%s attempt=%d/%d",
					evt.AnalysisID, nodeID, node.Retry, node.MaxRetry)
				return nil
			}
		}
		return exec.reconciler.Reconcile(ctx, exec.tracker.OnFailure(nodeID))

	default:
		return nil
	}
}

// dispatchReady claims ready nodes from the database and hands them to the pool.
func (o *Orchestrator) dispatchReady(
	ctx context.Context,
	runtime *dagruntime.RuntimeEngine,
	pool *dagruntime.WorkerPool,
	analysisID int64,
) error {
	// Workers only ever drain the pool queue, so enqueuing at most the current
	// free capacity guarantees every Enqueue below succeeds.
	slots := o.opts.ReadyQueueSize - pool.QueueLen()
	for index := 0; index < slots; index++ {
		node, err := runtime.ClaimNextReadyNode(ctx, analysisID)
		if err != nil {
			return fmt.Errorf("claim ready node failed: %w", err)
		}
		if node == nil {
			return nil
		}
		if !pool.Enqueue(node.ID) {
			logger.Warnf(ctx, "[DagOrchestratorV2] worker queue rejected node, analysis_id=%d node_id=%s", analysisID, node.NodeID)
			return nil
		}
		o.publisher.PublishNode(dagruntime.EventNodeSubmitted, analysisID, node.NodeID, node.ID)
	}
	return nil
}

// isRunComplete reports whether no further work can be produced for this run.
func (o *Orchestrator) isRunComplete(
	ctx context.Context,
	exec *runExecution,
	runtime *dagruntime.RuntimeEngine,
	pool *dagruntime.WorkerPool,
) (bool, error) {
	if !exec.reconciler.AllMaterialized() {
		return false, nil
	}

	snapshot, err := runtime.GetSnapshot(ctx, exec.analysis.ID)
	if err != nil {
		return false, fmt.Errorf("read runtime snapshot failed: %w", err)
	}
	if !snapshot.IsFinished {
		return false, nil
	}
	if pool.QueueLen() > 0 {
		return false, nil
	}
	if failed := snapshot.StatusCount[dagruntime.StatusFailed]; failed > 0 {
		return true, fmt.Errorf("%d dynamic dag node(s) failed", failed)
	}
	return true, nil
}

// resetForRerun clears persisted runtime rows when the analysis disables cache.
func (o *Orchestrator) resetForRerun(ctx context.Context, analysis *types.Analysis) error {
	if analysis.CacheType != types.CacheTypeRerunAll {
		return nil
	}
	err := o.repo.WithTransaction(ctx, func(tx interfaces.AnalysisRepository) error {
		if err := tx.DeleteAnalysisNodesByAnalysisID(ctx, analysis.ID); err != nil {
			return err
		}
		return tx.DeleteAnalysisEdgesByAnalysisID(ctx, analysis.ID)
	})
	if err != nil {
		return fmt.Errorf("reset cached analysis rows failed: %w", err)
	}
	return nil
}

// persistEdges replaces the persisted edge set with the compiled topology.
func (o *Orchestrator) persistEdges(ctx context.Context, graph *Graph) error {
	if err := o.repo.DeleteAnalysisEdgesByAnalysisID(ctx, graph.AnalysisID); err != nil {
		return fmt.Errorf("delete analysis edges failed: %w", err)
	}
	if err := o.repo.CreateAnalysisEdges(ctx, graph.AnalysisEdges()); err != nil {
		return fmt.Errorf("create analysis edges failed: %w", err)
	}
	return nil
}

// stopRequested checks the persisted stop flags set by the control API.
func (o *Orchestrator) stopRequested(ctx context.Context, analysisID int64) (bool, error) {
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return false, err
	}
	if analysis == nil {
		return false, nil
	}
	status := normaliseStatus(analysis.JobStatus)
	return status == types.AnalysisStatusStopping || status == types.AnalysisStatusStopped, nil
}
