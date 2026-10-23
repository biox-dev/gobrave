package agentruntime

import "errors"

// ErrNoPermissionResolver 表示当前 Runtime 没有绑定权限解析器（无法等待人工决策）。
//
// 原定义在 standalone.go（已删除），ToolRunner 在 Runtime 缺失时仍需要该错误值。
var ErrNoPermissionResolver = errors.New("agent: no permission resolver in runtime")
