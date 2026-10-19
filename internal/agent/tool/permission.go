package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

// ErrPermissionDenied 表示工具操作未获得授权。
var ErrPermissionDenied = errors.New("permission denied")

// PermissionRequest 描述一次工具操作授权请求。
//
// 字段与 opencode internal/permission.CreatePermissionRequest 对齐，使从
// opencode/internal/llm/tools 迁移过来的工具保持一致的权限语义：
//   - SessionID：发起调用的会话标识（缺省时由 RequestPermission 从 ctx 补齐）；
//   - ToolName / Action：工具名与授权动作（execute / write / read / fetch / delete ...）；
//   - Description：展示给用户的摘要（如 "Execute command: ls -la"）；
//   - Params：工具入参，供上层抽取命令 / diff 等细节；
//   - Path：授权作用路径（文件操作取目录，命令执行取工作目录）。
type PermissionRequest struct {
	SessionID   string `json:"session_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params,omitempty"`
	Path        string `json:"path,omitempty"`
}

// PermissionRequester 由上层注入，把工具操作授权请求桥接到 Runtime。
//
// 返回 true 表示允许，false 表示拒绝。tool 包不依赖 agent 包，因此这里只声明
// 最小端口，具体映射（PermissionRequest → Operation → Runtime.RequestPermission）
// 由上层实现（见 agent.WithToolPermission）。
type PermissionRequester interface {
	RequestPermission(ctx context.Context, req PermissionRequest) bool
}

// permissionRequesterKey 是 PermissionRequester 在 context 中的私有键类型。
type permissionRequesterKey struct{}

// WithPermissionRequester 把授权请求器注入工具执行 ctx；requester 为 nil 时原样返回。
func WithPermissionRequester(ctx context.Context, requester PermissionRequester) context.Context {
	if ctx == nil || requester == nil {
		return ctx
	}
	return context.WithValue(ctx, permissionRequesterKey{}, requester)
}

// PermissionRequesterFrom 取出注入的授权请求器；未注入时返回 nil。
func PermissionRequesterFrom(ctx context.Context) PermissionRequester {
	if ctx == nil {
		return nil
	}
	requester, _ := ctx.Value(permissionRequesterKey{}).(PermissionRequester)
	return requester
}

// RequestPermission 请求一次工具操作授权。
//
// 未注入授权请求器时直接放行：工具因此可以脱离 Agent Runtime 单独使用
// （如单元测试、无 UI 的一次性调用）。
func RequestPermission(ctx context.Context, req PermissionRequest) bool {
	requester := PermissionRequesterFrom(ctx)
	if requester == nil {
		return true
	}
	if req.SessionID == "" {
		req.SessionID = sessionIDString(ctx)
	}
	return requester.RequestPermission(ctx, req)
}

// RequirePermission 请求授权，拒绝时返回 ErrPermissionDenied。
func RequirePermission(ctx context.Context, req PermissionRequest) error {
	if RequestPermission(ctx, req) {
		return nil
	}
	return ErrPermissionDenied
}

// sessionIDString 把 RuntimeContext 中的会话主键转为权限请求使用的字符串标识。
func sessionIDString(ctx context.Context) string {
	id := SessionIDFrom(ctx)
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// permissionRequestFor 依据工具声明构造授权请求（供 Func.WithPermission 使用）。
func permissionRequestFor(ctx context.Context, name, action, pathFrom, description string, params any) PermissionRequest {
	if description == "" {
		description = "Run tool: " + name
	}
	return PermissionRequest{
		SessionID:   sessionIDString(ctx),
		ToolName:    name,
		Action:      action,
		Description: description,
		Params:      params,
		Path:        pathFromParams(pathFrom, params),
	}
}

// pathFromParams 从入参 JSON 中取指定字段作为授权路径；缺失或非法时返回空。
func pathFromParams(field string, params any) string {
	if field == "" || params == nil {
		return ""
	}
	data, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	path, _ := m[field].(string)
	return path
}
