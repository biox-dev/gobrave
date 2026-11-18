package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// SessionResourceHandler 提供会话结果（脚本 / 工作流 / 分析节点 / 分析运行 / 应用会话）的查询接口。
//
// 脚本 / 工作流 / 分析节点 / 分析运行来自 agent_session_resources 关联表；应用 / 终端会话
// 不落关联表，而是按 AppSession.AgentSessionID 实时查询。资源名称在返回时按主键补全；
// 资源已被删除时名称留空，但记录仍返回，便于前端提示「资源已不存在」而不是静默丢失。
type SessionResourceHandler struct {
	sessions      agentinterfaces.SessionService
	resources     agentinterfaces.SessionResourceService
	workflowSvc   interfaces.WorkflowService
	analysisRepo  interfaces.AnalysisRepository
	containerRepo interfaces.ContainerRepository
}

// NewSessionResourceHandler 创建会话资源处理器。
func NewSessionResourceHandler(
	sessions agentinterfaces.SessionService,
	resources agentinterfaces.SessionResourceService,
	workflowSvc interfaces.WorkflowService,
	analysisRepo interfaces.AnalysisRepository,
	containerRepo interfaces.ContainerRepository,
) *SessionResourceHandler {
	return &SessionResourceHandler{
		sessions:      sessions,
		resources:     resources,
		workflowSvc:   workflowSvc,
		analysisRepo:  analysisRepo,
		containerRepo: containerRepo,
	}
}

// sessionResourceItem 是会话结果列表项。
type sessionResourceItem struct {
	ResourceID   int64     `json:"resource_id,string"`
	ResourceType string    `json:"resource_type"`
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"created_at"`
}

// ListSessionResources godoc
// @Summary      查询会话关联的资源
// @Description  按资源类型返回当前用户的某个会话产生 / 修改过的结果（脚本 / 工作流 / 分析节点 / 分析运行 / 应用会话）
// @Tags         Agent
// @Produce      json
// @Param        session_id     query     string  true  "会话 ID"
// @Param        resource_type  query     string  true  "资源类型：script / workflow / analysis_node / analysis / app_session"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/resources [get]
func (h *SessionResourceHandler) ListSessionResources(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	sessionID, err := parseAgentID(c.Query("session_id"))
	if err != nil || sessionID <= 0 {
		c.Error(errors.NewValidationError("invalid session_id"))
		return
	}
	resourceType := agenttypes.SessionResourceType(strings.TrimSpace(c.Query("resource_type")))
	if !resourceType.Valid() {
		c.Error(errors.NewValidationError("invalid resource_type; expected script / workflow / analysis_node / analysis / app_session"))
		return
	}

	ctx := c.Request.Context()
	session, err := h.sessions.Get(ctx, sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	// 归属校验，防止越权查看他人会话的结果。
	if session.UserID != userID {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}

	// app_session 不走关联表，而是按 AppSession.AgentSessionID 实时查询。
	if resourceType == agenttypes.SessionResourceTypeAppSession {
		items, err := h.listAppSessions(ctx, userID, sessionID)
		if err != nil {
			handleAgentError(c, err, "failed to list session app sessions")
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": items})
		return
	}

	rows, err := h.resources.List(ctx, sessionID, resourceType)
	if err != nil {
		handleAgentError(c, err, "failed to list session resources")
		return
	}

	items := make([]sessionResourceItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, sessionResourceItem{
			ResourceID:   row.ResourceID,
			ResourceType: string(row.ResourceType),
			Name:         h.resolveName(ctx, resourceType, row.ResourceID),
			CreatedAt:    row.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// listAppSessions 返回由 AgentSessionID 指向该会话的应用 / 终端会话。
//
// 这里按 AppSession.AgentSessionID 实时查询，而不是读 agent_session_resources：
// 应用会话的归属是 AppSession 上的字段，没有必要再维护一份关联副本。
func (h *SessionResourceHandler) listAppSessions(ctx context.Context, userID, agentSessionID int64) ([]sessionResourceItem, error) {
	if h.containerRepo == nil {
		return []sessionResourceItem{}, nil
	}
	query := &types.AppSessionPageQuery{AgentSessionID: &agentSessionID}
	pagination := &types.Pagination{Page: 1, PageSize: 1000}
	rows, _, err := h.containerRepo.PageAppSessionByUserID(ctx, userID, pagination, query)
	if err != nil {
		return nil, err
	}
	items := make([]sessionResourceItem, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		items = append(items, sessionResourceItem{
			ResourceID:   row.ID,
			ResourceType: string(agenttypes.SessionResourceTypeAppSession),
			Name:         row.Name,
			CreatedAt:    row.CreatedAt,
		})
	}
	return items, nil
}

// resolveName 按资源类型补全展示名；资源已删除或读取失败时返回空串。
func (h *SessionResourceHandler) resolveName(ctx context.Context, resourceType agenttypes.SessionResourceType, resourceID int64) string {
	switch resourceType {
	case agenttypes.SessionResourceTypeScript:
		if h.workflowSvc == nil {
			return ""
		}
		if script, err := h.workflowSvc.GetScriptByID(ctx, resourceID); err == nil && script != nil {
			return script.Name
		}
	case agenttypes.SessionResourceTypeWorkflow:
		if h.workflowSvc == nil {
			return ""
		}
		if workflow, err := h.workflowSvc.GetWorkflowByID(ctx, resourceID); err == nil && workflow != nil {
			return workflow.Name
		}
	case agenttypes.SessionResourceTypeAnalysisNode:
		if h.analysisRepo == nil {
			return ""
		}
		if node, err := h.analysisRepo.GetAnalysisNodeByID(ctx, resourceID); err == nil && node != nil {
			return node.NodeName
		}
	case agenttypes.SessionResourceTypeAnalysis:
		if h.analysisRepo == nil {
			return ""
		}
		if analysis, err := h.analysisRepo.GetAnalysisByID(ctx, resourceID); err == nil && analysis != nil {
			return strings.TrimSpace(analysis.AnalysisName)
		}
	}
	return ""
}
