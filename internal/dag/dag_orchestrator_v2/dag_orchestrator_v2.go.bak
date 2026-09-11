package orchestratorv2

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	// dynamicV2EventDrainLimit bounds how many already-buffered runtime events one
	// loop turn folds into a single reconcile pass. Whatever exceeds the limit is
	// picked up on the next turn, so the cap only bounds batching latency.
	dynamicV2EventDrainLimit = 256

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
	// runtime event for this run can be observed without a sink.
	sink := o.router.Register(analysisID)

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

	incoming := buildIncomingEdgeMap(edges)
	outgoing := buildOutgoingNodeMap(edges)
	nodeTemplateByID := make(map[string]map[string]any, len(nodeTemplates))
	for _, row := range nodeTemplates {
		nid := strings.TrimSpace(dynamicToString(row["node_id"]))
		if nid == "" {
			continue
		}
		nodeTemplateByID[nid] = row
	}

	dep := newDynamicDependencyManager(nodeTemplateByID, outgoing)
	existing, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	existingByNodeID := make(map[string]*types.AnalysisNode, len(existing))
	for _, n := range existing {
		existingByNodeID[n.NodeID] = n
	}
	// Derive the dependency view from the persisted nodes instead of replaying deltas,
	// so an adopted run starts from the source of truth.
	dep.Resync(existingByNodeID)

	// state is the run-scoped, in-memory half of the reconciler: it answers "is there
	// work left?" without a database read, and it remembers which nodes already had
	// their cache decision taken so none is fingerprinted twice in one run.
	state := newDynamicRunState(len(nodeTemplateByID), existing)

	// Runtime events reach this loop through the process-wide router via the
	// analysis-scoped sink; no per-run bus subscription is created here.
	if sink == nil {
		// Defensive: without a sink the loop still converges through the watchdog.
		sink = dagruntime.NewAnalysisEventSink()
	}

	if err := o.reconcileDynamicCandidates(ctx, analysis, nodeTemplateByID, incoming, o.unsettledCandidates(dep.InitialCandidates(), state), dep, state); err != nil {
		return err
	}
	if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool, state); err != nil {
		return err
	}

	stopTicker := time.NewTicker(dynamicV2StopCheckInterval)
	defer stopTicker.Stop()
	watchdogTicker := time.NewTicker(dynamicV2WatchdogInterval)
	defer watchdogTicker.Stop()

	for {
		// Cheap in-memory gate first: the authoritative read only happens once the run
		// may actually be over, so a slightly stale view can cost one extra query but
		// never a wrong verdict.
		if finished, finishedErr := o.checkDynamicCompletion(ctx, analysisID, pool, dep, state); finishedErr != nil {
			return finishedErr
		} else if finished {
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
			// Level-triggered resync. A single authoritative read repairs the in-memory
			// dependency and accounting views, so the reconciler below never has to trust
			// that every event arrived. Filtering out the nodes whose scheduling decision
			// is already final is what keeps the tick cheap for a wide graph: otherwise
			// every ready node paid a script load plus two artifact preparations and two
			// digests every 5 seconds.
			observed, observeErr := o.observeRunState(ctx, analysisID, dep, state)
			if observeErr != nil {
				return observeErr
			}
			if err := o.reconcileDynamicCandidates(ctx, analysis, nodeTemplateByID, incoming, o.unsettledCandidates(dep.InitialCandidates(), state), dep, state); err != nil {
				return err
			}
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool, state); err != nil {
				return err
			}
			// The verdict is taken from the set read before this pass, so a node this pass
			// just claimed or materialized cannot be mistaken for a finished run: it still
			// shows as non-terminal there. The pass that does finish the run is caught by
			// the gate at the top of the next iteration.
			if finished, finishedErr := dynamicRunFinished(observed, state.templateCount); finishedErr != nil {
				return finishedErr
			} else if finished {
				return nil
			}
		case evt := <-sink.Events():
			// Fold every event already buffered into this turn, so an event storm costs
			// one reconcile pass and one completion probe instead of one of each per
			// event.
			candidates, dirty := o.drainRuntimeEvents(evt, sink, dep, state)
			if dirty {
				// Events were dropped, so the in-memory dependency view may be wrong:
				// rebuild it from the persisted nodes before reconciling.
				if _, err := o.observeRunState(ctx, analysisID, dep, state); err != nil {
					return err
				}
				candidates = dep.InitialCandidates()
			}
			// 算出并落库哪些节点现在可以/不可以跑
			// 作用：对本次事件影响到的候选节点做“对账/物化”。
			// 具体会做的事：
			// 看节点是否已存在于 analysis_node；
			// 若不存在且依赖满足，创建新节点（ready）；
			// 若被失败上游阻断，创建/标记为 skipped；
			// 若已存在，会按 cache 策略判断是否需要重新置为 ready 重跑。

			if err := o.reconcileDynamicCandidates(ctx, analysis, nodeTemplateByID, incoming, candidates, dep, state); err != nil {
				return err
			}
			// 第二段负责“把可以跑的节点真正送去执行”。
			// 作用：把数据库里当前可执行的 ready 节点“领取（claim）并直接推入 worker pool 队列”，交给 worker 实际执行。
			// 特点：这一步每批事件后都会跑一次（不依赖 candidates 是否为空），确保新变成 ready 的节点尽快被派发，减少调度延迟；
			// 但在内存计数表明没有任何可 claim 节点时会直接返回，不再为“必然查不到”的 claim 付一次数据库往返。
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool, state); err != nil {
				return err
			}
		case <-sink.Wake():
			// 事件溢出唤醒：不等 watchdog，立即做一次全量对账与派发。
			_ = sink.ConsumeDirty()
			// An overflow signal means the in-memory views may have missed transitions:
			// rebuild them from the persisted nodes, then reconcile the whole graph.
			if _, err := o.observeRunState(ctx, analysisID, dep, state); err != nil {
				return err
			}
			if err := o.reconcileDynamicCandidates(ctx, analysis, nodeTemplateByID, incoming, dep.InitialCandidates(), dep, state); err != nil {
				return err
			}
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, pool, state); err != nil {
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

type dynamicDependencyManager struct {
	// upstream is the immutable compiled topology, kept so the mutable waiting set can
	// always be rebuilt from it.
	upstream map[string][]string
	waiting  map[string]map[string]struct{}
	blocked  map[string]bool
	outgoing map[string][]string
}

func newDynamicDependencyManager(nodeTemplateByID map[string]map[string]any, outgoing map[string][]string) *dynamicDependencyManager {
	upstream := make(map[string][]string, len(nodeTemplateByID))
	waiting := make(map[string]map[string]struct{}, len(nodeTemplateByID))
	blocked := make(map[string]bool, len(nodeTemplateByID))
	for nodeID, row := range nodeTemplateByID {
		upstreamIDs := dynamicToStringSlice(row["upstream_ids"])
		deps := make(map[string]struct{}, len(upstreamIDs))
		for _, id := range upstreamIDs {
			deps[id] = struct{}{}
		}
		upstream[nodeID] = upstreamIDs
		waiting[nodeID] = deps
		blocked[nodeID] = false
	}
	return &dynamicDependencyManager{upstream: upstream, waiting: waiting, blocked: blocked, outgoing: outgoing}
}

// Resync rebuilds the readiness and blocking view from authoritative node statuses.
//
// It is the mechanism behind the level-triggered loop: instead of trusting that every
// runtime event arrived, the scheduler can rebuild the whole view from the persisted
// nodes in one pass. A dependency counts as satisfied only when its upstream ended in
// success, and a node counts as blocked as soon as one of its upstreams ended without
// succeeding - which reproduces, transitively, what OnNodeFailure propagates.
func (m *dynamicDependencyManager) Resync(existing map[string]*types.AnalysisNode) {
	waiting := make(map[string]map[string]struct{}, len(m.upstream))
	blocked := make(map[string]bool, len(m.upstream))
	for nodeID, upstreamIDs := range m.upstream {
		pending := make(map[string]struct{}, len(upstreamIDs))
		isBlocked := false
		for _, upstreamID := range upstreamIDs {
			upstream := existing[upstreamID]
			if isSuccessNode(upstream) {
				continue
			}
			if upstream != nil && dagruntime.IsTerminalStatus(strings.ToLower(strings.TrimSpace(upstream.Status))) {
				// Terminal without success: an upstream that cannot be retried in this
				// scheduler, so the block is final.
				isBlocked = true
				continue
			}
			pending[upstreamID] = struct{}{}
		}
		waiting[nodeID] = pending
		blocked[nodeID] = isBlocked
	}
	m.waiting = waiting
	m.blocked = blocked
}

func (m *dynamicDependencyManager) InitialCandidates() []string {
	out := make([]string, 0)
	for nodeID := range m.waiting {
		if m.IsReady(nodeID) || m.IsBlocked(nodeID) {
			out = append(out, nodeID)
		}
	}
	sort.Strings(out)
	return out
}

func (m *dynamicDependencyManager) OnNodeSuccess(nodeID string) []string {
	touched := map[string]struct{}{}
	for _, downstream := range m.outgoing[nodeID] {
		deps, ok := m.waiting[downstream]
		if !ok {
			continue
		}
		if _, exists := deps[nodeID]; exists {
			delete(deps, nodeID)
			touched[downstream] = struct{}{}
		}
	}
	return dynamicSortedKeys(touched)
}

func (m *dynamicDependencyManager) OnNodeFailure(nodeID string) []string {
	queue := []string{nodeID}
	visited := map[string]struct{}{nodeID: {}}
	touched := map[string]struct{}{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, downstream := range m.outgoing[current] {
			touched[downstream] = struct{}{}
			if !m.blocked[downstream] {
				m.blocked[downstream] = true
			}
			if _, seen := visited[downstream]; seen {
				continue
			}
			visited[downstream] = struct{}{}
			queue = append(queue, downstream)
		}
	}
	return dynamicSortedKeys(touched)
}

func (m *dynamicDependencyManager) IsReady(nodeID string) bool {
	deps, ok := m.waiting[nodeID]
	if !ok {
		return false
	}
	if m.blocked[nodeID] {
		return false
	}
	return len(deps) == 0
}

func (m *dynamicDependencyManager) IsBlocked(nodeID string) bool {
	return m.blocked[nodeID]
}

// dynamicIsTerminalNode mirrors the runtime engine's notion of a terminal node,
// including the "ready but served from cache" case, which counts as completed even
// though the status itself is not terminal.
func dynamicIsTerminalNode(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(node.Status))
	if status == dagruntime.StatusReady && node.CacheHit {
		return true
	}
	return dagruntime.IsTerminalStatus(status)
}

// dynamicIsClaimableNode reports whether the dispatch pump can claim a node: it must
// be ready, and a cache hit is satisfied without ever running, so it is excluded -
// which is exactly the predicate the claim query applies.
func dynamicIsClaimableNode(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(node.Status), dagruntime.StatusReady) && !node.CacheHit
}

// dynamicRunState is the run-scoped, in-memory view that turns the scheduler into a
// level-triggered reconciler.
//
// It answers two questions from memory instead of from the database:
//
//   - "is there anything left to do?", so the authoritative completion probe only
//     runs when the run may plausibly be over; and
//   - "which nodes still need a scheduling decision?", so a node's cache probe - the
//     expensive part, one script load plus two artifact preparations and two file
//     digests - runs once per run instead of once per watchdog tick.
//
// The view is owned by runDynamicLoop and never stored on the orchestrator, because a
// single orchestrator instance drives many concurrent runs.
type dynamicRunState struct {
	// templateCount is how many nodes the compiled graph must materialize.
	templateCount int
	// known holds the node ids already materialized for this run.
	known map[string]struct{}
	// terminal holds the subset of known nodes observed in a terminal state.
	terminal map[string]struct{}
	// settled holds the nodes whose scheduling decision is final for this run.
	//
	// Such a decision cannot change afterwards: it is derived from the compiled
	// template (immutable for the run) and from the upstream outputs, which are final
	// as soon as the node's dependencies are satisfied - which is exactly when the
	// decision is allowed to be taken.
	settled map[string]struct{}
	// claimable counts the materialized nodes the dispatch pump could still hand over
	// (ready and not served from cache).
	//
	// It exists so the pump can be skipped outright when there is nothing to dispatch:
	// a claim is a locking select plus an update, and paying for one on every event is
	// what made the loop's cost track the event rate rather than the actual work. The
	// count is recomputed exactly whenever the run's nodes are re-read, and a claim that
	// finds nothing resets it, so it can only ever be stale-low - and only for as long
	// as the watchdog interval.
	claimable int
}

func newDynamicRunState(templateCount int, existing []*types.AnalysisNode) *dynamicRunState {
	state := &dynamicRunState{
		templateCount: templateCount,
		known:         make(map[string]struct{}, templateCount),
		terminal:      make(map[string]struct{}, templateCount),
		settled:       make(map[string]struct{}),
	}
	state.resync(existing)
	return state
}

// resync replaces the materialization/terminal view with an authoritative node set.
// The settled set is deliberately preserved: it records decisions taken in this run,
// which the persisted rows cannot express.
func (s *dynamicRunState) resync(nodes []*types.AnalysisNode) {
	known := make(map[string]struct{}, len(nodes))
	terminal := make(map[string]struct{})
	claimable := 0
	for _, node := range nodes {
		if node == nil {
			continue
		}
		nodeID := strings.TrimSpace(node.NodeID)
		if nodeID == "" {
			continue
		}
		known[nodeID] = struct{}{}
		if dynamicIsTerminalNode(node) {
			terminal[nodeID] = struct{}{}
		}
		if dynamicIsClaimableNode(node) {
			claimable++
		}
	}
	s.known = known
	s.terminal = terminal
	s.claimable = claimable
}

func (s *dynamicRunState) isSettled(nodeID string) bool {
	if s == nil || nodeID == "" {
		return false
	}
	_, ok := s.settled[nodeID]
	return ok
}

func (s *dynamicRunState) settle(nodeID string) {
	if s == nil || nodeID == "" {
		return
	}
	s.settled[nodeID] = struct{}{}
}

// markCreated records a node materialized by this run. Creating a node is itself the
// scheduling decision for it, so it is settled in the same step.
func (s *dynamicRunState) markCreated(node *types.AnalysisNode) {
	if s == nil || node == nil {
		return
	}
	nodeID := strings.TrimSpace(node.NodeID)
	if nodeID == "" {
		return
	}
	s.known[nodeID] = struct{}{}
	if dynamicIsTerminalNode(node) {
		s.terminal[nodeID] = struct{}{}
	}
	if dynamicIsClaimableNode(node) {
		s.claimable++
	}
	s.settled[nodeID] = struct{}{}
}

// markTerminal records a terminal transition observed through a runtime event.
// Unknown ids are ignored so a stray event cannot inflate the accounting.
func (s *dynamicRunState) markTerminal(nodeID string) {
	if s == nil {
		return
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	if _, materialized := s.known[nodeID]; !materialized {
		return
	}
	s.terminal[nodeID] = struct{}{}
}

// markReopened drops the terminal marker of a node that was put back to ready.
func (s *dynamicRunState) markReopened(nodeID string) {
	if s == nil {
		return
	}
	delete(s.terminal, strings.TrimSpace(nodeID))
	// A reopened node is ready and will be claimed, so the pump must not skip it.
	s.claimable++
}

// hasClaimable reports whether the pump could have anything to hand over.
func (s *dynamicRunState) hasClaimable() bool {
	return s != nil && s.claimable > 0
}

// noteClaimed records that one claimable node was handed to the dispatcher.
func (s *dynamicRunState) noteClaimed() {
	if s == nil {
		return
	}
	if s.claimable > 0 {
		s.claimable--
	}
}

// noteNothingClaimable records that a claim attempt proved no node is ready to run.
func (s *dynamicRunState) noteNothingClaimable() {
	if s == nil {
		return
	}
	s.claimable = 0
}

// allMaterialized reports whether every compiled template has a persisted node.
func (s *dynamicRunState) allMaterialized() bool {
	return s != nil && len(s.known) >= s.templateCount
}

// couldBeFinished is the cheap gate for the authoritative completion probe: it only
// lets the database be read once every node exists and none is known to be running.
// Because the probe still decides, a stale view can cost one extra query but never a
// wrong verdict.
func (s *dynamicRunState) couldBeFinished() bool {
	if s == nil {
		return false
	}
	return s.allMaterialized() && len(s.terminal) >= len(s.known)
}

// unsettledCandidates drops the candidate templates whose scheduling decision is
// already final for this run.
//
// It exists so the watchdog can skip its database read entirely when nothing is left
// to decide, which is the common case for a wide graph: the ready/blocked set stops
// changing long before the run ends.
func (o *dynamicDagOrchestratorV2) unsettledCandidates(candidates []string, state *dynamicRunState) []string {
	if state == nil || len(state.settled) == 0 || len(candidates) == 0 {
		return candidates
	}
	filtered := make([]string, 0, len(candidates))
	for _, nodeID := range candidates {
		if state.isSettled(strings.TrimSpace(nodeID)) {
			continue
		}
		filtered = append(filtered, nodeID)
	}
	return filtered
}

// applyRuntimeEvent advances the in-memory dependency and accounting views for one
// runtime event and returns the templates whose scheduling decision may change
// because of it.
func (o *dynamicDagOrchestratorV2) applyRuntimeEvent(evt dagruntime.RuntimeEvent, dep *dynamicDependencyManager, state *dynamicRunState) []string {
	nodeID := strings.TrimSpace(evt.NodeID)
	switch strings.TrimSpace(evt.Name) {
	case dagruntime.EventNodeCompleted:
		state.markTerminal(nodeID)
		return dep.OnNodeSuccess(nodeID)
	case dagruntime.EventNodeFailed:
		state.markTerminal(nodeID)
		return dep.OnNodeFailure(nodeID)
	default:
		return nil
	}
}

// drainRuntimeEvents folds the event that woke the loop together with every event
// already buffered in the sink into a single batch.
//
// Batching is what keeps an event storm from amplifying into the database: the
// reconciler and the completion probe are triggered once per burst instead of once
// per event, so a hundred completions arriving together cost a handful of queries
// instead of hundreds.
//
// The dirty result reports that events were dropped (or that the sink overflowed),
// which makes the batch incomplete by definition: the caller must re-derive state
// from the persisted nodes instead of trusting it.
func (o *dynamicDagOrchestratorV2) drainRuntimeEvents(
	first dagruntime.RuntimeEvent,
	sink *dagruntime.AnalysisEventSink,
	dep *dynamicDependencyManager,
	state *dynamicRunState,
) ([]string, bool) {
	touched := make(map[string]struct{})
	collect := func(nodeIDs []string) {
		for _, nodeID := range nodeIDs {
			if nodeID = strings.TrimSpace(nodeID); nodeID != "" {
				touched[nodeID] = struct{}{}
			}
		}
	}

	collect(o.applyRuntimeEvent(first, dep, state))

	exhausted := false
	for consumed := 0; consumed < dynamicV2EventDrainLimit && !exhausted; consumed++ {
		select {
		case evt, ok := <-sink.Events():
			if !ok {
				exhausted = true
				continue
			}
			collect(o.applyRuntimeEvent(evt, dep, state))
		case <-sink.Wake():
			// Overflow signal: ConsumeDirty below turns it into a full resync.
		default:
			exhausted = true
		}
	}

	if sink.ConsumeDirty() {
		return nil, true
	}
	return dynamicSortedKeys(touched), false
}

// observeRunState performs the single authoritative read of a run's node set and
// rebuilds the in-memory dependency and accounting views from it.
//
// Rebuilding instead of patching is the whole point of the level-triggered design:
// the views stay correct even when runtime events were dropped, and it costs the same
// single query the completion probe already needed.
func (o *dynamicDagOrchestratorV2) observeRunState(
	ctx context.Context,
	analysisID int64,
	dep *dynamicDependencyManager,
	state *dynamicRunState,
) ([]*types.AnalysisNode, error) {
	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return nil, err
	}
	state.resync(nodes)
	if dep != nil {
		byNodeID := make(map[string]*types.AnalysisNode, len(nodes))
		for _, node := range nodes {
			if node == nil {
				continue
			}
			byNodeID[node.NodeID] = node
		}
		dep.Resync(byNodeID)
	}
	return nodes, nil
}

// dynamicRunFinished reports whether an authoritative node set means the run is over,
// and whether it ended with failures.
func dynamicRunFinished(nodes []*types.AnalysisNode, templateCount int) (bool, error) {
	if len(nodes) < templateCount {
		return false, nil
	}
	failed := 0
	for _, node := range nodes {
		if !dynamicIsTerminalNode(node) {
			return false, nil
		}
		if strings.EqualFold(strings.TrimSpace(node.Status), dagruntime.StatusFailed) {
			failed++
		}
	}
	if failed > 0 {
		return true, fmt.Errorf("one or more dynamic dag nodes failed")
	}
	return true, nil
}

// checkDynamicCompletion answers "is the run over?" from authoritative state.
//
// It reads the persisted node set at most once, and only when the in-memory view says
// the run may be over. Dispatch is a direct handoff into the pool, so an empty pool
// remains a necessary precondition, and it is the cheapest check available.
func (o *dynamicDagOrchestratorV2) checkDynamicCompletion(
	ctx context.Context,
	analysisID int64,
	pool *dagruntime.WorkerPool,
	dep *dynamicDependencyManager,
	state *dynamicRunState,
) (bool, error) {
	if pool.QueueLen() != 0 {
		return false, nil
	}
	if !state.couldBeFinished() {
		return false, nil
	}

	nodes, err := o.observeRunState(ctx, analysisID, dep, state)
	if err != nil {
		return false, err
	}
	return dynamicRunFinished(nodes, state.templateCount)
}

// pumpReadyQueue claims ready nodes and hands them straight to the worker pool.
//
// There is deliberately no intermediate ready-queue goroutine: a claim is a
// persisted ready -> submitted transition, so a claimed node must land in the pool
// in the same step. Dispatching from this goroutine, which is the one that owns
// pool.Stop(), also means an enqueue can never race with the pool being closed.
//
// It returns immediately while the run's in-memory view says nothing is claimable. A
// claim is a database round trip, and the pump used to pay for one on every event even
// when the graph had nothing to hand over, so most events cost a query that could only
// ever return nothing.
func (o *dynamicDagOrchestratorV2) pumpReadyQueue(ctx context.Context, runtime *dagruntime.RuntimeEngine, analysisID int64, pool *dagruntime.WorkerPool, state *dynamicRunState) error {
	if !state.hasClaimable() {
		return nil
	}

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
			// The claim proved nothing is left to run, which is exactly the fact the gate
			// above needs in order to skip the next pump.
			state.noteNothingClaimable()
			return nil
		}
		state.noteClaimed()
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

// reconcileDynamicCandidates materializes the candidates whose dependencies allow it
// and re-applies the cache policy to the ones already persisted.
//
// It is idempotent, and it skips every node whose scheduling decision is already final
// for this run (state.settled). That memo is what keeps a periodic full-graph pass from
// repeating the expensive cache probe.
func (o *dynamicDagOrchestratorV2) reconcileDynamicCandidates(
	ctx context.Context,
	analysis *types.Analysis,
	nodeTemplateByID map[string]map[string]any,
	incoming map[string][]*types.AnalysisEdge,
	candidates []string,
	dep *dynamicDependencyManager,
	state *dynamicRunState,
) error {
	// Filter before the read, not inside the loop: a batch that only names nodes whose
	// decision is already final must not cost a query at all.
	candidates = o.unsettledCandidates(candidates, state)
	if len(candidates) == 0 {
		return nil
	}

	existingNodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysis.ID)
	if err != nil {
		return err
	}
	existingByNodeID := make(map[string]*types.AnalysisNode, len(existingNodes))
	for _, n := range existingNodes {
		existingByNodeID[n.NodeID] = n
	}

	queue := append([]string(nil), candidates...)
	processed := map[string]struct{}{}
	newItems := make([]*types.AnalysisNode, 0)
	for len(queue) > 0 {
		nodeID := strings.TrimSpace(queue[0])
		queue = queue[1:]
		if nodeID == "" {
			continue
		}
		if _, seen := processed[nodeID]; seen {
			continue
		}
		processed[nodeID] = struct{}{}

		// A settled node already had its decision taken, and that decision cannot
		// change, so probing it again would only repeat disk I/O.
		if state.isSettled(nodeID) {
			continue
		}

		if existingNode, exists := existingByNodeID[nodeID]; exists {
			row, _ := nodeTemplateByID[nodeID]
			settled, existingErr := o.reconcileExistingNodeByCacheType(ctx, analysis, existingNode, row, incoming[nodeID], existingByNodeID, dep, state)
			if existingErr != nil {
				return existingErr
			}
			if settled {
				state.settle(nodeID)
			}
			continue
		}
		row, ok := nodeTemplateByID[nodeID]
		if !ok {
			continue
		}

		status := ""
		errorMessage := ""
		if dep.IsBlocked(nodeID) {
			status = dagruntime.StatusSkipped
			errorMessage = "blocked by failed upstream dependency"
		} else if dep.IsReady(nodeID) {
			status = dagruntime.StatusReady
		} else {
			continue
		}
		scriptId := dynamicToString(row["script_id"])
		script, err := o.workflowRepo.GetScriptByScriptID(ctx, analysis.ProjectID, scriptId)
		if err != nil {
			return err
		}

		node, buildErr := o.buildDynamicAnalysisNode(script, analysis, nodeID, row, incoming[nodeID], existingByNodeID, status, errorMessage)
		if buildErr != nil {
			return buildErr
		}
		// No runtime artifacts are produced here on purpose. The node is about to be
		// claimed and handed to NodeDispatcher, which prepares run.sh / params.json
		// exactly once (and cleans the output directory right before executing).
		// Preparing here as well would duplicate that work for every node, and for a
		// skipped node - which never executes - it would be pure waste.
		// The digests the cache policies compare against are captured by the
		// dispatcher after it prepares the node for execution.
		newItems = append(newItems, node)
		existingByNodeID[nodeID] = node
		// Materializing a node is itself the scheduling decision for it.
		state.markCreated(node)

		if status == dagruntime.StatusSkipped {
			queue = append(queue, dep.OnNodeFailure(nodeID)...)
		}
	}

	if len(newItems) == 0 {
		return nil
	}
	return o.repo.CreateAnalysisNodes(ctx, newItems)
}

// reconcileExistingNodeByCacheType applies the cache policy of the analysis to a
// node persisted by a previous run. Only the two fingerprint policies need the
// scheduler to produce artifacts, and they do so through the narrow
// NodeArtifactFingerprinter, never through the execution-time preparer.
func (o *dynamicDagOrchestratorV2) reconcileExistingNodeByCacheType(
	ctx context.Context,
	analysis *types.Analysis,
	existingNode *types.AnalysisNode,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
	dep *dynamicDependencyManager,
	state *dynamicRunState,
) (bool, error) {
	if analysis == nil || existingNode == nil {
		return false, nil
	}

	nodeID := strings.TrimSpace(existingNode.NodeID)
	if nodeID == "" {
		return false, nil
	}

	// The readiness gates come before the cache switch on purpose: they are the cheap
	// conditions, and only a node that is actually decidable may pay for the script load
	// and the fingerprint probe below. Loading the script first made every not-ready
	// candidate issue a query on every reconcile pass.
	if dep != nil {
		if dep.IsBlocked(nodeID) {
			// Blocked is final for this scheduler, so no probe can change anything.
			return true, nil
		}
		if !dep.IsReady(nodeID) {
			// Upstreams are still in flight, so the node's inputs - and therefore its
			// fingerprint - can still change: this decision must not be settled yet.
			return false, nil
		}
	}

	switch analysis.CacheType {
	case types.CacheTypeReuseExistingNode:
		// Nothing to compare: reuse whatever was persisted.
		return true, nil
	case types.CacheTypeReuseWhenScriptUnchanged, types.CacheTypeReuseWhenScriptAndParamsUnchanged:
		// The script is only loaded for the fingerprint policies: it is the input the
		// probe needs, and loading it here keeps reuse_existing free of that query.
		script, err := o.workflowRepo.GetScriptByScriptID(ctx, analysis.ProjectID, dynamicToString(row["script_id"]))
		if err != nil {
			return false, err
		}
		requireParamsMD5 := analysis.CacheType == types.CacheTypeReuseWhenScriptAndParamsUnchanged
		return o.reconcileExistingNodeByMD5Policy(ctx, script, existingNode, row, incomingEdges, existingByNodeID, requireParamsMD5, state)
	default:
		// Unknown cache types stay on the safe side: reuse the persisted node.
		return true, nil
	}
}

// reconcileExistingNodeByMD5Policy decides whether a persisted node can be reused.
// It reports whether the decision is final for this run, so the caller can stop
// revisiting the node.
func (o *dynamicDagOrchestratorV2) reconcileExistingNodeByMD5Policy(
	ctx context.Context,
	script *types.Script,
	existingNode *types.AnalysisNode,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
	requireParamsMD5 bool,
	state *dynamicRunState,
) (bool, error) {
	if existingNode == nil || row == nil {
		return false, nil
	}

	nodeID := strings.TrimSpace(existingNode.NodeID)
	if nodeID == "" {
		return false, nil
	}

	status := strings.ToLower(strings.TrimSpace(existingNode.Status))
	if status == dagruntime.StatusRunning || status == dagruntime.StatusSubmitted {
		// In flight: whether it is reusable is only decided once it reports back.
		return false, nil
	}
	if status == dagruntime.StatusReady && !existingNode.CacheHit {
		// Already queued for execution; the decision was taken when it was queued.
		return true, nil
	}

	probe := *existingNode
	probe.NodeName = dynamicToString(row["node_name"])
	probe.SampleID = dynamicToString(row["sample_id"])
	probe.Executor = dynamicToString(row["executor"])
	probe.ScriptID = script.ID
	probe.InputsPatterns = dynamicToJSONMap(row["inputs_patterns"])
	probe.OutputPatterns = dynamicToJSONMap(row["output_patterns"])
	probe.Params = dynamicToJSONMap(row["params"])
	probe.ResolvedInputs = dynamicToJSONMap(row["resolved_inputs"])
	probe.ResolvedOutputs = dynamicToJSONMap(row["resolved_outputs"])
	bootstrapInputsFromUpstream(row, probe.Params, probe.ResolvedInputs, incomingEdges, existingByNodeID)

	if err := o.fingerprintNodeArtifacts(ctx, &probe); err != nil {
		return false, err
	}

	commandMatched := strings.TrimSpace(probe.CommandMD5) == strings.TrimSpace(existingNode.CommandMD5)
	paramsMatched := strings.TrimSpace(probe.ParamsMD5) == strings.TrimSpace(existingNode.ParamsMD5)
	if commandMatched && (!requireParamsMD5 || paramsMatched) {
		return true, nil
	}

	rerunReason := buildNodeRerunReason(commandMatched, paramsMatched, requireParamsMD5)
	if err := o.markExistingNodeReadyForRerun(ctx, existingNode.AnalysisNodeID, &probe, rerunReason); err != nil {
		return false, err
	}
	// The node is no longer terminal: it is queued to run again.
	state.markReopened(nodeID)
	return true, nil
}

func buildNodeRerunReason(commandMatched bool, paramsMatched bool, requireParamsMD5 bool) string {
	if !commandMatched && requireParamsMD5 && !paramsMatched {
		return "command and params changed"
	}
	if !commandMatched {
		return "command changed"
	}
	if requireParamsMD5 && !paramsMatched {
		return "params changed"
	}
	return "node cache invalidated"
}

func (o *dynamicDagOrchestratorV2) markExistingNodeReadyForRerun(ctx context.Context, analysisNodeID string, probe *types.AnalysisNode, rerunReason string) error {
	if strings.TrimSpace(analysisNodeID) == "" || probe == nil {
		return nil
	}
	rerunReason = strings.TrimSpace(rerunReason)
	if rerunReason == "" {
		rerunReason = "node cache invalidated"
	}
	return o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, analysisNodeID, map[string]any{
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
	})
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

func dynamicSortedKeys(items map[string]struct{}) []string {
	out := make([]string, 0, len(items))
	for key := range items {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
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
