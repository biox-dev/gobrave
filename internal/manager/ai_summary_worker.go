package manager

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent"
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
	bus           event.Bus
	agentService  *agent.AgentService
	content       AISummaryContentProvider
}

// NewAISummaryWorker 创建 AISummaryWorker。
func NewAISummaryWorker(
	summaryRepo interfaces.AISummaryRepository,
	containerRepo interfaces.ContainerRepository,
	bus event.Bus,
	agentService *agent.AgentService,
	content AISummaryContentProvider,
) *AISummaryWorker {
	return &AISummaryWorker{
		summaryRepo:   summaryRepo,
		containerRepo: containerRepo,
		bus:           bus,
		agentService:  agentService,
		content:       content,
	}
}

// Handle 分发事件到对应的处理逻辑。
func (w *AISummaryWorker) Handle(evt event.Event) {
	switch e := evt.(type) {
	case AISummaryGenerateRequestEvent:
		w.handleGenerateRequest(context.Background(), e)
	}
}

// handleGenerateRequest 解析载荷并执行摘要生成，随后标记 outbox 事件状态。
func (w *AISummaryWorker) handleGenerateRequest(ctx context.Context, req AISummaryGenerateRequestEvent) {
	var payload types.AISummaryGeneratePayload
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[AISummaryWorker] unmarshal payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.containerRepo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	if err := w.process(ctx, payload.SummaryID); err != nil {
		logger.Errorf(ctx, "[AISummaryWorker] process summary failed, summary_id=%d err=%v", payload.SummaryID, err)
		w.markFailed(ctx, payload.SummaryID, err)

		_ = w.containerRepo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	if err := w.containerRepo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[AISummaryWorker] mark outbox sent failed, outbox_id=%d err=%v", req.OutboxID, err)
	}
}

// process 执行摘要生成，完整调用链路为：
//
//	加载记录 → 置为生成中 → 解析所属对象原始内容 → 创建 Agent 任务并绑定摘要 →
//	同步执行生成摘要 → 更新状态。
//
// 这里先通过 AgentService.CreateTask 创建一次性任务、立即把 taskID 写入摘要记录，
// 再通过 RunTaskSyncByID 同步执行，确保生成期间即使进程崩溃，摘要与任务也不脱钩；
// 若后续摘要需要边生成边推送给前端，可切换为任务流式模式并在 StreamHandler 中逐块发布状态事件。
func (w *AISummaryWorker) process(ctx context.Context, summaryID int64) error {
	summary, err := w.summaryRepo.GetAISummaryByID(ctx, summaryID)
	if err != nil {
		return err
	}

	summary.Status = types.SummaryStatusGenerating
	if err := w.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return err
	}
	w.publishStatus(ctx, summary)

	content, err := w.content.Resolve(ctx, summary.OwnerType, summary.OwnerID)
	if err != nil {
		return fmt.Errorf("resolve summary source: %w", err)
	}

	task, err := w.agentService.CreateTask(ctx, agent.Request{
		SystemPrompt: content.SystemPrompt,
		WorkingDir:   content.WorkingDir,
		Messages: []agent.Message{
			{Role: agent.RoleUser, Content: content.Text},
		},
	})
	if err != nil {
		return fmt.Errorf("create agent task: %w", err)
	}

	// 任务创建后立即把 task 关联到摘要记录，避免执行期间进程崩溃 / 失败时摘要与任务脱钩。
	summary.TaskID = task.ID
	if err := w.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return fmt.Errorf("bind agent task to summary: %w", err)
	}

	result, err := w.agentService.RunTaskSyncByID(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("agent run task: %w", err)
	}

	summary.Content = result.Content
	summary.Status = types.SummaryStatusSuccess
	if content.Title != "" {
		summary.Title = content.Title
	}
	if summary.Title == "" {
		summary.Title = fmt.Sprintf("摘要：%s-%d", summary.OwnerType, summary.OwnerID)
	}
	if err := w.summaryRepo.UpdateAISummary(ctx, summary); err != nil {
		return err
	}
	w.publishStatus(ctx, summary)

	logger.Infof(ctx, "[AISummaryWorker] summary generated, summary_id=%d owner_type=%s owner_id=%d", summary.ID, summary.OwnerType, summary.OwnerID)
	return nil
}

// markFailed 将错误信息写入摘要内容，置为失败状态并发布状态事件。
func (w *AISummaryWorker) markFailed(ctx context.Context, summaryID int64, err error) {
	summary, gerr := w.summaryRepo.GetAISummaryByID(ctx, summaryID)
	if gerr != nil {
		logger.Errorf(ctx, "[AISummaryWorker] load summary for failure marking failed, summary_id=%d err=%v", summaryID, gerr)
		return
	}

	summary.Content = err.Error()
	summary.Status = types.SummaryStatusFailed
	if uerr := w.summaryRepo.UpdateAISummary(ctx, summary); uerr != nil {
		logger.Errorf(ctx, "[AISummaryWorker] update summary failed status failed, summary_id=%d err=%v", summary.ID, uerr)
	}
	w.publishStatus(ctx, summary)
}

// publishStatus 发布摘要状态变更事件，供 AISummaryEventHandler 推送到前端。
func (w *AISummaryWorker) publishStatus(ctx context.Context, summary *types.AISummary) {
	if w.bus == nil || summary == nil {
		return
	}
	w.bus.Publish(types.AISummaryStatusEvent{
		SummaryID: summary.ID,
		OwnerType: summary.OwnerType,
		OwnerID:   summary.OwnerID,
		Status:    summary.Status,
	})
}
