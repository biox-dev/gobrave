package interfaces

import (
	"context"
	"time"
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
	// GetRunningInfo returns the in-memory running entry for analysisID, or nil when the DAG is not running.
	GetRunningInfo(ctx context.Context, analysisID int64) (*DagRunningInfo, error)
}
