package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

const (
	defaultAgentAnalysisTimeout = 30 * time.Minute
	agentAnalysisPollInterval   = 2 * time.Second
	agentAnalysisMaxLogBytes    = 12000
)

var errAgentAnalysisTimeout = errors.New("timeout waiting for agent analysis container to finish")

// agentAnalysisRunner 实现 tool.AgentAnalysisRunner：把一个「名称 + 容器模板 + 命令」
// 的模型请求转换成一次性的 AgentAnalysis 容器任务，并阻塞等待其结束。
//
// 目录约定（全部由 AgentAnalysis.WorkspaceDir 派生）：
//
//	<baseDir>/data/<projectId>/agent-analysis/<agentAnalysisId>/
//	    run.sh    —— 模型给出的 command
//	    command.log —— bash ./run.sh 的 tee 输出
//
// 容器创建完全复用现有异步链路：CreateByTemplate 只入队，由 OutboxDispatcher
// 分发到 ContainerCreateWorker.handleCreateRequest 真正创建并启动容器。
type agentAnalysisRunner struct {
	service       interfaces.AgentAnalysisService
	repo          interfaces.AgentAnalysisRepository
	containerRepo interfaces.ContainerRepository
	containerMgr  *manager.ContainerManager

	pollInterval   time.Duration
	defaultTimeout time.Duration
	maxLogBytes    int
}

// newAgentAnalysisRunner 构建 AgentAnalysis 执行器并注入内置工具体系。
func newAgentAnalysisRunner(
	service interfaces.AgentAnalysisService,
	repo interfaces.AgentAnalysisRepository,
	containerRepo interfaces.ContainerRepository,
	containerMgr *manager.ContainerManager,
) tool.AgentAnalysisRunner {
	return &agentAnalysisRunner{
		service:        service,
		repo:           repo,
		containerRepo:  containerRepo,
		containerMgr:   containerMgr,
		pollInterval:   agentAnalysisPollInterval,
		defaultTimeout: defaultAgentAnalysisTimeout,
		maxLogBytes:    agentAnalysisMaxLogBytes,
	}
}

// RunAgentAnalysis 创建 AgentAnalysis（工作目录 + run.sh + 记录），入队容器创建，
// 等待容器进入终态后回传状态、退出码与命令日志。
func (r *agentAnalysisRunner) RunAgentAnalysis(ctx context.Context, in tool.AgentAnalysisInput) (tool.AgentAnalysisResult, error) {
	var result tool.AgentAnalysisResult

	if in.ProjectID <= 0 {
		return result, fmt.Errorf("project id is required")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return result, fmt.Errorf("name is required")
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return result, fmt.Errorf("command is required")
	}
	if r.repo == nil || r.containerRepo == nil || r.containerMgr == nil || r.service == nil {
		return result, fmt.Errorf("agent analysis services are not configured")
	}

	// container_template_id 可选：未指定时回退到系统默认容器模板（is_default 的绑定行）。
	templateID := in.ContainerTemplateID
	if templateID <= 0 {
		resolved, resolveErr := r.resolveDefaultContainerTemplateID(ctx)
		if resolveErr != nil {
			return result, resolveErr
		}
		templateID = resolved
	}

	item, err := r.service.CreateAgentAnalysis(ctx, interfaces.AgentAnalysisCreateInput{
		ProjectID:    in.ProjectID,
		Name:         name,
		TemplateID:   templateID,
		Command:      command,
		WorkspaceDir: in.WorkspaceDir,
	})
	if err != nil {
		return result, err
	}

	result = tool.AgentAnalysisResult{
		AgentAnalysisID:     item.ID,
		ContainerInstanceID: item.ContainerInstanceID,
		Status:              item.Status,
		WorkspaceDir:        item.WorkspaceDir,
		CommandPath:         item.CommandPath,
		LogPath:             item.LogPath,
	}

	timeout := r.defaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}

	observed, waitErr := r.waitForContainer(ctx, item, timeout)

	status, success, exitCode := classifyAgentAnalysisResult(observed, waitErr)
	r.persistResult(ctx, item, observed, status, waitErr)

	logText := readAgentAnalysisLog(item.LogPath, r.maxLogBytes)
	if strings.TrimSpace(logText) == "" {
		// run.sh 的 tee 目标不可读时（例如容器未真正启动），回落到运行时日志。
		if runtimeLog, logErr := r.containerMgr.GetLogs(context.WithoutCancel(ctx), item.ContainerInstanceID, 200); logErr == nil {
			logText = truncateLog(runtimeLog, r.maxLogBytes)
		}
	}

	result.Status = status
	result.Success = success
	result.ExitCode = exitCode
	result.Log = logText

	if waitErr != nil {
		return result, waitErr
	}
	if !success {
		return result, fmt.Errorf("agent analysis container finished with status %s (exit_code=%d)", observed.Status, exitCode)
	}
	return result, nil
}

// resolveDefaultContainerTemplateID 返回系统默认容器模板（is_default = true 的绑定行）主键。
//
// 模型未指定 container_template_id 时回退到默认模板；没有默认模板时返回带用户指引导的错误：
// 模型会把错误原文回给用户，用户据此到「容器模板」页设置默认模板后重试。
func (r *agentAnalysisRunner) resolveDefaultContainerTemplateID(ctx context.Context) (int64, error) {
	definition, err := r.containerRepo.GetDefaultContainerTemplateDefinition(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("no container_template_id was provided and no default container template is configured; please ask the user to open the Container Template page and set one template as default")
		}
		return 0, fmt.Errorf("failed to resolve default container template: %w", err)
	}
	if definition == nil || definition.ID <= 0 {
		return 0, fmt.Errorf("no container_template_id was provided and no default container template is configured; please ask the user to open the Container Template page and set one template as default")
	}
	return definition.ID, nil
}

// waitForContainer 轮询容器实例状态直到终态；事件驱动在容器侧没有统一入口，
// 因此以持久化状态（唯一事实来源）为准，按固定间隔轮询。
func (r *agentAnalysisRunner) waitForContainer(ctx context.Context, item *types.AgentAnalysis, timeout time.Duration) (*types.ContainerInstance, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	runningMarked := false
	for {
		inst, err := r.containerRepo.GetContainerInstanceByID(ctx, item.ContainerInstanceID)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// ContainerDeleted 会删除实例记录，同样视为终态。
			return &types.ContainerInstance{ID: item.ContainerInstanceID, Status: types.ContainerDeleted}, nil
		case err != nil:
			logger.Warnf(ctx, "[agentAnalysisRunner] get container instance failed, agent_analysis_id=%d instance_id=%d err=%v", item.ID, item.ContainerInstanceID, err)
		default:
			if !runningMarked && inst.Status == types.ContainerRunning {
				runningMarked = true
				updates := map[string]any{"status": types.AgentAnalysisRunning}
				if inst.StartedAt != nil {
					updates["started_at"] = inst.StartedAt
				}
				_ = r.repo.UpdateAgentAnalysisByID(ctx, item.ID, updates)
			}
			if isAgentAnalysisTerminal(inst.Status) {
				return inst, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, errAgentAnalysisTimeout
		case <-ticker.C:
		}
	}
}

// persistResult 把最终状态写回 AgentAnalysis。使用 WithoutCancel 保证请求取消后仍能落库。
func (r *agentAnalysisRunner) persistResult(ctx context.Context, item *types.AgentAnalysis, inst *types.ContainerInstance, status string, runErr error) {
	exitCode := 0
	updates := map[string]any{
		"status":      status,
		"exit_code":   exitCode,
		"finished_at": time.Now().UTC(),
	}
	if inst != nil {
		if inst.ExitCode != nil {
			updates["exit_code"] = *inst.ExitCode
		}
		if inst.StartedAt != nil {
			updates["started_at"] = inst.StartedAt
		}
	}
	switch {
	case status == types.AgentAnalysisDone:
		updates["error_message"] = ""
	case runErr != nil:
		updates["error_message"] = runErr.Error()
	case inst != nil:
		updates["error_message"] = fmt.Sprintf("container finished with status %s", inst.Status)
	default:
		updates["error_message"] = "container instance is unavailable"
	}

	if err := r.repo.UpdateAgentAnalysisByID(context.WithoutCancel(ctx), item.ID, updates); err != nil {
		logger.Warnf(ctx, "[agentAnalysisRunner] persist final status failed, agent_analysis_id=%d status=%s err=%v", item.ID, status, err)
	}
}

// classifyAgentAnalysisResult 由容器状态 / 等待错误推导 AgentAnalysis 终态。
func classifyAgentAnalysisResult(inst *types.ContainerInstance, waitErr error) (status string, success bool, exitCode int) {
	switch {
	case errors.Is(waitErr, errAgentAnalysisTimeout):
		return types.AgentAnalysisTimeout, false, 0
	case waitErr != nil:
		return types.AgentAnalysisCanceled, false, 0
	}
	if inst == nil {
		return types.AgentAnalysisFailed, false, 0
	}
	if inst.ExitCode != nil {
		exitCode = *inst.ExitCode
	}
	// 只有「正常结束（stopped / exited）」且退出码为 0（或运行时未上报退出码）才算成功；
	// ContainerFailed 无论是否带退出码都视为失败。
	if (inst.Status == types.ContainerStopped || inst.Status == types.ContainerExited) && exitCode == 0 {
		return types.AgentAnalysisDone, true, exitCode
	}
	return types.AgentAnalysisFailed, false, exitCode
}

func isAgentAnalysisTerminal(status types.ContainerStatus) bool {
	switch status {
	case types.ContainerStopped, types.ContainerFailed, types.ContainerExited, types.ContainerDeleted:
		return true
	}
	return false
}

// readAgentAnalysisLog 读取 tee 落盘的命令日志；不存在或不可读时返回空串。
func readAgentAnalysisLog(logPath string, maxBytes int) string {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return truncateLog(string(data), maxBytes)
}

func truncateLog(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	return "...(truncated)\n" + text[len(text)-maxBytes:]
}
