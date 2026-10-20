package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type agentAnalysisService struct {
	repo         interfaces.AgentAnalysisRepository
	containerMgr *manager.ContainerManager
	cfg          *config.Config
}

func NewAgentAnalysisService(
	repo interfaces.AgentAnalysisRepository,
	containerMgr *manager.ContainerManager,
	cfg *config.Config,
) interfaces.AgentAnalysisService {
	return &agentAnalysisService{repo: repo, containerMgr: containerMgr, cfg: cfg}
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

func (s *agentAnalysisService) DeleteAgentAnalysis(ctx context.Context, id int64) error {
	item, err := s.repo.GetAgentAnalysisByID(ctx, id)
	if err != nil {
		return err
	}
	if item.ContainerInstanceID > 0 && s.containerMgr != nil {
		if err := s.containerMgr.Delete(ctx, item.ContainerInstanceID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			// 容器清理失败不阻塞记录删除；删除请求已尽量提交，避免页面卡住。
			logger.Warnf(ctx, "[AgentAnalysis] delete container failed, agent_analysis_id=%d instance_id=%d err=%v", item.ID, item.ContainerInstanceID, err)
		}
	}
	return s.repo.DeleteAgentAnalysis(ctx, item.ID)
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
