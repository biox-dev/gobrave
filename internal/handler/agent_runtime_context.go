package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// RuntimeEnvType 是与前端 llmEnv.type 约定的「业务上下文」枚举。
//
// 前端通过 setLLMEnv(id, type) 设置全局上下文，后端据此解析出：
//   - SystemPromptVars：渲染 Profile 系统提示词模板所需的变量（如工作目录、脚本路径）；
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
//
// 直接复用 tool.RuntimeContext：它只承载可序列化的业务事实（渲染 Profile 系统提示词模板
// 所需的 SystemPromptVars 与工作目录），随 TurnInput → agent.Request.RuntimeContext 一路
// 传递到工具执行期，并随 Request 一起序列化进 agent_tasks.request 持久化（重启后可恢复）。
//
// 工具执行期所需的服务能力（如 AnalysisNodeRunner）不在此结构内：它是进程级静态依赖，
// 改由 agent.Options 注入（见 buildAgentClient），避免服务对象被序列化进 agent_tasks。
type RuntimeContext = tool.RuntimeContext

// RuntimeContextResolver 把「业务 env(type,id) → 系统提示词 + 工作目录」的解析逻辑
// 从具体 Handler 中抽离出来，供 LLM 桥接（LLMHandler）与 Agent 会话（AgentHandler）
// 两套调用链路复用，避免重复实现导致的行为漂移。
type RuntimeContextResolver struct {
	cfg         *config.Config
	projectSvc  interfaces.ProjectService
	workflowSvc interfaces.WorkflowService
	analysisSvc interfaces.AnalysisService
	// summarySvc 用于解析项目报告中的 AI 摘要条目（标题 + 摘要文件路径）。
	summarySvc interfaces.AISummaryService
	// agentSvc 用于在 Describe 中按用户 Profile 渲染真正的系统提示词（可为空）。
	agentSvc *agent.AgentService
}

// NewRuntimeContextResolver 创建运行时上下文解析器。
func NewRuntimeContextResolver(
	cfg *config.Config,
	projectSvc interfaces.ProjectService,
	workflowSvc interfaces.WorkflowService,
	analysisSvc interfaces.AnalysisService,
	summarySvc interfaces.AISummaryService,
	agentSvc *agent.AgentService,
) *RuntimeContextResolver {
	return &RuntimeContextResolver{
		cfg:         cfg,
		projectSvc:  projectSvc,
		workflowSvc: workflowSvc,
		analysisSvc: analysisSvc,
		summarySvc:  summarySvc,
		agentSvc:    agentSvc,
	}
}

// Resolve 根据 env 解析出系统提示词模板变量与工作目录。
//
// 它不再返回拼装好的系统提示词，而是返回 SystemPromptVars；真正的系统提示词由
// AgentService 依据 Profile 模板渲染（见 applyProfile / RenderSystemPrompt）。
//
// env 为 nil 或 type 为空时，回退到当前用户激活项目的默认工作目录。
func (r *RuntimeContextResolver) Resolve(ctx context.Context, userID int64, env map[string]any) (*RuntimeContext, error) {
	if r.projectSvc == nil {
		return nil, fmt.Errorf("project service is not initialized")
	}

	// vars 收集渲染 Profile 系统提示词模板所需的变量（业务上下文事实）。
	vars := map[string]any{}

	var workingDir string
	var label string
	var rtcType string
	if env != nil {
		envType := normalizeEnvType(toString(env["type"]))
		if envType == "" {
			dir, lbl, err := r.resolveDefaultWorkingDirectory(ctx, userID)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve working directory: %w", err)
			}
			workingDir = dir
			label = lbl
		} else {
			rtcType = envType
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
				if s, sErr := r.workflowSvc.GetScriptByID(ctx, id); sErr == nil {
					label = s.Name
				}
				vars["script_workspace_directory"] = dir

			case EnvTypeAnalysisNode:
				node, nodeErr := r.analysisSvc.GetAnalysisNodeByID(ctx, id)

				if nodeErr != nil {
					return nil, nodeErr
				}
				script, err := r.workflowSvc.GetScriptByID(ctx, node.ScriptID)
				if err != nil {
					return nil, fmt.Errorf("failed to get script by id: %w", err)
				}
				project, err := r.projectSvc.GetProjectByID(ctx, node.ProjectID)
				if err != nil {
					return nil, fmt.Errorf("failed to get project by id: %w", err)
				}
				// 获取Script File
				scriptDir, scriptFile, _ := utils.GetScriptFile(r.cfg.Storage.BaseDir, project.ID, script.ScriptType, script.ScriptID)
				scriptFilePath := filepath.Join(scriptDir, scriptFile)

				ioSchemaPath := utils.ScriptIOSchemaPath(r.cfg.Storage.BaseDir, project.ID, script.ScriptID)

				dir = node.WorkspaceDir
				label = node.NodeName

				vars["script_file_path"] = scriptFilePath
				vars["params_file_path"] = node.ParamsPath
				vars["io_schema_path"] = ioSchemaPath
				vars["output_directory"] = node.OutputDir
				vars["script_type"] = script.ScriptType
				vars["analysis_node_id"] = strconv.FormatInt(node.ID, 10)

			case EnvTypeAnalysis:
				analysis, analysisErr := r.analysisSvc.GetAnalysisByID(ctx, id)
				if analysisErr != nil {
					return nil, analysisErr
				}
				dir = analysis.WorkspaceDir
				label = analysis.AnalysisName

			case EnvTypeProjectReport:
				report, reportErr := r.projectSvc.GetProjectReportByID(ctx, id)
				if reportErr != nil {
					return nil, reportErr
				}
				dir = utils.GetProjectReportDir(r.storageBaseDir(), report.ProjectID, fmt.Sprintf("%d", report.ID))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return nil, fmt.Errorf("failed to create project report working directory: %w", err)
				}
				outputFile := filepath.Join(dir, types.DefaultProjectReportFilename)
				label = report.Title
				// 科研报告撰写提示词由 Profile 模板负责组织，这里只提供变量（输出文件路径等）。
				vars["output_file"] = outputFile
				vars["report_title"] = report.Title
				// project_report_items 是报告条目列表，供 pongo2 模板遍历（每个条目仅含 name/content）。
				itemVars, itemErr := r.resolveProjectReportItemVars(ctx, userID, report)
				if itemErr != nil {
					return nil, itemErr
				}
				vars["project_report_items"] = itemVars

			default:
				return nil, fmt.Errorf("unsupported runtime env type: %s", envType)
			}
			workingDir = strings.TrimSpace(dir)
		}
	}

	// 当前用户激活项目：既作为 RuntimeContext.ProjectID（供工具定位项目级数据），
	// 也是默认工作目录的兜底来源。
	var projectID int64
	if project, pErr := r.projectSvc.GetActiveProjectByUserID(ctx, userID); pErr == nil && project != nil {
		projectID = project.ID
	}

	if strings.TrimSpace(workingDir) == "" {
		dir, lbl, err := r.resolveDefaultWorkingDirectory(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", err)
		}
		workingDir = dir
		if label == "" {
			label = lbl
		}
	}

	if label == "" {
		label = "Default workspace"
	}

	vars["workspace_directory"] = strings.TrimSpace(workingDir)
	vars["workspace_label"] = label

	return &RuntimeContext{
		Type:             rtcType,
		ProjectID:        projectID,
		Label:            label,
		SystemPromptVars: vars,
		WorkingDir:       workingDir,
	}, nil
}

// ProjectReportItemVar 是暴露给 pongo2 模板遍历的项目报告条目变量。
// 每个条目包含名称（Name）、内容（Content）与来源类型（OwnerType）：
//   - analysis / analysis_node：名称取 AnalysisName / NodeName，内容为
//     Analysis 工作目录 / 节点输出目录下的 output.md 文件路径；
//   - ai_summary：名称取摘要标题，内容为摘要文件路径（见 utils.GetAISummaryFile）；
//   - custom：名称取用户编辑的标题，内容为用户编辑的正文。
//
// OwnerType 为条目原始来源类型（取值见 types.ProjectReportItemOwnerType），供模板按来源
// 区分处理（如 {% if item.OwnerType == "ai_summary" %}）。
//
// 字段刻意不加 json tag：SystemPromptVars 会随 agent_tasks.request 一起 JSON 往返，
// 不加 tag 时字段名即 JSON 键（Name/Content/OwnerType），无论内存态还是反序列化后，
// 模板都能统一用 {{ item.Name }} / {{ item.Content }} / {{ item.OwnerType }} 访问。
//
// OwnerType 特意声明为 string，而不是 types.ProjectReportItemOwnerType：pongo2 的
// `==` 最终走 Go 的接口相等比较（见 pongo2.Value.EqualValueTo，仅整数/时间有特例），
// 动态类型不同即判定不相等，具名字符串类型永远不等于字符串字面量。一旦这里保留具名
// 类型，内存态渲染时 {% if item.OwnerType == "custom" %} 恒为假，循环体整段不输出；
// 而经 JSON 往返后的副本（agent_tasks 恢复）却变回 string 又能命中，同一模板出现
// 「首次不输出、恢复后正常」的诡异差异。构建时用 string(...) 显式转换即规避此坑。
type ProjectReportItemVar struct {
	Name      string
	Content   string
	OwnerType string
}

// resolveProjectReportItemVars 读取报告下的全部条目并解析为模板变量列表。
// 单个条目解析失败（owner 记录缺失等）时跳过该条目并记录告警，
// 避免一个失效引用导致整份报告上下文不可用。
func (r *RuntimeContextResolver) resolveProjectReportItemVars(ctx context.Context, userID int64, report *types.ProjectReport) ([]ProjectReportItemVar, error) {
	if report == nil {
		return nil, nil
	}

	items, err := r.projectSvc.ListProjectReportItemsByReportID(ctx, userID, report.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project report items: %w", err)
	}

	baseDir := r.storageBaseDir()
	result := make([]ProjectReportItemVar, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		name, content, ok := r.resolveProjectReportItemVar(ctx, baseDir, report, item)
		if !ok {
			logger.Warnf(ctx, "[RuntimeContext] skip project report item, item_id=%d owner_type=%s owner_id=%d", item.ID, item.OwnerType, item.OwnerID)
			continue
		}
		result = append(result, ProjectReportItemVar{Name: name, Content: content, OwnerType: string(item.OwnerType)})
	}
	return result, nil
}

// outputMarkdownPath 拼接 owner（Analysis / AnalysisNode）输出目录下的 output.md 路径。
// 目录为空时返回空串，避免模板拿到悬空的 "output.md"（与 utils 派生路径的空值口径一致）。
func outputMarkdownPath(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, types.DefaultProjectReportFilename)
}

// resolveProjectReportItemVar 按 owner 类型解析单个报告条目的名称与内容
// （名称与内容的口径见 ProjectReportItemVar）。返回 ok=false 表示无法解析
// （owner 缺失、服务不可用或 owner 类型不支持）。
func (r *RuntimeContextResolver) resolveProjectReportItemVar(ctx context.Context, baseDir string, report *types.ProjectReport, item *types.ProjectReportItem) (string, string, bool) {
	switch item.OwnerType {
	case types.ProjectReportItemOwnerAnalysis:
		analysis, err := r.analysisSvc.GetAnalysisByID(ctx, item.OwnerID)
		if err != nil {
			return "", "", false
		}
		// 内容给到 Analysis 工作目录下的 output.md，而不是目录本身：报告撰写
		// 模板需要的是可直接读取的结果文件路径（与 ai_summary 条目的口径一致）。
		return analysis.AnalysisName, outputMarkdownPath(analysis.WorkspaceDir), true

	case types.ProjectReportItemOwnerAnalysisNode:
		node, err := r.analysisSvc.GetAnalysisNodeByID(ctx, item.OwnerID)
		if err != nil {
			return "", "", false
		}
		// 同上：节点输出目录下的 output.md。
		return node.NodeName, outputMarkdownPath(node.OutputDir), true

	case types.ProjectReportItemOwnerAISummary:
		if r.summarySvc == nil {
			return "", "", false
		}
		summary, err := r.summarySvc.GetAISummaryByID(ctx, item.OwnerID)
		if err != nil {
			return "", "", false
		}
		// 摘要文件路径需要项目 ID：优先用摘要记录上的 ProjectID，
		// 尚未回填时回退到报告所属项目，保证路径仍可定位。
		projectID := summary.ProjectID
		if projectID == 0 && report != nil {
			projectID = report.ProjectID
		}
		return summary.Title, utils.GetAISummaryFile(baseDir, projectID, summary.ID), true

	case types.ProjectReportItemOwnerCustom:
		return item.Title, item.Content, true

	default:
		return "", "", false
	}
}

// RuntimeContextInfo 是用于前端展示的轻量上下文描述。
type RuntimeContextInfo struct {
	Type         string         `json:"type"`                        // 归一化后的业务类型（空表示默认工作区）
	Label        string         `json:"label"`                       // 人类可读的名称
	WorkingDir   string         `json:"working_dir"`                 // 工作目录
	SystemPrompt string         `json:"system_prompt,omitempty"`     // 用当前用户 Profile 模板渲染后的真实系统提示词
	Vars         map[string]any `json:"vars,omitempty"`              // 渲染系统提示词模板所用的变量
	ProfileName  string         `json:"profile_name,omitempty"`      // 用于渲染系统提示词的 Profile 名称
	ProfileID    int64          `json:"profile_id,omitempty,string"` // 用于渲染系统提示词的 Profile ID
}

// Describe 解析 env 并返回用于前端展示的上下文描述。
//
// 与 Resolve 共享同一套解析逻辑：先用 env 解析出 SystemPromptVars，再按当前用户选中的
// Profile（profileID）解析其系统提示词模板，用变量渲染出真正的系统提示词。
func (r *RuntimeContextResolver) Describe(ctx context.Context, userID int64, profileID int64, env map[string]any) (*RuntimeContextInfo, error) {
	rc, err := r.Resolve(ctx, userID, env)
	if err != nil {
		return nil, err
	}

	profile, systemPrompt, err := r.agentSvc.RenderSystemPrompt(ctx, userID, profileID, rc.SystemPromptVars)
	if err != nil {
		return nil, err
	}

	return &RuntimeContextInfo{
		Type:         rc.Type,
		Label:        rc.Label,
		WorkingDir:   rc.WorkingDir,
		SystemPrompt: systemPrompt,
		Vars:         rc.SystemPromptVars,
		ProfileName:  profile.Name,
		ProfileID:    profile.ID,
	}, nil
}

// resolveDefaultWorkingDirectory 返回当前用户激活项目的默认工作目录及其项目名（作为展示标签）。
func (r *RuntimeContextResolver) resolveDefaultWorkingDirectory(ctx context.Context, userID int64) (string, string, error) {
	project, err := r.projectSvc.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if project == nil || project.ID == 0 {
		return "", "", fmt.Errorf("active project is empty")
	}

	baseDir := r.storageBaseDir()
	if baseDir == "" {
		return "", "", fmt.Errorf("storage.base_dir is empty")
	}

	workingDir := filepath.Join(baseDir, "data", strconv.FormatInt(project.ID, 10))
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return "", "", err
	}
	return workingDir, strings.TrimSpace(project.ProjectName), nil
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
