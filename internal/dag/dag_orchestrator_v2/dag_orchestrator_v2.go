package orchestratorv2

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/compiler"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
)

const (
	// dynamicV2LeaseTTL is the staleness window for the DB running lease.
	// If updated_at is older than now-TTL, a new scheduler instance may take over.
	dynamicV2LeaseTTL = 90 * time.Second
	// dynamicV2Heartbeat is the interval for lease renewal while a run is active.
	dynamicV2Heartbeat = 15 * time.Second
	// dynamicV2DispatchQueueSize bounds both the worker pool queue and how many
	// nodes a single dispatch round may claim ahead of the workers.
	dynamicV2DispatchQueueSize = 64
	// dynamicV2StopCheckInterval is a lightweight stop-flag probe cadence.
	dynamicV2StopCheckInterval = 1 * time.Second
	// dynamicV2WatchdogInterval is a safety net in case runtime events are dropped.
	dynamicV2WatchdogInterval = 5 * time.Second

	// Analysis job statuses are shared with the other schedulers through types, so
	// the unified recovery entry can reason about running/stopping uniformly.
	// dynamicV2StatusStopping doubles as the cross-instance stop signal when
	// another instance owns the run.
	dynamicV2StatusStopping = types.AnalysisStatusStopping
	dynamicV2StatusStopped  = types.AnalysisStatusStopped
	dynamicV2StatusFinished = types.AnalysisStatusFinished
	dynamicV2StatusFailed   = types.AnalysisStatusFailed
	// dynamicV2StatusRunning is the job status while this scheduler owns the run.
	dynamicV2StatusRunning = types.AnalysisStatusRunning
)

// dynamicDagOrchestratorV2 provides a Nextflow-like dynamic materialization path
// on top of existing analysis/analysis_node tables.
//
// Design goals:
// 1) Keep existing JSON dag_definition contract.
// 2) Avoid changing legacy scheduler path.
// 3) Materialize analysis_node instances lazily when upstream data is ready.
// 4) Reuse existing RuntimeEngine + Dispatcher + Executors for long-term stability.
type dynamicDagOrchestratorV2 struct {
	// repo is the main persistence boundary for analysis, nodes, and edges.
	repo interfaces.AnalysisRepository
	// workflowRepo is used by runtime preparer/dispatcher for script/runtime metadata.
	workflowRepo interfaces.WorkflowRepository
	// workflowService rebuilds the DAG definition during recovery, using the same
	// source as the submit handler so recovered templates match the original run.
	workflowService interfaces.WorkflowService
	// containerRepo lets recovery tell nodes whose container is still alive (and must
	// therefore not be re-dispatched) apart from nodes abandoned by a crashed process.
	containerRepo interfaces.ContainerRepository
	// dispatcher is the DI-injected node dispatcher that performs actual node execution.
	dispatcher *dagruntime.NodeDispatcher
	// fingerprinter predicts the runtime artifacts (run.sh / params.json) of a
	// persisted node so the md5 cache policies can decide whether it is reusable.
	//
	// The scheduler deliberately does not hold a runtime preparer: preparing a node
	// for execution (including output cleanup) belongs to NodeDispatcher, and sharing
	// that capability here made every node pay for two preparations per run - one to
	// fingerprint, one to execute. Fingerprinting is now limited to the cache probe,
	// which is the only place a digest is actually needed to make a decision.
	fingerprinter NodeArtifactFingerprinter
	// cachePolicies resolves the analysis.cache_type strategy that decides whether
	// a node persisted by a previous run must be rerun. The registry is stateless,
	// so it is built here instead of being injected by the DI container.
	cachePolicies *CachePolicyRegistry
	// bus emits runtime events using the existing event pipeline.
	bus event.Bus

	// registry is the process-wide running registry injected by the container and
	// shared with the other schedulers, so "is this analysis running in this
	// process?" has exactly one answer for duplicate-run checks and stop state.
	registry *dagruntime.RunningRegistry

	// router is the process-wide runtime event subscriber injected by the
	// container. Each run registers its own analysis-scoped sink instead of
	// subscribing to the bus directly, so concurrent runs never add subscribers.
	router *dagruntime.EventRouter

	// projectID int64
	// mu is reserved for future critical sections in V2 orchestration state transitions.
	mu sync.Mutex
}

// NewDynamicDagOrchestratorV2 wires a standalone dynamic scheduler entrypoint.
// It is intentionally separate from the legacy orchestrator to keep rollout safe.
//
// registry and router are process-wide singletons owned by the DI container:
// the router is subscribed to the bus exactly once there (event.Bus has no
// Unsubscribe, so a per-orchestrator subscription would leak a subscriber), and
// the shared registry keeps duplicate-run and stop lookups consistent across
// schedulers.
//
// fingerprinter is the only artifact-producing dependency of the scheduler and is
// used solely to probe cache candidates (see NodeArtifactFingerprinter).
func NewDynamicDagOrchestratorV2(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	workflowService interfaces.WorkflowService,
	containerRepo interfaces.ContainerRepository,
	dispatcher *dagruntime.NodeDispatcher,
	fingerprinter NodeArtifactFingerprinter,
	registry *dagruntime.RunningRegistry,
	router *dagruntime.EventRouter,
	bus event.Bus,
) interfaces.DynamicDagOrchestrator {
	return &dynamicDagOrchestratorV2{
		repo:            repo,
		workflowRepo:    workflowRepo,
		workflowService: workflowService,
		containerRepo:   containerRepo,
		dispatcher:      dispatcher,
		fingerprinter:   fingerprinter,
		cachePolicies:   NewCachePolicyRegistry(),
		bus:             bus,
		registry:        registry,
		router:          router,
	}
}

// StartAsyncV2 starts a dynamic DAG run in background and returns immediately.
//
// High-level flow:
// 1) Validate analysis id and prevent duplicate local starts.
// 2) Acquire DB running lease (cross-instance guard).
// 3) Compile templates from current JSON dag_definition.
// 4) Register running state + heartbeat renewer.
// 5) Spawn run loop goroutine that performs dynamic materialization and dispatch.
func (o *dynamicDagOrchestratorV2) StartAsyncV2(ctx context.Context, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any) error {
	return o.startAsyncV2(ctx, analysisID, parseAnalysisResult, dagDefinition, false)
}

// startAsyncV2 is the single entry shared by fresh submissions (resume=false) and
// crash recovery (resume=true), so both paths keep identical lease and sink semantics.
//
// resume=true means "adopt an analysis that already exists":
//   - prepareAnalysisForCacheRerun is skipped. Otherwise CacheTypeRerunAll would delete
//     every existing node and turn a restart into a full rerun from scratch.
//   - After winning the lease, in-flight nodes abandoned by the crashed process are
//     rolled back so they can be dispatched again.
func (o *dynamicDagOrchestratorV2) startAsyncV2(ctx context.Context, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any, resume bool) error {
	if analysisID <= 0 {
		return fmt.Errorf("analysis_id is required")
	}
	if o.registry.IsRunning(analysisID) {
		// Already running in this process.
		return nil
	}

	now := time.Now().UTC()
	locked, err := o.repo.TryMarkAnalysisRunning(ctx, analysisID, now, now.Add(-dynamicV2LeaseTTL))
	if err != nil {
		return err
	}
	if !locked {
		// Lease is already held by another active scheduler.
		// Returning here is also what protects a live run's node states from being rewritten.
		return nil
	}

	// Register the analysis-scoped sink before the first event is published so no
	// runtime event for this run can be observed without a sink. The filter keeps
	// this loop's own echoes (node.submitted, dag.*) and the node.running noise out
	// of the sink, so it only wakes for events that can advance dependencies.
	sink := o.router.RegisterWithFilter(analysisID, dagruntime.SchedulerEventFilter)

	o.publishDagRuntimeEvent(dagruntime.EventDagStarted, analysisID, nil)

	if resume {
		// In-flight nodes left by the crashed process are neither re-dispatchable nor
		// terminal, so they would deadlock the graph. They must be rolled back first.
		if err := o.prepareNodesForResume(ctx, analysisID); err != nil {
			o.router.Unregister(analysisID)
			_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
				"job_status": dynamicV2StatusFailed,
				"updated_at": time.Now().UTC(),
			})
			o.publishDagRuntimeEvent(dagruntime.EventDagFailed, analysisID, map[string]any{"reason": err.Error()})
			return fmt.Errorf("prepare nodes for resume failed: %w", err)
		}
	} else if err := o.prepareAnalysisForCacheRerun(ctx, analysisID); err != nil {
		o.router.Unregister(analysisID)
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": dynamicV2StatusFailed,
			"updated_at": time.Now().UTC(),
		})
		o.publishDagRuntimeEvent(dagruntime.EventDagFailed, analysisID, map[string]any{"reason": err.Error()})
		return fmt.Errorf("prepare cached analysis rerun failed: %w", err)
	}

	compiled, err := compiler.BuildRuntimeTasks(analysisID, dynamicCloneAnyMap(parseAnalysisResult), dynamicCloneAnyMap(dagDefinition))
	if err != nil {
		// Compile failure is terminal for current submission; mark analysis failed.
		o.router.Unregister(analysisID)
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": dynamicV2StatusFailed,
			"updated_at": time.Now().UTC(),
		})
		o.publishDagRuntimeEvent(dagruntime.EventDagFailed, analysisID, map[string]any{"reason": err.Error()})
		return fmt.Errorf("compile runtime dag for dynamic v2 failed: %w", err)
	}

	nodeTemplates := dynamicToMapSlice(compiled["analysis_nodes"])
	edgeRows := dynamicToMapSlice(compiled["analysis_edges"])

	runCtx, runCancel := context.WithCancel(context.Background())
	o.registry.Register(&dagruntime.RunningEntry{
		AnalysisID:     analysisID,
		TaskName:       "dag-v2-run-" + fmt.Sprint(analysisID),
		MaxConcurrency: 1,
		QueueSize:      64,
		PollIntervalMs: 500,
		Status:         dynamicV2StatusRunning,
		Cancel:         runCancel,
	})

	heartbeatStop := make(chan struct{})
	// Lease renewer runs independently of dispatch loop.
	go o.renewRunningLease(analysisID, heartbeatStop)

	go func() {
		// Ensure heartbeat, sink and context are cleaned up no matter how run exits.
		// The sink is retired last: late events (for example from deferred container
		// completions) are then safely discarded instead of leaking a subscriber.
		defer o.router.Unregister(analysisID)
		defer close(heartbeatStop)
		defer runCancel()
		finalStatus := dynamicV2StatusFinished
		var finalErr error
		if err := o.runDynamicLoop(runCtx, analysisID, sink, nodeTemplates, edgeRows); err != nil {
			finalStatus = dynamicV2StatusFailed
			finalErr = err
			logger.Warnf(context.Background(), "[DynamicDagOrchestratorV2] run failed, analysis_id=%d err=%v", analysisID, err)
		}
		if o.isStopRequested(analysisID) {
			// A stop request wins over the loop result, whether it arrived through the
			// in-process registry (RequestStop) or as a persisted job_status written by
			// another instance that owns this analysis.
			finalStatus = dynamicV2StatusStopped
			finalErr = nil
		}
		o.registry.MarkFinished(analysisID, finalStatus)
		if finalStatus == dynamicV2StatusFinished {
			o.publishDagRuntimeEvent(dagruntime.EventDagCompleted, analysisID, map[string]any{"status": finalStatus})
		} else {
			payload := map[string]any{"status": finalStatus}
			if finalErr != nil {
				payload["reason"] = finalErr.Error()
			}
			if finalStatus == dynamicV2StatusStopped {
				payload["reason"] = "stopped"
			}
			o.publishDagRuntimeEvent(dagruntime.EventDagFailed, analysisID, payload)
		}
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": finalStatus,
			"updated_at": time.Now().UTC(),
		})
	}()

	return nil
}

// runDynamicLoop uses runtime events as the primary driver:
// 1) NodeCompleted/NodeFailed events update dependency state.
// 2) Only affected downstream templates are re-evaluated/materialized.
// 3) Ready nodes are claimed and handed straight to the worker pool for dispatch.
func (o *dynamicDagOrchestratorV2) runDynamicLoop(ctx context.Context, analysisID int64, sink *dagruntime.AnalysisEventSink, nodeTemplates []map[string]any, edgeRows []map[string]any) error {
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return err
	}

	edges := buildAnalysisEdges(analysisID, edgeRows)
	// Replace edges atomically for current run topology snapshot.
	if err := o.repo.DeleteAnalysisEdgesByAnalysisID(ctx, analysisID); err != nil {
		return err
	}
	if err := o.repo.CreateAnalysisEdges(ctx, edges); err != nil {
		return err
	}

	runtime := dagruntime.NewRuntimeEngine(o.repo)

	pool := dagruntime.NewWorkerPool(o.dispatcher, 1, dynamicV2DispatchQueueSize)
	pool.Start(ctx)
	defer pool.Stop()

	// The compiled node list is already in the compiler's topological order, so it
	// doubles as the run schedule: one upstream-first pass over it reconciles the
	// graph, and readiness is derived from persisted state rather than maintained.
	plan := newDynamicExecutionPlan(nodeTemplates, edges)

	// Runtime events reach this loop through the process-wide router via the
	// analysis-scoped sink; no per-run bus subscription is created here.
	if sink == nil {
		// Defensive: without a sink the loop still converges through the watchdog.
		sink = dagruntime.NewAnalysisEventSink()
	}

	if err := o.reconcilePlan(ctx, analysis, plan); err != nil {
		return err
	}
	if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool); err != nil {
		return err
	}

	stopTicker := time.NewTicker(dynamicV2StopCheckInterval)
	defer stopTicker.Stop()
	watchdogTicker := time.NewTicker(dynamicV2WatchdogInterval)
	defer watchdogTicker.Stop()

	for {
		finished, finishedErr := o.checkDynamicCompletion(ctx, analysisID, len(plan.order), runtime, pool)
		if finishedErr != nil {
			return finishedErr
		}
		if finished {
			return nil
		}

		select {
		case <-ctx.Done():
			// Context cancellation is treated as graceful exit; caller sets final status.
			return nil
		case <-stopTicker.C:
			if shouldStop, stopErr := o.shouldStopByJobStatus(ctx, analysisID); stopErr == nil && shouldStop {
				return nil
			}
		case <-watchdogTicker.C:
			if err := o.reconcilePlan(ctx, analysis, plan); err != nil {
				return err
			}
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool); err != nil {
				return err
			}
		case <-sink.Events():
			// The sink is registered with SchedulerEventFilter, so only a node
			// Completed/Failed event ever reaches this arm. Readiness is derived, so the
			// event is purely a wake signal: a full upstream-first pass observes the node's
			// new persisted state and advances whichever dependants it unlocked.
			// 事件缓冲区溢出意味着本批事件可能已丢失，但全量对账本身就是幂等的，无需特殊处理。
			_ = sink.ConsumeDirty()
			if err := o.reconcilePlan(ctx, analysis, plan); err != nil {
				return err
			}
			// 第二段负责“把可以跑的节点真正送去执行”：把数据库里当前可执行的 ready 节点
			// “领取（claim）并直接推入 worker pool 队列”，交给 worker 实际执行。
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool); err != nil {
				return err
			}
		case <-sink.Wake():
			// 事件溢出唤醒：不等 watchdog，立即做一次全量对账与派发。
			_ = sink.ConsumeDirty()
			if err := o.reconcilePlan(ctx, analysis, plan); err != nil {
				return err
			}
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool); err != nil {
				return err
			}
		}
	}
}

// GetRunningInfo exposes the in-memory running entry for analysisID so that
// the /analysis-runtime/snapshot endpoint can report running_info like Python.
func (o *dynamicDagOrchestratorV2) GetRunningInfo(_ context.Context, analysisID int64) (*interfaces.DagRunningInfo, error) {
	if analysisID <= 0 || o.registry == nil {
		return nil, nil
	}
	entry := o.registry.Get(analysisID)
	if entry == nil {
		return nil, nil
	}
	return &interfaces.DagRunningInfo{
		AnalysisID:     entry.AnalysisID,
		TaskName:       entry.TaskName,
		Status:         entry.Status,
		StartedAt:      entry.StartedAt,
		UpdatedAt:      entry.UpdatedAt,
		MaxConcurrency: entry.MaxConcurrency,
		QueueSize:      entry.QueueSize,
		PollIntervalMs: entry.PollIntervalMs,
		TimeoutSeconds: entry.TimeoutSeconds,
		StopRequested:  entry.StopRequested,
	}, nil
}

// RequestStop asks the in-process run to stop and reports whether this process
// owned a live run for analysisID.
//
// It satisfies interfaces.DynamicDagOrchestrator. Returning false means the run is
// either already finished here or owned by another instance, in which case the
// caller must fall back to the persisted job_status stop path.
func (o *dynamicDagOrchestratorV2) RequestStop(analysisID int64) bool {
	if analysisID <= 0 || o.registry == nil || !o.registry.IsRunning(analysisID) {
		return false
	}

	// Persist "stopping" first: it is the cross-instance signal and keeps the
	// runtime snapshot observable while the run winds down. Writing before the
	// registry mutation guarantees the terminal status resolution below can only
	// observe a stop that was already durable.
	if o.repo != nil {
		if err := o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": dynamicV2StatusStopping,
			"updated_at": time.Now().UTC(),
		}); err != nil {
			logger.Warnf(context.Background(), "[DynamicDagOrchestratorV2] mark analysis stopping failed, analysis_id=%d err=%v", analysisID, err)
		}
	}

	// Cancels runCtx, which unblocks the run loop through its ctx.Done() arm.
	return o.registry.RequestStop(analysisID)
}

// isStopRequested reports whether the run was stopped, either by an in-process
// RequestStop or by a persisted stopping/stopped job status set by another
// instance. It must be evaluated before registry.MarkFinished, which drops the
// in-memory entry.
func (o *dynamicDagOrchestratorV2) isStopRequested(analysisID int64) bool {
	if o.registry != nil && o.registry.IsStopping(analysisID) {
		return true
	}
	stopped, err := o.shouldStopByJobStatus(context.Background(), analysisID)
	if err != nil {
		logger.Warnf(context.Background(), "[DynamicDagOrchestratorV2] resolve stop flag failed, analysis_id=%d err=%v", analysisID, err)
		return false
	}
	return stopped
}

// prepareAnalysisForCacheRerun resets persisted runtime graph for reruns when
// cache_type requires full rerun.
func (o *dynamicDagOrchestratorV2) prepareAnalysisForCacheRerun(ctx context.Context, analysisID int64) error {
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return err
	}
	if analysis == nil || analysis.CacheType != types.CacheTypeRerunAll {
		return nil
	}

	return o.repo.WithTransaction(ctx, func(tx interfaces.AnalysisRepository) error {
		if err := tx.DeleteAnalysisNodesByAnalysisID(ctx, analysisID); err != nil {
			return err
		}
		if err := tx.DeleteAnalysisEdgesByAnalysisID(ctx, analysisID); err != nil {
			return err
		}
		return nil
	})
}

func (o *dynamicDagOrchestratorV2) checkDynamicCompletion(
	ctx context.Context,
	analysisID int64,
	templateCount int,
	runtime *dagruntime.RuntimeEngine,
	pool *dagruntime.WorkerPool,
) (bool, error) {
	snapshot, err := runtime.GetSnapshot(ctx, analysisID)
	if err != nil {
		return false, err
	}
	allCreated, err := o.allCandidateNodesCreated(ctx, analysisID, templateCount)
	if err != nil {
		return false, err
	}
	// pool.QueueLen() covers every claimed node that is not running yet: dispatch is a
	// direct handoff into the pool, so there is no second queue left to account for.
	if allCreated && snapshot.IsFinished && pool.QueueLen() == 0 {
		if snapshot.StatusCount[dagruntime.StatusFailed] > 0 {
			return true, fmt.Errorf("one or more dynamic dag nodes failed")
		}
		return true, nil
	}
	return false, nil
}

// pumpReadyQueue claims ready nodes and hands them straight to the worker pool.
//
// There is deliberately no intermediate ready-queue goroutine: a claim is a
// persisted ready -> submitted transition, so a claimed node must land in the pool
// in the same step. Dispatching from this goroutine, which is the one that owns
// pool.Stop(), also means an enqueue can never race with the pool being closed.
func (o *dynamicDagOrchestratorV2) pumpReadyQueue(ctx context.Context, runtime *dagruntime.RuntimeEngine, analysisID int64, pool *dagruntime.WorkerPool) error {
	for claimed := 0; claimed < pool.Cap(); claimed++ {
		// Queue saturation is the backpressure signal: stop claiming ahead and let the
		// next node event or watchdog tick resume dispatch.
		if pool.QueueLen() >= pool.Cap() {
			return nil
		}
		node, err := runtime.ClaimNextReadyNode(ctx, analysisID)
		if err != nil {
			return err
		}
		if node == nil {
			return nil
		}
		if o.bus != nil {
			o.bus.Publish(dagruntime.RuntimeEvent{
				Name:           dagruntime.EventNodeSubmitted,
				AnalysisID:     analysisID,
				AnalysisNodeID: node.ID,
				NodeID:         node.NodeID,
				OccurredAt:     time.Now().UTC(),
			})
		}
		if !pool.EnqueueWait(ctx, node.ID) {
			// ctx was cancelled while handing the node over. It stays claimed
			// (submitted) and is rolled back by prepareNodesForResume on a later start.
			return nil
		}
	}
	return nil
}

// 给定 plan（静态模板）+ 数据库当前节点行，重新推导每个节点此刻应该是什么状态，并只对"不一致的节点"做最小修正。

// reconcilePlan materializes and re-decides the compiled graph in a single
// upstream-first pass.
//
// Readiness is derived from persisted state on every pass, so there is no mutable
// waiting set to keep in sync: a cache-invalidated node that is flipped back to ready
// is simply not "satisfied", and every dependant therefore stops being runnable in the
// same pass - no re-arm, no hold-back. Iterating plan.order (the compiler's
// topological order) guarantees ancestors are settled before their dependants are
// queried, which is the only ordering requirement this relies on.
func (o *dynamicDagOrchestratorV2) reconcilePlan(ctx context.Context, analysis *types.Analysis, plan *dynamicExecutionPlan) error {
	if plan == nil || len(plan.order) == 0 {
		return nil
	}

	persisted, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysis.ID)
	if err != nil {
		return err
	}
	state := newDynamicState(plan, persisted)

	newItems := make([]*types.AnalysisNode, 0)
	for _, nodeID := range plan.order {
		current := state.nodes[nodeID]

		// Derived-readiness invariant, replacing the old hold-back pass: a claimable
		// node (ready, not a cache hit) whose upstream is not satisfied must not stay
		// claimable. The dispatch pump claims purely by persisted status, so leaving it
		// ready would let it overtake the upstream it consumes.
		if current != nil &&
			normaliseNodeStatus(current) == dagruntime.StatusReady &&
			!current.CacheHit &&
			!state.canRun(nodeID) {
			if err := o.demoteNodeToPending(ctx, current); err != nil {
				return err
			}
			continue
		}

		if state.isBlocked(nodeID) {
			// A blocked node is materialized once as skipped so completion converges; the
			// skip then blocks its own dependants through the same derived query on the
			// following iterations, without an explicit failure cascade.
			if current != nil {
				continue
			}
			node, err := o.materializePlannedNode(ctx, analysis, plan, state, nodeID, dagruntime.StatusSkipped, "blocked by failed upstream dependency")
			if err != nil {
				return err
			}
			newItems = append(newItems, node)
			continue
		}

		if !state.canRun(nodeID) {
			// Upstream is still in flight or queued for a rerun: leave the node alone.
			continue
		}

		if current == nil {
			node, err := o.materializePlannedNode(ctx, analysis, plan, state, nodeID, dagruntime.StatusReady, "")
			if err != nil {
				return err
			}
			newItems = append(newItems, node)
			continue
		}

		// The node exists and its upstream is satisfied: decide whether its persisted
		// result is reusable, or whether it must be flipped back to ready.
		switch normaliseNodeStatus(current) {
		case dagruntime.StatusRunning, dagruntime.StatusSubmitted:
			// Already executing; the runtime owns it.
			continue
		case dagruntime.StatusReady:
			if !current.CacheHit {
				// Already queued for execution.
				continue
			}
		}

		probe, decision, err := o.decideExistingNode(ctx, analysis, plan, state, current)
		if err != nil {
			return err
		}
		if !decision.Rerun {
			continue
		}
		if err := o.markExistingNodeReadyForRerun(ctx, current, probe, decision.Reason); err != nil {
			return err
		}
	}

	if len(newItems) == 0 {
		return nil
	}
	return o.repo.CreateAnalysisNodes(ctx, newItems)
}

// materializePlannedNode creates the analysis_node row for one compiled template and
// registers it in the pass state so its dependants observe it immediately.
func (o *dynamicDagOrchestratorV2) materializePlannedNode(
	ctx context.Context,
	analysis *types.Analysis,
	plan *dynamicExecutionPlan,
	state *dynamicState,
	nodeID string,
	status string,
	errorMessage string,
) (*types.AnalysisNode, error) {
	planned, ok := plan.nodes[nodeID]
	if !ok {
		return nil, fmt.Errorf("node %s is not part of the compiled plan", nodeID)
	}

	script, err := o.workflowRepo.GetScriptByScriptID(ctx, analysis.ProjectID, dynamicToString(planned.template["script_id"]))
	if err != nil {
		return nil, err
	}

	node, err := o.buildDynamicAnalysisNode(script, analysis, nodeID, planned.template, planned.incoming, state.nodes, status, errorMessage)
	if err != nil {
		return nil, err
	}
	// No runtime artifacts are produced here on purpose. The node is about to be
	// claimed and handed to NodeDispatcher, which prepares run.sh / params.json
	// exactly once (and cleans the output directory right before executing).
	// Preparing here as well would duplicate that work for every node, and for a
	// skipped node - which never executes - it would be pure waste.
	// The digests the cache policies compare against are captured by the
	// dispatcher after it prepares the node for execution.
	state.nodes[nodeID] = node
	return node, nil
}

// demoteNodeToPending rolls a claimable node back to pending so the dispatch pump
// cannot hand it to a worker before the upstream it consumes has produced anything.
func (o *dynamicDagOrchestratorV2) demoteNodeToPending(ctx context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return nil
	}
	if err := o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
		"status":        dagruntime.StatusPending,
		"started_at":    nil,
		"finished_at":   nil,
		"error_message": "",
		"exit_code":     0,
	}); err != nil {
		return err
	}
	node.Status = dagruntime.StatusPending
	return nil
}

// decideExistingNode applies the analysis cache policy to a node persisted by a
// previous run and reports whether it must be rerun, together with the probe payload
// the rerun must be written with.
//
// Only the fingerprint policies need the scheduler to produce artifacts, and they do
// so through the narrow NodeArtifactFingerprinter, never through the execution-time
// preparer.
func (o *dynamicDagOrchestratorV2) decideExistingNode(
	ctx context.Context,
	analysis *types.Analysis,
	plan *dynamicExecutionPlan,
	state *dynamicState,
	existing *types.AnalysisNode,
) (*types.AnalysisNode, CacheDecision, error) {
	if analysis == nil || existing == nil {
		return nil, CacheDecision{}, nil
	}
	planned, ok := plan.nodes[strings.TrimSpace(existing.NodeID)]
	if !ok {
		return nil, CacheDecision{}, nil
	}

	policy := o.cachePolicies.Resolve(analysis.CacheType)

	// Policies that do not compare fingerprints decide from the persisted node alone,
	// so both the script query and the probe are skipped entirely. The node itself is
	// reused as the probe: the rerun only resets its execution state.
	if !policy.RequiresFingerprint() {
		decision := policy.Decide(CacheFacts{CacheType: analysis.CacheType, Existing: existing})
		if !decision.Rerun {
			return nil, decision, nil
		}
		return existing, decision, nil
	}

	// The script is only loaded for the fingerprint policies: it is the input the
	// probe needs, and loading it here keeps reuse_existing free of that query.
	script, err := o.workflowRepo.GetScriptByScriptID(ctx, analysis.ProjectID, dynamicToString(planned.template["script_id"]))
	if err != nil {
		return nil, CacheDecision{}, err
	}
	probe := o.buildCacheProbe(script, existing, planned.template, planned.incoming, state.nodes)
	if err := o.fingerprintNodeArtifacts(ctx, probe); err != nil {
		return nil, CacheDecision{}, err
	}

	decision := policy.Decide(CacheFacts{
		CacheType:       analysis.CacheType,
		Existing:        existing,
		ProbeCommandMD5: probe.CommandMD5,
		ProbeParamsMD5:  probe.ParamsMD5,
	})
	return probe, decision, nil
}

// buildCacheProbe rebuilds the node payload from the freshly compiled template so
// the cache policies can compare what would run now against what ran last time.
func (o *dynamicDagOrchestratorV2) buildCacheProbe(
	script *types.Script,
	existingNode *types.AnalysisNode,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
) *types.AnalysisNode {
	probe := *existingNode
	probe.NodeName = dynamicToString(row["node_name"])
	probe.SampleID = dynamicToString(row["sample_id"])
	probe.Executor = dynamicToString(row["executor"])
	if script != nil {
		probe.ScriptID = script.ID
	}
	probe.InputsPatterns = dynamicToJSONMap(row["inputs_patterns"])
	probe.OutputPatterns = dynamicToJSONMap(row["output_patterns"])
	probe.Params = dynamicToJSONMap(row["params"])
	probe.ResolvedInputs = dynamicToJSONMap(row["resolved_inputs"])
	probe.ResolvedOutputs = dynamicToJSONMap(row["resolved_outputs"])
	bootstrapInputsFromUpstream(row, probe.Params, probe.ResolvedInputs, incomingEdges, existingByNodeID)
	return &probe
}

// markExistingNodeReadyForRerun resets a persisted node so the runtime can claim it
// again, and mirrors the reset onto the in-pass state so its dependants stop seeing it
// as satisfied immediately.
func (o *dynamicDagOrchestratorV2) markExistingNodeReadyForRerun(ctx context.Context, existing *types.AnalysisNode, probe *types.AnalysisNode, rerunReason string) error {
	if existing == nil || probe == nil {
		return nil
	}
	if strings.TrimSpace(existing.AnalysisNodeID) == "" {
		return nil
	}
	rerunReason = strings.TrimSpace(rerunReason)
	if rerunReason == "" {
		rerunReason = "node cache invalidated"
	}
	if err := o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, existing.AnalysisNodeID, map[string]any{
		"node_name":                probe.NodeName,
		"sample_id":                probe.SampleID,
		"script_id":                probe.ScriptID,
		"inputs_patterns":          probe.InputsPatterns,
		"resolved_inputs":          probe.ResolvedInputs,
		"output_patterns":          probe.OutputPatterns,
		"resolved_outputs":         types.JSONMap{},
		"params":                   probe.Params,
		"status":                   dagruntime.StatusReady,
		"executor":                 probe.Executor,
		"cache_hit":                false,
		"command_md5":              probe.CommandMD5,
		"params_md5":               probe.ParamsMD5,
		"rerun_reason":             rerunReason,
		"error_message":            "",
		"exit_code":                0,
		"started_at":               nil,
		"finished_at":              nil,
		"output_validation_errors": types.JSONSlice{},
	}); err != nil {
		return err
	}
	applyRerunToNode(existing, probe, rerunReason)
	return nil
}

// applyRerunToNode mirrors a rerun reset onto the in-memory node. The probe may be the
// node itself (non-fingerprint policies), in which case the field copies are no-ops.
func applyRerunToNode(node *types.AnalysisNode, probe *types.AnalysisNode, reason string) {
	if node == nil || probe == nil {
		return
	}
	node.NodeName = probe.NodeName
	node.SampleID = probe.SampleID
	node.ScriptID = probe.ScriptID
	node.InputsPatterns = probe.InputsPatterns
	node.ResolvedInputs = probe.ResolvedInputs
	node.OutputPatterns = probe.OutputPatterns
	node.ResolvedOutputs = types.JSONMap{}
	node.Params = probe.Params
	node.Executor = probe.Executor
	node.CommandMD5 = probe.CommandMD5
	node.ParamsMD5 = probe.ParamsMD5
	node.RerunReason = reason
	node.ErrorMessage = ""
	node.ExitCode = 0
	node.StartedAt = nil
	node.FinishedAt = nil
	node.OutputValidationErrors = types.JSONSlice{}
	node.CacheHit = false
	node.Status = dagruntime.StatusReady
}

// fingerprintNodeArtifacts fills a probe node's CommandMD5 / ParamsMD5 so the md5
// cache policies can compare the artifacts that would run now against the ones
// that ran last time.
//
// The probe never inherits the execution-time side effects of the preparer: the
// fingerprinter prepares with output cleanup disabled, because a node that turns
// out to be reusable must keep the results its downstream nodes already reference.
func (o *dynamicDagOrchestratorV2) fingerprintNodeArtifacts(ctx context.Context, node *types.AnalysisNode) error {
	if o.fingerprinter == nil {
		return fmt.Errorf("node artifact fingerprinter is not configured")
	}
	return o.fingerprinter.Fingerprint(ctx, node)
}

func (o *dynamicDagOrchestratorV2) buildDynamicAnalysisNode(
	script *types.Script,
	analysis *types.Analysis,
	nodeID string,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
	status string,
	errorMessage string,
) (*types.AnalysisNode, error) {
	upstreamIDs := dynamicToStringSlice(row["upstream_ids"])
	mergedParams := dynamicToJSONMap(row["params"])
	mergedInputs := dynamicToJSONMap(row["resolved_inputs"])
	bootstrapInputsFromUpstream(row, mergedParams, mergedInputs, incomingEdges, existingByNodeID)

	analysisNodeID := strings.TrimSpace(dynamicToString(row["analysis_node_id"]))
	if analysisNodeID == "" {
		analysisNodeID = "node-" + uuid.NewString()
	}
	indexID := utils.GenerateID()
	workspaceDir := filepath.Join(analysis.OutputDir, fmt.Sprintf("%d", indexID))
	outputDir := utils.GetAnalysisNodeOutputDir(workspaceDir) //filepath.Join(workspaceDir, "output")
	cacheDir := utils.GetAnalysisNodeCacheDir(workspaceDir)   //filepath.Join(workspaceDir, "cache")
	paramsPath := filepath.Join(workspaceDir, "params.json")
	commandPath := filepath.Join(workspaceDir, "run.sh")
	logPath := filepath.Join(workspaceDir, "command.log")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}

	var finishedAt *time.Time
	if status == dagruntime.StatusSkipped {
		now := time.Now().UTC()
		finishedAt = &now
	}

	return &types.AnalysisNode{
		ID:                     indexID,
		AnalysisNodeID:         analysisNodeID,
		AnalysisID:             analysis.ID,
		NodeID:                 nodeID,
		NodeName:               dynamicToString(row["node_name"]),
		SampleID:               dynamicToString(row["sample_id"]),
		ScriptID:               script.ID,
		InputsPatterns:         dynamicToJSONMap(row["inputs_patterns"]),
		ResolvedInputs:         mergedInputs,
		OutputPatterns:         dynamicToJSONMap(row["output_patterns"]),
		ResolvedOutputs:        dynamicToJSONMap(row["resolved_outputs"]),
		Params:                 mergedParams,
		Status:                 status,
		Executor:               dynamicToString(row["executor"]),
		Retry:                  dynamicIntFromAny(row["retry"], 0),
		MaxRetry:               dynamicIntFromAny(row["max_retry"], 3),
		CacheHit:               false,
		UpstreamIDs:            types.JSONSlice(dynamicStringSliceToAny(upstreamIDs)),
		DownstreamIDs:          dynamicToJSONSlice(row["downstream_ids"]),
		InputValidationErrors:  dynamicToJSONSlice(row["input_validation_errors"]),
		OutputValidationErrors: types.JSONSlice{},
		LogPath:                logPath,
		WorkspaceDir:           workspaceDir,
		OutputDir:              outputDir,
		CacheDir:               cacheDir,
		CommandPath:            commandPath,
		ParamsPath:             paramsPath,
		ErrorMessage:           errorMessage,
		RerunReason:            dynamicToString(row["rerun_reason"]),
		CreationSource:         "scheduler",
		FinishedAt:             finishedAt,
	}, nil
}

// allCandidateNodesCreated compares persisted nodes with compiled template count.
// It is used as one half of the loop completion gate.
func (o *dynamicDagOrchestratorV2) allCandidateNodesCreated(ctx context.Context, analysisID int64, total int) (bool, error) {
	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return false, err
	}
	return len(nodes) >= total, nil
}

// shouldStopByJobStatus checks persistent stop flags set by external control APIs.
func (o *dynamicDagOrchestratorV2) shouldStopByJobStatus(ctx context.Context, analysisID int64) (bool, error) {
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return false, err
	}
	status := strings.ToLower(strings.TrimSpace(analysis.JobStatus))
	return status == dynamicV2StatusStopping || status == dynamicV2StatusStopped, nil
}

// renewRunningLease periodically updates analysis.updated_at so stale lock recovery
// can distinguish live scheduler from abandoned runs.
func (o *dynamicDagOrchestratorV2) renewRunningLease(analysisID int64, stop <-chan struct{}) {
	ticker := time.NewTicker(dynamicV2Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
				"updated_at": time.Now().UTC(),
			})
		}
	}
}

func (o *dynamicDagOrchestratorV2) publishDagRuntimeEvent(name string, analysisID int64, payload map[string]any) {
	if o.bus == nil {
		return
	}
	evt := dagruntime.RuntimeEvent{
		Name:       name,
		AnalysisID: analysisID,
		OccurredAt: time.Now().UTC(),
	}
	if len(payload) > 0 {
		evt.Payload = payload
	}
	o.bus.Publish(evt)
}

// buildAnalysisEdges converts compiled edge rows into persistence entities.
func buildAnalysisEdges(analysisID int64, edgeRows []map[string]any) []*types.AnalysisEdge {
	out := make([]*types.AnalysisEdge, 0, len(edgeRows))
	for _, row := range edgeRows {
		out = append(out, &types.AnalysisEdge{
			AnalysisEdgeID: dynamicFallbackString(dynamicToString(row["analysis_edge_id"]), uuid.NewString()),
			AnalysisID:     analysisID,
			SourceNode:     dynamicToString(row["source_node"]),
			TargetNode:     dynamicToString(row["target_node"]),
			SourceHandle:   dynamicToString(row["source_handle"]),
			TargetHandle:   dynamicToString(row["target_handle"]),
		})
	}
	return out
}

// buildIncomingEdgeMap indexes target node -> incoming edges for fast input merge.
func buildIncomingEdgeMap(edges []*types.AnalysisEdge) map[string][]*types.AnalysisEdge {
	incoming := make(map[string][]*types.AnalysisEdge)
	for _, edge := range edges {
		if edge == nil {
			continue
		}
		incoming[edge.TargetNode] = append(incoming[edge.TargetNode], edge)
	}
	return incoming
}

// buildOutgoingNodeMap indexes source node -> direct downstream node ids.
func buildOutgoingNodeMap(edges []*types.AnalysisEdge) map[string][]string {
	outgoing := make(map[string][]string)
	for _, edge := range edges {
		if edge == nil {
			continue
		}
		source := strings.TrimSpace(edge.SourceNode)
		target := strings.TrimSpace(edge.TargetNode)
		if source == "" || target == "" {
			continue
		}
		outgoing[source] = append(outgoing[source], target)
	}
	return outgoing
}

// bootstrapInputsFromUpstream merges upstream outputs into target params/resolved_inputs.
// It respects list semantics when input is configured as multiple or already list-like.
func bootstrapInputsFromUpstream(
	row map[string]any,
	params types.JSONMap,
	resolvedInputs types.JSONMap,
	edges []*types.AnalysisEdge,
	existing map[string]*types.AnalysisNode,
) {
	patterns := dynamicToJSONMap(row["inputs_patterns"])
	for _, edge := range edges {
		if edge == nil {
			continue
		}
		source := existing[edge.SourceNode]
		if !isSuccessNode(source) {
			continue
		}
		value, ok := source.ResolvedOutputs[edge.SourceHandle]
		if !ok {
			continue
		}

		cfg := dynamicAsMap(patterns[edge.TargetHandle])
		multiple := dynamicAsBool(cfg["multiple"])
		if multiple || dynamicIsListValue(params[edge.TargetHandle]) || dynamicIsListValue(resolvedInputs[edge.TargetHandle]) {
			params[edge.TargetHandle] = dynamicAppendToList(params[edge.TargetHandle], value)
			resolvedInputs[edge.TargetHandle] = dynamicAppendToList(resolvedInputs[edge.TargetHandle], value)
			continue
		}
		params[edge.TargetHandle] = value
		resolvedInputs[edge.TargetHandle] = value
	}
}

// allUpstreamSuccess returns true only when every upstream node reached success state.
func allUpstreamSuccess(upstream []string, existing map[string]*types.AnalysisNode) bool {
	for _, id := range upstream {
		node := existing[id]
		if !isSuccessNode(node) {
			return false
		}
	}
	return true
}

// isSuccessNode normalizes success checks and preserves cache-hit behavior.
func isSuccessNode(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(node.Status))
	if status == dagruntime.StatusReady && node.CacheHit {
		return true
	}
	return dagruntime.IsSuccessStatus(status)
}

// dynamicToMapSlice converts mixed JSON-decoded list values into []map[string]any.
func dynamicToMapSlice(value any) []map[string]any {
	if value == nil {
		return []map[string]any{}
	}
	if rows, ok := value.([]map[string]any); ok {
		return rows
	}
	raw, ok := value.([]any)
	if !ok {
		return []map[string]any{}
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			rows = append(rows, m)
		}
	}
	return rows
}

// dynamicToString performs tolerant scalar-to-string conversion for map values.
func dynamicToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// dynamicFallbackString returns fallback when value is blank after trimming.
func dynamicFallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// dynamicIntFromAny is a tolerant converter for numeric fields coming from decoded JSON.
func dynamicIntFromAny(v any, fallback int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, err := strconvAtoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return fallback
}

// dynamicToJSONMap converts various map-like payloads to types.JSONMap.
func dynamicToJSONMap(v any) types.JSONMap {
	if v == nil {
		return types.JSONMap{}
	}
	if m, ok := v.(types.JSONMap); ok {
		return m
	}
	if m, ok := v.(map[string]any); ok {
		return types.JSONMap(dynamicCloneAnyMap(m))
	}
	if m, ok := v.(map[string]interface{}); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = val
		}
		return types.JSONMap(out)
	}
	return types.JSONMap{}
}

// dynamicToJSONSlice converts various slice-like payloads to types.JSONSlice.
func dynamicToJSONSlice(v any) types.JSONSlice {
	if v == nil {
		return types.JSONSlice{}
	}
	if s, ok := v.(types.JSONSlice); ok {
		return s
	}
	if s, ok := v.([]any); ok {
		return types.JSONSlice(s)
	}
	rv := reflect.ValueOf(v)
	if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		return types.JSONSlice(out)
	}
	return types.JSONSlice{}
}

// dynamicCloneAnyMap does a shallow clone to avoid unintended mutation of source maps.
func dynamicCloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// dynamicToStringSlice converts a mixed array payload into normalized []string.
func dynamicToStringSlice(v any) []string {
	if v == nil {
		return []string{}
	}
	if arr, ok := v.([]string); ok {
		out := make([]string, 0, len(arr))
		for _, s := range arr {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if arr, ok := v.([]any); ok {
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			s := strings.TrimSpace(dynamicToString(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{}
}

// dynamicStringSliceToAny converts []string to []any for JSONSlice compatibility.
func dynamicStringSliceToAny(items []string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

// dynamicAsMap performs best-effort map extraction.
func dynamicAsMap(v any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(types.JSONMap); ok {
		return map[string]any(m)
	}
	return map[string]any{}
}

// dynamicAsBool performs tolerant bool parsing for config-like values.
func dynamicAsBool(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		normalized := strings.TrimSpace(strings.ToLower(val))
		return normalized == "true" || normalized == "1" || normalized == "yes" || normalized == "y"
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	default:
		return false
	}
}

// dynamicIsListValue checks whether payload is slice/array for append semantics.
func dynamicIsListValue(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array
}

// dynamicAppendToList appends value to existing list-like payload safely.
func dynamicAppendToList(current any, value any) []any {
	if current == nil {
		return []any{value}
	}
	if arr, ok := current.([]any); ok {
		return append(arr, value)
	}
	rv := reflect.ValueOf(current)
	if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		out := make([]any, 0, rv.Len()+1)
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		out = append(out, value)
		return out
	}
	return []any{current, value}
}

// strconvAtoi is a small local parser to avoid extra import coupling in this file.
func strconvAtoi(v string) (int, error) {
	neg := false
	if strings.HasPrefix(v, "-") {
		neg = true
		v = strings.TrimPrefix(v, "-")
	}
	if v == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid")
		}
		n = n*10 + int(ch-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}
