package orchestratorv2

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/biox-dev/gobrave/internal/compiler"
	"github.com/biox-dev/gobrave/internal/config"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// Dependencies is the explicit dependency set of the orchestrator.
//
// It exists so the package can be constructed from a DI container (see
// NewDynamicDagOrchestratorV2) as well as from tests, without a 10-argument
// positional constructor.
type Dependencies struct {
	// Analyses owns analysis / analysis_node / analysis_edge persistence.
	Analyses interfaces.AnalysisRepository
	// Workflows resolves script metadata.
	Workflows interfaces.WorkflowRepository
	// WorkflowService resolves script files for the runtime preparer.
	WorkflowService interfaces.WorkflowService
	// Containers is reserved for future node/container lifecycle work.
	Containers interfaces.ContainerRepository
	// ContainerManager is reserved for future node/container lifecycle work.
	ContainerManager *manager.ContainerManager
	// Projects resolves project metadata for the runtime preparer.
	Projects interfaces.ProjectRepository
	// RunScriptBuilders generates run scripts per script type.
	RunScriptBuilders *prepare.RunScriptBuilderRegistry
	// Dispatcher executes a single claimed node.
	Dispatcher *dagruntime.NodeDispatcher
	// Config provides storage roots and runtime options.
	Config *config.Config
	// Bus is the shared runtime event bus.
	Bus event.Bus
}

// Orchestrator is the dynamic DAG scheduler facade.
//
// It implements interfaces.DynamicDagOrchestrator and is safe for concurrent
// use: every run owns its own execution state, while the lease, event bridge
// and running registry are shared.
type Orchestrator struct {
	repo              interfaces.AnalysisRepository
	workflowRepo      interfaces.WorkflowRepository
	workflowService   interfaces.WorkflowService
	containerRepo     interfaces.ContainerRepository
	containerMgr      *manager.ContainerManager
	projectRepo       interfaces.ProjectRepository
	runScriptBuilders *prepare.RunScriptBuilderRegistry
	dispatcher        *dagruntime.NodeDispatcher
	cfg               *config.Config

	opts      Options
	lease     LeaseKeeper
	events    *EventBridge
	publisher *Publisher
	registry  *dagruntime.RunningRegistry
}

// NewDynamicDagOrchestratorV2 is the DI entry point registered by the
// application container.
//
// The signature intentionally matches the legacy service-level constructor so
// container wiring is a one-line replacement.
func NewDynamicDagOrchestratorV2(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	workflowService interfaces.WorkflowService,
	containerRepo interfaces.ContainerRepository,
	containerMgr *manager.ContainerManager,
	projectRepo interfaces.ProjectRepository,
	runScriptBuilders *prepare.RunScriptBuilderRegistry,
	dispatcher *dagruntime.NodeDispatcher,
	cfg *config.Config,
	bus event.Bus,
) interfaces.DynamicDagOrchestrator {
	return New(Dependencies{
		Analyses:          repo,
		Workflows:         workflowRepo,
		WorkflowService:   workflowService,
		Containers:        containerRepo,
		ContainerManager:  containerMgr,
		Projects:          projectRepo,
		RunScriptBuilders: runScriptBuilders,
		Dispatcher:        dispatcher,
		Config:            cfg,
		Bus:               bus,
	})
}

// New builds an Orchestrator with the given dependencies and options.
func New(deps Dependencies, opts ...Option) *Orchestrator {
	resolved := defaultOptions()
	resolved.apply(opts...)
	resolved = resolved.normalise()

	return &Orchestrator{
		repo:              deps.Analyses,
		workflowRepo:      deps.Workflows,
		workflowService:   deps.WorkflowService,
		containerRepo:     deps.Containers,
		containerMgr:      deps.ContainerManager,
		projectRepo:       deps.Projects,
		runScriptBuilders: deps.RunScriptBuilders,
		dispatcher:        deps.Dispatcher,
		cfg:               deps.Config,
		opts:              resolved,
		lease:             NewRepositoryLease(deps.Analyses, resolved.LeaseTTL, resolved.HeartbeatInterval),
		events:            NewEventBridge(deps.Bus),
		publisher:         NewPublisher(deps.Bus),
		registry:          dagruntime.NewRunningRegistry(),
	}
}

// StartAsyncV2 compiles the workflow and starts the scheduling loop in the
// background, returning as soon as the run has been accepted.
//
// Duplicate submissions are absorbed: an in-process run or a live cross
// instance lease makes the call a no-op, matching the legacy behaviour.
func (o *Orchestrator) StartAsyncV2(
	ctx context.Context,
	analysisID int64,
	parseAnalysisResult map[string]any,
	dagDefinition map[string]any,
) error {
	if analysisID <= 0 {
		return fmt.Errorf("analysis_id is required")
	}
	if o.registry.IsRunning(analysisID) {
		return nil
	}

	acquired, err := o.lease.Acquire(ctx, analysisID)
	if err != nil {
		return fmt.Errorf("acquire dag running lease failed: %w", err)
	}
	if !acquired {
		// Another live scheduler owns this analysis.
		return nil
	}

	graph, err := compileGraph(analysisID, parseAnalysisResult, dagDefinition)
	if err != nil {
		o.failSubmission(analysisID, err)
		return fmt.Errorf("compile dynamic dag failed: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	o.registry.Register(&dagruntime.RunningEntry{
		AnalysisID:     analysisID,
		TaskName:       fmt.Sprintf("dag-v2-run-%d", analysisID),
		MaxConcurrency: o.opts.Workers,
		QueueSize:      o.opts.ReadyQueueSize,
		PollIntervalMs: o.opts.StopCheckInterval.Milliseconds(),
		Status:         statusRunning,
		Cancel:         cancel,
	})

	go o.supervise(runCtx, cancel, analysisID, graph)
	return nil
}

// GetRunningInfo exposes the in-memory running entry so the runtime snapshot
// endpoint can report running_info like the Python implementation.
func (o *Orchestrator) GetRunningInfo(_ context.Context, analysisID int64) (*interfaces.DagRunningInfo, error) {
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

// RequestStop asks the in-process run to stop. It satisfies
// interfaces.DynamicDagOrchestrator; the persisted job_status flag remains the
// cross-instance stop mechanism.
func (o *Orchestrator) RequestStop(analysisID int64) bool {
	if o.registry == nil {
		return false
	}
	return o.registry.RequestStop(analysisID)
}

// RecoverRunningAnalyses satisfies interfaces.DynamicDagOrchestrator.
//
// This implementation is not registered in the DI container, so it never persists
// scheduler_mode = dynamic_v2 and owns no analyses to recover. Recovery is served by
// the wired orchestrator in internal/dag/dag_orchestrator_v2 (see its recovery_v2.go),
// which the container recovery loop dispatches to by scheduler_mode.
func (o *Orchestrator) RecoverRunningAnalyses(_ context.Context, _ *types.Analysis) (bool, error) {
	return false, nil
}

// supervise owns the run lifecycle: lease heartbeat, terminal status
// resolution, runtime events and persistence of the final job status.
func (o *Orchestrator) supervise(runCtx context.Context, cancel context.CancelFunc, analysisID int64, graph *Graph) {
	defer cancel()

	heartbeatStop := make(chan struct{})
	go o.lease.KeepAlive(runCtx, analysisID, heartbeatStop)
	defer close(heartbeatStop)

	o.publisher.PublishDag(dagruntime.EventDagStarted, analysisID, nil)

	status := statusFinished
	var runErr error
	switch err := o.execute(runCtx, analysisID, graph); {
	case errors.Is(err, errRunStopped):
		status = statusStopped
	case err != nil:
		status = statusFailed
		runErr = err
		logger.Warnf(context.Background(), "[DagOrchestratorV2] run failed, analysis_id=%d err=%v", analysisID, err)
	}
	if o.registry.IsStopping(analysisID) {
		// An explicit in-process stop request wins over the loop result.
		status = statusStopped
		runErr = nil
	}

	o.registry.MarkFinished(analysisID, status)
	o.publishTerminalStatus(analysisID, status, runErr)
	o.persistJobStatus(analysisID, status)
}

// publishTerminalStatus emits dag.completed or dag.failed and notifies users.
func (o *Orchestrator) publishTerminalStatus(analysisID int64, status string, runErr error) {
	if status == statusFinished {
		o.publisher.PublishDag(dagruntime.EventDagCompleted, analysisID, map[string]any{"status": status})
		return
	}
	payload := map[string]any{"status": status}
	switch {
	case status == statusStopped:
		payload["reason"] = "stopped"
	case runErr != nil:
		payload["reason"] = runErr.Error()
	}
	o.publisher.PublishDag(dagruntime.EventDagFailed, analysisID, payload)
}

// persistJobStatus writes the terminal job status onto the analysis row.
func (o *Orchestrator) persistJobStatus(analysisID int64, status string) {
	if err := o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
		"job_status": status,
		"updated_at": time.Now().UTC(),
	}); err != nil {
		logger.Warnf(context.Background(), "[DagOrchestratorV2] persist job status failed, analysis_id=%d status=%s err=%v", analysisID, status, err)
	}
}

// failSubmission marks a rejected submission as failed.
func (o *Orchestrator) failSubmission(analysisID int64, cause error) {
	if err := o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
		"job_status": statusFailed,
		"updated_at": time.Now().UTC(),
	}); err != nil {
		logger.Warnf(context.Background(), "[DagOrchestratorV2] persist failed job status failed, analysis_id=%d err=%v", analysisID, err)
	}
	o.publisher.PublishDag(dagruntime.EventDagFailed, analysisID, map[string]any{"reason": cause.Error()})
}

// compileGraph runs the runtime compiler and wraps the result in a Graph.
func compileGraph(analysisID int64, parseAnalysisResult, dagDefinition map[string]any) (*Graph, error) {
	compiled, err := compiler.BuildRuntimeTasks(
		analysisID,
		cloneAnyMap(parseAnalysisResult),
		cloneAnyMap(dagDefinition),
	)
	if err != nil {
		return nil, err
	}
	return NewGraph(analysisID, compiled)
}
