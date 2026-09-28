package utils

import "strings"

// AcceptFormats 读取 form item 的 `resolver.accept_formats`，也就是这个输入接受哪些
// 角色（`input_type=assay` 匹配 `go_assay.role`，`input_type=file` 匹配
// `go_dataset_file.role`）。返回去空白、去重后的角色列表并保持声明顺序；
// form item / resolver / accept_formats 缺失或类型不对时返回 nil。
//
// 约定：返回空切片（nil）表示“没有声明角色”，调用方据此决定是“不过滤”还是“没有候选”，
// 例如 ListAssayByProjectID 收到空 roles 时不加 role 条件。
func AcceptFormats(formItem map[string]interface{}) []string {
	if formItem == nil {
		return nil
	}

	resolver, ok := formItem["resolver"].(map[string]interface{})
	if !ok {
		return nil
	}

	return AcceptFormatsFromResolver(resolver)
}

// AcceptFormatsFromResolver 同 AcceptFormats，但直接接收 `resolver` map。
// 同时接受 `[]string`（程序内构造的 formJSON）与 `[]interface{}`（JSON 解析结果）。
func AcceptFormatsFromResolver(resolver map[string]interface{}) []string {
	if resolver == nil {
		return nil
	}

	seen := make(map[string]struct{})
	result := make([]string, 0, 4)
	appendFormat := func(item interface{}) {
		format, ok := item.(string)
		if !ok {
			return
		}
		format = strings.TrimSpace(format)
		if format == "" {
			return
		}
		if _, exists := seen[format]; exists {
			return
		}
		seen[format] = struct{}{}
		result = append(result, format)
	}

	switch v := resolver["accept_formats"].(type) {
	case []string:
		for _, one := range v {
			appendFormat(one)
		}
	case []interface{}:
		for _, one := range v {
			appendFormat(one)
		}
	default:
		return nil
	}

	if len(result) == 0 {
		return nil
	}
	return result
}
