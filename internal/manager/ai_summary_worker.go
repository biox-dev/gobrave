package manager

import (
	"context"
	"encoding/json"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// OutboxEventTypeAISummaryGenerateRequest 是 AI 摘要生成请求的 outbox 事件类型。
const OutboxEventTypeAISummaryGenerateRequest = "AISummaryGenerateRequest"

// Ensure AISummaryWorker implements event.Handler.
var _ event.Handler = (*AISummaryWorker)(nil)

// AISummaryWorker 订阅 AISummaryGenerateRequestEvent，异步执行 AI 摘要生成。
type AISummaryWorker struct {
	summaryRepo   interfaces.AISummaryRepository
	containerRepo interfaces.ContainerRepository
	// TODO: 注入 LLM 调用能力，用于生成摘要内容。
}

// NewAISummaryWorker 创建 AISummaryWorker。
func NewAISummaryWorker(summaryRepo interfaces.AISummaryRepository, containerRepo interfaces.ContainerRepository) *AISummaryWorker {
	return &AISummaryWorker{summaryRepo: summaryRepo, containerRepo: containerRepo}
}

// Handle 分发事件到对应的处理逻辑。
func (w *AISummaryWorker) Handle(evt event.Event) {
	switch e := evt.(type) {
	case AISummaryGenerateRequestEvent:
		w.handleGenerateRequest(context.Background(), e)
	}
}

// handleCreateRequest 解析载荷并执行摘要生成，随后标记 outbox 事件状态。
func (w *AISummaryWorker) handleGenerateRequest(ctx context.Context, req AISummaryGenerateRequestEvent) {
	var payload types.AISummaryGeneratePayload
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[AISummaryWorker] unmarshal payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.containerRepo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	if err := w.process(ctx, payload.SummaryID); err != nil {
		logger.Errorf(ctx, "[AISummaryWorker] process summary failed, summary_id=%d err=%v", payload.SummaryID, err)
		_ = w.containerRepo.MarkOutboxEventPending(ctx, req.OutboxID)
		return
	}

	if err := w.containerRepo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[AISummaryWorker] mark outbox sent failed, outbox_id=%d err=%v", req.OutboxID, err)
	}
}

// process 执行摘要生成：加载记录 → 置为生成中 → 调用 LLM 生成内容 → 更新状态。
func (w *AISummaryWorker) process(ctx context.Context, summaryID int64) error {
	summary, err := w.summaryRepo.GetAISummaryByID(ctx, summaryID)
	if err != nil {
		return err
	}

	summary.Status = types.SummaryStatusGenerating
	if err := w.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return err
	}

	// TODO: 调用 LLM，根据 summary.OwnerType / summary.OwnerID 拉取 Analysis 或
	// AnalysisNode 的 output 内容并生成摘要。成功后更新 summary.Content 并置
	// Status=SummaryStatusSuccess，失败则置 Status=SummaryStatusFailed。
	return nil
}
