package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateScriptToolName 是「创建脚本 + 分析节点」工具名。
const CreateScriptToolName = "create_script"

// DefaultScriptType 是 create_script 未指定 script_type 时使用的脚本类型。
const DefaultScriptType = "qmd"

// CreateScriptInput 是 create_script 工具的入参。
//
// ProjectID 不由模型提供：它取自 RuntimeContext.ProjectID（当前用户激活项目），
// 避免模型把脚本写到别的项目里。
type CreateScriptInput struct {
	Name                string `json:"name"`                  // 脚本名称（Script.Name）
	Description         string `json:"description"`           // 脚本描述（Script.Description）
	ContainerTemplateID string `json:"container_template_id"` // 容器运行配置 ID（字符串以保精度），为空时取默认模板
	ScriptType          string `json:"script_type"`           // 脚本类型：qmd（默认）/ r / python / shell / jupyter
	// Inputs / Outputs 是脚本的输入输出契约（写入 io_schema.json 的 inputs / outputs）。
	// 为空时使用系统的默认 schema。
	Inputs   []map[string]any `json:"inputs"`
	Outputs  []map[string]any `json:"outputs"`
	NodeName string           `json:"node_name"` // 分析节点名称（AnalysisNode.NodeName）
}

// CreateScriptOutput 是 create_script 工具返回给模型的结果。
//
// 除 ID 之外，把新脚本 / 新节点在磁盘上的关键路径一并返回，模型可直接读写：
//   - ScriptFilePath：脚本主文件（如 main.R），由 utils.GetScriptFile 推导；
//   - IOSchemaPath：脚本目录下的 io_schema.json，由 utils.ScriptIOSchemaPath 推导；
//   - WorkspaceDir：节点工作目录；
//   - ParamsPath：节点 params.json 路径。
type CreateScriptOutput struct {
	ScriptID       int64  `json:"script_id"`
	ScriptKey      string `json:"script_key,omitempty"`
	ScriptName     string `json:"script_name,omitempty"`
	ScriptFilePath string `json:"script_file_path,omitempty"`
	IOSchemaPath   string `json:"io_schema_path,omitempty"`
	// ScriptType 是实际生效的脚本类型（未传时为 qmd）。
	ScriptType string `json:"script_type,omitempty"`

	// ContainerTemplateID / ContainerTemplateName 是实际生效的容器模板：
	// 未传 container_template_id 时为系统默认模板。
	ContainerTemplateID   int64  `json:"container_template_id,omitempty"`
	ContainerTemplateName string `json:"container_template_name,omitempty"`

	AnalysisNodeID int64  `json:"analysis_node_id"`
	NodeName       string `json:"node_name,omitempty"`
	WorkspaceDir   string `json:"workspace_dir,omitempty"`
	ParamsPath     string `json:"params_path,omitempty"`

	Error string `json:"error,omitempty"`
}

// CreateScript 返回一个「创建脚本 + 分析节点」工具。
//
// 行为：
//   - 从 RuntimeContext 取当前项目 ID（缺失时直接报错，避免写到错误项目）；
//   - script_type 为空时按 DefaultScriptType（qmd）处理；
//   - container_template_id 为空时由 ScriptCreator 解析系统默认容器模板，
//     没有默认模板时回到模型一个错误提示，让用户先去容器模板页配置默认模板；
//   - inputs / outputs 非空时写入脚本目录的 io_schema.json，并用它初始化节点的
//     inputs_patterns / output_patterns（否则回落默认 schema），保证「脚本契约」与
//     「节点记录」从创建那一刻就一致，无需事后再改 schema 重建节点；
//   - 通过注入的 ScriptCreator 落库 Script 与 AnalysisNode，并初始化脚本目录（主文件、
//     io_schema.json）与节点工作目录（params.json 等）；
//   - 把新脚本主文件路径、io_schema.json 路径、节点工作目录与 params.json 路径回传给模型。
//
// 该工具会创建业务数据（写库 + 写盘），因此声明 write 授权：上层在执行前会先走权限门禁。
func CreateScript() tool.Tool {
	return tool.NewFunc(
		CreateScriptToolName,
		"创建一个新的分析脚本，同时为该脚本创建可独立运行的默认分析节点。返回脚本主文件路径、io_schema.json 路径、节点工作目录与 params.json 路径，可直接用于后续编写脚本内容。所属项目取自当前会话上下文，无需传入。请在调用时一次性给出 inputs / outputs（脚本输入输出契约，参数名将与 params.json 的键、脚本内读取的参数名一一对应；不传则使用默认 input1/output）。未指定 script_type 时默认使用 qmd；未指定 container_template_id 时使用系统默认容器模板（没有默认模板时会报错，需让用户先去容器模板页设置默认）。",
		tool.Schema("创建脚本及其分析节点的入参", map[string]any{
			"name":                  tool.StringProperty("脚本名称（同名脚本在同一项目下应唯一，由业务侧校验）。"),
			"description":           tool.StringProperty("脚本用途描述（可选），用于说明该脚本做什么。"),
			"container_template_id": tool.StringProperty("容器运行配置 ID（整数，字符串以保精度）。为空时使用系统默认容器模板。"),
			"script_type":           tool.StringProperty("脚本类型：qmd（默认）/ r / python / shell / jupyter（决定主文件名与初始化模板）。"),
			"inputs":                ioItemsSchema("脚本输入契约列表（io_schema.json 的 inputs）。name 即 params.json 中的参数名，脚本内需按该名取参数；type 常见值：BaseInput / file / string / number / boolean 等。"),
			"outputs":               ioItemsSchema("脚本输出契约列表（io_schema.json 的 outputs）。name 为输出名，type 通常为 file。"),
			"node_name":             tool.StringProperty("分析节点名称（用于在节点列表中展示）。"),
		}, "name", "node_name"),
		func(ctx context.Context, in CreateScriptInput) (CreateScriptOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil || rc.ProjectID <= 0 {
				return CreateScriptOutput{}, fmt.Errorf("create_script: runtime context with project_id is required (no active project in this conversation)")
			}

			name := strings.TrimSpace(in.Name)
			if name == "" {
				return CreateScriptOutput{}, fmt.Errorf("create_script: name is required")
			}
			scriptType := strings.ToLower(strings.TrimSpace(in.ScriptType))
			if scriptType == "" {
				scriptType = DefaultScriptType
			}
			nodeName := strings.TrimSpace(in.NodeName)
			if nodeName == "" {
				nodeName = name
			}

			containerTemplateID, err := parseContainerTemplateID(in.ContainerTemplateID)
			if err != nil {
				return CreateScriptOutput{}, err
			}

			creator := tool.ScriptCreatorFrom(ctx)
			if creator == nil {
				return CreateScriptOutput{}, fmt.Errorf("create_script: script creator is not available in this runtime")
			}

			result, err := creator.CreateScriptWithNode(ctx, tool.ScriptCreateInput{
				ProjectID:           rc.ProjectID,
				Name:                name,
				Description:         strings.TrimSpace(in.Description),
				ContainerTemplateID: containerTemplateID,
				ScriptType:          scriptType,
				Inputs:              normalizeIOSchemaItems(in.Inputs),
				Outputs:             normalizeIOSchemaItems(in.Outputs),
				NodeName:            nodeName,
			})
			if err != nil {
				return CreateScriptOutput{Error: err.Error()}, nil
			}

			out := CreateScriptOutput{
				ScriptID:              result.ScriptID,
				ScriptKey:             result.ScriptKey,
				ScriptName:            result.ScriptName,
				ScriptFilePath:        result.ScriptFilePath,
				IOSchemaPath:          result.IOSchemaPath,
				ScriptType:            scriptType,
				ContainerTemplateID:   result.ContainerTemplateID,
				ContainerTemplateName: result.ContainerTemplateName,
				AnalysisNodeID:        result.AnalysisNodeID,
				NodeName:              result.NodeName,
				WorkspaceDir:          result.WorkspaceDir,
				ParamsPath:            result.ParamsPath,
			}

			return out, nil
		},
	).WithPermission("write", "", "创建脚本及其分析节点（写库并初始化脚本 / 节点目录）")
}

// ioItemsSchema 构造 io_schema inputs / outputs 的 JSON Schema（对象数组）。
func ioItemsSchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "参数名（唯一，params.json 中的键）。"},
				"type":        map[string]any{"type": "string", "description": "输入 / 输出类型，如 BaseInput、file、string、number、boolean。"},
				"label":       map[string]any{"type": "string", "description": "表单展示名（可选，默认同 name）。"},
				"required":    map[string]any{"type": "boolean", "description": "是否必填（可选，默认 true）。"},
				"description": map[string]any{"type": "string", "description": "参数说明，会展示在人类用户的表单上（可选）。"},
			},
			"required": []string{"name"},
		},
	}
}

// normalizeIOSchemaItems 清洗模型给出的 io_schema 条目：
//   - 丢弃 name 为空的条目（无法作为参数键）；
//   - 缺失 label 时用 name 补齐，缺失 required 时补 true，缺失 type 时补 BaseInput。
//
// 输出侧的默认 type 由上层（container）统一改为 file，这里只保证结构完整。
// 返回 nil 表示「未提供」，由上层回落默认 schema。
func normalizeIOSchemaItems(items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		name := strings.TrimSpace(fmt.Sprintf("%v", item["name"]))
		if name == "" || name == "<nil>" {
			continue
		}
		normalized := make(map[string]any, len(item)+3)
		for k, v := range item {
			normalized[k] = v
		}
		normalized["name"] = name
		if strings.TrimSpace(fmt.Sprintf("%v", normalized["type"])) == "" || fmt.Sprint(normalized["type"]) == "<nil>" {
			normalized["type"] = "BaseInput"
		}
		if strings.TrimSpace(fmt.Sprintf("%v", normalized["label"])) == "" || fmt.Sprint(normalized["label"]) == "<nil>" {
			normalized["label"] = name
		}
		if _, ok := normalized["required"]; !ok {
			normalized["required"] = true
		}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseContainerTemplateID 解析 container_template_id；为空时返回 0（由业务侧解析默认模板）。
func parseContainerTemplateID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("create_script: invalid container_template_id %q: %w", idStr, err)
	}
	if id < 0 {
		return 0, fmt.Errorf("create_script: container_template_id must not be negative")
	}
	return id, nil
}
