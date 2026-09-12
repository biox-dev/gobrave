package dag

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/dag/scheduler"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// dagRecoveryInterval is the periodic scan cadence for the unified DAG recovery.
const dagRecoveryInterval = 300 * time.Second

// RecoverDag is the single recovery entry for every DAG scheduler.
//
// It scans job_status running/stopping analyses in a background goroutine and
// hands each one back to the scheduler that owns it, resolved from
// analysis.scheduler_mode through the shared registry. Because ownership is a
// property of the analysis, one analysis can never be advanced by two schedulers
// at the same time.
//
// Each orchestrator's RecoverRunningAnalyses handles a single analysis: running
// triggers a resume, stopping converges the stop request, so this loop does not
// need a separate cycle per job status.
func RecoverDag(
	cfg *config.Config,
	repo interfaces.AnalysisRepository,
	schedulers *scheduler.Registry,
) {
	enabled := true
	if cfg != nil && cfg.Container != nil {
		enabled = cfg.Container.RecoverRunningDagOnStart
	}
	if !enabled {
		logger.Infof(context.Background(), "[DAG] startup running DAG recovery disabled by config")
		return
	}

	go func() {
		// 启动后立即恢复一次，随后周期性地扫描，接管崩溃进程遗留的运行态。
		recoverAllAnalyses(context.Background(), repo, schedulers)

		ticker := time.NewTicker(dagRecoveryInterval)
		defer ticker.Stop()
		for range ticker.C {
			recoverAllAnalyses(context.Background(), repo, schedulers)
		}
	}()
}

// recoverAllAnalyses collects running + stopping analyses once and dispatches each
// one to its owning scheduler.
func recoverAllAnalyses(
	ctx context.Context,
	repo interfaces.AnalysisRepository,
	schedulers *scheduler.Registry,
) {
	if repo == nil {
		return
	}

	items := make([]*types.Analysis, 0)
	for _, jobStatus := range []string{types.AnalysisStatusRunning, types.AnalysisStatusStopping} {
		found, err := repo.ListAnalysisByJobStatus(ctx, jobStatus)
		if err != nil {
			logger.Warnf(ctx, "[DAG] list %s analyses failed: %v", jobStatus, err)
			continue
		}
		items = append(items, found...)
	}

	for _, item := range items {
		recoverAnalysisByScheduler(ctx, item, schedulers)
	}
}

// recoverAnalysisByScheduler routes a single analysis to the scheduler that owns
// it. Ownership is decided by analysis.scheduler_mode so the same analysis is
// never advanced by two schedulers at the same time.
func recoverAnalysisByScheduler(
	ctx context.Context,
	item *types.Analysis,
	schedulers *scheduler.Registry,
) {
	if item == nil || item.ID <= 0 {
		return
	}

	orchestrator := schedulers.Resolve(item.SchedulerMode)
	if orchestrator == nil {
		logger.Warnf(ctx, "[DAG] no scheduler registered, analysis_id=%d scheduler_mode=%s", item.ID, item.SchedulerMode)
		return
	}

	recovered, err := orchestrator.RecoverRunningAnalyses(ctx, item)
	if err != nil {
		logger.Warnf(ctx, "[DAG] recover analysis failed, analysis_id=%d scheduler=%s job_status=%s err=%v",
			item.ID, orchestrator.Name(), item.JobStatus, err)
		return
	}
	if recovered {
		logger.Infof(ctx, "[DAG] recovered analysis, analysis_id=%d scheduler=%s", item.ID, orchestrator.Name())
	}
}
