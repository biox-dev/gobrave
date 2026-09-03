package service

import (
	"context"
	"encoding/json"

	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

type aiSummaryService struct {
	summaryRepo   interfaces.AISummaryRepository
	containerRepo interfaces.ContainerRepository
	content       manager.AISummaryContentProvider
}

func NewAISummaryService(
	summaryRepo interfaces.AISummaryRepository,
	containerRepo interfaces.ContainerRepository,
	content manager.AISummaryContentProvider,
) interfaces.AISummaryService {
	return &aiSummaryService{summaryRepo: summaryRepo, containerRepo: containerRepo, content: content}
}

// CreateAISummary 创建摘要记录（pending 状态）并投递 outbox 事件，由
// AISummaryWorker 异步消费生成摘要内容。
func (s *aiSummaryService) CreateAISummary(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (*types.AISummary, error) {
	summary := &types.AISummary{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Status:    types.SummaryStatusPending,
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

// ListAISummariesByOwner 按所属对象类型与 ID 查询摘要列表。
func (s *aiSummaryService) ListAISummariesByOwner(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) ([]*types.AISummary, error) {
	return s.summaryRepo.ListAISummariesByOwner(ctx, ownerType, ownerID)
}

// UpdateAISummary 按摘要 ID 更新标题与内容，nil 表示不修改对应字段。
func (s *aiSummaryService) UpdateAISummary(ctx context.Context, id int64, title, content *string) (*types.AISummary, error) {
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
		Title:        content.Title,
		SystemPrompt: content.SystemPrompt,
		WorkingDir:   content.WorkingDir,
		Text:         content.Text,
	}, nil
}
