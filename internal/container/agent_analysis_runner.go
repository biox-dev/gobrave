package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
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
	repo          interfaces.AgentAnalysisRepository
	containerRepo interfaces.ContainerRepository
	containerMgr  *manager.ContainerManager
	cfg           *config.Config

	pollInterval   time.Duration
	defaultTimeout time.Duration
	maxLogBytes    int
}

// newAgentAnalysisRunner 构建 AgentAnalysis 执行器并注入内置工具体系。
func newAgentAnalysisRunner(
	repo interfaces.AgentAnalysisRepository,
	containerRepo interfaces.ContainerRepository,
	containerMgr *manager.ContainerManager,
	cfg *config.Config,
) tool.AgentAnalysisRunner {
	return &agentAnalysisRunner{
		repo:           repo,
		containerRepo:  containerRepo,
		containerMgr:   containerMgr,
		cfg:            cfg,
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
	if in.TemplateID <= 0 {
		return result, fmt.Errorf("template_id is required")
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return result, fmt.Errorf("command is required")
	}
	if r.repo == nil || r.containerRepo == nil || r.containerMgr == nil {
		return result, fmt.Errorf("agent analysis services are not configured")
	}

	baseDir := r.storageBaseDir()
	if baseDir == "" {
		return result, fmt.Errorf("storage.base_dir is not configured")
	}

	item := &types.AgentAnalysis{
		ID:         utils.GenerateID(),
		ProjectID:  in.ProjectID,
		Name:       name,
		TemplateID: in.TemplateID,
		Command:    command,
		Status:     types.AgentAnalysisPending,
	}
	item.WorkspaceDir = utils.GetAgentAnalysisDir(baseDir, item.ProjectID, item.ID)
	item.HydrateDerivedPaths()

	if err := writeAgentAnalysisRunScript(item.WorkspaceDir, command); err != nil {
		return result, fmt.Errorf("prepare agent analysis workspace: %w", err)
	}
	if err := r.repo.CreateAgentAnalysis(ctx, item); err != nil {
		return result, fmt.Errorf("create agent analysis %d: %w", item.ID, err)
	}

	result = tool.AgentAnalysisResult{
		AgentAnalysisID: item.ID,
		Status:          item.Status,
		WorkspaceDir:    item.WorkspaceDir,
		CommandPath:     item.CommandPath,
		LogPath:         item.LogPath,
	}

	// CreateByTemplate 只落库并写入 outbox；真正的 runtime.Create/Start 由
	// ContainerCreateWorker.handleCreateRequest 异步完成。
	inst, err := r.containerMgr.CreateByTemplate(ctx, item.TemplateID, types.ContainerOwnerAgentAnalysis, item.ID, item.Name)
	if err != nil {
		r.persistResult(ctx, item, nil, types.AgentAnalysisFailed, err)
		result.Status = types.AgentAnalysisFailed
		result.Log = readAgentAnalysisLog(item.LogPath, r.maxLogBytes)
		return result, fmt.Errorf("enqueue agent analysis container: %w", err)
	}

	item.ContainerInstanceID = inst.ID
	result.ContainerInstanceID = inst.ID
	if err := r.repo.UpdateAgentAnalysisByID(ctx, item.ID, map[string]any{"container_instance_id": inst.ID}); err != nil {
		logger.Warnf(ctx, "[agentAnalysisRunner] persist container instance failed, agent_analysis_id=%d instance_id=%d err=%v", item.ID, inst.ID, err)
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

// writeAgentAnalysisRunScript 创建工作目录并写入 run.sh。
func writeAgentAnalysisRunScript(workspaceDir, command string) error {
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return err
	}
	script := "#!/usr/bin/env bash\nset -euo pipefail\n\n" + strings.TrimSpace(command) + "\n"
	return os.WriteFile(filepath.Join(workspaceDir, "run.sh"), []byte(script), 0o755)
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

// storageBaseDir 返回存储根目录（容忍 cfg/Storage 为空）。
func (r *agentAnalysisRunner) storageBaseDir() string {
	if r == nil || r.cfg == nil || r.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Storage.BaseDir)
}
