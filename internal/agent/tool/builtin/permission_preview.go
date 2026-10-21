package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/diff"
)

// 本文件提供「授权前预览」的公共构造。
//
// create / update 类分析节点工具不再把原始入参直接作为授权参数，而是构造一段
// unified diff（run.sh / params.json 的改动）放进 ToolPermissionsParams 同款字段
// （file_path / diff）。上层 agent.operationFromToolPermission 会把它们提到
// Operation.Metadata，前端便能在用户批准前渲染 diff —— 与 coder edit / write 一致。

// runScriptContent 返回与 prepare.prepareCommandNode 落盘的 run.sh 完全一致的内容：
// 去掉首尾空白后的命令 + 结尾换行（命令为空时返回空串）。
func runScriptContent(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if !strings.HasSuffix(command, "\n") {
		command += "\n"
	}
	return command
}

// runScriptPath 返回命令型节点 run.sh 的展示路径：优先用节点派生路径，
// 其次按工作目录推导，最后回退到相对文件名（节点尚未创建时）。
func runScriptPath(commandPath, workspaceDir string) string {
	if commandPath != "" {
		return commandPath
	}
	if workspaceDir != "" {
		return filepath.Join(workspaceDir, "run.sh")
	}
	return "run.sh"
}

// paramsJSONContent 按 params.json 的落盘口径（两空格缩进 + 结尾换行）序列化参数。
// params 为 nil 时返回空串（视为「原本没有该文件」）。
func paramsJSONContent(params map[string]any) string {
	if params == nil {
		return ""
	}
	content, err := json.MarshalIndent(params, "", "  ")
	if err != nil {
		return ""
	}
	return string(content) + "\n"
}

// textDiffPreview 生成 before → after 的 unified diff 预览。
func textDiffPreview(path, before, after string) tool.PermissionPreview {
	if path == "" {
		path = "unnamed"
	}
	text, _, _ := diff.GenerateDiff(before, after, path)
	return tool.PermissionPreview{FilePath: path, Diff: text}
}

// analysisNodeForPreview 读取节点当前状态，用于生成更新类工具的 diff 预览。
//
// 任何失败（上下文缺项目 / ID 非法 / 未注入浏览器 / 节点不存在）都返回 ok=false，
// 由调用方退化为「无旧值」的预览；真正的错误仍由工具主体返回给模型。
func analysisNodeForPreview(ctx context.Context, toolName, rawNodeID string) (tool.AnalysisNodeBrief, bool) {
	projectID, err := requireProjectID(ctx, toolName)
	if err != nil {
		return tool.AnalysisNodeBrief{}, false
	}
	nodeID, err := parseAnalysisNodeID(rawNodeID)
	if err != nil {
		return tool.AnalysisNodeBrief{}, false
	}
	browser := tool.ProjectResourceBrowserFrom(ctx)
	if browser == nil {
		return tool.AnalysisNodeBrief{}, false
	}
	brief, err := browser.GetAnalysisNodeByID(ctx, projectID, nodeID)
	if err != nil {
		return tool.AnalysisNodeBrief{}, false
	}
	return brief, true
}
