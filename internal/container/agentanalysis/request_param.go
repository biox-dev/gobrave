package agentanalysis

import (
	"encoding/json"
	"strings"
)

// DecodeRequestParam 把节点上保存的 request_param JSON 文本反解成 map。
//
// 节点字段为字符串（types.AnalysisNode.RequestParam）：运行前未传参时用它回退到
// 「节点已保存的参数」，只读浏览（get_analysis_node）也用它回传结构化参数。
// 内容为空 / 非法 JSON / 空对象时返回 nil。
func DecodeRequestParam(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
