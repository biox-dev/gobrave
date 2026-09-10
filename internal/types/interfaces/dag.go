package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type DagOrchestrator interface {
	StartAsync(ctx context.Context, analysisID int64) error
	StopAsync(ctx context.Context, analysisID int64) error
	// RecoverRunningAnalyses adopts a single analysis owned by this scheduler.
	//
	// The container recovery loop fetches every running/stopping analysis and
	// routes each one here (or to the dynamic V2 orchestrator) according to
	// scheduler_mode, so this method no longer scans the tables itself:
	//   - running:  restart the run so it can continue after a crash.
	//   - stopping: converge the stop request, cancelling a live run or finalizing.
	//
	// It reports whether a live run is now tracked in this process.
	RecoverRunningAnalyses(ctx context.Context, item *types.Analysis) (bool, error)
}
