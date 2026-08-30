package skill

import (
	"encoding/json"
	"fmt"
)

// Result 是一次技能调用的结果。
//
// 无论成功还是失败，Content 都会回传给模型（因此始终是可读文本）；
// IsError 标识这次调用是否失败，模型可据此纠正后续行为。
type Result struct {
	Content string `json:"content"`        // 回传给模型的文本
	Data    any    `json:"data,omitempty"` // 结构化结果（供上层业务使用）
	IsError bool   `json:"is_error"`       // 是否为失败结果
	Err     error  `json:"-"`              // 底层错误（仅供日志 / 观测，不回传模型）
}

// Success 构造成功结果，Content 由 data 序列化而来。
func Success(data any) Result {
	return Result{Content: stringify(data), Data: data}
}

// Failure 构造失败结果（IsError=true）。
func Failure(err error) Result {
	if err == nil {
		err = fmt.Errorf("unknown skill error")
	}
	return Result{Content: err.Error(), IsError: true, Err: err}
}

// stringify 把任意值转成可回传模型的文本：字符串 / 字节原样返回，其余 JSON 序列化。
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case json.RawMessage:
		return string(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
