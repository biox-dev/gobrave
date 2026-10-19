package coder

// 本文件保留 opencode 工具的轻量响应结构，便于工具内部逻辑原样复用。
//
// opencode 的 ToolResponse 里还有 Metadata（供 TUI 展示 diff 统计等）。gobrave 的
// tool.Tool 只把 Content 回传给模型，因此这里丢弃 Metadata，仅保留「文本 + 是否失败」。

// ToolResponse 是工具内部产生的文本结果。
type ToolResponse struct {
	// Content 回传给模型的文本。
	Content string
	// IsError 表示这次调用失败（模型可据此纠正）。
	IsError bool
}

// NewTextResponse 构造成功结果。
func NewTextResponse(content string) ToolResponse {
	return ToolResponse{Content: content}
}

// NewTextErrorResponse 构造失败结果。
func NewTextErrorResponse(content string) ToolResponse {
	return ToolResponse{Content: content, IsError: true}
}

// WithResponseMetadata 兼容 opencode 的调用形式；元信息不参与模型回传，直接忽略。
func WithResponseMetadata(response ToolResponse, _ any) ToolResponse {
	return response
}

// result 把内部响应转成 tool.Tool 的返回值：失败映射为 error。
func (r ToolResponse) result() (any, error) {
	if r.IsError {
		return nil, toolError(r.Content)
	}
	return r.Content, nil
}
