package handler

import "strings"

// toString 把任意值尽力转为去空白的字符串；非 string 一律返回空串。
//
// 原来定义在 llm.go（已删除），仍被 analysis.go / agent_runtime_context.go 使用，
// 因此单独保留在本文件。
func toString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
