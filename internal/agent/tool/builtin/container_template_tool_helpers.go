package builtin

import (
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
)

// 本文件提供容器模板写工具的公共部分：
//
//   - create/update_container_template_spec 与 create/update_container_template_definition
//     共用的输出结构与 JSON Schema 构造器；
//   - 容器模板 / 运行配置 ID 的解析（容错剥离界面引用前缀）。
//
// 写能力本身由 runtimecontext.ContainerTemplateSpecWriter /
// ContainerTemplateDefinitionWriter 抽象，业务实现见 internal/container。

// ContainerTemplateSpecWriteOutput 是 create/update_container_template_spec 返回给模型的结果。
//
// 直接内嵌 runtimecontext.ContainerTemplateSpecRecord，保证「创建」与「更新」回传字段一致。
type ContainerTemplateSpecWriteOutput struct {
	runtimecontext.ContainerTemplateSpecRecord
	Error string `json:"error,omitempty"`
}

// ContainerTemplateDefinitionWriteOutput 是 create/update_container_template_definition
// 返回给模型的结果。
type ContainerTemplateDefinitionWriteOutput struct {
	runtimecontext.ContainerTemplateDefinitionRecord
	Error string `json:"error,omitempty"`
}

// containerTemplateEnvSchema 构造 env 的 JSON Schema（字符串键 → 任意值）。
func containerTemplateEnvSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "容器环境变量（写入 spec.env，键值对）。值可以是字符串或数字；支持 $VAR 形式的运行时变量（如 $PROJECT_DIR / $USERID）。",
		"additionalProperties": true,
	}
}

// containerTemplateLabelsSchema 构造 labels 的 JSON Schema。
func containerTemplateLabelsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "容器标签（写入 spec.labels，键值对字符串），例如 traefik 路由标签。",
		"additionalProperties": true,
	}
}

// containerTemplateMountsSchema 构造 mounts 的 JSON Schema（挂载列表）。
func containerTemplateMountsSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "容器挂载列表（写入 spec.mounts）。每项描述一个宿主机路径 → 容器路径的挂载；source / target 必填。",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{"type": "string", "description": "宿主机路径（可为 $VAR 变量，如 $PROJECT_DIR）。"},
				"target": map[string]any{"type": "string", "description": "容器内绝对路径。"},
				"mode":   map[string]any{"type": "string", "description": "挂载模式，常见 rw / ro。"},
				"type":   map[string]any{"type": "string", "description": "挂载类型：file 表示 source 按文件处理，其余按目录。"},
				"owner":  map[string]any{"type": "string", "description": "挂载归属的容器用途；设置后只有匹配的容器类型才应用（如 app_session / dag_node）。"},
			},
			"required": []string{"source", "target"},
		},
	}
}

// containerTemplateSchedulingConstraintSchema 构造 scheduling_constraint 的 JSON Schema。
func containerTemplateSchedulingConstraintSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "调度约束（写入 spec.scheduling_constraint），形如 {\"constraints\":[{\"type\":\"node\",\"key\":\"hostname\",\"operator\":\"In\",\"values\":[\"node-1\"]}]}。",
		"properties": map[string]any{
			"constraints": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type":     map[string]any{"type": "string", "description": "约束类型（如 node / pod）。"},
						"key":      map[string]any{"type": "string", "description": "约束键（如 hostname）。"},
						"operator": map[string]any{"type": "string", "description": "运算符（如 In / NotIn / Exists）。"},
						"values":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "候选值列表。"},
					},
					"required": []string{"type", "key", "operator"},
				},
			},
		},
		"required": []string{"constraints"},
	}
}

// parseContainerTemplateSpecID 解析必填的共享运行配置 ID。
//
// 容错剥离可能的 "@container_spec:" 前缀，语义与 parsePositiveID 一致。
func parseContainerTemplateSpecID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@container_spec:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@container_spec:"):])
	}
	return parsePositiveID(idStr, "container_template_spec_id")
}

// parseContainerTemplateDefinitionID 解析必填的绑定行 ID。
//
// 复用 @container:<id> 引用前缀：该前缀来自界面「Ask Agent」插入的引用 chip，
// 其冒号后的值正是 ContainerTemplateDefinition.ID。
func parseContainerTemplateDefinitionID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@container:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@container:"):])
	}
	return parsePositiveID(idStr, "container_template_definition_id")
}
