package interfaces

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
)

// DagRunningInfo describes an in-flight DAG run tracked by an orchestrator.
// It mirrors Python brave's running_dag_registry entry that is exposed as the
// running_info field of /analysis-runtime/snapshot.
type DagRunningInfo struct {
	AnalysisID     int64     `json:"analysis_id,string"`
	TaskName       string    `json:"task_name"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	MaxConcurrency int       `json:"max_concurrency"`
	QueueSize      int       `json:"queue_size"`
	PollIntervalMs int64     `json:"poll_interval_ms"`
	TimeoutSeconds int64     `json:"timeout_seconds"`
	StopRequested  bool      `json:"stop_requested"`
}

// DagOrchestrator is the single scheduling contract shared by every DAG
// orchestrator implementation (static graph, dynamic materialization, dataflow).
//
// The concrete implementation that advances an analysis is selected at runtime
// from analysis.scheduler_mode through dag/scheduler.Registry, so handlers, the
// stop API and crash recovery all share one entry point instead of hardcoding a
// scheduler per endpoint.
type DagOrchestrator interface {
	// Name returns the stable scheduler name persisted in analysis.scheduler_mode.
	// It is also the registry key, so it must never change once released.
	Name() string

	// StartAsync submits (or resumes) the run for analysisID in the background.
	// parseAnalysisResult and dagDefinition carry the already-parsed request
	// payload so schedulers that materialize nodes at runtime do not have to
	// re-parse the request; schedulers that rely on the persisted graph may
	// ignore them.
	StartAsync(ctx context.Context, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any) error

	// StopAsync asks this scheduler to stop the analysis. Implementations persist
	// the "stopping" signal first and then either cancel the in-process run or
	// converge the analysis to a terminal state themselves.
	StopAsync(ctx context.Context, analysisID int64) error

	// PersistsGraphOnSave reports whether the scheduler needs the compiled
	// analysis graph persisted at save time. Schedulers that materialize nodes at
	// runtime return false and ignore the persisted graph.
	PersistsGraphOnSave() bool

	// RecoverRunningAnalyses adopts a single analysis owned by this scheduler.
	//
	// The container recovery loop fetches every running/stopping analysis and
	// routes each one here according to scheduler_mode, so this method never scans
	// the tables itself:
	//   - running:  restart the run so it can continue after a crash.
	//   - stopping: converge the stop request, cancelling a live run or finalizing.
	//
	// It reports whether a live run is now tracked in this process.
	RecoverRunningAnalyses(ctx context.Context, item *types.Analysis) (bool, error)

	// GetRunningInfo returns the in-memory running entry for analysisID, or nil
	// when the run is not tracked by this process.
	GetRunningInfo(ctx context.Context, analysisID int64) (*DagRunningInfo, error)
}
