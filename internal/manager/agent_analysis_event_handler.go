package manager

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

var _ event.Handler = (*AgentAnalysisEventHandler)(nil)

// AgentAnalysisEventHandler 根据容器事件同步 AgentAnalysis 状态。
//
// 页面（以及 Agent 工具）依赖这个状态判断任务是否仍在运行；只有 running 时
// UI 才允许连接交互式终端。
type AgentAnalysisEventHandler struct {
	repo          interfaces.AgentAnalysisRepository
	containerRepo interfaces.ContainerRepository
	db            *gorm.DB
	hub           *realtime.Hub
}

func NewAgentAnalysisEventHandler(repo interfaces.AgentAnalysisRepository, containerRepo interfaces.ContainerRepository, db *gorm.DB, hub *realtime.Hub) *AgentAnalysisEventHandler {
	return &AgentAnalysisEventHandler{repo: repo, containerRepo: containerRepo, db: db, hub: hub}
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

	// 重新运行后旧容器的退出事件不应该覆盖新容器实例的状态。
	if item.ContainerInstanceID != 0 && item.ContainerInstanceID != inst.ID {
		return
	}
	// 手动停止后保持 canceled，不被之后的 stopped 事件覆盖。
	if item.Status == types.AgentAnalysisCanceled {
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
		return
	}

	h.notifyStatusChanged(ctx, item, normalized)
}

// notifyStatusChanged 把状态变更推送给项目用户，前端据此刷新 AgentAnalysis 列表。
func (h *AgentAnalysisEventHandler) notifyStatusChanged(ctx context.Context, item *types.AgentAnalysis, normalizedEvent string) {
	if h.hub == nil || item == nil || item.ProjectID == 0 {
		return
	}

	userIDs, err := h.listProjectUserIDs(ctx, item.ProjectID)
	if err != nil {
		logger.Warnf(ctx, "[AgentAnalysisEventHandler] load project users failed project_id=%d err=%v", item.ProjectID, err)
		return
	}
	if len(userIDs) == 0 {
		return
	}

	id := strconv.FormatInt(item.ID, 10)
	msg := map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": "agent-analysis",
			"id":       id,
			"method":   agentAnalysisRealtimeMethod(normalizedEvent),
			"args": map[string]any{
				"id":         id,
				"project_id": strconv.FormatInt(item.ProjectID, 10),
				"status":     normalizedEvent,
			},
		},
	}

	for _, userID := range userIDs {
		if err := h.hub.PushMessage(userID, msg); err != nil {
			if strings.Contains(err.Error(), "no_client_for_user:") {
				continue
			}
			logger.Warnf(ctx, "[AgentAnalysisEventHandler] push agent analysis event failed user_id=%d agent_analysis_id=%d event=%s err=%v", userID, item.ID, normalizedEvent, err)
		}
	}
}

func (h *AgentAnalysisEventHandler) listProjectUserIDs(ctx context.Context, projectID int64) ([]int64, error) {
	rows := make([]int64, 0)
	err := h.db.WithContext(ctx).
		Model(&types.UserProject{}).
		Distinct("user_id").
		Where("project_id = ?", projectID).
		Pluck("user_id", &rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// agentAnalysisRealtimeMethod 把容器事件映射成前端组件方法名。
func agentAnalysisRealtimeMethod(normalizedEvent string) string {
	switch normalizedEvent {
	case "creating", "starting":
		return "analysisSubmitted"
	case "running":
		return "analysisStarted"
	case "failed":
		return "analysisFailed"
	case "stopped":
		return "analysisDone"
	default:
		return "refresh"
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
