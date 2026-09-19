package handler

import (
	"encoding/json"

	"github.com/biox-dev/gobrave/internal/types"
)

// buildNodeFormJSON 根据 DAG 节点定义与 io_schema 构造节点表单。
// io_schema 由调用方从脚本目录的 io_schema.json 读取后传入（不再来自 script 字段）。
func buildNodeFormJSON(dagDefinitionRaw string, ioSchema map[string]interface{}, script *types.Script, scriptID string) ([]interface{}, error) {
	formJSON := make([]interface{}, 0)

	nodeInDag := make(map[string]interface{})
	if dagDefinitionRaw != "" {
		dagDefinition := make(map[string]interface{})
		if err := json.Unmarshal([]byte(dagDefinitionRaw), &dagDefinition); err != nil {
			return nil, err
		}

		nodes, _ := dagDefinition["nodes"].([]interface{})
		for _, nodeAny := range nodes {
			node, ok := nodeAny.(map[string]interface{})
			if !ok {
				continue
			}
			nodeScriptID, _ := node["script_id"].(string)
			if nodeScriptID == scriptID {
				nodeInDag = node
				break
			}
		}
	}

	// 合并 DAG 节点字段到 io_schema（不修改调用方传入的 map）。
	mergedSchema := make(map[string]interface{}, len(ioSchema)+len(nodeInDag))
	for k, v := range ioSchema {
		mergedSchema[k] = v
	}
	for k, v := range nodeInDag {
		mergedSchema[k] = v
	}

	if script.Content != "" {
		content := make(map[string]interface{})
		if err := json.Unmarshal([]byte(script.Content), &content); err != nil {
			return nil, err
		}
		if contentFormJSON, ok := content["formJson"].([]interface{}); ok {
			formJSON = append(formJSON, contentFormJSON...)
		}
	}

	if params, ok := mergedSchema["params"].([]interface{}); ok {
		formJSON = append(formJSON, params...)
	}

	return formJSON, nil
}
