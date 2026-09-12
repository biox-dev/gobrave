package dataflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

const (
	// dataflowV3LeaseTTL is how long a run's lease is considered valid without a heartbeat.
	dataflowV3LeaseTTL = 90 * time.Second
	// dataflowV3Heartbeat renews analysis.updated_at while the run is active.
	dataflowV3Heartbeat = 15 * time.Second
	// dataflowV3StopCheckInterval is how often the persisted stop flag is polled.
	dataflowV3StopCheckInterval = 1 * time.Second
)

// dataflowDagOrchestratorV3 is the framework entry for a Nextflow-like
// data-driven scheduler.
//
// Current stage:
// - Build V3 dataflow planning skeleton.
// - Keep existing frontend payload, executor path, and analysis persistence untouched.
// - Delegate runtime execution to V2 for safe rollout.
type dataflowDagOrchestratorV3 struct {
	bus             event.Bus
	repo            interfaces.AnalysisRepository
	workflowRepo    interfaces.WorkflowRepository
	workflowService interfaces.WorkflowService
	projectRepo     interfaces.ProjectRepository
	containerMgr    *manager.ContainerManager
	dispatcher      *dagruntime.NodeDispatcher
	// runScriptBuilders map[string]prepare.RunScriptBuilder
	cfg *config.Config
	// router is the process-wide runtime event subscriber injected by the
	// container. Each run registers an analysis-scoped sink instead of
	// subscribing to the bus directly, so concurrent runs never add subscribers.
	router *dagruntime.EventRouter
	// cachePolicies resolves analysis.cache_type to the shared cache policy used by
	// every scheduler (internal/dag), so the dataflow and dynamic schedulers can
	// never disagree on what a cache type means.
	cachePolicies *dagruntime.CachePolicyRegistry
	// fingerprinter renders the artifacts that would run now (run.sh / params.json)
	// and records their digests, so the fingerprint cache types (3/4) can tell
	// whether a persisted instance is still reusable.
	fingerprinter dagruntime.NodeArtifactFingerprinter
}

func NewDataflowDagOrchestratorV3(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	workflowService interfaces.WorkflowService,
	containerMgr *manager.ContainerManager,
	projectRepo interfaces.ProjectRepository,
	dispatcher *dagruntime.NodeDispatcher,
	fingerprinter dagruntime.NodeArtifactFingerprinter,
	// runScriptBuilders map[string]prepare.RunScriptBuilder,
	cfg *config.Config,
	bus event.Bus,
	router *dagruntime.EventRouter,
) interfaces.DagOrchestrator {
	return &dataflowDagOrchestratorV3{
		bus:             bus,
		repo:            repo,
		workflowRepo:    workflowRepo,
		workflowService: workflowService,
		dispatcher:      dispatcher,
		containerMgr:    containerMgr,
		projectRepo:     projectRepo,
		// runScriptBuilders: runScriptBuilders,
		cfg:           cfg,
		router:        router,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
		fingerprinter: fingerprinter,
	}
}

// Name implements interfaces.DagOrchestrator. It is the persisted
// analysis.scheduler_mode value and the registry key for this scheduler.
func (o *dataflowDagOrchestratorV3) Name() string { return types.SchedulerModeDataflow }

// PersistsGraphOnSave implements interfaces.DagOrchestrator: the dataflow
// scheduler materializes process instances at runtime, so no graph is persisted
// at save time.
func (o *dataflowDagOrchestratorV3) PersistsGraphOnSave() bool { return false }

// resolveProjectID looks up the project that owns the analysis so the dataflow
// runtime can resolve workspace paths. The unified scheduler entry point does not
// receive a project id, so it is read from the persisted analysis record.
func (o *dataflowDagOrchestratorV3) resolveProjectID(ctx context.Context, analysisID int64) int64 {
	if o == nil || o.repo == nil || analysisID <= 0 {
		return 0
	}
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil || analysis == nil {
		return 0
	}
	return analysis.ProjectID
}

// StartAsync implements interfaces.DagOrchestrator and launches a dataflow run
// in the background, returning immediately.
func (o *dataflowDagOrchestratorV3) StartAsync(ctx context.Context, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any) error {
	if analysisID <= 0 {
		return fmt.Errorf("analysis_id is required")
	}

	bgCtx := context.Background()
	projectID := o.resolveProjectID(bgCtx, analysisID)

	// Acquire the cross-instance running lease. This is also what transitions the
	// analysis from created/updated to running, so a run that never reaches the
	// loop is still observable as running until it finalizes below.
	if o.repo != nil {
		now := time.Now().UTC()
		locked, err := o.repo.TryMarkAnalysisRunning(bgCtx, analysisID, now, now.Add(-dataflowV3LeaseTTL))
		if err != nil {
			return err
		}
		if !locked {
			// Lease is already held by another active scheduler; do not double-run.
			logger.Warnf(bgCtx, "[DataflowDagOrchestratorV3] analysis already running, skip start, analysis_id=%d", analysisID)
			return nil
		}
	}

	// Register the analysis-scoped sink before the first event is published so no
	// runtime event for this run is observed without a sink.
	sink := o.router.RegisterWithFilter(analysisID, dagruntime.SchedulerEventFilter)

	o.publishDagRuntimeEvent(dagruntime.EventDagStarted, analysisID, nil)

	heartbeatStop := make(chan struct{})
	go o.renewRunningLease(analysisID, heartbeatStop)

	go func() {
		defer o.router.Unregister(analysisID)
		defer close(heartbeatStop)

		finalStatus := types.AnalysisStatusFinished
		var finalErr error
		if err := o.runStartAsyncV3(bgCtx, projectID, analysisID, sink, parseAnalysisResult, dagDefinition); err != nil {
			finalStatus = types.AnalysisStatusFailed
			finalErr = err
			logger.Errorf(bgCtx, "[DataflowDagOrchestratorV3] async run failed, analysis_id=%d err=%v", analysisID, err)
		}
		if o.isStopRequested(bgCtx, analysisID) {
			// A persisted stop request wins over the loop result.
			finalStatus = types.AnalysisStatusStopped
			finalErr = nil
		}

		if finalStatus == types.AnalysisStatusFinished {
			o.publishDagRuntimeEvent(dagruntime.EventDagCompleted, analysisID, map[string]any{"status": finalStatus})
		} else {
			payload := map[string]any{"status": finalStatus}
			if finalErr != nil {
				payload["reason"] = finalErr.Error()
			}
			if finalStatus == types.AnalysisStatusStopped {
				payload["reason"] = "stopped"
			}
			o.publishDagRuntimeEvent(dagruntime.EventDagFailed, analysisID, payload)
		}

		o.markAnalysisStatus(bgCtx, analysisID, finalStatus)
	}()

	return nil
}

func (o *dataflowDagOrchestratorV3) runStartAsyncV3(ctx context.Context, projectID int64, analysisID int64, sink *dagruntime.AnalysisEventSink, parseAnalysisResult map[string]any, dagDefinition map[string]any) error {
	if err := o.prepareAnalysisByCacheTypeV3(ctx, analysisID); err != nil {
		return fmt.Errorf("prepare analysis by cache_type failed: %w", err)
	}

	spec := o.buildGraphSpec(analysisID, dagDefinition)
	o.logFrameworkPhase(ctx, spec)

	// runtimeEngine := dagruntime.NewRuntimeEngine(o.repo)
	// storageBase := ""
	// if o.cfg != nil && o.cfg.Storage != nil {
	// 	storageBase = strings.TrimSpace(o.cfg.Storage.BaseDir)
	// }
	// preparer := dagruntime.NewFileSystemNodeRuntimePreparerWithBuilders(o.repo, o.workflowRepo, o.projectRepo, storageBase, o.runScriptBuilders)
	// dispatcher := dagruntime.NewNodeDispatcher(
	// 	runtimeEngine,
	// 	o.repo,
	// 	o.bus,
	// 	executor.NewFactory(executor.FactoryDeps{
	// 		WorkflowRepository: o.workflowRepo,
	// 		ContainerManager:   o.containerMgr,
	// 	}),
	// 	nil,
	// 	preparer,
	// )

	// Runtime events reach this loop through the process-wide router via the
	// analysis-scoped sink registered by the caller; no per-run bus subscription is
	// created here. The filter restricts the sink to node terminal transitions, which
	// are the only events this loop acts on (see onRuntimeEvent), so noise never wakes it.
	var kernel *dataflowKernel
	runtime := &persistentDataflowRuntime{
		repo:       o.repo,
		dispatcher: o.dispatcher,
		onNodeSubmitChange: func(nodeID string, delta int) {
			if kernel == nil {
				return
			}
			kernel.adjustSubmittedCount(nodeID, delta)
		},
		onInstanceReused: func(ctx context.Context, node *types.AnalysisNode) error {
			if kernel == nil {
				return nil
			}
			return kernel.onInstanceReused(ctx, node)
		},
		buildPersistParams: func(req DataflowProcessRunRequest) (*DataflowAnalysisNodePersistParams, bool) {
			if kernel == nil {
				return nil, false
			}
			return kernel.buildAnalysisNodePersistParams(req)
		}, workflowRepo: o.workflowRepo,
		projectID:     projectID,
		cachePolicies: o.cachePolicies,
		fingerprinter: o.fingerprinter,
	}
	kernel = newDataflowKernel(spec, runtime, parseAnalysisResult)
	kernel.repo = o.repo
	if err := kernel.bootstrapSourceProcesses(ctx); err != nil {
		return err
	}
	// if err := kernel.closeAll(ctx); err != nil {
	// 	return err
	// }

	idleWindow := 200 * time.Millisecond
	idleTimer := time.NewTimer(idleWindow)
	defer idleTimer.Stop()

	// Poll the persisted stop flag set by external control APIs so a stop request
	// converges the run instead of being ignored until completion.
	stopTicker := time.NewTicker(dataflowV3StopCheckInterval)
	defer stopTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stopTicker.C:
			if o.isStopRequested(ctx, analysisID) {
				return nil
			}
		case evt := <-sink.Events():
			runtime.onRuntimeEvent(evt)
			eventName := strings.TrimSpace(evt.Name)
			switch eventName {
			case dagruntime.EventNodeCompleted:
				if err := kernel.onNodeCompleted(ctx, evt.AnalysisNodeID); err != nil {
					return err
				}
			case dagruntime.EventNodeFailed:
				if err := kernel.onNodeFailed(ctx, evt.AnalysisNodeID); err != nil {
					return err
				}
			default:
				continue
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleWindow)
		case <-idleTimer.C:
			inflight := runtime.InflightDispatches()
			if inflight == 0 {
				select {
				case evt := <-sink.Events():
					runtime.onRuntimeEvent(evt)
					eventName := strings.TrimSpace(evt.Name)
					switch eventName {
					case dagruntime.EventNodeCompleted:
						if err := kernel.onNodeCompleted(ctx, evt.AnalysisNodeID); err != nil {
							return err
						}
					case dagruntime.EventNodeFailed:
						if err := kernel.onNodeFailed(ctx, evt.AnalysisNodeID); err != nil {
							return err
						}
					}
					idleTimer.Reset(idleWindow)
				default:
					// Channel closure is normally driven by completion events, but a
					// cache-reuse completion can resolve several nodes inside a single
					// synchronous call and the loop never retries closure on its own.
					// Reconcile here so closure always propagates and the run converges.
					if err := kernel.reconcileOutputChannelClosures(ctx); err != nil {
						return err
					}
					inflight = runtime.InflightDispatches()
					if kernel.checkFinished(inflight) {
						return nil
					}
					idleTimer.Reset(idleWindow)
				}
				continue
			}
			idleTimer.Reset(idleWindow)
		}
	}
}

// markAnalysisStatus persists the analysis job_status together with updated_at.
// Terminal statuses (finished/failed/stopped) flow through the same entry as
// running, so every status change is observable in the nextflow table.
func (o *dataflowDagOrchestratorV3) markAnalysisStatus(ctx context.Context, analysisID int64, status string) {
	if o == nil || o.repo == nil || analysisID <= 0 || strings.TrimSpace(status) == "" {
		return
	}
	if err := o.repo.UpdateAnalysisByID(ctx, analysisID, map[string]any{
		"job_status": status,
		"updated_at": time.Now().UTC(),
	}); err != nil {
		logger.Warnf(ctx,
			"[DataflowDagOrchestratorV3] update analysis job_status failed, analysis_id=%d status=%s err=%v",
			analysisID,
			status,
			err,
		)
	}
}

// shouldStopByJobStatus checks the persistent stop flags set by external control APIs.
func (o *dataflowDagOrchestratorV3) shouldStopByJobStatus(ctx context.Context, analysisID int64) (bool, error) {
	if o == nil || o.repo == nil || analysisID <= 0 {
		return false, nil
	}
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return false, err
	}
	if analysis == nil {
		return false, nil
	}
	status := strings.ToLower(strings.TrimSpace(analysis.JobStatus))
	return status == types.AnalysisStatusStopping || status == types.AnalysisStatusStopped, nil
}

// isStopRequested reports whether a persisted stopping/stopped flag was written
// by an external control API (V3 has no in-process stop entry yet).
func (o *dataflowDagOrchestratorV3) isStopRequested(ctx context.Context, analysisID int64) bool {
	stopped, err := o.shouldStopByJobStatus(ctx, analysisID)
	if err != nil {
		logger.Warnf(ctx,
			"[DataflowDagOrchestratorV3] resolve stop flag failed, analysis_id=%d err=%v",
			analysisID,
			err,
		)
		return false
	}
	return stopped
}

// renewRunningLease periodically updates analysis.updated_at so stale-lock
// recovery can tell a live scheduler apart from an abandoned run.
func (o *dataflowDagOrchestratorV3) renewRunningLease(analysisID int64, stop <-chan struct{}) {
	ticker := time.NewTicker(dataflowV3Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if o.repo == nil {
				continue
			}
			_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
				"updated_at": time.Now().UTC(),
			})
		}
	}
}

// publishDagRuntimeEvent emits a DAG lifecycle event so the realtime notifier can
// push started/completed/failed to the frontend, matching V2 and legacy behavior.
func (o *dataflowDagOrchestratorV3) publishDagRuntimeEvent(name string, analysisID int64, payload map[string]any) {
	if o == nil || o.bus == nil || analysisID <= 0 {
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
