package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// RuntimeEnvType 是与前端 llmEnv.type 约定的「业务上下文」枚举。
//
// 前端通过 setLLMEnv(id, type) 设置全局上下文，后端据此解析出：
//   - SystemPrompt：注入给 Agent 的系统提示词（含工作目录与工具使用约束）；
//   - WorkingDir：Agent 执行时的工作目录。
//
// 这些枚举是前端与后端之间的契约，新增业务上下文时在此登记并同步前端 llm-env.ts。
const (
	EnvTypeScript        = "script"        // 脚本工作区
	EnvTypeAnalysis      = "analysis"      // 分析（analysis 级）
	EnvTypeAnalysisNode  = "analysisNode"  // 分析节点
	EnvTypeProjectReport = "projectReport" // 项目报告
)

// normalizeEnvType 把前端传入的 type 字符串规整为上述规范枚举。
//
// 前端历史版本曾使用 "analysisNodeId" / "analsyisNode" / "analysisId" 等字面量，
// 这里做别名归一，保证旧调用方与 LLM 桥接链路不受影响。
func normalizeEnvType(typ string) string {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "script":
		return EnvTypeScript
	case "analysis", "analysisid":
		return EnvTypeAnalysis
	case "analysisnode", "analysisnodeid", "analsyisnode", "analysis_node", "analysis_node_id":
		return EnvTypeAnalysisNode
	case "projectreport", "project_report":
		return EnvTypeProjectReport
	default:
		return ""
	}
}

// RuntimeContext 是解析后的运行时上下文。
type RuntimeContext struct {
	SystemPrompt string // 注入给 Agent 的完整系统提示词
	WorkingDir   string // Agent 执行工作目录
}

// RuntimeContextResolver 把「业务 env(type,id) → 系统提示词 + 工作目录」的解析逻辑
// 从具体 Handler 中抽离出来，供 LLM 桥接（LLMHandler）与 Agent 会话（AgentHandler）
// 两套调用链路复用，避免重复实现导致的行为漂移。
type RuntimeContextResolver struct {
	cfg         *config.Config
	projectSvc  interfaces.ProjectService
	workflowSvc interfaces.WorkflowService
	analysisSvc interfaces.AnalysisService
}

// NewRuntimeContextResolver 创建运行时上下文解析器。
func NewRuntimeContextResolver(
	cfg *config.Config,
	projectSvc interfaces.ProjectService,
	workflowSvc interfaces.WorkflowService,
	analysisSvc interfaces.AnalysisService,
) *RuntimeContextResolver {
	return &RuntimeContextResolver{
		cfg:         cfg,
		projectSvc:  projectSvc,
		workflowSvc: workflowSvc,
		analysisSvc: analysisSvc,
	}
}

// Resolve 根据 env 解析出系统提示词与工作目录。
//
// env 为 nil 或 type 为空时，回退到当前用户激活项目的默认工作目录。
func (r *RuntimeContextResolver) Resolve(ctx context.Context, userID string, env map[string]any) (*RuntimeContext, error) {
	if r.projectSvc == nil {
		return nil, fmt.Errorf("project service is not initialized")
	}

	lines := []string{
		"You are operating inside Gobrave's LLM runtime.",
		"Follow the runtime context below when choosing tools or file locations.",
		fmt.Sprintf("current_user_id: %s", strings.TrimSpace(userID)),
	}

	var workingDir string
	if env != nil {
		envType := normalizeEnvType(toString(env["type"]))
		if envType == "" {
			dir, err := r.resolveDefaultWorkingDirectory(ctx, userID)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve working directory: %w", err)
			}
			workingDir = dir
		} else {
			id, err := parseEnvID(env["id"])
			if err != nil {
				return nil, err
			}

			var dir string
			switch envType {
			case EnvTypeScript:
				dir, _, err = r.workflowSvc.GetScriptFileByScriptID(ctx, id)
				if err != nil {
					return nil, fmt.Errorf("failed to get script file by script id: %w", err)
				}
				lines = append(lines,
					"When you need to run a script-based task, resolve the script workspace from env.id through the runtime context.",
				)

			case EnvTypeAnalysisNode:
				node, nodeErr := r.analysisSvc.GetAnalysisNodeByID(ctx, id)
				if nodeErr != nil {
					return nil, nodeErr
				}
				dir = node.WorkspaceDir
				lines = append(lines,
					"When you need to run or inspect the current analysis node, use env.id as the analysis_node_id and do not guess a different ID.",
					"For executing workflow scripts in this analysis node context (for example main.R, main.py, or similar entry scripts), you must call the tool run_analysis_node with analysis_node_id=env.id.",
					"Do not execute scripts directly via shell/system calls such as Rscript, python, python3, bash, sh, or equivalent runtime commands.",
					"The runtime will execute the node in the correct container automatically through run_analysis_node.",
				)

			case EnvTypeAnalysis:
				analysis, analysisErr := r.analysisSvc.GetAnalysisByID(ctx, id)
				if analysisErr != nil {
					return nil, analysisErr
				}
				dir = analysis.WorkDir

			case EnvTypeProjectReport:
				report, reportErr := r.projectSvc.GetProjectReportByID(ctx, id)
				if reportErr != nil {
					return nil, reportErr
				}
				dir = utils.GetProjectReportDir(r.storageBaseDir(), report.ProjectID, fmt.Sprintf("%d", report.ID))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return nil, fmt.Errorf("failed to create project report working directory: %w", err)
				}

			default:
				return nil, fmt.Errorf("unsupported runtime env type: %s", envType)
			}
			workingDir = strings.TrimSpace(dir)
		}
	}

	if strings.TrimSpace(workingDir) == "" {
		dir, err := r.resolveDefaultWorkingDirectory(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", err)
		}
		workingDir = dir
	}

	lines = append(lines, fmt.Sprintf("working_directory: %s", strings.TrimSpace(workingDir)))

	return &RuntimeContext{
		SystemPrompt: strings.Join(lines, "\n"),
		WorkingDir:   workingDir,
	}, nil
}

// resolveDefaultWorkingDirectory 返回当前用户激活项目的默认工作目录。
func (r *RuntimeContextResolver) resolveDefaultWorkingDirectory(ctx context.Context, userID string) (string, error) {
	project, err := r.projectSvc.GetActiveProjectByUserID(ctx, strings.TrimSpace(userID))
	if err != nil {
		return "", err
	}
	if project == nil || strings.TrimSpace(project.ProjectID) == "" {
		return "", fmt.Errorf("active project is empty")
	}

	baseDir := r.storageBaseDir()
	if baseDir == "" {
		return "", fmt.Errorf("storage.base_dir is empty")
	}

	workingDir := filepath.Join(baseDir, "data", strings.TrimSpace(project.ProjectID))
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return "", err
	}
	return workingDir, nil
}

// storageBaseDir 返回存储根目录（容忍 cfg/Storage 为空）。
func (r *RuntimeContextResolver) storageBaseDir() string {
	if r.cfg == nil || r.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Storage.BaseDir)
}

// parseEnvID 解析 env.id（可能是 string / float64 / int64 等），返回 int64 业务对象 ID。
func parseEnvID(v any) (int64, error) {
	idStr, ok := v.(string)
	if !ok {
		idStr = fmt.Sprintf("%v", v)
	}
	idStr = strings.TrimSpace(idStr)
	if idStr == "" || idStr == "<nil>" {
		return 0, fmt.Errorf("env.id is required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid env.id: %w", err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("env.id must be a positive integer")
	}
	return id, nil
}
