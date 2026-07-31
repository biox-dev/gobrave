package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gobravedev/gobrave/internal/compiler"
	"github.com/gobravedev/gobrave/internal/config"
	dagruntime "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/dag/executor"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"github.com/google/uuid"
)

const (
	// dynamicV2LeaseTTL is the staleness window for the DB running lease.
	// If updated_at is older than now-TTL, a new scheduler instance may take over.
	dynamicV2LeaseTTL = 90 * time.Second
	// dynamicV2Heartbeat is the interval for lease renewal while a run is active.
	dynamicV2Heartbeat = 15 * time.Second
	// dynamicV2ReadyQueueSize is the in-memory ready queue capacity before worker dispatch.
	dynamicV2ReadyQueueSize = 64
	// dynamicV2StopCheckInterval is a lightweight stop-flag probe cadence.
	dynamicV2StopCheckInterval = 1 * time.Second
	// dynamicV2WatchdogInterval is a safety net in case runtime events are dropped.
	dynamicV2WatchdogInterval = 5 * time.Second
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
	// containerRepo is reserved for future V2 node/container lifecycle enhancements.
	containerRepo interfaces.ContainerRepository
	// containerMgr is passed into executor factory so container-backed executors are reused.

	projectRepo       interfaces.ProjectRepository
	containerMgr      *manager.ContainerManager
	runScriptBuilders map[string]dagruntime.RunScriptBuilder
	// cfg provides storage roots and runtime options.
	cfg *config.Config
	// bus emits runtime events using the existing event pipeline.
	bus event.Bus

	// registry tracks in-memory running tasks for fast duplicate-run checks and stop state.
	registry *dagruntime.RunningRegistry

	// projectID int64
	// mu is reserved for future critical sections in V2 orchestration state transitions.
	mu sync.Mutex
}

// NewDynamicDagOrchestratorV2 wires a standalone dynamic scheduler entrypoint.
// It is intentionally separate from the legacy orchestrator to keep rollout safe.
func NewDynamicDagOrchestratorV2(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	containerRepo interfaces.ContainerRepository,
	containerMgr *manager.ContainerManager,
	projectRepo interfaces.ProjectRepository,
	runScriptBuilders map[string]dagruntime.RunScriptBuilder,
	cfg *config.Config,
	bus event.Bus,
) interfaces.DynamicDagOrchestrator {
	return &dynamicDagOrchestratorV2{
		repo:              repo,
		workflowRepo:      workflowRepo,
		containerRepo:     containerRepo,
		containerMgr:      containerMgr,
		projectRepo:       projectRepo,
		runScriptBuilders: runScriptBuilders,
		cfg:               cfg,
		bus:               bus,
		registry:          dagruntime.NewRunningRegistry(),
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
		return nil
	}

	o.publishDagRuntimeEvent(dagruntime.EventDagStarted, analysisID, nil)

	if err := o.prepareAnalysisForCacheRerun(ctx, analysisID); err != nil {
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": "failed",
			"updated_at": time.Now().UTC(),
		})
		o.publishDagRuntimeEvent(dagruntime.EventDagFailed, analysisID, map[string]any{"reason": err.Error()})
		return fmt.Errorf("prepare cached analysis rerun failed: %w", err)
	}

	compiled, err := compiler.BuildRuntimeTasks(analysisID, dynamicCloneAnyMap(parseAnalysisResult), dynamicCloneAnyMap(dagDefinition))
	if err != nil {
		// Compile failure is terminal for current submission; mark analysis failed.
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": "failed",
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
		Status:         "running",
		Cancel:         runCancel,
	})

	heartbeatStop := make(chan struct{})
	// Lease renewer runs independently of dispatch loop.
	go o.renewRunningLease(analysisID, heartbeatStop)

	go func() {
		// Ensure heartbeat and context are cleaned up no matter how run exits.
		defer close(heartbeatStop)
		defer runCancel()
		finalStatus := "finished"
		var finalErr error
		if err := o.runDynamicLoop(runCtx, analysisID, nodeTemplates, edgeRows); err != nil {
			finalStatus = "failed"
			finalErr = err
			logger.Warnf(context.Background(), "[DynamicDagOrchestratorV2] run failed, analysis_id=%d err=%v", analysisID, err)
		}
		if o.registry.IsStopping(analysisID) {
			// External stop request wins over loop result.
			finalStatus = "stopped"
		}
		o.registry.MarkFinished(analysisID, finalStatus)
		if finalStatus == "finished" {
			o.publishDagRuntimeEvent(dagruntime.EventDagCompleted, analysisID, map[string]any{"status": finalStatus})
		} else {
			payload := map[string]any{"status": finalStatus}
			if finalErr != nil {
				payload["reason"] = finalErr.Error()
			}
			if finalStatus == "stopped" {
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

type dynamicRuntimeEventHandler struct {
	analysisID int64
	events     chan<- dagruntime.RuntimeEvent
}

func (h *dynamicRuntimeEventHandler) Handle(evt event.Event) {
	runtimeEvt, ok := evt.(dagruntime.RuntimeEvent)
	if !ok || runtimeEvt.AnalysisID != h.analysisID {
		return
	}
	select {
	case h.events <- runtimeEvt:
	default:
	}
}

type dynamicDependencyManager struct {
	waiting  map[string]map[string]struct{}
	blocked  map[string]bool
	outgoing map[string][]string
}

func newDynamicDependencyManager(nodeTemplateByID map[string]map[string]any, outgoing map[string][]string) *dynamicDependencyManager {
	waiting := make(map[string]map[string]struct{}, len(nodeTemplateByID))
	blocked := make(map[string]bool, len(nodeTemplateByID))
	for nodeID, row := range nodeTemplateByID {
		upstream := dynamicToStringSlice(row["upstream_ids"])
		deps := make(map[string]struct{}, len(upstream))
		for _, id := range upstream {
			deps[id] = struct{}{}
		}
		waiting[nodeID] = deps
		blocked[nodeID] = false
	}
	return &dynamicDependencyManager{waiting: waiting, blocked: blocked, outgoing: outgoing}
}

func (m *dynamicDependencyManager) SeedFromExisting(existing map[string]*types.AnalysisNode) {
	for nodeID, node := range existing {
		if node == nil {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(node.Status))
		if status == dagruntime.StatusReady && node.CacheHit {
			_ = m.OnNodeSuccess(nodeID)
			continue
		}
		if dagruntime.IsSuccessStatus(status) {
			_ = m.OnNodeSuccess(nodeID)
			continue
		}
		if dagruntime.IsTerminalStatus(status) {
			_ = m.OnNodeFailure(nodeID)
		}
	}
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

// runDynamicLoop uses runtime events as the primary driver:
// 1) NodeCompleted/NodeFailed events update dependency state.
// 2) Only affected downstream templates are re-evaluated/materialized.
// 3) Ready nodes are claimed and pushed into ReadyQueue for worker dispatch.
func (o *dynamicDagOrchestratorV2) runDynamicLoop(ctx context.Context, analysisID int64, nodeTemplates []map[string]any, edgeRows []map[string]any) error {
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
	storageBase := ""
	if o.cfg != nil && o.cfg.Storage != nil {
		storageBase = strings.TrimSpace(o.cfg.Storage.BaseDir)
	}
	preparer := dagruntime.NewFileSystemNodeRuntimePreparerWithBuilders(o.repo, o.workflowRepo, o.projectRepo, storageBase, o.runScriptBuilders)
	dispatcher := dagruntime.NewNodeDispatcher(
		runtime,
		o.repo,
		o.bus,
		executor.NewFactory(executor.FactoryDeps{
			WorkflowRepository: o.workflowRepo,
			ContainerManager:   o.containerMgr,
		}),
		nil,
		preparer,
	)

	pool := dagruntime.NewWorkerPool(dispatcher, 1, dynamicV2ReadyQueueSize)
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
	dep.SeedFromExisting(existingByNodeID)

	readyQueue := make(chan int64, dynamicV2ReadyQueueSize)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case analysisNodeID := <-readyQueue:
				for {
					if ok := pool.Enqueue(analysisNodeID); ok {
						break
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(25 * time.Millisecond):
					}
				}
			}
		}
	}()

	runtimeEvents := make(chan dagruntime.RuntimeEvent, 256)
	// Subscribe to runtime events for current analysis_id.
	if o.bus != nil {
		o.bus.Subscribe(&dynamicRuntimeEventHandler{analysisID: analysisID, events: runtimeEvents})
	}

	if err := o.reconcileDynamicCandidates(ctx, analysis, nodeTemplateByID, incoming, dep.InitialCandidates(), dep, preparer); err != nil {
		return err
	}
	if err := o.pumpReadyQueue(ctx, runtime, analysisID, readyQueue); err != nil {
		return err
	}

	stopTicker := time.NewTicker(dynamicV2StopCheckInterval)
	defer stopTicker.Stop()
	watchdogTicker := time.NewTicker(dynamicV2WatchdogInterval)
	defer watchdogTicker.Stop()

	for {
		finished, finishedErr := o.checkDynamicCompletion(ctx, analysisID, len(nodeTemplateByID), runtime, pool, readyQueue)
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
			if err := o.reconcileDynamicCandidates(ctx, analysis, nodeTemplateByID, incoming, dep.InitialCandidates(), dep, preparer); err != nil {
				return err
			}
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, readyQueue); err != nil {
				return err
			}
		case evt := <-runtimeEvents:
			var candidates []string
			switch strings.TrimSpace(evt.Name) {
			case dagruntime.EventNodeCompleted:
				candidates = dep.OnNodeSuccess(strings.TrimSpace(evt.NodeID))
			case dagruntime.EventNodeFailed:
				candidates = dep.OnNodeFailure(strings.TrimSpace(evt.NodeID))
			default:
				candidates = nil
			}
			// 算出并落库哪些节点现在可以/不可以跑
			// 作用：对本次事件影响到的候选节点做“对账/物化”。
			// 具体会做的事：
			// 看节点是否已存在于 analysis_node；
			// 若不存在且依赖满足，创建新节点（ready）；
			// 若被失败上游阻断，创建/标记为 skipped；
			// 若已存在，会按 cache 策略判断是否需要重新置为 ready 重跑。

			if len(candidates) > 0 {
				if err := o.reconcileDynamicCandidates(ctx, analysis, nodeTemplateByID, incoming, candidates, dep, preparer); err != nil {
					return err
				}
			}
			// 第二段负责“把可以跑的节点真正送去执行”。
			// 作用：把数据库里当前可执行的 ready 节点“领取（claim）并推入内存 readyQueue”，交给 worker pool 实际执行。
			// 特点：这一步每次事件后都会跑一次（不依赖 candidates 是否为空），确保新变成 ready 的节点尽快被派发，减少调度延迟。
			if err := o.pumpReadyQueue(ctx, runtime, analysisID, readyQueue); err != nil {
				return err
			}
		}
	}
}

func (o *dynamicDagOrchestratorV2) checkDynamicCompletion(
	ctx context.Context,
	analysisID int64,
	templateCount int,
	runtime *dagruntime.RuntimeEngine,
	pool *dagruntime.WorkerPool,
	readyQueue chan int64,
) (bool, error) {
	snapshot, err := runtime.GetSnapshot(ctx, analysisID)
	if err != nil {
		return false, err
	}
	allCreated, err := o.allCandidateNodesCreated(ctx, analysisID, templateCount)
	if err != nil {
		return false, err
	}
	if allCreated && snapshot.IsFinished && pool.QueueLen() == 0 && len(readyQueue) == 0 {
		if snapshot.StatusCount[dagruntime.StatusFailed] > 0 {
			return true, fmt.Errorf("one or more dynamic dag nodes failed")
		}
		return true, nil
	}
	return false, nil
}

func (o *dynamicDagOrchestratorV2) pumpReadyQueue(ctx context.Context, runtime *dagruntime.RuntimeEngine, analysisID int64, readyQueue chan<- int64) error {
	for len(readyQueue) < cap(readyQueue) {
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
		select {
		case readyQueue <- node.ID:
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

func (o *dynamicDagOrchestratorV2) reconcileDynamicCandidates(
	ctx context.Context,
	analysis *types.Analysis,
	nodeTemplateByID map[string]map[string]any,
	incoming map[string][]*types.AnalysisEdge,
	candidates []string,
	dep *dynamicDependencyManager,
	preparer dagruntime.NodeRuntimePreparer,
) error {
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

		if existingNode, exists := existingByNodeID[nodeID]; exists {
			row, _ := nodeTemplateByID[nodeID]
			if existingErr := o.reconcileExistingNodeByCacheType(ctx, analysis, existingNode, row, incoming[nodeID], existingByNodeID, dep, preparer); existingErr != nil {
				return existingErr
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
		if prepErr := prepareNodeArtifactsAndFillMD5(ctx, preparer, node); prepErr != nil {
			return prepErr
		}
		newItems = append(newItems, node)
		existingByNodeID[nodeID] = node

		if status == dagruntime.StatusSkipped {
			queue = append(queue, dep.OnNodeFailure(nodeID)...)
		}
	}

	if len(newItems) == 0 {
		return nil
	}
	return o.repo.CreateAnalysisNodes(ctx, newItems)
}

// reconcileExistingNodeByCacheType is a skeleton entrypoint for existing-node
// cache policy handling. MD5-based decisions are added in a follow-up step.
func (o *dynamicDagOrchestratorV2) reconcileExistingNodeByCacheType(
	ctx context.Context,
	analysis *types.Analysis,
	existingNode *types.AnalysisNode,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
	dep *dynamicDependencyManager,
	preparer dagruntime.NodeRuntimePreparer,
) error {
	if analysis == nil || existingNode == nil {
		return nil
	}

	nodeID := strings.TrimSpace(existingNode.NodeID)
	if nodeID == "" {
		return nil
	}

	if dep != nil && dep.IsBlocked(nodeID) {
		return nil
	}
	scriptId := dynamicToString(row["script_id"])
	script, err := o.workflowRepo.GetScriptByScriptID(ctx, analysis.ProjectID, scriptId)
	if err != nil {
		return err
	}

	switch analysis.CacheType {
	case types.CacheTypeReuseExistingNode:
		return nil
	case types.CacheTypeReuseWhenScriptUnchanged:
		return o.reconcileExistingNodeByMD5Policy(ctx, script, existingNode, row, incomingEdges, existingByNodeID, dep, preparer, false)
	case types.CacheTypeReuseWhenScriptAndParamsUnchanged:
		return o.reconcileExistingNodeByMD5Policy(ctx, script, existingNode, row, incomingEdges, existingByNodeID, dep, preparer, true)
	default:
		return nil
	}
}

func (o *dynamicDagOrchestratorV2) reconcileExistingNodeByMD5Policy(
	ctx context.Context,
	script *types.Script,
	existingNode *types.AnalysisNode,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
	dep *dynamicDependencyManager,
	preparer dagruntime.NodeRuntimePreparer,
	requireParamsMD5 bool,
) error {
	if existingNode == nil || row == nil {
		return nil
	}

	nodeID := strings.TrimSpace(existingNode.NodeID)
	if nodeID == "" {
		return nil
	}
	if dep != nil && !dep.IsReady(nodeID) {
		return nil
	}

	status := strings.ToLower(strings.TrimSpace(existingNode.Status))
	if status == dagruntime.StatusRunning || status == dagruntime.StatusSubmitted {
		return nil
	}
	if status == dagruntime.StatusReady && !existingNode.CacheHit {
		return nil
	}

	probe := *existingNode
	probe.NodeName = dynamicToString(row["node_name"])
	probe.SampleID = dynamicToString(row["sample_id"])
	probe.Executor = dynamicToString(row["executor"])
	// if scriptID := strings.TrimSpace(dynamicToString(row["script_id"])); scriptID != "" {
	// 	probe.ScriptID = scriptID
	// }
	probe.ScriptID = script.ID
	probe.InputsPatterns = dynamicToJSONMap(row["inputs_patterns"])
	probe.OutputPatterns = dynamicToJSONMap(row["output_patterns"])
	probe.Params = dynamicToJSONMap(row["params"])
	probe.ResolvedInputs = dynamicToJSONMap(row["resolved_inputs"])
	probe.ResolvedOutputs = dynamicToJSONMap(row["resolved_outputs"])
	bootstrapInputsFromUpstream(row, probe.Params, probe.ResolvedInputs, incomingEdges, existingByNodeID)

	if err := prepareNodeArtifactsAndFillMD5(ctx, preparer, &probe); err != nil {
		return err
	}

	commandMatched := strings.TrimSpace(probe.CommandMD5) == strings.TrimSpace(existingNode.CommandMD5)
	paramsMatched := strings.TrimSpace(probe.ParamsMD5) == strings.TrimSpace(existingNode.ParamsMD5)
	if commandMatched && (!requireParamsMD5 || paramsMatched) {
		return nil
	}

	rerunReason := buildNodeRerunReason(commandMatched, paramsMatched, requireParamsMD5)
	return o.markExistingNodeReadyForRerun(ctx, existingNode.AnalysisNodeID, &probe, rerunReason)
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

func prepareNodeArtifactsAndFillMD5(ctx context.Context, preparer dagruntime.NodeRuntimePreparer, node *types.AnalysisNode) error {
	if node == nil {
		return fmt.Errorf("analysis node is nil")
	}
	if preparer == nil {
		return fmt.Errorf("node runtime preparer is nil")
	}
	// TODO 这里的 preparer 与 NodeDispatcher 的 preparer 重复执行
	if err := preparer.Prepare(ctx, node); err != nil {
		return fmt.Errorf("prepare dynamic node runtime artifacts failed: %w", err)
	}

	commandMD5, err := fileMD5Hex(node.CommandPath)
	if err != nil {
		return fmt.Errorf("compute run.sh md5 failed: %w", err)
	}
	paramsMD5, err := fileMD5Hex(node.ParamsPath)
	if err != nil {
		return fmt.Errorf("compute params.json md5 failed: %w", err)
	}

	node.CommandMD5 = commandMD5
	node.ParamsMD5 = paramsMD5
	return nil
}

func fileMD5Hex(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := md5.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
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
	outputDir := filepath.Join(workspaceDir, "output")
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
	return status == "stopping" || status == "stopped", nil
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
