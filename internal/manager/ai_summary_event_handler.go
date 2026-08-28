package manager

import (
	"context"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

var _ event.Handler = (*AISummaryEventHandler)(nil)

// AISummaryEventHandler 订阅 AISummaryStatusEvent，并将摘要状态变更推送给
// 所属项目用户，前端据此刷新 AISummaryPanel。
type AISummaryEventHandler struct {
	db           *gorm.DB
	analysisRepo interfaces.AnalysisRepository
	projectRepo  interfaces.ProjectRepository
	hub          *realtime.Hub
}

func NewAISummaryEventHandler(db *gorm.DB, analysisRepo interfaces.AnalysisRepository, projectRepo interfaces.ProjectRepository, hub *realtime.Hub) *AISummaryEventHandler {
	return &AISummaryEventHandler{
		db:           db,
		analysisRepo: analysisRepo,
		projectRepo:  projectRepo,
		hub:          hub,
	}
}

func (h *AISummaryEventHandler) Handle(evt event.Event) {
	if h.hub == nil {
		return
	}

	e, ok := evt.(types.AISummaryStatusEvent)
	if !ok {
		return
	}

	ctx := context.Background()
	projectID, err := h.resolveProjectID(ctx, e.OwnerType, e.OwnerID)
	if err != nil || projectID == 0 {
		return
	}

	project, err := h.projectRepo.GetProjectByID(ctx, projectID)
	if err != nil || project == nil {
		return
	}

	userIDs, err := h.listProjectUserIDs(ctx, project.ProjectID)
	if err != nil || len(userIDs) == 0 {
		return
	}

	msg := map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": "ai-summary",
			"id":       strconv.FormatInt(e.OwnerID, 10),
			"method":   "refresh",
			"args": map[string]any{
				"owner_id":   strconv.FormatInt(e.OwnerID, 10),
				"owner_type": string(e.OwnerType),
				"status":     string(e.Status),
				"summary_id": strconv.FormatInt(e.SummaryID, 10),
			},
		},
	}

	for _, userID := range userIDs {
		if err := h.hub.PushMessage(userID, msg); err != nil {
			if strings.Contains(err.Error(), "no_client_for_user:") {
				continue
			}
			logger.Warnf(ctx, "[AISummaryEventHandler] push ai summary event failed user_id=%s summary_id=%d status=%s err=%v", userID, e.SummaryID, e.Status, err)
		}
	}
}

// resolveProjectID 根据摘要所属对象解析项目 ID。
func (h *AISummaryEventHandler) resolveProjectID(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (int64, error) {
	switch ownerType {
	case types.SummaryOwnerAnalysis:
		analysis, err := h.analysisRepo.GetAnalysisByID(ctx, ownerID)
		if err != nil {
			return 0, err
		}
		return analysis.ProjectID, nil

	case types.SummaryOwnerAnalysisNode:
		node, err := h.analysisRepo.GetAnalysisNodeByID(ctx, ownerID)
		if err != nil {
			return 0, err
		}
		if node.ProjectID != 0 {
			return node.ProjectID, nil
		}
		analysis, err := h.analysisRepo.GetAnalysisByID(ctx, node.AnalysisID)
		if err != nil {
			return 0, err
		}
		return analysis.ProjectID, nil
	}

	return 0, nil
}

func (h *AISummaryEventHandler) listProjectUserIDs(ctx context.Context, projectID string) ([]string, error) {
	rows := make([]string, 0)
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
