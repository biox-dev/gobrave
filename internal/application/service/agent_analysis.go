package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type agentAnalysisService struct {
	repo          interfaces.AgentAnalysisRepository
	containerRepo interfaces.ContainerRepository
	containerMgr  *manager.ContainerManager
	cfg           *config.Config
}

func NewAgentAnalysisService(
	repo interfaces.AgentAnalysisRepository,
	containerRepo interfaces.ContainerRepository,
	containerMgr *manager.ContainerManager,
	cfg *config.Config,
) interfaces.AgentAnalysisService {
	return &agentAnalysisService{
		repo:          repo,
		containerRepo: containerRepo,
		containerMgr:  containerMgr,
		cfg:           cfg,
	}
}

func (s *agentAnalysisService) CreateAgentAnalysis(ctx context.Context, input interfaces.AgentAnalysisCreateInput) (*types.AgentAnalysis, error) {
	if input.ProjectID <= 0 {
		return nil, fmt.Errorf("project id is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if input.TemplateID <= 0 {
		return nil, fmt.Errorf("template_id is required")
	}
	command := strings.TrimSpace(input.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	baseDir := s.storageBaseDir()
	if baseDir == "" {
		return nil, fmt.Errorf("storage.base_dir is not configured")
	}

	id := utils.GenerateID()
	workspaceDir, err := resolveAgentAnalysisWorkspace(baseDir, input.ProjectID, id, input.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	if err := writeAgentAnalysisRunScript(workspaceDir, command); err != nil {
		return nil, fmt.Errorf("prepare agent analysis workspace: %w", err)
	}

	item := &types.AgentAnalysis{
		ID:           id,
		ProjectID:    input.ProjectID,
		Name:         name,
		TemplateID:   input.TemplateID,
		Command:      command,
		Status:       types.AgentAnalysisPending,
		WorkspaceDir: workspaceDir,
	}
	item.HydrateDerivedPaths()

	if err := s.repo.CreateAgentAnalysis(ctx, item); err != nil {
		return nil, fmt.Errorf("create agent analysis %d: %w", id, err)
	}

	inst, err := s.containerMgr.CreateByTemplate(ctx, item.TemplateID, types.ContainerOwnerAgentAnalysis, item.ID, item.Name)
	if err != nil {
		_ = s.repo.UpdateAgentAnalysisByID(ctx, item.ID, map[string]any{
			"status":        types.AgentAnalysisFailed,
			"error_message": err.Error(),
		})
		return nil, fmt.Errorf("enqueue agent analysis container: %w", err)
	}

	item.ContainerInstanceID = inst.ID
	if err := s.repo.UpdateAgentAnalysisByID(ctx, item.ID, map[string]any{
		"container_instance_id": inst.ID,
	}); err != nil {
		logger.Warnf(ctx, "[AgentAnalysis] persist container instance failed, agent_analysis_id=%d instance_id=%d err=%v", item.ID, inst.ID, err)
	}
	return item, nil
}

func (s *agentAnalysisService) GetAgentAnalysis(ctx context.Context, id int64) (*types.AgentAnalysis, error) {
	return s.repo.GetAgentAnalysisByID(ctx, id)
}

func (s *agentAnalysisService) ListAgentAnalysisByProjectID(ctx context.Context, projectID int64) ([]*types.AgentAnalysis, error) {
	return s.repo.ListAgentAnalysisByProjectID(ctx, projectID)
}

func (s *agentAnalysisService) PageAgentAnalysisByProjectID(ctx context.Context, projectID int64, pagination *types.Pagination) ([]*types.AgentAnalysis, int64, error) {
	return s.repo.PageAgentAnalysisByProjectID(ctx, projectID, pagination)
}

func (s *agentAnalysisService) UpdateAgentAnalysis(ctx context.Context, id int64, input interfaces.AgentAnalysisUpdateInput) (*types.AgentAnalysis, error) {
	item, err := s.repo.GetAgentAnalysisByID(ctx, id)
	if err != nil {
		return nil, err
	}

	active := isAgentAnalysisActive(item.Status)
	if active && (input.TemplateID != nil || input.Command != nil || input.WorkspaceDir != nil) {
		return nil, fmt.Errorf("agent analysis %d is %s; template/command/workspace can only be changed after it finishes", id, item.Status)
	}

	updates := map[string]any{}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, fmt.Errorf("name is required")
		}
		updates["name"] = name
	}
	if input.TemplateID != nil {
		if *input.TemplateID <= 0 {
			return nil, fmt.Errorf("template_id must be positive")
		}
		updates["template_id"] = *input.TemplateID
	}

	baseDir := s.storageBaseDir()
	workspaceDir := item.WorkspaceDir
	if input.WorkspaceDir != nil {
		resolved, err := resolveAgentAnalysisWorkspace(baseDir, item.ProjectID, item.ID, *input.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		workspaceDir = resolved
		updates["workspace_dir"] = resolved
	}

	if input.Command != nil {
		command := strings.TrimSpace(*input.Command)
		if command == "" {
			return nil, fmt.Errorf("command is required")
		}
		updates["command"] = command
	}
	if input.Command != nil || input.WorkspaceDir != nil {
		command := item.Command
		if input.Command != nil {
			command = strings.TrimSpace(*input.Command)
		}
		if err := writeAgentAnalysisRunScript(workspaceDir, command); err != nil {
			return nil, fmt.Errorf("prepare agent analysis workspace: %w", err)
		}
	}

	if len(updates) == 0 {
		return item, nil
	}
	if err := s.repo.UpdateAgentAnalysisByID(ctx, item.ID, updates); err != nil {
		return nil, err
	}
	return s.repo.GetAgentAnalysisByID(ctx, item.ID)
}

// RerunAgentAnalysis 重新创建一个容器实例。
//
// 已完成 / 失败 / 已取消的任务可以用当前模板与命令重跑：先补写 run.sh，
// 再创建新的容器实例并把任务指过去，最后尽力删除旧容器。
func (s *agentAnalysisService) RerunAgentAnalysis(ctx context.Context, id int64) (*types.AgentAnalysis, error) {
	item, err := s.repo.GetAgentAnalysisByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if isAgentAnalysisActive(item.Status) {
		return nil, fmt.Errorf("agent analysis %d is %s; stop it before rerunning", id, item.Status)
	}
	if s.containerMgr == nil {
		return nil, fmt.Errorf("container manager is not configured")
	}
	if item.TemplateID <= 0 {
		return nil, fmt.Errorf("template_id is required")
	}
	command := strings.TrimSpace(item.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	// run.sh 可能被用户或上一次运行修改 / 删除，重跑前按当前 Command 重新生成。
	if err := writeAgentAnalysisRunScript(item.WorkspaceDir, command); err != nil {
		return nil, fmt.Errorf("prepare agent analysis workspace: %w", err)
	}

	inst, err := s.containerMgr.CreateByTemplate(ctx, item.TemplateID, types.ContainerOwnerAgentAnalysis, item.ID, item.Name)
	if err != nil {
		return nil, fmt.Errorf("enqueue agent analysis container: %w", err)
	}

	previousInstanceID := item.ContainerInstanceID
	if err := s.repo.UpdateAgentAnalysisByID(ctx, item.ID, map[string]any{
		"container_instance_id": inst.ID,
		"status":                types.AgentAnalysisPending,
		"exit_code":             0,
		"error_message":         "",
		"started_at":            nil,
		"finished_at":           nil,
	}); err != nil {
		return nil, fmt.Errorf("reset agent analysis %d: %w", item.ID, err)
	}

	if previousInstanceID > 0 && previousInstanceID != inst.ID {
		if err := s.deleteContainerInstance(ctx, previousInstanceID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Warnf(ctx, "[AgentAnalysis] delete previous container failed, agent_analysis_id=%d instance_id=%d err=%v", item.ID, previousInstanceID, err)
		}
	}
	return s.repo.GetAgentAnalysisByID(ctx, item.ID)
}

// StopAgentAnalysis 停止正在运行的容器；若容器还在创建队列中，则取消创建。
func (s *agentAnalysisService) StopAgentAnalysis(ctx context.Context, id int64) (*types.AgentAnalysis, error) {
	item, err := s.repo.GetAgentAnalysisByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isAgentAnalysisActive(item.Status) {
		return nil, fmt.Errorf("agent analysis %d is %s; only pending or running can be stopped", id, item.Status)
	}
	if item.ContainerInstanceID <= 0 {
		return nil, fmt.Errorf("agent analysis %d has no container instance yet", id)
	}
	if s.containerMgr == nil {
		return nil, fmt.Errorf("container manager is not configured")
	}
	if s.containerRepo == nil {
		return nil, fmt.Errorf("container repository is not configured")
	}

	inst, err := s.containerRepo.GetContainerInstanceByID(ctx, item.ContainerInstanceID)
	if err != nil {
		return nil, err
	}
	if inst.Status == types.ContainerCreatePending {
		// 还没真正创建，先标记失败让 create worker 丢弃该请求，再提交删除。
		if err := s.containerMgr.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerCreateCanceled"); err != nil {
			return nil, fmt.Errorf("cancel pending container create: %w", err)
		}
		if err := s.containerMgr.Delete(ctx, inst.ID); err != nil {
			logger.Warnf(ctx, "[AgentAnalysis] delete canceled container failed, agent_analysis_id=%d instance_id=%d err=%v", item.ID, inst.ID, err)
		}
	} else if err := s.containerMgr.Stop(ctx, inst.ID); err != nil && !errors.Is(err, types.ErrContainerAlreadyStopped) {
		// creating 等状态不允许 stop，直接提交删除来取消创建 / 销毁容器。
		if deleteErr := s.containerMgr.Delete(ctx, inst.ID); deleteErr != nil {
			return nil, fmt.Errorf("stop container instance %d: %w", inst.ID, err)
		}
	}

	if err := s.repo.UpdateAgentAnalysisByID(ctx, item.ID, map[string]any{
		"status":      types.AgentAnalysisCanceled,
		"finished_at": time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	return s.repo.GetAgentAnalysisByID(ctx, item.ID)
}

func (s *agentAnalysisService) DeleteAgentAnalysis(ctx context.Context, id int64) error {
	item, err := s.repo.GetAgentAnalysisByID(ctx, id)
	if err != nil {
		return err
	}

	instanceID := item.ContainerInstanceID
	if instanceID <= 0 && s.containerRepo != nil {
		// 兼容 ContainerInstanceID 回填失败的历史记录。
		if inst, getErr := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAgentAnalysis, item.ID); getErr == nil && inst != nil {
			instanceID = inst.ID
		}
	}
	if instanceID > 0 && s.containerMgr != nil {
		if err := s.deleteContainerInstance(ctx, instanceID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			// 容器清理失败不阻塞记录删除；删除请求已尽量提交，避免页面卡住。
			logger.Warnf(ctx, "[AgentAnalysis] delete container failed, agent_analysis_id=%d instance_id=%d err=%v", item.ID, instanceID, err)
		}
	}
	return s.repo.DeleteAgentAnalysis(ctx, item.ID)
}

// deleteContainerInstance 提交容器删除请求。
//
// 尚在创建队列中的实例不能直接删除（FSM 只允许 creating/failed -> delete_pending），
// 先标记失败让 create worker 丢弃请求，避免之后创建出无人管理的容器。
func (s *agentAnalysisService) deleteContainerInstance(ctx context.Context, instanceID int64) error {
	if s.containerRepo != nil {
		if inst, err := s.containerRepo.GetContainerInstanceByID(ctx, instanceID); err == nil && inst != nil && inst.Status == types.ContainerCreatePending {
			if transitionErr := s.containerMgr.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerCreateCanceled"); transitionErr != nil {
				logger.Warnf(ctx, "[AgentAnalysis] cancel pending container create failed, instance_id=%d err=%v", instanceID, transitionErr)
			}
		}
	}
	return s.containerMgr.Delete(ctx, instanceID)
}

func (s *agentAnalysisService) storageBaseDir() string {
	if s.cfg == nil || s.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.Storage.BaseDir)
}

func isAgentAnalysisActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case types.AgentAnalysisPending, types.AgentAnalysisRunning:
		return true
	default:
		return false
	}
}

// resolveAgentAnalysisWorkspace 解析 AgentAnalysis 工作目录。
//
// 空值使用默认目录；相对路径以 storage.base_dir 为根；绝对路径必须落在
// storage.base_dir 下（容器只挂载该目录，目录外的路径在容器内不可见）。
func resolveAgentAnalysisWorkspace(baseDir string, projectID, id int64, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return utils.GetAgentAnalysisDir(baseDir, projectID, id), nil
	}
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("storage.base_dir is not configured")
	}

	target := raw
	if !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, target)
	}
	workspace, err := utils.SafePathUnderBase(baseDir, target)
	if err != nil {
		return "", fmt.Errorf("workspace_dir must be under storage.base_dir: %w", err)
	}
	return workspace, nil
}

func writeAgentAnalysisRunScript(workspaceDir, command string) error {
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return err
	}
	script := "#!/usr/bin/env bash\nset -euo pipefail\n\n" + strings.TrimSpace(command) + "\n"
	return os.WriteFile(filepath.Join(workspaceDir, "run.sh"), []byte(script), 0o755)
}
