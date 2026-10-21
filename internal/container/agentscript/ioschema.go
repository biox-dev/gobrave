package agentscript

import (
	"encoding/json"
	"fmt"
	"strings"
)

// defaultIOSchemaJSON 是新建脚本默认写入 io_schema.json 的内容。
//
// 与 handler.GetDefaultIOSchame 保持同一口径（一个输入、一个输出），但不直接引用
// handler 包：container 已是 handler 的下游，再反向依赖会形成循环引用。
const defaultIOSchemaJSON = `{
    "inputs": [
        {
            "label": "input1",
            "name": "input1",
            "type": "BaseInput",
            "required": true
        }
    ],
    "outputs": [
        {
            "name": "output",
            "type": "file"
        }
    ],
    "params": [],
    "resources": {
        "cpu": 4,
        "memory": "6GB"
    }
}
`

// defaultIOSchemaResourcesJSON 是新建脚本 io_schema 的默认 resources 段（与 GetDefaultIOSchame 一致）。
const defaultIOSchemaResourcesJSON = `{
    "cpu": 4,
    "memory": "6GB"
}`

// buildIOSchemaJSON 把工具传入的 inputs / outputs 组装成完整的 io_schema.json 文本。
//
// inputs / outputs 都为空时直接返回 defaultIOSchemaJSON（保持既有默认口径）。
// 只提供其中一侧时，另一侧回落默认值：inputs 回落到默认的单个 input1，outputs 回落到
// 默认的 output。条目内缺失的字段按如下规则补齐（与 utils.FormatIOSchemaItems 的消费
// 口径一致：name 是参数键，type / required / label 会展示在人类用户的表单上）：
//   - name / type 必须有值，name 为空的条目被丢弃；
//   - label 缺失时取 name；inputs 的 required 缺失时按 true；
//   - outputs 的 type 缺失时按 file。
func buildIOSchemaJSON(inputs, outputs []map[string]any) (string, error) {
	normalizedInputs := normalizeIOSchemaSide(inputs, "BaseInput", true)
	normalizedOutputs := normalizeIOSchemaSide(outputs, "file", false)
	if len(normalizedInputs) == 0 && len(normalizedOutputs) == 0 {
		return defaultIOSchemaJSON, nil
	}

	var defaultSchema map[string]any
	if err := json.Unmarshal([]byte(defaultIOSchemaJSON), &defaultSchema); err != nil {
		return "", fmt.Errorf("create_script: invalid builtin default io_schema: %w", err)
	}

	schema := map[string]any{
		"inputs":  normalizedInputs,
		"outputs": normalizedOutputs,
	}
	if len(normalizedInputs) == 0 {
		schema["inputs"] = defaultSchema["inputs"]
	}
	if len(normalizedOutputs) == 0 {
		schema["outputs"] = defaultSchema["outputs"]
	}
	schema["params"] = []any{}
	var resources map[string]any
	if err := json.Unmarshal([]byte(defaultIOSchemaResourcesJSON), &resources); err == nil {
		schema["resources"] = resources
	}

	raw, err := json.MarshalIndent(schema, "", "    ")
	if err != nil {
		return "", fmt.Errorf("create_script: failed to marshal io_schema: %w", err)
	}
	return string(raw) + "\n", nil
}

// normalizeIOSchemaSide 清洗 inputs / outputs 的一侧；名称重复时以后出现的为准。
func normalizeIOSchemaSide(items []map[string]any, defaultType string, defaultRequired bool) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]int, len(items))
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
		if v, _ := normalized["type"].(string); strings.TrimSpace(v) == "" {
			normalized["type"] = defaultType
		}
		if v, _ := normalized["label"].(string); strings.TrimSpace(v) == "" {
			normalized["label"] = name
		}
		if _, ok := normalized["required"]; !ok {
			normalized["required"] = defaultRequired
		}
		if idx, ok := seen[name]; ok {
			out[idx] = normalized
			continue
		}
		seen[name] = len(out)
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// defaultScriptInitContent 是新建脚本主文件的初始内容占位（按脚本类型区分）。
func defaultScriptInitContent(scriptType string) string {
	switch strings.ToLower(strings.TrimSpace(scriptType)) {
	case "jupyter":
		return "{\n \"cells\": [],\n \"metadata\": {},\n \"nbformat\": 4,\n \"nbformat_minor\": 5\n}\n"
	default:
		return "\n"
	}
}
