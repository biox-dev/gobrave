package dag

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// dagRecoveryInterval is the periodic scan cadence for the unified DAG recovery.
const dagRecoveryInterval = 30 * time.Second

// RecoverDag 是 DAG 恢复的唯一入口（替代原先 legacy / dynamic V2 各自一个 Invoke）。
//
// 它在一个后台 goroutine 中扫描 job_status 为 running / stopping 的 analysis，
// 并按 scheduler_mode 把每一个 analysis 交回给拥有它的调度器：
//   - dynamic_v2 → interfaces.DynamicDagOrchestrator
//   - 其它（含 dag_v1 / node_v1 / 历史空值）→ legacy interfaces.DagOrchestrator
//
// 每个 orchestrator 的 RecoverRunningAnalyses 只处理单个 analysis：running 走恢复，
// stopping 走停止收敛，因此这里无需为两种状态各维护一套循环，也不会出现两个调度器
// 同时接管同一个 analysis 的双跑问题。
func RecoverDag(
	cfg *config.Config,
	repo interfaces.AnalysisRepository,
	orchestrator interfaces.DagOrchestrator,
	orchestratorV2 interfaces.DynamicDagOrchestrator,
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
		recoverAllAnalyses(context.Background(), repo, orchestrator, orchestratorV2)

		ticker := time.NewTicker(dagRecoveryInterval)
		defer ticker.Stop()
		for range ticker.C {
			recoverAllAnalyses(context.Background(), repo, orchestrator, orchestratorV2)
		}
	}()
}

// recoverAllAnalyses collects running + stopping analyses once and dispatches each
// one to its owning scheduler.
func recoverAllAnalyses(
	ctx context.Context,
	repo interfaces.AnalysisRepository,
	orchestrator interfaces.DagOrchestrator,
	orchestratorV2 interfaces.DynamicDagOrchestrator,
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
		recoverAnalysisByScheduler(ctx, item, orchestrator, orchestratorV2)
	}
}

// recoverAnalysisByScheduler routes a single analysis to the scheduler that owns it.
// Ownership is decided by analysis.scheduler_mode so the same analysis is never
// advanced by two schedulers at the same time.
func recoverAnalysisByScheduler(
	ctx context.Context,
	item *types.Analysis,
	orchestrator interfaces.DagOrchestrator,
	orchestratorV2 interfaces.DynamicDagOrchestrator,
) {
	if item == nil || item.ID <= 0 {
		return
	}

	var (
		recovered bool
		err       error
	)
	switch types.NormalizeSchedulerMode(item.SchedulerMode) {
	case types.SchedulerModeDynamicV2:
		if orchestratorV2 == nil {
			return
		}
		recovered, err = orchestratorV2.RecoverRunningAnalyses(ctx, item)
	default:
		if orchestrator == nil {
			return
		}
		recovered, err = orchestrator.RecoverRunningAnalyses(ctx, item)
	}

	if err != nil {
		logger.Warnf(ctx, "[DAG] recover analysis failed, analysis_id=%d scheduler_mode=%s job_status=%s err=%v",
			item.ID, item.SchedulerMode, item.JobStatus, err)
		return
	}
	if recovered {
		logger.Infof(ctx, "[DAG] recovered analysis, analysis_id=%d scheduler_mode=%s", item.ID, item.SchedulerMode)
	}
}
