package manager

import (
	"context"
	"strings"
	"time"

	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
)

func (m *ContainerManager) RecoverRuntimeMonitoring(ctx context.Context) (int, error) {
	instances, err := m.repo.ListContainerInstance(ctx)
	if err != nil {
		return 0, err
	}

	recovered := 0
	for _, inst := range instances {
		if !shouldRecoverRuntimeMonitoring(inst) {
			continue
		}

		rt, err := m.getRuntimeByInstance(inst)
		if err != nil {
			logger.Warnf(ctx, "[ContainerManager] resolve runtime for monitoring failed, instance_id=%d runtime_id=%s err=%v", inst.ID, inst.RuntimeID, err)
			continue
		}

		monitor, ok := rt.(containerruntime.RuntimeMonitor)
		if !ok {
			continue
		}

		if err := monitor.Monitor(ctx, inst.RuntimeID); err != nil {
			logger.Warnf(ctx, "[ContainerManager] recover runtime monitoring failed, instance_id=%d runtime_id=%s err=%v", inst.ID, inst.RuntimeID, err)
			continue
		}
		logger.Debugf(ctx, "[ContainerManager] recover runtime monitoring succeeded, name=%s instance_id=%d runtime_id=%s", inst.Name, inst.ID, inst.RuntimeID)

		recovered++
	}

	return recovered, nil
}

func (m *ContainerManager) BackfillRuntimeNodeName(ctx context.Context) (int, error) {
	instances, err := m.repo.ListContainerInstance(ctx)
	if err != nil {
		return 0, err
	}

	backfilled := 0
	for _, inst := range instances {
		if !shouldBackfillRuntimeNodeName(inst) {
			continue
		}

		rt, err := m.getRuntimeByInstance(inst)
		if err != nil {
			logger.Warnf(ctx, "[ContainerManager] resolve runtime for node backfill failed, instance_id=%d runtime_id=%s err=%v", inst.ID, inst.RuntimeID, err)
			continue
		}

		beforeNodeName := strings.TrimSpace(inst.RuntimeNodeName)
		m.syncInstanceIPAddress(ctx, rt, inst)
		afterNodeName := strings.TrimSpace(inst.RuntimeNodeName)
		if beforeNodeName == "" && afterNodeName != "" {
			backfilled++
		}
	}

	return backfilled, nil
}

// RunRuntimeReconciler periodically reconnects runtime monitoring and backfills runtime node names.
func (m *ContainerManager) RunRuntimeReconciler(ctx context.Context, interval time.Duration) {
	m.monitorOnce.Do(func() {
		if interval <= 0 {
			interval = 30 * time.Second
		}
		nodeBackfillInterval := interval * 10
		if nodeBackfillInterval < 2*time.Minute {
			nodeBackfillInterval = 2 * time.Minute
		}
		if ctx == nil {
			ctx = context.Background()
		}

		go func() {
			recovered, err := m.RecoverRuntimeMonitoring(ctx)
			if err != nil {
				logger.Warnf(ctx, "[ContainerManager] startup runtime monitor recovery failed: %v", err)
			} else {
				logger.Infof(ctx, "[ContainerManager] startup runtime monitor recovery completed, recovered=%d", recovered)
			}

			backfilled, err := m.BackfillRuntimeNodeName(ctx)
			if err != nil {
				logger.Warnf(ctx, "[ContainerManager] startup runtime node backfill failed: %v", err)
			} else if backfilled > 0 {
				logger.Infof(ctx, "[ContainerManager] startup runtime node backfill completed, backfilled=%d", backfilled)
			}

			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			nodeTicker := time.NewTicker(nodeBackfillInterval)
			defer nodeTicker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					recovered, err := m.RecoverRuntimeMonitoring(context.Background())
					if err != nil {
						logger.Warnf(context.Background(), "[ContainerManager] periodic runtime monitor recovery failed: %v", err)
						continue
					}
					if recovered > 0 {
						logger.Infof(context.Background(), "[ContainerManager] periodic runtime monitor recovery completed, recovered=%d", recovered)
					}
				case <-nodeTicker.C:
					backfilled, err := m.BackfillRuntimeNodeName(context.Background())
					if err != nil {
						logger.Warnf(context.Background(), "[ContainerManager] periodic runtime node backfill failed: %v", err)
						continue
					}
					if backfilled > 0 {
						logger.Infof(context.Background(), "[ContainerManager] periodic runtime node backfill completed, backfilled=%d", backfilled)
					}
				}
			}
		}()
	})
}

func shouldBackfillRuntimeNodeName(inst *types.ContainerInstance) bool {
	if inst == nil {
		return false
	}
	if strings.TrimSpace(inst.RuntimeID) == "" {
		return false
	}
	if strings.TrimSpace(inst.RuntimeNodeName) != "" {
		return false
	}

	switch inst.Status {
	case types.ContainerCreating, types.ContainerRunning, types.ContainerPaused:
		return true
	default:
		return false
	}
}

func shouldRecoverRuntimeMonitoring(inst *types.ContainerInstance) bool {
	if inst == nil {
		return false
	}
	if strings.TrimSpace(inst.RuntimeID) == "" {
		return false
	}

	switch inst.Status {
	case types.ContainerCreating, types.ContainerPaused:
		return true
	case types.ContainerRunning:
		// TODO inst 暂时没有 runtime type 字段，暂时通过 runtimeID 判断是否为 job 类型的容器
		// 判断只有job才需要恢复监控，其他类型的容器不需要恢复监控
		if inst.RuntimeID != "" && strings.Contains(inst.RuntimeID, "job") {
			// For Kubernetes, we only recover monitoring for running containers if they are in the monitoring registry.
			return true
		}
		return false
	default:
		return false
	}
}
