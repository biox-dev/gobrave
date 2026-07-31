package interfaces

import "context"

// DynamicDagOrchestrator provides a Nextflow-like dynamic scheduling path
// without changing the existing DAG orchestrator behavior.
type DynamicDagOrchestrator interface {
	StartAsyncV2(ctx context.Context, analysisID int64, parseAnalysisResult map[string]any, dagDefinition map[string]any) error
}
