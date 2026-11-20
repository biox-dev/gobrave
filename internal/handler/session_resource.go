package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// SessionResourceHandler 提供会话结果（脚本 / 工作流 / 分析节点 / 分析运行 / 项目报告）的查询接口。
//
// 脚本 / 工作流 / 分析节点 / 分析运行 / 项目报告来自 agent_session_resources 关联表。资源名称在返回时
// 按主键补全；资源已被删除时名称留空，但记录仍返回，便于前端提示「资源已不存在」而不是静默丢失。
type SessionResourceHandler struct {
	sessions     agentinterfaces.SessionService
	resources    agentinterfaces.SessionResourceService
	workflowSvc  interfaces.WorkflowService
	analysisRepo interfaces.AnalysisRepository
	// projects 用于会话可见性判断：会话按项目隔离，项目成员都能查看。
	projects interfaces.ProjectService
}

// NewSessionResourceHandler 创建会话资源处理器。
func NewSessionResourceHandler(
	sessions agentinterfaces.SessionService,
	resources agentinterfaces.SessionResourceService,
	workflowSvc interfaces.WorkflowService,
	analysisRepo interfaces.AnalysisRepository,
	projects interfaces.ProjectService,
) *SessionResourceHandler {
	return &SessionResourceHandler{
		sessions:     sessions,
		resources:    resources,
		workflowSvc:  workflowSvc,
		analysisRepo: analysisRepo,
		projects:     projects,
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
// @Description  按资源类型返回当前用户的某个会话产生 / 修改过的结果（脚本 / 工作流 / 分析节点 / 分析运行 / 项目报告）
// @Tags         Agent
// @Produce      json
// @Param        session_id     query     string  true  "会话 ID"
// @Param        resource_type  query     string  true  "资源类型：script / workflow / analysis_node / analysis / project_report"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
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
		c.Error(errors.NewValidationError("invalid resource_type; expected script / workflow / analysis_node / analysis / project_report"))
		return
	}

	ctx := c.Request.Context()
	session, err := h.sessions.Get(ctx, sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	// 可见性校验：会话按项目共享，项目成员都能查看；非项目成员返回 403。
	if err := ensureProjectAccessible(ctx, h.projects, session.ProjectID, userID); err != nil {
		c.Error(err)
		return
	}

	rows, err := h.resources.List(ctx, sessionID, resourceType)
	if err != nil {
		handleAgentError(c, err, "failed to list session resources")
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": h.buildItems(ctx, rows)})
}

// sessionResourceGroups 是按资源类型分组的会话结果。
//
// 每个分组始终返回数组（无结果时为 `[]`），前端一次请求即可拿到全部分区的内容。
type sessionResourceGroups struct {
	Script        []sessionResourceItem `json:"script"`
	Workflow      []sessionResourceItem `json:"workflow"`
	AnalysisNode  []sessionResourceItem `json:"analysis_node"`
	Analysis      []sessionResourceItem `json:"analysis"`
	ProjectReport []sessionResourceItem `json:"project_report"`
}

// ListAllSessionResources godoc
// @Summary      一次查询会话关联的全部资源（按类型分组）
// @Description  返回当前用户的某个会话产生 / 修改过的全部结果，并按资源类型分组
// @Tags         Agent
// @Produce      json
// @Param        session_id  query     string  true  "会话 ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/resources/all [get]
func (h *SessionResourceHandler) ListAllSessionResources(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	sessionID, err := parseAgentID(c.Query("session_id"))
	if err != nil || sessionID <= 0 {
		c.Error(errors.NewValidationError("invalid session_id"))
		return
	}

	ctx := c.Request.Context()
	session, err := h.sessions.Get(ctx, sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	// 可见性校验：会话按项目共享，项目成员都能查看；非项目成员返回 403。
	if err := ensureProjectAccessible(ctx, h.projects, session.ProjectID, userID); err != nil {
		c.Error(err)
		return
	}

	groups := sessionResourceGroups{
		Script:        []sessionResourceItem{},
		Workflow:      []sessionResourceItem{},
		AnalysisNode:  []sessionResourceItem{},
		Analysis:      []sessionResourceItem{},
		ProjectReport: []sessionResourceItem{},
	}

	// 关联表一次查出所有类型，避免前端按类型发多次请求。
	rows, err := h.resources.ListBySession(ctx, sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to list session resources")
		return
	}
	for _, item := range h.buildItems(ctx, rows) {
		switch agenttypes.SessionResourceType(item.ResourceType) {
		case agenttypes.SessionResourceTypeScript:
			groups.Script = append(groups.Script, item)
		case agenttypes.SessionResourceTypeWorkflow:
			groups.Workflow = append(groups.Workflow, item)
		case agenttypes.SessionResourceTypeAnalysisNode:
			groups.AnalysisNode = append(groups.AnalysisNode, item)
		case agenttypes.SessionResourceTypeAnalysis:
			groups.Analysis = append(groups.Analysis, item)
		case agenttypes.SessionResourceTypeProjectReport:
			groups.ProjectReport = append(groups.ProjectReport, item)
		}
	}

	c.JSON(http.StatusOK, gin.H{"data": groups})
}

// buildItems 把关联记录转换成返回项，并按资源主键补全展示名。
func (h *SessionResourceHandler) buildItems(ctx context.Context, rows []agenttypes.SessionResource) []sessionResourceItem {
	items := make([]sessionResourceItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, sessionResourceItem{
			ResourceID:   row.ResourceID,
			ResourceType: string(row.ResourceType),
			Name:         h.resolveName(ctx, row.ResourceType, row.ResourceID),
			CreatedAt:    row.CreatedAt,
		})
	}
	return items
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
	case agenttypes.SessionResourceTypeProjectReport:
		if h.projects == nil {
			return ""
		}
		if report, err := h.projects.GetProjectReportByID(ctx, resourceID); err == nil && report != nil {
			return strings.TrimSpace(report.Title)
		}
	}
	return ""
}
