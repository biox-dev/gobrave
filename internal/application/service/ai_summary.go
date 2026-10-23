package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

type aiSummaryService struct {
	summaryRepo   interfaces.AISummaryRepository
	containerRepo interfaces.ContainerRepository
	projectRepo   interfaces.ProjectRepository
	content       manager.AISummaryContentProvider
	// analysisRepo 用于解析摘要所属对象的输出目录，以补充响应中的 URL 前缀。
	analysisRepo interfaces.AnalysisRepository
	cfg          *config.Config
	// agentSvc 用于按摘要使用的 Profile 渲染真实的系统提示词（可为空）。
	agentSvc *agentservice.AgentService
}

func NewAISummaryService(
	summaryRepo interfaces.AISummaryRepository,
	containerRepo interfaces.ContainerRepository,
	projectRepo interfaces.ProjectRepository,
	content manager.AISummaryContentProvider,
	analysisRepo interfaces.AnalysisRepository,
	cfg *config.Config,
	agentSvc *agentservice.AgentService,
) interfaces.AISummaryService {
	return &aiSummaryService{
		summaryRepo:   summaryRepo,
		containerRepo: containerRepo,
		projectRepo:   projectRepo,
		content:       content,
		analysisRepo:  analysisRepo,
		cfg:           cfg,
		agentSvc:      agentSvc,
	}
}

// CreateAISummary 创建摘要记录（pending 状态）并投递 outbox 事件，由
// AISummaryWorker 异步消费生成摘要内容。
//
// profileID 为生成时使用的 Agent Profile ID（用于按 ID 解析），profile 为其展示名（仅用于展示）。
// profileID 为 0 表示使用内置 summary Profile。
// 摘要所属项目 ID 由 AISummaryWorker 从所属对象解析后回填，此处不设置。
func (s *aiSummaryService) CreateAISummary(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64, profileID int64, profile string) (*types.AISummary, error) {
	summary := &types.AISummary{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Status:    types.SummaryStatusPending,
		ProfileID: profileID,
		Profile:   strings.TrimSpace(profile),
	}
	if err := s.summaryRepo.CreateAISummary(ctx, summary); err != nil {
		return nil, err
	}

	if err := s.enqueueGeneration(ctx, summary.ID); err != nil {
		return nil, err
	}

	return summary, nil
}

// RegenerateAISummary 将已有摘要重置为 pending 并重新投递异步生成事件。
func (s *aiSummaryService) RegenerateAISummary(ctx context.Context, id int64) (*types.AISummary, error) {
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, id)
	if err != nil {
		return nil, err
	}

	summary.Status = types.SummaryStatusPending
	if err := s.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return nil, err
	}

	// 删除旧的正文文件，等待重新生成（正文不再落库）。
	if projectID := s.resolveSummaryProjectID(ctx, summary); projectID != 0 {
		if err := utils.DeleteAISummaryFile(s.storageBaseDir(), projectID, summary.ID); err != nil {
			logger.Warnf(ctx, "[AISummary] delete summary file failed, summary_id=%d err=%v", summary.ID, err)
		}
	}

	if err := s.enqueueGeneration(ctx, summary.ID); err != nil {
		return nil, err
	}

	return summary, nil
}

// enqueueGeneration 投递一条摘要生成 outbox 事件。
func (s *aiSummaryService) enqueueGeneration(ctx context.Context, summaryID int64) error {
	payload, err := json.Marshal(&types.AISummaryGeneratePayload{SummaryID: summaryID})
	if err != nil {
		return err
	}

	return s.containerRepo.CreateOutboxEvent(ctx, &types.OutboxEvent{
		Type:    manager.OutboxEventTypeAISummaryGenerateRequest,
		Payload: payload,
		Status:  "pending",
	})
}

// GetAISummaryByID 按摘要 ID 查询详情，从文件填充正文（content），并填充 Prefix
// （所属对象输出目录对应的 /data-analysis URL 前缀），供前端渲染正文中的相对图片/链接。
func (s *aiSummaryService) GetAISummaryByID(ctx context.Context, id int64) (*types.AISummary, error) {
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, id)
	if err != nil {
		return nil, err
	}

	summary.Prefix = s.resolveOwnerURLPrefix(ctx, summary.OwnerType, summary.OwnerID)
	summary.Content = s.readSummaryContent(ctx, summary)
	return summary, nil
}

// ListAISummariesByOwner 按所属对象类型与 ID 查询摘要列表，为每个 item 从文件填充正文
// 并补充所属对象输出目录对应的 /data-analysis URL 前缀。
func (s *aiSummaryService) ListAISummariesByOwner(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) ([]*types.AISummary, error) {
	items, err := s.summaryRepo.ListAISummariesByOwner(ctx, ownerType, ownerID)
	if err != nil {
		return nil, err
	}

	prefix := s.resolveOwnerURLPrefix(ctx, ownerType, ownerID)
	for _, item := range items {
		if item == nil {
			continue
		}
		item.Prefix = prefix
		item.Content = s.readSummaryContent(ctx, item)
	}

	return items, nil
}

// PageAISummariesByProjectID 按项目 ID 分页查询摘要列表。列表项不包含 content，
// 详情由 GetAISummaryByID 按 ID 单独返回。
func (s *aiSummaryService) PageAISummariesByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.AISummary, int64, error) {
	return s.summaryRepo.PageAISummariesByProjectID(ctx, pagination, projectID)
}

// resolveOwnerURLPrefix 解析摘要所属对象输出目录对应的 URL 前缀。
// 对象不存在或目录为空时返回空串：前缀只是响应增强字段，
// 不应阻塞摘要列表本身的返回。
func (s *aiSummaryService) resolveOwnerURLPrefix(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) string {
	baseDir := ""
	if s.cfg != nil && s.cfg.Storage != nil {
		baseDir = s.cfg.Storage.BaseDir
	}
	return resolveSummaryOwnerURLPrefix(ctx, s.analysisRepo, baseDir, ownerType, ownerID)
}

// resolveSummaryOwnerURLPrefix 将摘要所属对象（Analysis / AnalysisNode）的输出目录
// 解析为 /data-analysis URL 前缀。对象不存在或目录为空时返回空串。
// 供 aiSummaryService 与 projectService 共享。
func resolveSummaryOwnerURLPrefix(ctx context.Context, analysisRepo interfaces.AnalysisRepository, baseDir string, ownerType types.SummaryOwnerType, ownerID int64) string {
	if analysisRepo == nil {
		return ""
	}

	var outputDir string
	switch ownerType {
	case types.SummaryOwnerAnalysisNode:
		node, err := analysisRepo.GetAnalysisNodeByID(ctx, ownerID)
		if err != nil {
			logger.Warnf(ctx, "[AISummary] resolve analysis node output dir failed, analysis_node_id=%d err=%v", ownerID, err)
			return ""
		}
		outputDir = node.OutputDir
	case types.SummaryOwnerAnalysis:
		analysis, err := analysisRepo.GetAnalysisByID(ctx, ownerID)
		if err != nil {
			logger.Warnf(ctx, "[AISummary] resolve analysis output dir failed, analysis_id=%d err=%v", ownerID, err)
			return ""
		}
		outputDir = analysis.WorkspaceDir
	default:
		return ""
	}

	if strings.TrimSpace(outputDir) == "" {
		return ""
	}

	return utils.GetAnalysisURLPrefix(baseDir, outputDir)
}

// storageBaseDir 返回去除首尾空白后的 storage.base_dir，未配置时返回空串。
func (s *aiSummaryService) storageBaseDir() string {
	if s.cfg == nil || s.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.Storage.BaseDir)
}

// readSummaryContent 按摘要所属项目读取正文文件（见 utils.GetAISummaryFile），
// 文件不存在或项目无法解析时返回空串（前端按空内容展示，而非报错）。
func (s *aiSummaryService) readSummaryContent(ctx context.Context, summary *types.AISummary) string {
	projectID := s.resolveSummaryProjectID(ctx, summary)
	if projectID == 0 {
		return ""
	}
	content, err := utils.ReadAISummaryFile(s.storageBaseDir(), projectID, summary.ID)
	if err != nil {
		logger.Warnf(ctx, "[AISummary] read summary file failed, summary_id=%d err=%v", summary.ID, err)
		return ""
	}
	return content
}

// resolveSummaryProjectID 返回摘要所属对象的项目数值主键（t_project.id）：优先使用摘要记录上
// 已持久化的 ProjectID，缺失（例如尚未生成完成）时回退到所属对象（Analysis / AnalysisNode）解析。
func (s *aiSummaryService) resolveSummaryProjectID(ctx context.Context, summary *types.AISummary) int64 {
	if summary == nil {
		return 0
	}
	if summary.ProjectID != 0 {
		return summary.ProjectID
	}
	if s.content == nil {
		return 0
	}
	content, err := s.content.Resolve(ctx, summary.OwnerType, summary.OwnerID)
	if err != nil {
		logger.Warnf(ctx, "[AISummary] resolve summary owner failed, summary_id=%d err=%v", summary.ID, err)
		return 0
	}
	return content.ProjectID
}

// UpdateAISummary 按摘要 ID 更新标题、内容与 Agent Profile，nil 表示不修改对应字段。
// 摘要正文（content）写入文件（见 utils.GetAISummaryFile），不再落库。
func (s *aiSummaryService) UpdateAISummary(ctx context.Context, id int64, title, content *string, profileID *int64, profile *string) (*types.AISummary, error) {
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if title != nil {
		summary.Title = *title
	}
	if content != nil {
		projectID := s.resolveSummaryProjectID(ctx, summary)
		if projectID == 0 {
			return nil, fmt.Errorf("resolve summary project failed, summary_id=%d", summary.ID)
		}
		if err := utils.WriteAISummaryFile(s.storageBaseDir(), projectID, summary.ID, *content); err != nil {
			return nil, fmt.Errorf("write summary file: %w", err)
		}
		// 供响应回显（非持久化字段）。
		summary.Content = *content
	}
	if profileID != nil {
		summary.ProfileID = *profileID
	}
	if profile != nil {
		summary.Profile = strings.TrimSpace(*profile)
	}

	if err := s.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return nil, err
	}

	return summary, nil
}

// DeleteAISummary 按摘要 ID 删除摘要记录，并删除对应的正文文件（见 utils.GetAISummaryFile）。
// 删除记录前先解析所属项目（记录删除后无法再解析）；文件删除失败仅告警，
// 不影响记录删除的结果。
func (s *aiSummaryService) DeleteAISummary(ctx context.Context, id int64) error {
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, id)
	if err != nil {
		return err
	}

	projectID := s.resolveSummaryProjectID(ctx, summary)

	if err := s.summaryRepo.DeleteAISummary(ctx, id); err != nil {
		return err
	}

	if projectID != 0 {
		if err := utils.DeleteAISummaryFile(s.storageBaseDir(), projectID, id); err != nil {
			logger.Warnf(ctx, "[AISummary] delete summary file failed, summary_id=%d err=%v", id, err)
		}
	}

	return nil
}

// GetAISummaryInput 按摘要 ID 解析生成该摘要时交给 LLM 的输入信息。
//
// 与 DescribeEnv 类似：先取摘要记录，按所属对象（Analysis / AnalysisNode）解析出
// 系统提示词模板变量，再按该摘要使用的 Agent Profile（取自摘要记录 ProfileID，为 0 回退
// 内置 agenttypes.BuiltinSummaryProfileID）渲染出真正的系统提示词。
func (s *aiSummaryService) GetAISummaryInput(ctx context.Context, id int64) (*types.AISummaryInput, error) {
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, id)
	if err != nil {
		return nil, err
	}

	content, err := s.content.Resolve(ctx, summary.OwnerType, summary.OwnerID)
	if err != nil {
		return nil, err
	}

	profileID := summary.ProfileID
	if profileID == 0 {
		profileID = agenttypes.BuiltinSummaryProfileID
	}

	profile, systemPrompt, err := s.agentSvc.RenderSystemPrompt(ctx, 0, profileID, content.SystemPromptVars)
	if err != nil {
		return nil, err
	}

	return &types.AISummaryInput{
		Title:        content.Title,
		ProfileName:  profile.Name,
		ProfileID:    profile.ID,
		SystemPrompt: systemPrompt,
		Vars:         content.SystemPromptVars,
		WorkingDir:   content.WorkingDir,
	}, nil
}
