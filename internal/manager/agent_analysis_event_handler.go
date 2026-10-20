package manager

import (
	"context"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

var _ event.Handler = (*AgentAnalysisEventHandler)(nil)

// AgentAnalysisEventHandler 根据容器事件同步 AgentAnalysis 状态。
//
// 页面（以及 Agent 工具）依赖这个状态判断任务是否仍在运行；只有 running 时
// UI 才允许连接交互式终端。
type AgentAnalysisEventHandler struct {
	repo          interfaces.AgentAnalysisRepository
	containerRepo interfaces.ContainerRepository
}

func NewAgentAnalysisEventHandler(repo interfaces.AgentAnalysisRepository, containerRepo interfaces.ContainerRepository) *AgentAnalysisEventHandler {
	return &AgentAnalysisEventHandler{repo: repo, containerRepo: containerRepo}
}

func (h *AgentAnalysisEventHandler) Handle(evt event.Event) {
	ce, ok := evt.(types.ContainerEvent)
	if !ok {
		return
	}
	ctx := context.Background()

	inst, err := h.containerRepo.GetContainerInstanceByID(ctx, ce.ContainerInstanceID)
	if err != nil || inst == nil || inst.OwnerType != types.ContainerOwnerAgentAnalysis {
		return
	}

	item, err := h.repo.GetAgentAnalysisByID(ctx, inst.OwnerID)
	if err != nil || item == nil {
		return
	}

	normalized := normalizeContainerEvent(ce.Event)
	if normalized == "" || normalized == "deleting" || normalized == "deleted" {
		return
	}

	now := time.Now().UTC()
	updates := map[string]any{}
	switch normalized {
	case "creating", "starting":
		updates["status"] = types.AgentAnalysisPending
	case "running":
		updates["status"] = types.AgentAnalysisRunning
		updates["started_at"] = firstTime(inst.StartedAt, now)
		updates["finished_at"] = nil
		updates["error_message"] = ""
	case "stopped":
		updates["status"] = agentAnalysisStatusFromExitCode(inst, types.AgentAnalysisDone)
		updates["exit_code"] = exitCodeValue(inst)
		updates["finished_at"] = firstTime(inst.FinishedAt, now)
	case "failed":
		updates["status"] = types.AgentAnalysisFailed
		updates["exit_code"] = exitCodeValue(inst)
		updates["finished_at"] = firstTime(inst.FinishedAt, now)
		updates["error_message"] = strings.TrimSpace(ce.Message)
		if strings.TrimSpace(ce.Message) == "" {
			updates["error_message"] = "container failed"
		}
	default:
		return
	}

	if err := h.repo.UpdateAgentAnalysisByID(ctx, item.ID, updates); err != nil {
		logger.Warnf(ctx, "[AgentAnalysisEventHandler] update agent analysis failed, agent_analysis_id=%d event=%s err=%v", item.ID, ce.Event, err)
	}
}

func agentAnalysisStatusFromExitCode(inst *types.ContainerInstance, fallback string) string {
	if inst == nil || inst.ExitCode == nil || *inst.ExitCode == 0 {
		return fallback
	}
	return types.AgentAnalysisFailed
}

func exitCodeValue(inst *types.ContainerInstance) int {
	if inst == nil || inst.ExitCode == nil {
		return 0
	}
	return *inst.ExitCode
}

func firstTime(value *time.Time, fallback time.Time) time.Time {
	if value != nil && !value.IsZero() {
		return *value
	}
	return fallback
}
