package builtin

import (
	"fmt"
	"strconv"
	"strings"
)

// 列表类内置工具共用的入参归一化阈值。
const (
	// defaultListLimit 是列表工具未给 limit 时的默认返回条数。
	defaultListLimit = 50
	// maxListLimit 是列表工具允许的最大返回条数（超出会被截断到该值）。
	maxListLimit = 200
)

// optionalPositiveID 解析可选的 ID 字符串：空串返回空串（表示不过滤），
// 非空时必须能解析为。1 的正整数；容错剥离界面插入的 "@<prefix>:<id>" 引用前缀。
//
// ID 一律以字符串在工具层流转，避免 int64 大整数在模型侧 JSON 往返时丢失精度。
func optionalPositiveID(raw string, toolName string, field string) (string, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		return "", nil
	}
	// 容错处理 "@analysis:<id>" / "@workflow:<id>" / "@script:<id>" 等引用文本。
	if idx := strings.LastIndex(idStr, ":"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+1:])
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("%s: invalid %s %q: %w", toolName, field, idStr, err)
	}
	if id <= 0 {
		return "", fmt.Errorf("%s: %s must be a positive integer", toolName, field)
	}
	return strconv.FormatInt(id, 10), nil
}

// normalizeListLimit 归一化列表工具返回条数上限：<=0 取默认值，超过上限时截断。
func normalizeListLimit(limit int, toolName string) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%s: limit must not be negative", toolName)
	}
	if limit == 0 {
		return defaultListLimit, nil
	}
	if limit > maxListLimit {
		return maxListLimit, nil
	}
	return limit, nil
}
