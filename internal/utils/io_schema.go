package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// IOSchemaFileName 是脚本目录 / 工作流目录下 IO schema 的文件名。
//
// io_schema 不再作为数据库字段持久化，io_schema.json 是唯一数据源：
// 脚本侧见 ScriptIOSchemaPath 等函数，工作流侧见 WorkflowIOSchemaPath 等函数，
// 两者共用本文件里的落盘 / 读取原语，避免各自的格式化与容错逻辑漂移。
const IOSchemaFileName = "io_schema.json"

// writeIOSchemaFile 将 io_schema 内容格式化（两空格缩进）后写入指定路径。
//
// 格式化走 utils.FormatJSONIndent：只重排空白，保留原始键顺序与数字字面量，
// 这样前端压缩过的 JSON 落盘后是可读、易 diff 的格式（git 提交时 diff 才稳定）。
// 内容为空（含纯空白）时不写入（保留磁盘上已有文件），因此不会用空值覆盖历史 schema；
// 内容不是合法 JSON 时返回错误且不落盘，避免把非法内容固化到磁盘。
func writeIOSchemaFile(path, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	formatted, err := FormatJSONIndent([]byte(content))
	if err != nil {
		return fmt.Errorf("invalid io_schema json: %w", err)
	}
	return WriteFileEnsureDir(path, formatted)
}

// readIOSchemaFile 读取指定路径下 io_schema.json 的原始内容。
// 文件不存在时返回 nil（不视为错误），便于调用方按“无 schema”处理。
func readIOSchemaFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// readIOSchema 读取并解析指定路径下的 io_schema.json。
// 文件不存在或内容为空时返回空 map（不视为错误）；内容非法 JSON 时返回错误。
func readIOSchema(path string) (map[string]any, error) {
	data, err := readIOSchemaFile(path)
	if err != nil || len(data) == 0 {
		return map[string]any{}, err
	}
	result := make(map[string]any)
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{}, err
	}
	return result, nil
}

// FormatIOSchemaItems 把 io_schema 的 inputs / outputs 数组转成以 name 为键的 map：
// 每项去掉 name 后，其余字段（type / required / description 等）原样保留。
//
// 例： [{"name":"tsv","type":"file"},{"name":"plot","type":"file"}]
//
//	→  {"tsv":{"type":"file"},"plot":{"type":"file"}}
//
// 该形态与 analysis_nodes.inputs_patterns / output_patterns 的存储结构一致，
// 因此也是脚本可视化节点（buildScriptVisItem）与独立节点 OutputPatterns 的公共构造逻辑。
// name 为空或类型不符的项被跳过（无法作为 handle 键）；raw 不是数组时返回空 map。
func FormatIOSchemaItems(raw any) map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return map[string]any{}
	}

	result := make(map[string]any, len(items))
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}

		formatted := make(map[string]any, len(item))
		for k, v := range item {
			if k == "name" {
				continue
			}
			formatted[k] = v
		}
		result[name] = formatted
	}

	return result
}
