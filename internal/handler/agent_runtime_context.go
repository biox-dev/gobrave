package handler

import (
	"context"
	"fmt"
	"strings"

	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// RuntimeContextResolver 负责把「当前用户 → 默认工作目录」的解析逻辑从具体 Handler
// 中抽离出来，供 Agent 会话（AgentHandler）使用，避免重复实现导致的行为漂移。
type RuntimeContextResolver struct {
	cfg        *config.Config
	projectSvc interfaces.ProjectService
	// agentSvc 用于在 Describe 中按用户 Profile 渲染真正的系统提示词（可为空）。
	agentSvc *agentservice.AgentService
}

// NewRuntimeContextResolver 创建运行时上下文解析器。
func NewRuntimeContextResolver(
	cfg *config.Config,
	projectSvc interfaces.ProjectService,
	agentSvc *agentservice.AgentService,
) *RuntimeContextResolver {
	return &RuntimeContextResolver{
		cfg:        cfg,
		projectSvc: projectSvc,
		agentSvc:   agentSvc,
	}
}

// Resolve 解析当前激活项目，并返回其默认工作目录（data/<projectID>）。
//
// 业务上下文（env）已移除：Agent 一律在激活项目的默认工作目录下执行。
func (r *RuntimeContextResolver) Resolve(ctx context.Context, userID int64) (string, *types.Project, error) {
	if r.projectSvc == nil {
		return "", nil, fmt.Errorf("project service is not initialized")
	}
	workingDir, project, err := r.resolveDefaultWorkingDirectory(ctx, userID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to resolve working directory: %w", err)
	}
	return strings.TrimSpace(workingDir), project, nil
}

// outputMarkdownPath 拼接 owner（Analysis / AnalysisNode）输出目录下的 output.md 路径。
// 目录为空时返回空串，避免模板拿到悬空的 "output.md"（与 utils 派生路径的空值口径一致）。
func outputMarkdownPath(dir string) string {
	return utils.GetProjectReportOutputFile(dir)
}

type RuntimeContextInfo struct {
	// Label        string         `json:"label"`                       // 人类可读的名称（当前激活项目名）
	// WorkingDir   string         `json:"working_dir"`                 // 工作目录
	SystemPrompt string         `json:"system_prompt,omitempty"`     // 用当前用户 Profile 模板渲染后的真实系统提示词
	Vars         map[string]any `json:"vars,omitempty"`              // 渲染系统提示词模板所用的变量
	ProfileName  string         `json:"profile_name,omitempty"`      // 用于渲染系统提示词的 Profile 名称
	ProfileID    int64          `json:"profile_id,omitempty,string"` // 用于渲染系统提示词的 Profile ID
	// Skills 是 Profile 启用的技能名列表（见 Profile.Skills）。
	// 不加 omitempty：空数组表示「不限制技能」（Profile 未配置技能时使用全部技能），
	// 与「没有 Profile」区分开，前端据此展示 Skills 区域。
	Skills []string `json:"skills"`
}

// Describe 返回用于前端展示的上下文描述（工作目录 + 渲染后的系统提示词）。
//
// 业务上下文（env）已移除：工作目录固定为当前激活项目的默认目录；
// 系统提示词仍按当前用户选中的 Profile（profileID）模板 + 工作目录变量渲染。
func (r *RuntimeContextResolver) Describe(ctx context.Context, userID int64, profileID int64) (*RuntimeContextInfo, error) {
	if r.projectSvc == nil {
		return nil, fmt.Errorf("project service is not initialized")
	}
	// workingDir, project, err := r.resolveDefaultWorkingDirectory(ctx, userID)
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to resolve working directory: %w", err)
	// }
	// workingDir = strings.TrimSpace(workingDir)
	// label := strings.TrimSpace(project.ProjectName)
	// if label == "" {
	// 	label = "Default workspace"
	// }

	vars := map[string]any{
		"workspace_directory": "workspace_directory", // 由 Provider 在工具执行期注入（builtin 工具可读取）
	}

	info := &RuntimeContextInfo{
		// Label:      label,
		// WorkingDir: workingDir,
		Vars: vars,
	}

	// Profile 可能解析失败（未配置 ProfileManager 等），此时只返回上下文事实。
	if r.agentSvc == nil {
		return info, nil
	}
	profile, systemPrompt, err := r.agentSvc.RenderSystemPrompt(ctx, userID, profileID, vars)
	if err != nil {
		return nil, err
	}
	info.SystemPrompt = systemPrompt
	if profile != nil {
		info.ProfileName = profile.Name
		info.ProfileID = profile.ID
		// Profile.Skills 为 nil/空表示不限制技能（本轮使用全部技能）。
		info.Skills = profile.Skills
		if info.Skills == nil {
			info.Skills = []string{}
		}
	}

	return info, nil
}

// resolveDefaultWorkingDirectory 返回当前用户激活项目及其默认工作目录。
func (r *RuntimeContextResolver) resolveDefaultWorkingDirectory(ctx context.Context, userID int64) (string, *types.Project, error) {
	project, err := r.projectSvc.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	if project == nil || project.ID == 0 {
		return "", nil, fmt.Errorf("active project is empty")
	}

	baseDir := r.storageBaseDir()
	if baseDir == "" {
		return "", nil, fmt.Errorf("storage.base_dir is empty")
	}

	workingDir, err := utils.EnsureProjectDir(baseDir, project.ID)
	if err != nil {
		return "", nil, err
	}
	return workingDir, project, nil
}

// storageBaseDir 返回存储根目录（容忍 cfg/Storage 为空）。
func (r *RuntimeContextResolver) storageBaseDir() string {
	if r.cfg == nil || r.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Storage.BaseDir)
}
