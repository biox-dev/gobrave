package interfaces

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
)

// DagRunningInfo describes an in-flight DAG run tracked by the orchestrator.
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

// DynamicDagOrchestrator provides a Nextflow-like dynamic scheduling path
// without changing the existing DAG orchestrator behavior.
type DynamicDagOrchestrator interface {
	StartAsyncV2(ctx context.Context, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any) error
	// RecoverRunningAnalyses adopts a single analysis owned by dynamic_v2.
	//
	// The container recovery loop fetches every running/stopping analysis and
	// routes each one here (or to the legacy orchestrator) according to
	// scheduler_mode, so this method no longer scans the tables itself:
	//   - running:  a stale-lease run is restarted with resume semantics.
	//   - stopping: a live run is cancelled, otherwise the analysis is finalized.
	//
	// It reports whether a live run is now tracked in this process.
	RecoverRunningAnalyses(ctx context.Context, item *types.Analysis) (bool, error)
	// GetRunningInfo returns the in-memory running entry for analysisID, or nil when the DAG is not running.
	GetRunningInfo(ctx context.Context, analysisID int64) (*DagRunningInfo, error)
	// RequestStop asks the in-process run to stop and reports whether this process
	// owned a live run for analysisID. When it returns false the caller must fall
	// back to the persisted job_status stop path, which also covers runs owned by
	// another instance.
	RequestStop(analysisID int64) bool
}
