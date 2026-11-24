package provider

import (
	"bytes"
	"io"
	"net/http"

	"github.com/biox-dev/gobrave/internal/logger"
)

// maxLoggedBodyBytes 是调试日志里请求体的最大长度（防止超长 prompt / 工具定义刷屏）。
const maxLoggedBodyBytes = 32 * 1024

// debugMiddleware 是挂到 SDK 上的 HTTP 中间件：debug 级别且 log.llm_requests 开启时
// 打印实际发出的 API 请求。
//
// openai-go / anthropic-sdk-go 都支持 option.WithMiddleware(debugMiddleware)，
// 两边的 MiddlewareNext 均为 func(*http.Request) (*http.Response, error) 的别名，
// 因此可以共用一个函数。中间件执行时 body 已由 SDK 序列化完成（含 WithJSONSet
// 追加的 reasoning_effort 等非标准字段），打印的就是真正发出去的报文。
func debugMiddleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	logAPIRequest(req)
	return next(req)
}

// logAPIRequest 在 debug 级别 + log.llm_requests 开启时打印请求方法、URL 与 JSON 请求体。
//
// 两个条件任一不满足就直接返回，不读取 body、不做任何字符串分配，正常运行没有额外开销。
// 打印后会还原 req.Body，不影响实际发送与 SDK 的重试（重试依赖 GetBody）。
func logAPIRequest(req *http.Request) {
	if req == nil || req.Body == nil || !logger.LLMRequestLogEnabled(req.Context()) {
		return
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Debugf(req.Context(), "[LLM] request %s %s (read body failed: %v)", req.Method, req.URL.String(), err)
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))

	body := string(raw)
	if len(body) > maxLoggedBodyBytes {
		body = body[:maxLoggedBodyBytes] + "...(truncated)"
	}
	logger.Debugf(req.Context(), "[LLM] request %s %s\n%s", req.Method, req.URL.String(), body)
}
