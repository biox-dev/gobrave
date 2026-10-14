package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// OutboxEventTypeAISummaryGenerateRequest 是 AI 摘要生成请求的 outbox 事件类型。
const OutboxEventTypeAISummaryGenerateRequest = "AISummaryGenerateRequest"

// Ensure AISummaryWorker implements event.Handler.
var _ event.Handler = (*AISummaryWorker)(nil)

// AISummaryWorker 订阅 AISummaryGenerateRequestEvent，异步执行 AI 摘要生成。
type AISummaryWorker struct {
	summaryRepo   interfaces.AISummaryRepository
	containerRepo interfaces.ContainerRepository
	projectRepo   interfaces.ProjectRepository
	bus           event.Bus
	agentService  *agent.AgentService
	content       AISummaryContentProvider
	cfg           *config.Config
}

// NewAISummaryWorker 创建 AISummaryWorker。
func NewAISummaryWorker(
	summaryRepo interfaces.AISummaryRepository,
	containerRepo interfaces.ContainerRepository,
	projectRepo interfaces.ProjectRepository,
	bus event.Bus,
	agentService *agent.AgentService,
	content AISummaryContentProvider,
	cfg *config.Config,
) *AISummaryWorker {
	return &AISummaryWorker{
		summaryRepo:   summaryRepo,
		containerRepo: containerRepo,
		projectRepo:   projectRepo,
		bus:           bus,
		agentService:  agentService,
		content:       content,
		cfg:           cfg,
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
//
// 生成使用的 Agent Profile 取自摘要记录（用户创建/更新时选定），为 0 时回退到内置
// agent.BuiltinSummaryProfileID，保证旧数据仍按原有行为生成。
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

	// 项目 ID 取自所属对象（Analysis / AnalysisNode），与创建摘要的用户无关。
	if content.ProjectID != 0 {
		summary.ProjectID = content.ProjectID
	}

	profileID := summary.ProfileID
	if profileID == 0 {
		profileID = agent.BuiltinSummaryProfileID
	}

	task, err := w.agentService.CreateTask(ctx, agent.Request{
		WorkingDir:       content.WorkingDir,
		SystemPromptVars: content.SystemPromptVars,
		Messages: []agent.Message{
			{Role: agent.RoleUser, Content: "请根据系统提示词完成对应的任务"},
		},
		Provider:  agent.ProviderCustom,
		ProfileID: profileID,
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

	// 摘要正文落盘到 GetAISummaryFile（data/<projectCode>/AiSummary/<summaryId>/summary.md）。
	// summary.ProjectID 是数值主键，需换成存储目录使用的字符串标识（t_project.project_id）。
	projectCode := ResolveProjectCode(ctx, w.projectRepo, summary.ProjectID)
	if projectCode == "" {
		return fmt.Errorf("resolve project code failed, project_id=%d", summary.ProjectID)
	}
	if err := utils.WriteAISummaryFile(w.storageBaseDir(), projectCode, summary.ID, result.Content); err != nil {
		return fmt.Errorf("write summary file: %w", err)
	}

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

// markFailed 将错误信息写入摘要正文文件，置为失败状态并发布状态事件。
func (w *AISummaryWorker) markFailed(ctx context.Context, summaryID int64, err error) {
	summary, gerr := w.summaryRepo.GetAISummaryByID(ctx, summaryID)
	if gerr != nil {
		logger.Errorf(ctx, "[AISummaryWorker] load summary for failure marking failed, summary_id=%d err=%v", summaryID, gerr)
		return
	}

	// 摘要可能在解析出所属项目之前就失败，这里按所属对象解析一次，保证失败原因也能落盘。
	projectID := w.resolveProjectID(ctx, summary)
	if projectID == 0 {
		logger.Warnf(ctx, "[AISummaryWorker] resolve project id for failed summary failed, summary_id=%d", summary.ID)
	} else {
		summary.ProjectID = projectID
		projectCode := ResolveProjectCode(ctx, w.projectRepo, projectID)
		if projectCode == "" {
			logger.Warnf(ctx, "[AISummaryWorker] resolve project code for failed summary failed, summary_id=%d", summary.ID)
		} else if werr := utils.WriteAISummaryFile(w.storageBaseDir(), projectCode, summary.ID, err.Error()); werr != nil {
			logger.Errorf(ctx, "[AISummaryWorker] write failed summary file failed, summary_id=%d err=%v", summary.ID, werr)
		}
	}

	summary.Status = types.SummaryStatusFailed
	if uerr := w.summaryRepo.UpdateAISummary(ctx, summary); uerr != nil {
		logger.Errorf(ctx, "[AISummaryWorker] update summary failed status failed, summary_id=%d err=%v", summary.ID, uerr)
	}
	w.publishStatus(ctx, summary)
}

// resolveProjectID 返回摘要所属对象的项目 ID：优先使用摘要记录上已持久化的
// ProjectID，缺失（例如尚未生成完成）时回退到所属对象（Analysis / AnalysisNode）解析。
func (w *AISummaryWorker) resolveProjectID(ctx context.Context, summary *types.AISummary) int64 {
	if summary == nil {
		return 0
	}
	if summary.ProjectID != 0 {
		return summary.ProjectID
	}
	if w.content == nil {
		return 0
	}
	content, err := w.content.Resolve(ctx, summary.OwnerType, summary.OwnerID)
	if err != nil {
		logger.Warnf(ctx, "[AISummaryWorker] resolve summary owner failed, summary_id=%d err=%v", summary.ID, err)
		return 0
	}
	return content.ProjectID
}

// storageBaseDir 返回去除首尾空白后的 storage.base_dir，未配置时返回空串。
func (w *AISummaryWorker) storageBaseDir() string {
	if w.cfg == nil || w.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(w.cfg.Storage.BaseDir)
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
