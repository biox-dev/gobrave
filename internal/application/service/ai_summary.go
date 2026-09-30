package service

import (
	"context"
	"encoding/json"
	"strings"

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
	content       manager.AISummaryContentProvider
	// analysisRepo 用于解析摘要所属对象的输出目录，以补充响应中的 URL 前缀。
	analysisRepo interfaces.AnalysisRepository
	cfg          *config.Config
}

func NewAISummaryService(
	summaryRepo interfaces.AISummaryRepository,
	containerRepo interfaces.ContainerRepository,
	content manager.AISummaryContentProvider,
	analysisRepo interfaces.AnalysisRepository,
	cfg *config.Config,
) interfaces.AISummaryService {
	return &aiSummaryService{
		summaryRepo:   summaryRepo,
		containerRepo: containerRepo,
		content:       content,
		analysisRepo:  analysisRepo,
		cfg:           cfg,
	}
}

// CreateAISummary 创建摘要记录（pending 状态）并投递 outbox 事件，由
// AISummaryWorker 异步消费生成摘要内容。
//
// profile 为生成时使用的 Agent Profile 名称，为空表示使用内置 summary Profile。
func (s *aiSummaryService) CreateAISummary(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64, profile string) (*types.AISummary, error) {
	summary := &types.AISummary{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Status:    types.SummaryStatusPending,
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
	summary.Content = "" // 清空旧内容，等待重新生成
	if err := s.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return nil, err
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

func (s *aiSummaryService) GetAISummaryByID(ctx context.Context, id int64) (*types.AISummary, error) {
	return s.summaryRepo.GetAISummaryByID(ctx, id)
}

// ListAISummariesByOwner 按所属对象类型与 ID 查询摘要列表，并为每个 item 补充
// 所属对象输出目录对应的 /data-analysis URL 前缀。
func (s *aiSummaryService) ListAISummariesByOwner(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) ([]*types.AISummary, error) {
	items, err := s.summaryRepo.ListAISummariesByOwner(ctx, ownerType, ownerID)
	if err != nil {
		return nil, err
	}

	prefix := s.resolveOwnerURLPrefix(ctx, ownerType, ownerID)
	if prefix == "" {
		return items, nil
	}
	for _, item := range items {
		if item != nil {
			item.Prefix = prefix
		}
	}

	return items, nil
}

// resolveOwnerURLPrefix 解析摘要所属对象输出目录对应的 URL 前缀。
// 对象不存在或目录为空时返回空串：前缀只是响应增强字段，
// 不应阻塞摘要列表本身的返回。
func (s *aiSummaryService) resolveOwnerURLPrefix(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) string {
	if s.analysisRepo == nil {
		return ""
	}

	var outputDir string
	switch ownerType {
	case types.SummaryOwnerAnalysisNode:
		node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, ownerID)
		if err != nil {
			logger.Warnf(ctx, "[AISummary] resolve analysis node output dir failed, analysis_node_id=%d err=%v", ownerID, err)
			return ""
		}
		outputDir = node.OutputDir
	case types.SummaryOwnerAnalysis:
		analysis, err := s.analysisRepo.GetAnalysisByID(ctx, ownerID)
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

	baseDir := ""
	if s.cfg != nil && s.cfg.Storage != nil {
		baseDir = s.cfg.Storage.BaseDir
	}
	return utils.GetAnalysisURLPrefix(baseDir, outputDir)
}

// UpdateAISummary 按摘要 ID 更新标题、内容与 Agent Profile，nil 表示不修改对应字段。
func (s *aiSummaryService) UpdateAISummary(ctx context.Context, id int64, title, content, profile *string) (*types.AISummary, error) {
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if title != nil {
		summary.Title = *title
	}
	if content != nil {
		summary.Content = *content
	}
	if profile != nil {
		summary.Profile = strings.TrimSpace(*profile)
	}

	if err := s.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return nil, err
	}

	return summary, nil
}

// DeleteAISummary 按摘要 ID 删除摘要记录。
func (s *aiSummaryService) DeleteAISummary(ctx context.Context, id int64) error {
	return s.summaryRepo.DeleteAISummary(ctx, id)
}

// GetAISummaryInput 按所属对象类型与 ID 解析生成摘要时交给 LLM 的输入信息。
func (s *aiSummaryService) GetAISummaryInput(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (*types.AISummaryInput, error) {
	content, err := s.content.Resolve(ctx, ownerType, ownerID)
	if err != nil {
		return nil, err
	}

	return &types.AISummaryInput{
		Title: content.Title,
		// SystemPrompt: content.SystemPrompt,
		WorkingDir: content.WorkingDir,
		Text:       content.Text,
	}, nil
}
